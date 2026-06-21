"""GitHub governing logic for the agent egress proxy (PROXY_PROVIDER=github).

This is the GitHub leg of the single outbound chokepoint. Unlike the claude/codex
AuthInjector (which injects one shared OAuth token it owns), GitHub is governed
per-session: every intercepted request carries the pod's own auth.romaine.life
*role=service* JWT — the exact token mcp-github already consumes — and THIS proxy,
not the agent, holds GitHub write capability.

Per request:

    identity  -> read the role=service JWT off Authorization; parse session + scope
                 + owner exactly as mcp-github's auth_romaine._parse_service_sub does
                 (`sub = svc:<scope>:<session_id>`, `actor_email = <owner>`).
    policy    -> decide allow/deny AND the mint scope (read / write / full). This is
                 the grant point for things GitHub's token model can't express, e.g.
                 "push refs/heads/feature-* but not main" — the App token stays coarse
                 (contents:write can touch any branch) and the wall rejects the ref.
                 Day-one policy is allow-all + write, matching today's unrestricted
                 sessions; ref rules slot into evaluate_policy without touching the IO.
    mint      -> RELAY the same JWT to mcp-github's mint_clone_token (its actor->
                 installation routing mints the owner's token). The proxy invents no
                 identity and holds no SA token of its own.
    inject    -> strip the JWT, set Authorization to the App token, forward.
    record    -> POST the github.* control action to the same control-actions endpoint
                 ControlActionAuditor uses — same JWT as bearer, same scope routing,
                 same body — so a raw `gh`/`git` PR is recorded identically to one made
                 through the MCP tool. This is the 100%-capture the agent can't skip.

This module is deliberately PURE (stdlib only, no grpc/httpx/envoy imports) so the
contract logic is unit-tested cluster-independently. The gRPC ext_proc servicer and
the httpx mint/record IO live in the egress IO shell, which is validated by dark
deploy (it injects real tokens and walls real egress — not provable by unit test).
"""

from __future__ import annotations

import base64
import json
import re
from dataclasses import dataclass
from typing import Any

# Scope values that ControlActionAuditor._url_for_scope treats as "the default
# orchestrator" rather than a slot. Kept byte-identical so the proxy routes a
# recorded action to the same place mcp-github would.
_DEFAULT_SCOPES = {"", "tank", "default", "tank-operator"}

_RE_API_REPO = re.compile(r"^/repos/([^/]+)/([^/]+)")
# git smart-HTTP: /{owner}/{repo}(.git)?/(info/refs|git-receive-pack|git-upload-pack)
_RE_GIT_REPO = re.compile(
    r"^/([^/]+)/([^/]+?)(?:\.git)?/(?:info/refs|git-(?:receive|upload)-pack)"
)
_RE_PULLS_CREATE = re.compile(r"^/repos/([^/]+)/([^/]+)/pulls/?(?:\?.*)?$")
_RE_PULLS_MERGE = re.compile(r"^/repos/([^/]+)/([^/]+)/pulls/(\d+)/merge/?(?:\?.*)?$")
# A PR object edit (title/body/state/base): PATCH /repos/{o}/{r}/pulls/{N}. The
# (?!\d+/merge) lookahead keeps /pulls/{N}/merge out — that is the merge path,
# matched by _RE_PULLS_MERGE and denied at the wall, never a pr_write edit.
_RE_PULLS_EDIT = re.compile(r"^/repos/([^/]+)/([^/]+)/pulls/(\d+)/?(?:\?.*)?$")
# A PR/issue comment: POST /repos/{o}/{r}/issues/{N}/comments. A PR comment is an
# issue comment on GitHub, so `gh pr comment` POSTs here.
_RE_ISSUE_COMMENTS = re.compile(r"^/repos/([^/]+)/([^/]+)/issues/(\d+)/comments/?(?:\?.*)?$")


# ---- identity -------------------------------------------------------------

@dataclass(frozen=True)
class SessionIdentity:
    """Who an intercepted request belongs to, read off the relayed role=service
    JWT. session_id/session_scope drive control-action attribution + routing;
    owner_email drives the App-token installation; raw_jwt is relayed onward."""

    raw_jwt: str
    role: str
    session_id: str
    session_scope: str
    owner_email: str

    @property
    def ok(self) -> bool:
        # mcp-github accepts role=service with an actor_email and a per-session
        # sub only; mirror that gate so the proxy fails the same way it would.
        return bool(
            self.raw_jwt
            and self.role == "service"
            and self.session_id
            and self.owner_email
        )


