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
from dataclasses import dataclass, field
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


# ---- policy ---------------------------------------------------------------

@dataclass(frozen=True)
class Decision:
    """The grant. `allow` gates the request at the wall; `write`/`full` pick the
    mint scope. Ref-level rules (the thing GitHub can't express) set allow=False
    for a protected ref while leaving the token coarse — see evaluate_policy."""

    allow: bool
    write: bool = False
    full: bool = False
    reason: str = ""


def evaluate_policy(ident: SessionIdentity, method: str, authority: str, path: str) -> Decision:
    """Day-one policy: allow-all, mint with write — i.e. exactly today's
    unrestricted session (push to any branch, no merges blocked), now routed
    through the wall and recorded. This is the single function a future per-session
    policy edits: deny by ref (read the receive-pack refs and refuse refs/heads/main
    for a test session), cap a session to read-only (write=False), or open the
    break-glass superset (full=True) — none of which touches identity, mint, or IO."""
    return Decision(allow=True, write=True, reason="default: allow-all+write")


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

def mint_call_payload(repo_full: str, decision: Decision, *, call_id: int = 1) -> dict[str, Any]:
    """JSON-RPC tools/call for mcp-github's mint_clone_token, scoped to the one
    repo this request touches, at the scope the policy granted. Relayed with the
    session's own JWT as bearer — mcp-github's actor->installation routing picks
    the owner's installation."""
    args: dict[str, Any] = {"repos": [repo_full]}
    if decision.full:
        args["full"] = True
    elif decision.write:
        args["write"] = True
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
