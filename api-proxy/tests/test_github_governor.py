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