def _jwt_payload(token: str) -> dict[str, Any]:
    """Decode (NOT verify) a JWT payload. Verification happens where it always
    has — mcp-github + the control-actions endpoint verify the relayed JWT against
    auth.romaine.life. A forged token gets no App token and no record, so it can
    move nothing; these claims only route."""
    parts = token.split(".")
    if len(parts) < 2:
        return {}
    seg = parts[1] + "=" * (-len(parts[1]) % 4)
    try:
        return json.loads(base64.urlsafe_b64decode(seg.encode()))
    except Exception:
        return {}


def _parse_service_sub(sub: str) -> tuple[str, str]:
    """(session_id, session_scope) from `svc:<scope>:<session_id>`. Byte-identical
    to mcp-github.auth_romaine._parse_service_sub: 3-part split, returns
    (stable_id, consumer); ("","") on anything else."""
    parts = sub.split(":")
    if len(parts) != 3:
        return "", ""
    _prefix, consumer, stable_id = parts
    return stable_id, consumer


def identity_from_jwt(raw_jwt: str) -> SessionIdentity:
    claims = _jwt_payload(raw_jwt)
    session_id, session_scope = _parse_service_sub(str(claims.get("sub") or ""))
    return SessionIdentity(
        raw_jwt=raw_jwt,
        role=str(claims.get("role") or "").strip(),
        session_id=session_id,
        session_scope=session_scope,
        owner_email=str(claims.get("actor_email") or "").strip().lower(),
    )


# ---- scope routing --------------------------------------------------------

def url_for_scope(session_scope: str, default_url: str) -> str:
    """Where a session's control actions are recorded. Byte-identical to
    ControlActionAuditor._url_for_scope so a proxy-recorded action lands in the
    same orchestrator (default vs. per-slot) as an mcp-github-recorded one."""
    scope = (session_scope or "").strip()
    if scope in _DEFAULT_SCOPES:
        return default_url.rstrip("/")
    return f"http://tank-operator.{scope}.svc:80"


def break_glass_grant_url(
    session_scope: str, session_id: str, repo_full: str, default_url: str
) -> str:
    """The session-scoped git break-glass grant lookup URL — the SAME endpoint the
    in-pod minter hits (GET /api/internal/sessions/<id>/git-break-glass/grant?repo=).
    Routed through url_for_scope so a slot session reads its slot orchestrator. The
    grant is repo-scoped, so the one repo this request touches is the query."""
    base = url_for_scope(session_scope, default_url)
    from urllib.parse import quote

    params = f"?repo={quote(repo_full, safe='')}" if repo_full else ""
    return f"{base}/api/internal/sessions/{session_id}/git-break-glass/grant{params}"


def session_egress_url(session_scope: str, session_id: str, default_url: str) -> str:
    """The session's egress-governance context
    (GET /api/internal/sessions/<id>/egress-context), routed through url_for_scope like
    the grant lookup so a slot session reads its slot orchestrator. It carries the
    durable repos (to scope the /graphql READ mint, since /graphql has no repo in the
    URL) AND whether the session is restricted (to pick the mint scope: least-privilege
    vs full). Every session routes through the wall now, so the wall needs both for
    every session it serves."""
    base = url_for_scope(session_scope, default_url)
    return f"{base}/api/internal/sessions/{session_id}/egress-context"


# ---- policy ---------------------------------------------------------------

@dataclass(frozen=True)
class Decision:
    """The grant. `allow` gates the request at the wall; `write`/`pr_write`/`full`
    pick the mint scope. Ref-level rules (the thing GitHub can't express) set
    allow=False for a protected ref while leaving the token coarse — see
    evaluate_policy.

    The mint scopes are mutually exclusive and ordered by blast radius:
      * full     -> the installation's entire permission set (break-glass only).
      * write    -> contents:write (git push; can touch any branch — lane-confined
                    in the body phase).
      * pr_write -> {pull_requests:write, issues:write, metadata:read}, NO
                    contents. Lets a restricted session manage a PR
                    (create/edit/title/body/ready/comment) but NOT merge or push —
                    those need contents:write, which this scope withholds, so they
                    403 by capability with no body-parsing denylist.
      * (none)   -> read-only."""

    allow: bool
    write: bool = False
    full: bool = False
    pr_write: bool = False
    reason: str = ""


