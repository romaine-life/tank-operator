"""Pure-core contract tests for the GitHub egress governor.

These pin the proxy's logic to mcp-github's real contracts (auth_romaine sub
parsing, ControlActionAuditor scope routing + body, mint_clone_token call/return)
so a drift in either side fails here rather than silently in the cluster. The
module under test is stdlib-only by design, so no proto stubs / grpc are needed.
"""

from __future__ import annotations

import base64
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from tank_api_proxy import github_governor as gg  # noqa: E402

DEFAULT_URL = "http://tank-operator.tank-operator.svc.cluster.local"


def _jwt(payload: dict) -> str:
    def b64(obj) -> str:
        raw = json.dumps(obj).encode()
        return base64.urlsafe_b64encode(raw).decode().rstrip("=")
    return f"{b64({'alg': 'RS256'})}.{b64(payload)}.sig"


# ---- identity -------------------------------------------------------------

def test_identity_parses_tank_session_like_mcp_github() -> None:
    ident = gg.identity_from_jwt(
        _jwt({"sub": "svc:tank:47", "role": "service", "actor_email": "Owner@Example.Test"})
    )
    assert ident.session_id == "47"
    assert ident.session_scope == "tank"
    assert ident.owner_email == "owner@example.test"  # normalized lower, as auth_romaine does
    assert ident.role == "service"
    assert ident.ok


def test_identity_parses_slot_scope() -> None:
    ident = gg.identity_from_jwt(
        _jwt({"sub": "svc:tank-operator-slot-3:47", "role": "service", "actor_email": "o@x.test"})
    )
    assert ident.session_scope == "tank-operator-slot-3"
    assert ident.session_id == "47"
    assert ident.ok


def test_identity_rejects_non_service_or_missing_actor() -> None:
    assert not gg.identity_from_jwt(
        _jwt({"sub": "svc:tank:47", "role": "user", "actor_email": "o@x.test"})
    ).ok
    assert not gg.identity_from_jwt(
        _jwt({"sub": "svc:tank:47", "role": "service"})
    ).ok
    assert not gg.identity_from_jwt("not-a-jwt").ok


# ---- scope routing (must match ControlActionAuditor._url_for_scope) -------

def test_url_for_scope_default_buckets() -> None:
    for scope in ("", "tank", "default", "tank-operator"):
        assert gg.url_for_scope(scope, DEFAULT_URL) == DEFAULT_URL


def test_url_for_scope_routes_slot_to_slot_orchestrator() -> None:
    # Exactly the URL test_control_action_audit_tools asserts mcp-github uses.
    assert (
        gg.url_for_scope("tank-operator-slot-3", DEFAULT_URL)
        == "http://tank-operator.tank-operator-slot-3.svc:80"
    )


# ---- request classification ----------------------------------------------

def test_repo_from_path_rest_and_git() -> None:
    assert gg.repo_from_path("api.github.com", "/repos/romaine-life/tank-operator/pulls") == (
        "romaine-life",
        "tank-operator",
    )
    assert gg.repo_from_path("github.com", "/romaine-life/tank-operator.git/git-receive-pack") == (
        "romaine-life",
        "tank-operator",
    )
    assert gg.repo_from_path("github.com", "/romaine-life/tank-operator/info/refs?service=git-upload-pack") == (
        "romaine-life",
        "tank-operator",
    )
    assert gg.repo_from_path("api.github.com", "/user") is None


def test_recordable_pr_action_open_and_merge() -> None:
    assert gg.recordable_pr_action("POST", "api.github.com", "/repos/o/r/pulls") == (
        "github.pull_request.open",
        None,
    )
    assert gg.recordable_pr_action("PUT", "api.github.com", "/repos/o/r/pulls/857/merge") == (
        "github.pull_request.merge",
        857,
    )
    assert gg.recordable_pr_action("GET", "api.github.com", "/repos/o/r") is None
    assert gg.recordable_pr_action("POST", "github.com", "/o/r.git/git-receive-pack") is None


# ---- mint relay -----------------------------------------------------------