def evaluate_policy(ident: SessionIdentity, method: str, authority: str, path: str, restricted: bool = True) -> Decision:
    """Mint scope for a proxied session — least privilege per request (when restricted).

    The agent never holds the minted token, but the SCOPE still matters: a coarse
    token lets the agent reach the whole repo through the REST API (PUT /contents,
    PATCH /git/refs, POST /merges, …), bypassing the lane/merge governance — which
    only inspects the git transport. So write capability is granted ONLY for the two
    governed write paths:

      * git push (receive-pack) -> `write` (contents). The body phase confines it to
        the session's branch lane (push_violation), widened only by a break-glass
        grant.
      * PR open (POST /pulls) -> `full` (needs pull_requests:write). It is the one
        REST write the wall records as a control action; merging stays denied.

    EVERYTHING ELSE — clone/fetch and the entire REST surface, GETs AND any ungoverned
    write — is minted READ-ONLY. A read token cannot mutate the repo, so GitHub itself
    403s those writes and the wall need not enumerate every dangerous endpoint. This is
    the REST analog of the lane check: git is governed by inspecting refs, REST by
    withholding the capability. (The IO shell layers the one sanctioned exception on
    top: an active *unlimited* break-glass grant — the human-approved full-API blast
    radius — elevates a would-be-read REST write back to `full`.)

    `restricted` is the session's restricted_git status (from the egress-context
    lookup, fail-closed to True). EVERY session routes through the wall now; an
    UNRESTRICTED session is minted FULL for every request — nothing withheld — but is
    still observed (the IO shell keeps the PR-open/merge recording and skips the
    merge/lane checks for it). Observation is universal; only restriction is scoped."""
    if not restricted:
        return Decision(allow=True, write=True, full=True, reason="unrestricted: full mint; observed, not restricted")
    if is_push_intent(authority, path):
        return Decision(allow=True, write=True, reason="git push (advertisement + upload): contents:write, lane-confined in the body phase")
    if is_pr_write(method, authority, path):
        # Expected PR management (create/edit/title/body/comment via REST). pr_write
        # carries pull_requests:write + issues:write but NO contents, so the agent
        # can manage the PR yet cannot merge or push — those 403 by capability. PR
        # open is still recorded as a control action (recordable_pr_action keys on it
        # in the IO shell, independent of the mint scope).
        return Decision(allow=True, pr_write=True, reason="PR-metadata write: pull_requests+issues:write, no contents (merge/push stay denied)")
    if is_graphql(authority, path):
        # /graphql serves BOTH gh pr list/view (reads) AND gh pr edit/ready/comment
        # (mutations). One pr_write mint covers both: reads work, the PR mutations
        # work, and a merge-via-GraphQL (mergePullRequest) still 403s — it needs
        # contents, which pr_write withholds. Scoped to the session's repos by the IO
        # shell (the /graphql URL carries no repo).
        return Decision(allow=True, pr_write=True, reason="/graphql: pr_write covers pr reads + edit/ready/comment mutations; merge still needs contents")
    return Decision(allow=True, reason="read-only: clone/fetch + REST get no write capability; GitHub 403s ungoverned writes")


# ---- branch-lane enforcement (the "restricted" in restricted git) ----------
#
# A restricted session may write ONLY its own pre-made branch lane —
# refs/heads/tank/session/<session_id>/<repo> (the repo-cloner creates one per
# cloned repo). Not main, not a stray new branch, not another session's lane. main
# reaches code only through the human/automated merge path. The lane is derived from
# the session id on the relayed JWT, so the wall needs no extra state.


def is_merge_request(method: str, authority: str, path: str) -> bool:
    """A PR-merge API call (PUT /repos/o/r/pulls/N/merge). Denied at the wall for
    proxied (restricted) sessions — merging is the human/automated path, and the
    sidecar already disables the merge_pull_request MCP tool in restricted mode, so
    this just closes the raw gh/REST hole."""
    host = (authority or "").split(":", 1)[0].lower()
    return (
        host == "api.github.com"
        and (method or "").upper() == "PUT"
        and bool(_RE_PULLS_MERGE.match(path or ""))
    )


def is_push_intent(authority: str, path: str) -> bool:
    """Any leg of a git PUSH — needs a write token. A push is TWO requests: the
    capability advertisement (GET /{o}/{r}/info/refs?service=git-receive-pack) AND
    the upload itself (POST /{o}/{r}/git-receive-pack); BOTH require receive-pack
    (write) access, so a read token 403s the advertisement and the push never starts.
    Fetch legs (service=git-upload-pack / git-upload-pack) are reads and excluded."""
    host = (authority or "").split(":", 1)[0].lower()
    if host != "github.com":
        return False
    p = (path or "").rstrip("/")
    return p.endswith("/git-receive-pack") or "service=git-receive-pack" in p


def is_rest_write(method: str, authority: str, path: str) -> bool:
    """A mutating REST API call: POST/PUT/PATCH/DELETE to api.github.com on a repo
    path. With the read-by-default mint these are exactly the requests GitHub 403s,
    and the ones an active *unlimited* break-glass grant elevates back to a full
    token. GET/HEAD reads and the git-transport host are excluded."""
    host = (authority or "").split(":", 1)[0].lower()
    if host != "api.github.com":
        return False
    if (method or "").upper() not in ("POST", "PUT", "PATCH", "DELETE"):
        return False
    return repo_from_path(authority, path) is not None


def is_pr_write(method: str, authority: str, path: str) -> bool:
    """An EXPECTED PR-management write a restricted session should be allowed to do
    through the wall, minted pr_write (pull_requests+issues:write, no contents):

      * POST  /repos/{o}/{r}/pulls                  — open a PR
      * PATCH /repos/{o}/{r}/pulls/{N}              — edit title/body/state/base
      * POST  /repos/{o}/{r}/issues/{N}/comments   — comment (PR comment == issue comment)

    Deliberately NOT matched: PUT /pulls/{N}/merge (the merge path — denied at the
    wall and capability-denied by the missing contents perm) and every other REST
    write (PUT /contents, PATCH /git/refs, POST /merges, …), which stay read-only so
    GitHub 403s them. gh pr edit/ready run as GraphQL mutations — see is_graphql,
    which gets the same pr_write mint."""
    host = (authority or "").split(":", 1)[0].lower()
    if host != "api.github.com":
        return False
    m = (method or "").upper()
    p = path or ""
    if m == "POST" and _RE_PULLS_CREATE.match(p):
        return True
    if m == "PATCH" and _RE_PULLS_EDIT.match(p):
        return True
    if m == "POST" and _RE_ISSUE_COMMENTS.match(p):
        return True
    return False


def is_graphql(authority: str, path: str) -> bool:
    """The GitHub GraphQL endpoint (POST api.github.com/graphql). It carries NO repo
    in the URL (the repo lives in the query body), so repo_from_path can't scope a
    mint for it — instead the IO shell scopes a READ mint to the SESSION's repos.
    gh's pr list/view/status are GraphQL reads served by that read token; a GraphQL
    *mutation* sent with a read token is refused by GitHub, so writes stay governed
    exactly as on the REST surface (the read-by-default mint, no enumerated denylist)."""
    host = (authority or "").split(":", 1)[0].lower()
    return host == "api.github.com" and (path or "").split("?", 1)[0].rstrip("/") == "/graphql"


def is_receive_pack(authority: str, path: str) -> bool:
    """The git push endpoint (POST github.com/{o}/{r}(.git)/git-receive-pack), whose
    body carries the ref-update commands we must inspect to block pushes to main."""
    host = (authority or "").split(":", 1)[0].lower()
    return host == "github.com" and (path or "").rstrip("/").endswith("/git-receive-pack")


def parse_push_commands(data: bytes) -> tuple[list[tuple[str, str, str]], bool]:
    """Parse the ref-update commands at the head of a git-receive-pack request body
    (pkt-line framed), up to the flush packet. Returns (commands, saw_flush) where
    each command is ``(old_sha, new_sha, ref)``.

    Each command line is ``<old-sha> <new-sha> <ref>`` (the first also carries
    ``\\0<capabilities>``). The commands always precede the binary PACK, so the first
    body chunk contains them. saw_flush=True means the full command list was present
    in `data` (the set is complete); False means more bytes are needed (or the input
    wasn't parseable pkt-line). The new_sha lets the IO shell record a precise commit
    ref when a push is allowed only by a break-glass grant."""
    cmds: list[tuple[str, str, str]] = []
    i, n = 0, len(data)
    while i + 4 <= n:
        try:
            length = int(data[i : i + 4], 16)
        except ValueError:
            break  # not pkt-line framed (e.g. gzipped) — caller fails closed
        if length == 0:
            return cmds, True  # flush packet: end of the command list
        if length < 4 or i + length > n:
            break  # truncated line: need more bytes
        line = data[i + 4 : i + length]
        i += length
        nul = line.find(b"\x00")
        if nul >= 0:
            line = line[:nul]  # drop capabilities on the first command
        parts = line.rstrip(b"\n").split(b" ")
        if len(parts) >= 3:
            cmds.append(
                (
                    parts[0].decode("utf-8", "ignore"),
                    parts[1].decode("utf-8", "ignore"),
                    parts[2].decode("utf-8", "ignore"),
                )
            )
    return cmds, False


def parse_push_refs(data: bytes) -> tuple[list[str], bool]:
    """Destination refs from a git-receive-pack body, + saw_flush. Thin wrapper over
    parse_push_commands (which also exposes the old/new shas)."""
    cmds, saw_flush = parse_push_commands(data)
    return [c[2] for c in cmds], saw_flush