def _pkt(payload: bytes) -> bytes:
    return f"{len(payload) + 4:04x}".encode() + payload


_FLUSH = b"0000"
_ZERO = b"0" * 40


def test_parse_push_refs_extracts_dest_refs_and_flush() -> None:
    # First command carries \0capabilities; a second command follows; then flush+PACK.
    body = (
        _pkt(_ZERO + b" " + b"a" * 40 + b" refs/heads/feature\x00 report-status side-band-64k\n")
        + _pkt(b"b" * 40 + b" " + b"c" * 40 + b" refs/heads/other\n")
        + _FLUSH
        + b"PACK\x00\x00\x00\x02....binary...."
    )
    refs, saw_flush = gg.parse_push_refs(body)
    assert refs == ["refs/heads/feature", "refs/heads/other"]
    assert saw_flush is True


def test_push_violation_confines_to_session_lane() -> None:
    sid = "1166"
    lane = "refs/heads/tank/session/1166/"
    assert gg.session_branch_lane(sid) == lane
    # in-lane (the session's own pre-made branch, one per repo) -> allowed
    assert gg.push_violation([lane + "tank-operator"], sid) is None
    assert gg.push_violation([lane + "tank-operator", lane + "glimmung"], sid) is None
    # parse a real receive-pack push to the lane branch -> allowed
    in_lane = _pkt(_ZERO + b" " + b"d" * 40 + b" " + (lane + "tank-operator").encode() + b"\x00report-status\n") + _FLUSH + b"PACK.."
    refs, ok = gg.parse_push_refs(in_lane)
    assert ok and gg.push_violation(refs, sid) is None
    # main, a stray branch, and another session's lane are all violations
    assert gg.push_violation(["refs/heads/main"], sid) == "refs/heads/main"
    assert gg.push_violation(["refs/heads/feature-x"], sid) == "refs/heads/feature-x"
    assert gg.push_violation(["refs/heads/tank/session/9999/x"], sid) == "refs/heads/tank/session/9999/x"
    # any out-of-lane ref among several is caught
    assert gg.push_violation([lane + "ok", "refs/heads/main"], sid) == "refs/heads/main"
    # empty session id -> fail closed (deny everything)
    assert gg.push_violation([lane + "x"], "") == lane + "x"


def test_parse_push_refs_non_pktline_is_not_flushed() -> None:
    # gzipped / binary body: no valid pkt-line framing -> saw_flush False so the
    # caller fails closed rather than wave the push through.
    refs, saw_flush = gg.parse_push_refs(b"\x1f\x8b\x08\x00random-gzip-bytes")
    assert saw_flush is False


def test_is_merge_request_and_receive_pack() -> None:
    assert gg.is_merge_request("PUT", "api.github.com", "/repos/o/r/pulls/12/merge") is True
    assert gg.is_merge_request("POST", "api.github.com", "/repos/o/r/pulls") is False
    assert gg.is_merge_request("PUT", "api.github.com", "/repos/o/r/pulls/12") is False
    assert gg.is_receive_pack("github.com", "/o/r.git/git-receive-pack") is True
    assert gg.is_receive_pack("github.com", "/o/r.git/info/refs?service=git-receive-pack") is False
    assert gg.is_receive_pack("api.github.com", "/repos/o/r/pulls") is False


def test_mint_payload_scopes_to_repo_and_grant() -> None:
    write = gg.mint_call_payload("romaine-life/tank-operator", gg.Decision(allow=True, write=True))
    assert write["method"] == "tools/call"
    assert write["params"]["name"] == "mint_clone_token"
    assert write["params"]["arguments"] == {"repos": ["romaine-life/tank-operator"], "write": True}

    full = gg.mint_call_payload("o/r", gg.Decision(allow=True, full=True))
    assert full["params"]["arguments"] == {"repos": ["o/r"], "full": True}

    read = gg.mint_call_payload("o/r", gg.Decision(allow=True))
    assert read["params"]["arguments"] == {"repos": ["o/r"]}


def test_parse_mint_result_structured_and_text() -> None:
    structured = {"result": {"structuredContent": {"token": "ghs_abc", "expires_at": "2026-06-20T01:00:00Z"}}}
    assert gg.parse_mint_result(structured) == ("ghs_abc", "2026-06-20T01:00:00Z")

    text = {"result": {"content": [{"type": "text", "text": json.dumps({"token": "ghs_xyz", "expires_at": "Z"})}]}}
    assert gg.parse_mint_result(text) == ("ghs_xyz", "Z")

    try:
        gg.parse_mint_result({"result": {}})
    except ValueError:
        pass
    else:  # pragma: no cover
        raise AssertionError("expected fail-closed on missing token")


# ---- recording (mirror ControlActionAuditor body) -------------------------

def test_record_body_matches_auditor_shape() -> None:
    body = gg.build_record_body(
        invocation_id="ctrl_deadbeef",
        source_tool="git",
        action="github.pull_request.open",
        status="succeeded",
        target_ref="https://github.com/romaine-life/tank-operator/pull/1372",
        repo_owner="romaine-life",
        repo_name="tank-operator",
        pr_number=1372,
    )
    assert body["event_id"] == "ctrl_deadbeef_succeeded"
    assert body["invocation_id"] == "ctrl_deadbeef"
    assert body["source_service"] == "agent-egress-proxy"
    assert body["action"] == "github.pull_request.open"
    assert body["status"] == "succeeded"
    assert body["target_ref"].endswith("/pull/1372")
    assert body["pr_number"] == 1372
    # superset of the auditor's required keys
    for k in ("repo_owner", "repo_name", "result_sha", "error", "payload", "target_kind"):
        assert k in body


def test_parse_exchange_result() -> None:
    assert gg.parse_exchange_result({"token": "svc.jwt.x", "expires_at": 1700000000}) == ("svc.jwt.x", 1700000000.0)
    assert gg.parse_exchange_result({"token": "t", "expires_at": "1700000000"}) == ("t", 1700000000.0)
    # missing token -> fail closed (caller gates on the empty token; expiry is moot)
    assert gg.parse_exchange_result({"expires_at": 1})[0] == ""
    # non-numeric expiry -> 0.0 (caller applies a default TTL)
    assert gg.parse_exchange_result({"token": "t", "expires_at": "2026-06-20T00:00:00Z"}) == ("t", 0.0)
    assert gg.parse_exchange_result("nope") == ("", 0.0)


def test_jwt_from_authorization_token_bearer_and_basic() -> None:
    # `gh` and the GitHub REST API default to the `token` scheme — this is the leg that
    # was missing, leaving every gh API call unminted and 401'd in restricted sessions.
    assert gg.jwt_from_authorization("token aa.bb.cc") == "aa.bb.cc"
    # raw curl / REST clients send Bearer.
    assert gg.jwt_from_authorization("Bearer aa.bb.cc") == "aa.bb.cc"
    # git smart-HTTP: credential helper hands the JWT as the Basic password.
    basic = base64.b64encode(b"x-access-token:aa.bb.cc").decode()
    assert gg.jwt_from_authorization(f"Basic {basic}") == "aa.bb.cc"
    assert gg.jwt_from_authorization("") == ""
    assert gg.jwt_from_authorization("Basic !!notbase64!!") == ""
    assert gg.jwt_from_authorization("Negotiate xyz") == ""  # unknown scheme -> nothing


def test_first_json_object_sse_and_bare() -> None:
    sse = 'event: message\ndata: {"result": {"structuredContent": {"token": "ghs_1"}}}\n\n'
    assert gg.first_json_object(sse) == {"result": {"structuredContent": {"token": "ghs_1"}}}
    assert gg.first_json_object('{"a": 1}') == {"a": 1}
    assert gg.first_json_object("nope") == {}
    # end-to-end with the mint parser, mirroring the real reply path
    assert gg.parse_mint_result(gg.first_json_object(sse))[0] == "ghs_1"