def session_branch_lane(session_id: str) -> str:
    """The ref prefix a session is allowed to push. Matches the repo-cloner's
    `tank/session/<id>/<repo>` naming (one branch per cloned repo)."""
    return f"refs/heads/tank/session/{session_id}/"


def out_of_lane_refs(refs: list[str], session_id: str) -> list[str]:
    """The pushed refs OUTSIDE the session's own branch lane. Fails closed: an empty
    session_id makes every ref out-of-lane (a session we can't identify writes
    nothing). The common case — a session pushing only its own lane — returns []
    so the IO shell never has to fetch a grant for it."""
    if not session_id:
        return list(refs)
    lane = session_branch_lane(session_id)
    return [r for r in refs if not r.startswith(lane)]


def push_violation(refs: list[str], session_id: str, grant: dict[str, Any] | None = None) -> str | None:
    """The first pushed ref that is neither in the session's own branch lane NOR
    permitted by an active break-glass grant, or None if every ref is allowed.

    The lane is the baseline 'restricted' rule: a session writes its own pre-made
    branch freely and nothing else. A break-glass grant WIDENS that baseline — an
    admin-approved grant lets the session also push the branches the grant names
    (`named`), any branch (`unlimited`), or any branch up to a budget (`count`).
    main, stray branches, and other sessions' lanes stay rejected unless a grant
    explicitly covers them. With grant=None this is exactly the lane-only rule, so
    the no-grant hot path is unchanged. Fails closed via out_of_lane_refs."""
    for r in out_of_lane_refs(refs, session_id):
        if not grant_branch_allows(grant, r):
            return r
    return None


# ---- break-glass grants (widen the lane; mirror mcp-auth-proxy) -------------
#
# The wall confines a restricted session to its lane, but an admin can approve a
# break-glass grant that widens it. The grant lives in Tank's control-action
# ledger and is read through the SAME session-scoped endpoint the in-pod minter
# uses (GET /api/internal/sessions/<id>/git-break-glass/grant?repo=<slug>), so the
# wall and the minter can never diverge on what's active. These helpers mirror
# mcp-auth-proxy's _active_break_glass_grant / _grant_branch_allows byte-for-byte;
# the IO shell does the GET + caches it, then feeds the grant dict here.


def active_grant_from_response(status_code: int, body_text: str) -> dict[str, Any] | None:
    """The active grant dict from the git-break-glass/grant response, or None.
    Mirrors mcp-auth-proxy._active_break_glass_grant: 204/empty/non-2xx -> no grant;
    a JSON body is honored only when active is True (the endpoint returns
    {"active": false, ...} when nothing is live, count is exhausted, or it expired)."""
    if status_code == 204 or not (body_text or "").strip():
        return None
    if status_code >= 400:
        return None  # fail closed: a failed lookup is treated as no grant
    try:
        value = json.loads(body_text)
    except Exception:
        return None
    if isinstance(value, dict) and value.get("active") is True:
        return value
    return None


def parse_egress_context(status_code: int, body_text: str) -> tuple[list[str], bool]:
    """(repos, restricted) from the internal egress-context endpoint.

    Fails CLOSED to restricted: a non-2xx status or unparseable/missing body yields
    ([], True). restricted=True means a Tank hiccup can never silently un-restrict a
    restricted session (the wall keeps minting least-privilege); the cost is that an
    unrestricted session is briefly degraded to read-only until the lookup recovers —
    the safe direction. []-repos means the /graphql mint is skipped (forward unminted)
    rather than minting an unscoped token. `restricted` is True unless the body
    explicitly says false, so only a clean, current answer un-restricts."""
    if status_code < 200 or status_code >= 300 or not (body_text or "").strip():
        return [], True
    try:
        value = json.loads(body_text)
    except Exception:
        return [], True
    if not isinstance(value, dict):
        return [], True
    repos_raw = value.get("repos")
    repos: list[str] = []
    if isinstance(repos_raw, list):
        for item in repos_raw:
            slug = str(item or "").strip()
            if slug:
                repos.append(slug)
    # Only an explicit boolean false un-restricts; true/missing/null/non-bool -> True.
    restricted = value.get("restricted") is not False
    return repos, restricted