def test_pr_fields_from_response_json() -> None:
    created = json.dumps(
        {"number": 1372, "html_url": "https://github.com/romaine-life/tank-operator/pull/1372", "state": "open"}
    )
    assert gg.pr_fields_from_response_json(created) == (
        1372,
        "https://github.com/romaine-life/tank-operator/pull/1372",
    )
    assert gg.pr_fields_from_response_json('{"merged": true, "sha": "x"}') == (0, "")
    assert gg.pr_fields_from_response_json("not json") == (0, "")


# ---- break-glass grants (widen the lane; mirror mcp-auth-proxy) ------------

def _grant(
    kind: str,
    *,
    branches=None,
    count: int = 0,
    operations=None,
    event_id: str = "ev-123",
    expires_at: str = "2099-01-01T00:00:00Z",
) -> dict:
    """The shape handleInternalGetGitBreakGlassGrant returns for an active grant."""
    bs: dict = {"kind": kind}
    if branches is not None:
        bs["branches"] = branches
    if count:
        bs["count"] = count
    return {
        "active": True,
        "event_id": event_id,
        "branch_scope": bs,
        "operations": list(operations or []),
        "expires_at": expires_at,
    }


def test_parse_push_commands_exposes_old_new_ref() -> None:
    body = (
        _pkt(_ZERO + b" " + b"a" * 40 + b" refs/heads/feature\x00 report-status\n")
        + _pkt(b"b" * 40 + b" " + b"c" * 40 + b" refs/heads/other\n")
        + _FLUSH
        + b"PACK.."
    )
    cmds, saw_flush = gg.parse_push_commands(body)
    assert saw_flush is True
    assert cmds[0] == ("0" * 40, "a" * 40, "refs/heads/feature")
    assert cmds[1] == ("b" * 40, "c" * 40, "refs/heads/other")
    # parse_push_refs stays a thin wrapper over the same parse
    assert gg.parse_push_refs(body) == (["refs/heads/feature", "refs/heads/other"], True)


def test_active_grant_from_response_only_honors_active_true() -> None:
    assert gg.active_grant_from_response(204, "") is None
    assert gg.active_grant_from_response(200, "  ") is None
    # a non-2xx body fails closed even if it claims active
    assert gg.active_grant_from_response(500, '{"active": true}') is None
    assert gg.active_grant_from_response(200, "not json") is None
    assert gg.active_grant_from_response(200, '{"active": false, "repo": "o/r"}') is None
    g = gg.active_grant_from_response(200, '{"active": true, "event_id": "e1"}')
    assert g is not None and g["event_id"] == "e1"


def test_grant_branch_allows_mirrors_in_pod_matcher() -> None:
    # unlimited -> any ref
    assert gg.grant_branch_allows(_grant("unlimited"), "refs/heads/main") is True
    # named -> exact short-name match (refs/heads/ stripped both sides)
    named = _grant("named", branches=["release-1.0", "refs/heads/hotfix"])
    assert gg.grant_branch_allows(named, "refs/heads/release-1.0") is True
    assert gg.grant_branch_allows(named, "refs/heads/hotfix") is True
    assert gg.grant_branch_allows(named, "refs/heads/main") is False
    assert gg.grant_branch_allows(named, "release-1.0") is True
    # count -> any ref (budget enforced by the endpoint flipping active->false)
    assert gg.grant_branch_allows(_grant("count", count=3), "refs/heads/anything") is True
    # no grant / malformed scope / empty named set -> nothing
    assert gg.grant_branch_allows(None, "refs/heads/main") is False
    assert gg.grant_branch_allows({"branch_scope": "bad"}, "refs/heads/main") is False
    assert gg.grant_branch_allows(_grant("named", branches=[]), "refs/heads/x") is False


def test_out_of_lane_refs() -> None:
    sid = "1166"
    lane = "refs/heads/tank/session/1166/"
    assert gg.out_of_lane_refs([lane + "a", lane + "b"], sid) == []
    assert gg.out_of_lane_refs([lane + "a", "refs/heads/main"], sid) == ["refs/heads/main"]
    # empty session -> everything is out of lane (fail closed)
    assert gg.out_of_lane_refs(["refs/heads/x"], "") == ["refs/heads/x"]


def test_push_violation_widens_with_grant() -> None:
    sid = "1166"
    lane = "refs/heads/tank/session/1166/"
    # in-lane is always fine, grant or not
    assert gg.push_violation([lane + "tank-operator"], sid, None) is None
    # out-of-lane denied without a grant (unchanged no-grant behavior)
    assert gg.push_violation(["refs/heads/release-1.0"], sid, None) == "refs/heads/release-1.0"
    # a named grant covers exactly its branch; a different out-of-lane ref still violates
    named = _grant("named", branches=["release-1.0"])
    assert gg.push_violation(["refs/heads/release-1.0"], sid, named) is None
    assert gg.push_violation(["refs/heads/release-1.0", "refs/heads/main"], sid, named) == "refs/heads/main"
    # the session's own lane is still allowed alongside a granted branch
    assert gg.push_violation([lane + "x", "refs/heads/release-1.0"], sid, named) is None
    # unlimited grant covers anything (incl. main)
    assert gg.push_violation(["refs/heads/main"], sid, _grant("unlimited")) is None
    # count grant covers any branch
    assert gg.push_violation(["refs/heads/wip"], sid, _grant("count", count=2)) is None
    # empty session id fails closed without a grant (in practice ident.ok gates this
    # path, so push_violation is only ever reached with a real session id)
    assert gg.push_violation([lane + "x"], "", None) == lane + "x"


def test_grant_allows_merge_only_for_full_api_grants() -> None:
    full = _grant("unlimited", operations=["push_current_head", "full_github_api"])
    assert gg.grant_allows_merge(full) is True
    assert gg.grant_allows_merge(_grant("named", branches=["x"], operations=["push_current_head"])) is False
    assert gg.grant_allows_merge(_grant("count", count=2)) is False
    assert gg.grant_allows_merge(None) is False


def test_grant_event_id() -> None:
    assert gg.grant_event_id(_grant("unlimited", event_id="ev-9")) == "ev-9"
    assert gg.grant_event_id(None) == ""


def test_grant_servable_respects_expiry() -> None:
    # negative result always servable within the cache TTL
    assert gg.grant_servable(None, 1_000_000.0) is True
    # a positive grant is servable only before its own expires_at (epoch 946684800)
    g = _grant("unlimited", expires_at="2000-01-01T00:00:00Z")
    assert gg.grant_servable(g, 946684700.0) is True
    assert gg.grant_servable(g, 946684900.0) is False
    # missing/garbage expiry -> never serve (can't bound it)
    assert gg.grant_servable({"active": True}, 1.0) is False


def test_build_break_glass_push_body_matches_count_tracking_shape() -> None:
    body = gg.build_break_glass_push_body(
        invocation_id="ctrl_abc",
        grant_event_id="ev-123",
        repo_owner="romaine-life",
        repo_name="tank-operator",
        branch="release-1.0",
        new_sha="f" * 40,
    )
    assert body["action"] == "github.break_glass.push"
    assert body["status"] == "succeeded"
    assert body["source_service"] == "agent-egress-proxy"
    # the two fields countBreakGlassGrantBranches keys on
    assert body["payload"]["grant_event_id"] == "ev-123"
    assert body["payload"]["branch"] == "release-1.0"
    assert body["payload"]["repo_path"] == "romaine-life/tank-operator"
    # a sha promotes the target to the commit
    assert body["target_kind"] == "github_commit"
    assert body["target_ref"] == "https://github.com/romaine-life/tank-operator/commit/" + "f" * 40
    # no sha -> repository target
    no_sha = gg.build_break_glass_push_body(
        invocation_id="ctrl_x", grant_event_id="e", repo_owner="o", repo_name="r", branch="b",
    )
    assert no_sha["target_kind"] == "github_repository"
    assert no_sha["target_ref"] == "https://github.com/o/r"