def grant_branch_allows(grant: dict[str, Any] | None, ref: str) -> bool:
    """Whether an active grant's branch scope permits pushing `ref`. Byte-identical
    to mcp-auth-proxy._grant_branch_allows: unlimited -> any ref; named -> the ref's
    short name is in the granted set (both sides strip refs/heads/); count -> any ref
    (the budget is enforced by the endpoint flipping active->false once the
    github.break_glass.push records reach the count, which is why the IO shell must
    record each elevated push)."""
    if not grant:
        return False
    branch_scope = grant.get("branch_scope")
    if not isinstance(branch_scope, dict):
        return False
    kind = str(branch_scope.get("kind") or "").strip()
    if kind == "unlimited":
        return True
    if kind == "named":
        branches = branch_scope.get("branches")
        if not isinstance(branches, list) or not branches:
            return False
        allowed = {str(item).removeprefix("refs/heads/").strip() for item in branches}
        return ref.removeprefix("refs/heads/").strip() in allowed
    if kind == "count":
        return True
    return False


def grant_allows_merge(grant: dict[str, Any] | None) -> bool:
    """Whether an active grant unlocks PR merge (normally the human/automated path,
    denied at the wall). Only an `unlimited` grant carries the App's full API set,
    advertised as the `full_github_api` operation — branch/count grants stay
    least-privilege and never merge. Mirrors the in-pod gate that mints full=True
    only for unlimited grants."""
    if not grant:
        return False
    ops = grant.get("operations")
    return isinstance(ops, list) and "full_github_api" in ops


def grant_event_id(grant: dict[str, Any] | None) -> str:
    """The grant's ledger event id, threaded into github.break_glass.push records so
    countBreakGlassGrantBranches can tally a count grant's distinct branches."""
    if not grant:
        return ""
    return str(grant.get("event_id") or "")


def _grant_expires_epoch(grant: dict[str, Any]) -> float:
    """The grant's own expires_at as an epoch (0.0 if absent/unparseable). Lets the
    IO-shell cache never serve a positive grant past its real expiry."""
    raw = str(grant.get("expires_at") or "").strip()
    if not raw:
        return 0.0
    from datetime import datetime, timezone

    try:
        dt = datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        return 0.0
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.timestamp()


def grant_servable(grant: dict[str, Any] | None, now_epoch: float) -> bool:
    """Whether a cached grant lookup may still be served. A negative result (None)
    is always servable within the cache TTL; a positive grant only while it is still
    before its own expires_at, so a cached grant can never outlive the real one.
    Mirrors mcp-auth-proxy._cached_grant_servable."""
    if grant is None:
        return True
    exp = _grant_expires_epoch(grant)
    return exp > now_epoch


# ---- request classification ----------------------------------------------

def repo_from_path(authority: str, path: str) -> tuple[str, str] | None:
    """(owner, repo) for an intercepted request, or None. Covers the REST host
    (/repos/{o}/{r}/...) and the git smart-HTTP host (/{o}/{r}(.git)/info/refs|
    git-receive-pack|git-upload-pack)."""
    host = (authority or "").split(":", 1)[0].lower()
    if host == "api.github.com":
        m = _RE_API_REPO.match(path or "")
    else:
        m = _RE_GIT_REPO.match(path or "")
    if not m:
        return None
    return m.group(1), m.group(2)


def recordable_pr_action(method: str, authority: str, path: str) -> tuple[str, int | None] | None:
    """If this request is a PR open/merge, return (action, pr_number-or-None).
    Open is detected on the request (POST /pulls) but its number is only known
    from the response; merge carries the number in the path. Other writes
    (commit.push via git-receive-pack) are recorded by the IO shell separately."""
    host = (authority or "").split(":", 1)[0].lower()
    if host != "api.github.com":
        return None
    m = (method or "").upper()
    if m == "POST" and _RE_PULLS_CREATE.match(path or ""):
        return "github.pull_request.open", None
    merge = _RE_PULLS_MERGE.match(path or "")
    if m == "PUT" and merge:
        return "github.pull_request.merge", int(merge.group(3))
    return None


# ---- mint (relay to mcp-github) -------------------------------------------

def mint_call_payload(repos: list[str], decision: Decision, *, call_id: int = 1) -> dict[str, Any]:
    """JSON-RPC tools/call for mcp-github's mint_clone_token, scoped to the repos this
    request touches — a single repo for a REST/git path (from the URL), or the
    session's whole create-time set for /graphql (which carries no repo in the URL) —
    at the scope the policy granted. Relayed with the session's own JWT as bearer, so
    mcp-github's actor->installation routing picks the owner's installation."""
    args: dict[str, Any] = {"repos": list(repos)}
    if decision.full:
        args["full"] = True
    elif decision.write:
        args["write"] = True
    elif decision.pr_write:
        args["pr_write"] = True
    return {
        "jsonrpc": "2.0",
        "id": call_id,
        "method": "tools/call",
        "params": {"name": "mint_clone_token", "arguments": args},
    }