def test_break_glass_grant_url_routes_by_scope_and_repo() -> None:
    # default scope -> default orchestrator, repo as an encoded query param
    url = gg.break_glass_grant_url("tank", "1166", "romaine-life/tank-operator", DEFAULT_URL)
    assert url == (
        DEFAULT_URL
        + "/api/internal/sessions/1166/git-break-glass/grant?repo=romaine-life%2Ftank-operator"
    )
    # slot scope -> slot orchestrator (mirrors url_for_scope)
    slot = gg.break_glass_grant_url("tank-operator-slot-3", "47", "o/r", DEFAULT_URL)
    assert slot == (
        "http://tank-operator.tank-operator-slot-3.svc:80"
        + "/api/internal/sessions/47/git-break-glass/grant?repo=o%2Fr"
    )
    # no repo -> no query param
    assert gg.break_glass_grant_url("tank", "1166", "", DEFAULT_URL).endswith("/git-break-glass/grant")


# ---- mint scope: least privilege per request (the REST write-hole closer) ---

def _ident() -> "gg.SessionIdentity":
    return gg.identity_from_jwt(_jwt({"sub": "svc:tank:1", "role": "service", "actor_email": "a@b.c"}))


def test_evaluate_policy_scopes_mint_by_request() -> None:
    ID = _ident()
    # git push BOTH legs -> write (the advertisement GET needs receive-pack too, or the
    # push 403s before it starts); NOT full; lane-confined in body phase
    d = gg.evaluate_policy(ID, "POST", "github.com", "/romaine-life/tank-operator/git-receive-pack")
    assert d.write is True and d.full is False
    d = gg.evaluate_policy(ID, "GET", "github.com", "/o/r/info/refs?service=git-receive-pack")
    assert d.write is True and d.full is False
    # PR open (POST /pulls) -> full (needs pull_requests:write); the one governed REST write
    d = gg.evaluate_policy(ID, "POST", "api.github.com", "/repos/o/r/pulls")
    assert d.full is True
    # REST read -> read-only
    d = gg.evaluate_policy(ID, "GET", "api.github.com", "/repos/o/r")
    assert d.write is False and d.full is False
    # clone/fetch (upload-pack) -> read-only
    d = gg.evaluate_policy(ID, "GET", "github.com", "/o/r/info/refs?service=git-upload-pack")
    assert d.write is False and d.full is False
    # THE HOLE CLOSERS: every ungoverned REST write mints READ (GitHub then 403s it)
    for m, p in [
        ("PUT", "/repos/o/r/contents/x"),
        ("PATCH", "/repos/o/r/git/refs/heads/main"),
        ("POST", "/repos/o/r/git/refs"),
        ("POST", "/repos/o/r/merges"),
        ("DELETE", "/repos/o/r/git/refs/heads/x"),
    ]:
        d = gg.evaluate_policy(ID, m, "api.github.com", p)
        assert d.write is False and d.full is False, f"{m} {p} must mint read-only, got write={d.write} full={d.full}"


def test_is_push_intent_covers_both_push_legs() -> None:
    # both legs of a push need write
    assert gg.is_push_intent("github.com", "/o/r.git/git-receive-pack") is True
    assert gg.is_push_intent("github.com", "/o/r/info/refs?service=git-receive-pack") is True
    # fetch legs are reads
    assert gg.is_push_intent("github.com", "/o/r/info/refs?service=git-upload-pack") is False
    assert gg.is_push_intent("github.com", "/o/r.git/git-upload-pack") is False
    # REST host is not a git push
    assert gg.is_push_intent("api.github.com", "/repos/o/r/pulls") is False


def test_is_rest_write() -> None:
    assert gg.is_rest_write("PUT", "api.github.com", "/repos/o/r/contents/x") is True
    assert gg.is_rest_write("POST", "api.github.com", "/repos/o/r/merges") is True
    assert gg.is_rest_write("DELETE", "api.github.com", "/repos/o/r/git/refs/heads/x") is True
    assert gg.is_rest_write("GET", "api.github.com", "/repos/o/r") is False  # read, not a write
    assert gg.is_rest_write("POST", "github.com", "/o/r/git-receive-pack") is False  # git transport, not REST
    assert gg.is_rest_write("POST", "api.github.com", "/zen") is False  # no repo in path