def parse_mint_result(body: dict[str, Any]) -> tuple[str, str]:
    """(token, expires_at_iso) from a FastMCP tools/call response. Prefers
    structuredContent (dict return); falls back to the text content block which
    carries the same {"token","expires_at"} JSON. Raises on a missing token so the
    request fails closed (no token => no injection => the agent gets nothing)."""
    result = body.get("result") or {}
    sc = result.get("structuredContent")
    if isinstance(sc, dict) and sc.get("token"):
        return str(sc["token"]), str(sc.get("expires_at") or "")
    for block in result.get("content") or []:
        if isinstance(block, dict) and block.get("type") == "text":
            try:
                inner = json.loads(block.get("text") or "")
            except Exception:
                continue
            if isinstance(inner, dict) and inner.get("token"):
                return str(inner["token"]), str(inner.get("expires_at") or "")
    raise ValueError("mcp-github mint returned no token")


# ---- recording (mirror ControlActionAuditor) ------------------------------

def build_record_body(
    *,
    invocation_id: str,
    source_tool: str,
    action: str,
    status: str,
    target_ref: str,
    repo_owner: str,
    repo_name: str,
    pr_number: int,
    result_sha: str = "",
    error: str = "",
    payload: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """The control-actions append body — byte-compatible with
    ControlActionAuditor.finish so the durable PR-sighting hook keys on it
    identically. source_service marks who recorded it (the wall) vs. mcp-github,
    but the shape and the resulting sighting are the same."""
    return {
        "event_id": f"{invocation_id}_{status}",
        "invocation_id": invocation_id,
        "source_service": "agent-egress-proxy",
        "source_tool": source_tool,
        "action": action,
        "status": status,
        "target_kind": "github_pull_request",
        "target_ref": target_ref,
        "repo_owner": repo_owner,
        "repo_name": repo_name,
        "pr_number": pr_number,
        "result_sha": result_sha,
        "error": error[:1200],
        "payload": payload or {},
    }


def build_break_glass_push_body(
    *,
    invocation_id: str,
    grant_event_id: str,
    repo_owner: str,
    repo_name: str,
    branch: str,
    new_sha: str = "",
    status: str = "succeeded",
    error: str = "",
) -> dict[str, Any]:
    """The control-action body for an out-of-lane push the wall allowed only because
    a break-glass grant covered it. Mirrors mcp-auth-proxy._record_break_glass_use so
    countBreakGlassGrantBranches tallies a `count` grant's distinct branches off the
    same `github.break_glass.push` + payload.{grant_event_id,branch} shape — AND so
    every elevated push lands in the same ledger a lane push would (the wall records
    nothing the agent can skip). source_service marks the wall as the recorder."""
    slug = f"{repo_owner}/{repo_name}"
    has_sha = bool(new_sha)
    return {
        "event_id": f"{invocation_id}_{status}",
        "invocation_id": invocation_id,
        "source_service": "agent-egress-proxy",
        "source_tool": "git",
        "action": "github.break_glass.push",
        "status": status,
        "target_kind": "github_commit" if has_sha else "github_repository",
        "target_ref": (
            f"https://github.com/{slug}/commit/{new_sha}" if has_sha else f"https://github.com/{slug}"
        ),
        "repo_owner": repo_owner,
        "repo_name": repo_name,
        "result_sha": new_sha,
        "error": error[:1200],
        "payload": {"grant_event_id": grant_event_id, "branch": branch, "repo_path": slug},
    }


def jwt_from_authorization(authorization: str) -> str:
    """The pod's credential off the request, across every auth shape git/gh/REST send:
      * ``token <tok>``  — what ``gh`` and the GitHub REST API default to;
      * ``Bearer <tok>`` — what a raw ``curl``/REST client sends;
      * ``Basic base64(x-access-token:<tok>)`` — git smart-HTTP, where the credential
        helper hands the token as the password.
    All three carry the pod's RAW auth.romaine.life k8s SA token, which the proxy
    exchanges (see parse_exchange_result) into the role=service JWT before
    minting/recording. NOTE: ``gh`` uses the ``token`` scheme, NOT ``Bearer`` —
    omitting it is what left every ``gh api`` / ``gh pr view`` / ``gh run list`` call
    unminted and 401'd in restricted sessions (the wall forwarded the raw SA token,
    which GitHub rejects). Reads still get a read-scoped mint and writes still 403, so
    recognizing the scheme restores gh reads without widening write capability."""
    if not authorization:
        return ""
    scheme, _, rest = authorization.partition(" ")
    s = scheme.strip().lower()
    if s in ("bearer", "token"):
        return rest.strip()
    if s == "basic":
        try:
            decoded = base64.b64decode(rest.strip()).decode()
        except Exception:
            return ""
        _user, _, password = decoded.partition(":")
        return password.strip()
    return ""


def parse_exchange_result(body: dict[str, Any]) -> tuple[str, float]:
    """(role=service JWT, expires_at_epoch) from auth.romaine.life's
    /api/auth/exchange/k8s response. The proxy POSTs the pod's RAW k8s SA token with
    an EMPTY body; the IdP auto-resolves the session's own owner and mints a
    role=service JWT carrying actor_email + sub=svc:tank:<session_id> — exactly the
    token mcp-github + the control-actions endpoint require (mirrors the in-pod
    mcp-auth-proxy's AuthRomaineServiceProvider). Returns ("", 0.0) on a missing
    token so the caller fails closed (no exchange -> no mint -> the agent gets
    nothing)."""
    if not isinstance(body, dict):
        return "", 0.0
    token = str(body.get("token") or "")
    raw = body.get("expires_at")
    if isinstance(raw, bool):
        exp = 0.0
    elif isinstance(raw, (int, float)):
        exp = float(raw)
    elif isinstance(raw, str) and raw.strip().isdigit():
        exp = float(raw.strip())
    else:
        exp = 0.0
    return token, exp


# -- exchange retry policy (pure) ------------------------------------------
# The wall exchanges the pod's RAW k8s SA token for the session's role=service
# JWT (parse_exchange_result above) before it can mint. At the very start of a
# pod's life the IdP can transiently reject that exchange — a startup race where
# the just-created session is not yet resolvable. It is observed as an instant
# 401 (~1-2ms, ~5% of exchanges) that heals within seconds: the SAME pod
# exchanges 200 moments later. Silently failing it turned the race into a hard
# `git push` failure (the raw SA token fell through to GitHub; the agent saw a
# confusing 500 / "proxy going down" — romaine-life/tank-operator session 1194).
# The wall now retries a transient exchange with bounded backoff so the race is
# absorbed before it surfaces.
#
# EXCHANGE_RETRY_BACKOFFS_S is the sleep BETWEEN attempts: N backoffs => N+1
# total attempts. Combined with EXCHANGE_ATTEMPT_TIMEOUT_S it is sized to stay
# under the egress Envoy's 10s ext_proc message_timeout even after the
# egress-context fetch + mint that follow in the same header phase
# (test_exchange_retry_budget_fits_envoy pins that headroom).
EXCHANGE_RETRY_BACKOFFS_S: tuple[float, ...] = (0.3, 0.8, 1.5)
EXCHANGE_ATTEMPT_TIMEOUT_S: float = 1.2


def is_retryable_exchange_status(status: int) -> bool:
    """Should the wall retry an exchange that returned this HTTP status (or 0 for a
    network/timeout error before any response)? The startup race shows up as an
    instant 401, an over-eager throttle as 429, and an IdP hiccup as 5xx/timeout —
    all transient, all retried. A 400 is the only non-retryable error class (a
    malformed request will not fix itself, and the wall always POSTs a fixed empty
    body so it never provokes one). A 2xx never reaches here on success (the caller
    returns the token); a 2xx WITHOUT a token is treated as non-retryable here and
    fails closed, since it signals an IdP contract break, not a race."""
    if status == 0:  # connect/read timeout, DNS, reset — transient
        return True
    if status == 400:  # malformed request — deterministic, do not retry
        return False
    if status in (401, 403, 408, 429):  # startup race / auth-not-ready / throttle
        return True
    return status >= 500  # IdP 5xx — transient


def first_json_object(text: str) -> dict[str, Any]:
    """Pull the first JSON-RPC object out of an mcp-github reply, tolerating both
    bare JSON and SSE `data: {json}` framing — the same two shapes
    git-credential-tank.sh and the Go mcpgithub client both parse."""
    for line in (text or "").splitlines():
        line = line.strip()
        if not line:
            continue
        if line.startswith("data:"):
            line = line[len("data:"):].strip()
        if line.startswith("{"):
            try:
                obj = json.loads(line)
            except Exception:
                continue
            if isinstance(obj, dict):
                return obj
    return {}


def pr_fields_from_response_json(body_text: str) -> tuple[int, str]:
    """(pr_number, html_url) from a created/merged PR REST response body. Returns
    (0, "") when the body isn't a PR object (e.g. a merge ack carries no number)."""
    try:
        obj = json.loads(body_text)
    except Exception:
        return 0, ""
    if not isinstance(obj, dict):
        return 0, ""
    number = obj.get("number")
    return (int(number) if isinstance(number, int) else 0), str(obj.get("html_url") or "")
