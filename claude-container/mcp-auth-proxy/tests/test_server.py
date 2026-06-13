from __future__ import annotations

import asyncio
import json
import subprocess
from datetime import datetime, timedelta, timezone

import pytest
from aiohttp import ClientSession, ClientTimeout, web
from aiohttp.test_utils import TestClient, TestServer

from mcp_auth_proxy.server import (
    LISTENERS,
    _MAX_UPSTREAM_ATTEMPTS,
    AuthRomaineServiceProvider,
    SPIRELENS_MCP_PORT,
    _append_ci_reminder,
    _append_tank_publish_tool,
    _break_glass_approval_url,
    _activate_break_glass_mcp_config,
    _checks_state,
    _effective_listeners,
    _first_pr_from_response,
    _github_tool_block_response,
    _prepare_glimmung_hot_swap_call,
    _handle_tank_break_glass_tool,
    _handle_break_glass_mcp,
    _json_objects_from_mcp_body,
    _make_handler,
    _mint_github_installation_token,
    _parse_mcp_tool_call,
    _push_head_with_token,
    _repo_slug_from_remote,
)


class _FakeResponse:
    def __init__(self, status: int, body: dict | None = None, text: str = "") -> None:
        self.status = status
        self._body = body or {}
        self._text = text

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_args):
        return False

    async def json(self) -> dict:
        return self._body

    async def text(self) -> str:
        return self._text


class _FakeHTTP:
    def __init__(self, response: _FakeResponse) -> None:
        self.response = response
        self.calls: list[dict] = []

    def post(self, url: str, *, headers: dict, json: dict):
        self.calls.append({"url": url, "headers": headers, "json": json})
        return self.response


class _FakeRawResponse:
    def __init__(self, status: int, body: bytes) -> None:
        self.status = status
        self._body = body

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_args):
        return False

    async def read(self) -> bytes:
        return self._body

    async def text(self) -> str:
        return self._body.decode("utf-8")


class _FakeRawHTTP:
    def __init__(self, response: _FakeRawResponse) -> None:
        self.response = response
        self.calls: list[dict] = []

    def post(self, url: str, *, headers: dict, json: dict):
        self.calls.append({"url": url, "headers": headers, "json": json})
        return self.response

    def get(self, url: str, *, headers: dict):
        self.calls.append({"url": url, "headers": headers})
        return self.response


class _FakeRawHTTPByMethod:
    def __init__(self, *, get_response: _FakeRawResponse, post_response: _FakeRawResponse) -> None:
        self.get_response = get_response
        self.post_response = post_response
        self.calls: list[dict] = []

    def post(self, url: str, *, headers: dict, json: dict):
        self.calls.append({"method": "POST", "url": url, "headers": headers, "json": json})
        return self.post_response

    def get(self, url: str, *, headers: dict):
        self.calls.append({"method": "GET", "url": url, "headers": headers})
        return self.get_response


class _HotSwapHTTP:
    def __init__(self, sha: str) -> None:
        self.sha = sha
        self.posts: list[dict] = []
        self.requests: list[dict] = []

    def post(self, url: str, *, headers: dict, json: dict):
        self.posts.append({"url": url, "headers": headers, "json": json})
        if "mcp-github" in url:
            body = (
                b"event: message\n"
                b'data: {"jsonrpc":"2.0","result":{"structuredContent":{"token":"github-token"}}}\n\n'
            )
            return _FakeRawResponse(200, body)
        return _FakeRawResponse(
            200,
            json_module_dumps({
                "allowed": True,
                "repo": "romaine-life/tank-operator",
                "branch": "tank/session/95/tank-operator",
                "sha": self.sha,
                "publish_verified": True,
                "ci_verified": True,
                "merge_verified": True,
                "pr_number": 1113,
            }),
        )

    def request(self, method: str, url: str, *, headers: dict, **_kwargs):
        self.requests.append({"method": method, "url": url, "headers": headers})
        if "/branches/" in url:
            return _FakeRawResponse(200, json_module_dumps({"commit": {"sha": self.sha}}))
        if "/pulls?head=" in url:
            return _FakeRawResponse(
                200,
                json_module_dumps([
                    {
                        "number": 1113,
                        "html_url": "https://github.com/romaine-life/tank-operator/pull/1113",
                        "head": {"sha": self.sha},
                    }
                ]),
            )
        if "/pulls/1113" in url:
            return _FakeRawResponse(200, json_module_dumps({"mergeable": True, "mergeable_state": "clean"}))
        if "/check-runs" in url:
            return _FakeRawResponse(
                200,
                json_module_dumps({"check_runs": [{"name": "test", "status": "completed", "conclusion": "success"}]}),
            )
        if "/status" in url:
            return _FakeRawResponse(200, json_module_dumps({"state": "success", "statuses": []}))
        return _FakeRawResponse(404, b"{}")


def json_module_dumps(value: dict | list) -> bytes:
    return json.dumps(value).encode("utf-8")


def test_auth_romaine_service_provider_exchanges_sa_token(tmp_path) -> None:
    token_path = tmp_path / "token"
    token_path.write_text("pod-sa-token\n", encoding="utf-8")
    expires_at = datetime.now(timezone.utc) + timedelta(minutes=15)
    http = _FakeHTTP(
        _FakeResponse(
            200,
            {
                "token": "auth-romaine-service-jwt",
                "expires_at": expires_at.isoformat().replace("+00:00", "Z"),
            },
        )
    )
    provider = AuthRomaineServiceProvider(
        http,
        exchange_url="https://auth.romaine.life/api/auth/exchange/k8s",
        token_path=token_path,
    )

    token = asyncio.run(provider.token())
    token_again = asyncio.run(provider.token())

    assert token == "auth-romaine-service-jwt"
    assert token_again == "auth-romaine-service-jwt"
    # Second call hits cache; only one outbound exchange.
    assert len(http.calls) == 1
    assert http.calls[0] == {
        "url": "https://auth.romaine.life/api/auth/exchange/k8s",
        "headers": {"Authorization": "Bearer pod-sa-token"},
        "json": {},
    }


def test_auth_romaine_service_provider_requires_exchange_url(tmp_path) -> None:
    token_path = tmp_path / "token"
    token_path.write_text("pod-sa-token\n", encoding="utf-8")
    provider = AuthRomaineServiceProvider(
        _FakeHTTP(_FakeResponse(200)),
        exchange_url="",
        token_path=token_path,
    )

    with pytest.raises(RuntimeError, match="AUTH_ROMAINE_EXCHANGE_URL"):
        asyncio.run(provider.token())


def test_auth_romaine_service_provider_rejects_unauthenticated_response(tmp_path) -> None:
    token_path = tmp_path / "token"
    token_path.write_text("pod-sa-token\n", encoding="utf-8")
    http = _FakeHTTP(_FakeResponse(401, text="upstream rejected"))
    provider = AuthRomaineServiceProvider(
        http,
        exchange_url="https://auth.romaine.life/api/auth/exchange/k8s",
        token_path=token_path,
    )

    with pytest.raises(RuntimeError, match="returned 401"):
        asyncio.run(provider.token())


def test_auth_romaine_service_provider_rejects_expired_response(tmp_path) -> None:
    # exchange responded 200 with a token whose `expires_at` is already
    # in the past — provider must refuse rather than cache+serve.
    token_path = tmp_path / "token"
    token_path.write_text("pod-sa-token\n", encoding="utf-8")
    expired = datetime.now(timezone.utc) - timedelta(seconds=5)
    http = _FakeHTTP(
        _FakeResponse(
            200,
            {
                "token": "stale",
                "expires_at": expired.isoformat().replace("+00:00", "Z"),
            },
        )
    )
    provider = AuthRomaineServiceProvider(
        http,
        exchange_url="https://auth.romaine.life/api/auth/exchange/k8s",
        token_path=token_path,
    )

    with pytest.raises(RuntimeError, match="response was invalid"):
        asyncio.run(provider.token())


def test_effective_listeners_omit_spirelens_by_default() -> None:
    listeners = _effective_listeners("")

    assert listeners == LISTENERS
    assert all(port != SPIRELENS_MCP_PORT for port, _upstream in listeners)


def test_effective_listeners_add_spirelens_when_configured() -> None:
    upstream = "http://nelsonlaptop:15527"
    listeners = _effective_listeners(upstream)

    assert listeners[:-1] == LISTENERS
    assert listeners[-1] == (SPIRELENS_MCP_PORT, upstream)


def test_github_tool_call_parser_recognizes_create_pull_request() -> None:
    body = json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": "create_pull_request",
            "arguments": {"owner": "romaine-life", "name": "tank-operator"},
        },
    }).encode()

    parsed = _parse_mcp_tool_call(body)

    assert parsed == (
        "create_pull_request",
        {"owner": "romaine-life", "name": "tank-operator"},
    )


def test_first_pr_from_mcp_response_finds_pull_request_url() -> None:
    response = [{
        "result": {
            "structuredContent": {
                "url": "https://github.com/romaine-life/tank-operator/pull/123",
            }
        }
    }]

    assert _first_pr_from_response(response) == (
        "romaine-life",
        "tank-operator",
        "https://github.com/romaine-life/tank-operator/pull/123",
        123,
    )


def test_append_ci_reminder_augments_sse_mcp_result() -> None:
    raw = b'data: {"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"created"}]}}\n\n'

    augmented = _append_ci_reminder(raw)

    text = augmented.decode()
    assert "created" in text
    assert "watch the PR's current HEAD CI checks until they pass" in text


def test_mcp_sse_parser_accepts_event_prefixed_response() -> None:
    raw = (
        b"event: message\n"
        b'data: {"jsonrpc":"2.0","id":"debug","result":{"structuredContent":{"token":"abc123"}}}\n\n'
    )

    parsed = _json_objects_from_mcp_body(raw)

    assert parsed == [{
        "jsonrpc": "2.0",
        "id": "debug",
        "result": {"structuredContent": {"token": "abc123"}},
    }]


def test_mint_github_installation_token_requests_write_scope() -> None:
    body = (
        b"event: message\n"
        b'data: {"jsonrpc":"2.0","result":{"structuredContent":{"token":"write-token"}}}\n\n'
    )
    http = _FakeRawHTTP(_FakeRawResponse(200, body))

    token = asyncio.run(_mint_github_installation_token(http, "auth-token", "romaine-life/tank-operator"))

    assert token == "write-token"
    assert http.calls[0]["json"]["params"]["arguments"] == {
        "repos": ["romaine-life/tank-operator"],
        "write": True,
    }


def test_push_head_with_token_bypasses_local_pre_push_hook(monkeypatch, tmp_path) -> None:
    calls: list[tuple[tuple[str, ...], dict | None]] = []

    async def fake_run_git(repo_path, *args, env=None, timeout=None):
        calls.append((args, env))
        return 0, "", ""

    monkeypatch.setattr("mcp_auth_proxy.server._run_git", fake_run_git)

    asyncio.run(_push_head_with_token(tmp_path, "tank/session/1/repo", "token"))

    args, env = calls[0]
    assert args[:3] == ("push", "--no-verify", "origin")
    assert args[3] == "HEAD:refs/heads/tank/session/1/repo"
    assert env["GIT_TERMINAL_PROMPT"] == "0"
    assert env["GITHUB_TOKEN"] == "token"


def test_glimmung_hot_swap_gate_rewrites_git_ref_to_verified_sha(monkeypatch, tmp_path) -> None:
    workspace = tmp_path
    repo = workspace / "tank-operator"
    repo.mkdir()
    subprocess.run(["git", "init"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(["git", "config", "user.email", "agent@example.test"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.name", "Agent"], cwd=repo, check=True)
    (repo / "README.md").write_text("test\n", encoding="utf-8")
    subprocess.run(["git", "add", "README.md"], cwd=repo, check=True)
    subprocess.run(["git", "commit", "-m", "initial"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(["git", "checkout", "-b", "tank/session/95/tank-operator"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(["git", "remote", "add", "origin", "https://github.com/romaine-life/tank-operator.git"], cwd=repo, check=True)
    sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repo, text=True).strip()
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", workspace)
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    body = json.dumps({
        "jsonrpc": "2.0",
        "id": 12,
        "method": "tools/call",
        "params": {
            "name": "apply_test_slot_hot_swap",
            "arguments": {
                "project": "tank-operator",
                "slot_index": 2,
                "artifact_kind": "codex_runner",
                "validation_target": "existing_session",
                "git_ref": "tank/session/95/tank-operator",
                "repo_path": str(repo),
            },
        },
    }).encode("utf-8")

    prepared = asyncio.run(
        _prepare_glimmung_hot_swap_call(
            _HotSwapHTTP(sha),
            _StaticTokenProvider("service-token"),
            12,
            body,
            json.loads(body)["params"]["arguments"],
        )
    )

    assert not hasattr(prepared, "text")
    forwarded_body, verification = prepared
    forwarded = json.loads(forwarded_body)
    forwarded_args = forwarded["params"]["arguments"]
    assert forwarded_args["git_ref"] == sha
    assert forwarded_args["project"] == "tank-operator"
    assert "repo_path" not in forwarded_args
    assert verification["sha"] == sha


def test_append_tank_publish_tool_augments_event_prefixed_sse_tools_list() -> None:
    raw = (
        b"event: message\n"
        b'data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_transcript"}]}}\n\n'
    )

    augmented = _append_tank_publish_tool(raw).decode()

    assert "event: message" in augmented
    assert "publish_current_head" in augmented
    assert "request_git_break_glass" in augmented


def test_tank_publish_tool_is_added_to_tools_list() -> None:
    raw = b'{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_transcript"}]}}'

    augmented = json.loads(_append_tank_publish_tool(raw))

    names = [tool["name"] for tool in augmented["result"]["tools"]]
    assert names == ["read_transcript", "publish_current_head", "request_git_break_glass"]
    publish = augmented["result"]["tools"][1]
    assert publish["inputSchema"]["properties"]["repo_path"]["type"] == "string"
    break_glass = augmented["result"]["tools"][2]
    assert "approval URL" in break_glass["description"]
    assert "token" not in break_glass["inputSchema"]["properties"]


def test_hot_swap_tool_schema_gets_repo_path_for_tank_gate() -> None:
    raw = json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "result": {
            "tools": [
                {
                    "name": "apply_test_slot_hot_swap",
                    "inputSchema": {
                        "type": "object",
                        "properties": {
                            "project": {"type": "string"},
                            "git_ref": {"type": "string"},
                        },
                        "additionalProperties": False,
                    },
                }
            ]
        },
    }).encode("utf-8")

    augmented = json.loads(_append_tank_publish_tool(raw))

    hot_swap = augmented["result"]["tools"][0]
    properties = hot_swap["inputSchema"]["properties"]
    assert properties["repo"]["type"] == "string"
    assert properties["repo_path"]["type"] == "string"
    assert hot_swap["inputSchema"]["additionalProperties"] is False


def test_break_glass_approval_url_carries_request_context() -> None:
    url = _break_glass_approval_url(
        "95",
        "romaine-life/tank-operator",
        "need to repair a branch conflict",
        "agent",
    )

    assert url.startswith("https://auth.romaine.life/admin?")
    assert "intent=git-break-glass" in url
    assert "session_id=95" in url
    assert "session_scope=default" in url
    assert "repo=romaine-life%2Ftank-operator" in url
    assert "reason=need+to+repair+a+branch+conflict" in url


def test_break_glass_approval_url_carries_slot_scope() -> None:
    url = _break_glass_approval_url(
        "95",
        "romaine-life/tank-operator",
        "",
        "agent",
        session_scope="tank-operator-slot-6",
    )

    assert "session_scope=tank-operator-slot-6" in url


def test_break_glass_mcp_lists_no_tools_before_activation(monkeypatch, tmp_path) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", tmp_path)

    async def run() -> dict:
        request = type("Request", (), {"read": lambda self: asyncio.sleep(0, result=b'{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}')})()
        response = await _handle_break_glass_mcp(
            _FakeRawHTTP(_FakeRawResponse(200, b"{}")),
            _StaticTokenProvider("service-token"),
            request,
        )
        return json.loads(response.text)

    payload = asyncio.run(run())

    assert payload["result"]["tools"] == []


def test_break_glass_mcp_rejects_call_before_activation(monkeypatch, tmp_path) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", tmp_path)

    async def run() -> dict:
        body = {
            "jsonrpc": "2.0",
            "id": 8,
            "method": "tools/call",
            "params": {
                "name": "mint_full_git_token",
                "arguments": {"repo": "romaine-life/tank-operator"},
            },
        }
        request = type("Request", (), {"read": lambda self: asyncio.sleep(0, result=json.dumps(body).encode("utf-8"))})()
        response = await _handle_break_glass_mcp(
            _FakeRawHTTP(_FakeRawResponse(200, b'{"active":true}')),
            _StaticTokenProvider("service-token"),
            request,
        )
        return json.loads(response.text)

    payload = asyncio.run(run())

    assert payload["error"]["code"] == -32022
    assert "not activated" in payload["error"]["message"]


def test_break_glass_activation_writes_separate_mcp_entry(monkeypatch, tmp_path) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", tmp_path)
    (tmp_path / ".mcp.json").write_text('{"mcpServers":{"tank-operator":{"type":"http","url":"http://127.0.0.1:9996/"}}}', encoding="utf-8")
    (tmp_path / ".tank" / "codex").mkdir(parents=True)
    (tmp_path / ".tank" / "codex" / "config.toml").write_text('[mcp_servers.tank-operator]\nurl = "http://127.0.0.1:9996/"\n', encoding="utf-8")
    (tmp_path / ".tank" / "claude").mkdir(parents=True)
    (tmp_path / ".tank" / "claude" / "settings.json").write_text('{"permissions":{"allow":["mcp__tank-operator"]}}', encoding="utf-8")

    activation = _activate_break_glass_mcp_config(
        "romaine-life/tank-operator",
        {"event_id": "grant-1", "expires_at": "2026-06-12T23:00:00Z"},
    )

    mcp_config = json.loads((tmp_path / ".mcp.json").read_text(encoding="utf-8"))
    assert mcp_config["mcpServers"]["tank-git-break-glass"]["url"] == "http://127.0.0.1:9999/"
    marker = json.loads((tmp_path / ".tank" / "git-break-glass-active.json").read_text(encoding="utf-8"))
    assert marker["repo"] == "romaine-life/tank-operator"
    assert marker["grant_event_id"] == "grant-1"
    assert "tank-git-break-glass" in (tmp_path / ".tank" / "codex" / "config.toml").read_text(encoding="utf-8")
    settings = json.loads((tmp_path / ".tank" / "claude" / "settings.json").read_text(encoding="utf-8"))
    assert "mcp__tank-git-break-glass" in settings["permissions"]["allow"]
    assert activation["reload_required"] is True


def test_break_glass_activation_tolerates_read_only_workspace_mcp(monkeypatch, tmp_path) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", tmp_path)
    mcp_path = tmp_path / ".mcp.json"
    mcp_path.write_text('{"mcpServers":{"tank-operator":{"type":"http","url":"http://127.0.0.1:9996/"}}}', encoding="utf-8")
    mcp_path.chmod(0o444)
    (tmp_path / ".tank" / "codex").mkdir(parents=True)
    codex_config = tmp_path / ".tank" / "codex" / "config.toml"
    codex_config.write_text('[mcp_servers.tank-operator]\nurl = "http://127.0.0.1:9996/"\n', encoding="utf-8")

    try:
        activation = _activate_break_glass_mcp_config(
            "romaine-life/tank-operator",
            {"event_id": "grant-1", "expires_at": "2026-06-12T23:00:00Z"},
        )
    finally:
        mcp_path.chmod(0o644)

    assert activation["reload_required"] is True
    assert str(tmp_path / ".tank" / "git-break-glass-active.json") in activation["changed_files"]
    assert str(codex_config) in activation["changed_files"]
    assert "tank-git-break-glass" in codex_config.read_text(encoding="utf-8")


def test_tank_break_glass_tool_records_request_without_revealing_token(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b"not-json"),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_break_glass_tool(
            http,
            _StaticTokenProvider("service-token"),
            9,
            {"repo": "romaine-life/tank-operator", "reason": "need branch repair"},
        )
    )

    payload = json.loads(response.text)
    assert payload["result"]["structuredContent"]["approval_url"].startswith("https://auth.romaine.life/admin?")
    assert payload["result"]["structuredContent"]["privileged_tools_visible"] is False
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    recorded = recorded_call["json"]
    assert recorded["action"] == "github.break_glass.request"
    assert recorded["source_tool"] == "request_git_break_glass"
    assert recorded["target_ref"] == "https://github.com/romaine-life/tank-operator"
    assert recorded["payload"]["reason"] == "need branch repair"
    assert recorded_call["headers"]["Authorization"] == "Bearer service-token"


def test_github_write_tool_block_response_returns_mcp_error() -> None:
    body = json.dumps({
        "jsonrpc": "2.0",
        "id": 7,
        "method": "tools/call",
        "params": {"name": "mint_clone_token", "arguments": {"repos": ["romaine-life/tank-operator"]}},
    }).encode()

    response = _github_tool_block_response(body, "mint_clone_token")

    assert response is not None
    payload = json.loads(response.text)
    assert payload["id"] == 7
    assert payload["error"]["data"]["replacement_tool"] == "publish_current_head"
    assert payload["error"]["data"]["break_glass_tool"] == "request_git_break_glass"


def test_repo_slug_from_remote_accepts_https_and_ssh() -> None:
    assert _repo_slug_from_remote("https://github.com/romaine-life/tank-operator.git") == (
        "romaine-life",
        "tank-operator",
    )
    assert _repo_slug_from_remote("git@github.com:romaine-life/tank-operator.git") == (
        "romaine-life",
        "tank-operator",
    )


def test_checks_state_classifies_pending_failure_and_success() -> None:
    assert _checks_state([], None)[0] == "started"
    succeeded_with_empty_legacy_status, _, _ = _checks_state(
        [{"name": "test", "status": "completed", "conclusion": "success"}],
        {"state": "pending", "statuses": [], "total_count": 0},
    )
    assert succeeded_with_empty_legacy_status == "succeeded"
    failed, error, payload = _checks_state(
        [{"name": "test", "status": "completed", "conclusion": "failure"}],
        {"state": "success", "statuses": []},
    )
    assert failed == "failed"
    assert "test: failure" in error
    assert payload["failed"] == ["test: failure"]
    succeeded, _, success_payload = _checks_state(
        [{"name": "test", "status": "completed", "conclusion": "success"}],
        {"state": "success", "statuses": []},
    )
    assert succeeded == "succeeded"
    assert success_payload["completed"] == 1


# --- retry-loop tests ----------------------------------------------
#
# The handler exists to prevent an unrecoverable "MCP server not
# connected" state in the Claude agent SDK. The relevant failure
# modes:
#   - upstream transport error during pod rotation (no response yet,
#     safe to retry the whole call)
#   - upstream 502/503/504 returned by the kube-rbac-proxy sidecar
#     while the MCP container restarts (safe to retry; no body has
#     been streamed back to the client yet)
#   - terminal exhaustion (must return JSON-shaped 502 so the SDK's
#     JSON parser doesn't crash on a plain-text body and leave the
#     transport stuck for the rest of the session)


class _StaticTokenProvider:
    def __init__(self, token: str = "test-token") -> None:
        self._token = token

    async def token(self) -> str:
        return self._token


async def _flapping_upstream_app(status_sequence: list[int], success_body: bytes) -> tuple[web.Application, list[int]]:
    """Upstream test app that returns each status in `status_sequence`
    in order, then 200 with `success_body` from then on. Records every
    call's index so tests can assert how many attempts the proxy made."""
    call_index = [0]

    async def handler(request: web.Request) -> web.Response:
        idx = call_index[0]
        call_index[0] += 1
        if idx < len(status_sequence):
            return web.Response(status=status_sequence[idx], text=f"transient {status_sequence[idx]}")
        return web.Response(status=200, body=success_body, content_type="application/json")

    app = web.Application()
    app.router.add_route("*", "/{tail:.*}", handler)
    return app, call_index


async def _proxy_app_for(upstream_url: str, http: ClientSession) -> web.Application:
    handler = _make_handler(upstream_url, http, _StaticTokenProvider())
    app = web.Application()
    app.router.add_route("*", "/{tail:.*}", handler)
    return app


def test_handler_forwards_static_caller_context_headers() -> None:
    async def run() -> dict[str, str]:
        seen: dict[str, str] = {}

        async def handler(request: web.Request) -> web.Response:
            seen.update(dict(request.headers))
            return web.json_response({"ok": True})

        upstream_app = web.Application()
        upstream_app.router.add_route("*", "/{tail:.*}", handler)
        upstream_server = TestServer(upstream_app)
        await upstream_server.start_server()
        try:
            http = ClientSession(timeout=ClientTimeout(total=10, sock_connect=2))
            try:
                upstream_url = f"http://{upstream_server.host}:{upstream_server.port}"
                proxy_app = web.Application()
                proxy_app.router.add_route(
                    "*",
                    "/{tail:.*}",
                    _make_handler(
                        upstream_url,
                        http,
                        _StaticTokenProvider("sa-token"),
                        static_headers={
                            "X-Tank-Caller-System": "tank-operator",
                            "X-Tank-Caller-Kind": "session",
                            "X-Tank-Caller-Session-Id": "709",
                            "X-Tank-Caller-Session-Scope": "default",
                            "X-Tank-Origin-Session-Avatar-Id": "jp1-grant",
                        },
                    ),
                )
                proxy_server = TestServer(proxy_app)
                client = TestClient(proxy_server)
                await client.start_server()
                try:
                    resp = await client.post("/mcp/some/path", data=b"{}")
                    assert resp.status == 200
                    return seen
                finally:
                    await client.close()
            finally:
                await http.close()
        finally:
            await upstream_server.close()

    headers = asyncio.run(run())
    assert headers["Authorization"] == "Bearer sa-token"
    assert headers["X-Tank-Caller-System"] == "tank-operator"
    assert headers["X-Tank-Caller-Kind"] == "session"
    assert headers["X-Tank-Caller-Session-Id"] == "709"
    assert headers["X-Tank-Caller-Session-Scope"] == "default"
    assert headers["X-Tank-Origin-Session-Avatar-Id"] == "jp1-grant"


async def _run_proxy_against_upstream(status_sequence: list[int], success_body: bytes = b'{"ok":true}') -> tuple[int, str, int]:
    """Wire upstream + proxy via TestServer, POST one request through,
    return (response_status, response_text, upstream_call_count)."""
    upstream_app, call_index = await _flapping_upstream_app(status_sequence, success_body)
    upstream_server = TestServer(upstream_app)
    await upstream_server.start_server()
    try:
        http = ClientSession(timeout=ClientTimeout(total=10, sock_connect=2))
        try:
            upstream_url = f"http://{upstream_server.host}:{upstream_server.port}"
            proxy_app = await _proxy_app_for(upstream_url, http)
            proxy_server = TestServer(proxy_app)
            client = TestClient(proxy_server)
            await client.start_server()
            try:
                resp = await client.post("/mcp/some/path", data=b'{"jsonrpc":"2.0"}')
                text = await resp.text()
                return resp.status, text, call_index[0]
            finally:
                await client.close()
        finally:
            await http.close()
    finally:
        await upstream_server.close()


def test_handler_retries_transient_5xx_then_succeeds() -> None:
    # First call: 503 (kube-rbac-proxy returning transient while MCP
    # pod restarts). Second call: 200. Proxy must hide the 503 from
    # the client.
    status, body, calls = asyncio.run(
        _run_proxy_against_upstream([503], success_body=b'{"jsonrpc":"2.0","result":"ok"}')
    )
    assert status == 200
    assert body == '{"jsonrpc":"2.0","result":"ok"}'
    assert calls == 2  # one failed, one succeeded


def test_handler_retries_502_then_504_then_succeeds() -> None:
    # Worst case within budget: two transient statuses then success.
    status, body, calls = asyncio.run(
        _run_proxy_against_upstream([502, 504], success_body=b'{"ok":1}')
    )
    assert status == 200
    assert body == '{"ok":1}'
    assert calls == 3


def test_handler_returns_json_502_after_budget_exhausted() -> None:
    # All three attempts return 502 — must surface as a JSON-shaped
    # 502 so the SDK's MCP transport parses it cleanly instead of
    # leaving the server marked "not connected".
    status, body, calls = asyncio.run(
        _run_proxy_against_upstream([502, 502, 502])
    )
    assert status == 502
    assert calls == _MAX_UPSTREAM_ATTEMPTS == 3
    payload = json.loads(body)
    assert payload["error"] == "upstream_unavailable"
    assert payload["attempts"] == _MAX_UPSTREAM_ATTEMPTS
    assert "mcp_server" in payload


def test_handler_passes_non_transient_4xx_through_without_retry() -> None:
    # 401/403/404/etc. are real upstream answers — never retry them.
    # A 401 from the MCP means kube-rbac-proxy rejected our bearer;
    # retrying just spams the IdP.
    status, body, calls = asyncio.run(
        _run_proxy_against_upstream([401])
    )
    assert status == 401
    assert calls == 1
    assert body == "transient 401"


async def _run_proxy_against_dead_upstream() -> tuple[int, str]:
    """No upstream listening at all → ClientConnectorError on every
    attempt. Must exhaust the retry budget and return JSON 502."""
    http = ClientSession(timeout=ClientTimeout(total=10, sock_connect=1))
    try:
        # Reserve a port by binding then immediately closing — the
        # subsequent connect attempts get ECONNREFUSED.
        import socket
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
        s.close()
        upstream_url = f"http://127.0.0.1:{port}"
        proxy_app = await _proxy_app_for(upstream_url, http)
        proxy_server = TestServer(proxy_app)
        client = TestClient(proxy_server)
        await client.start_server()
        try:
            resp = await client.post("/mcp/some/path", data=b"{}")
            text = await resp.text()
            return resp.status, text
        finally:
            await client.close()
    finally:
        await http.close()


def test_handler_returns_json_502_on_transport_error() -> None:
    status, body = asyncio.run(_run_proxy_against_dead_upstream())
    assert status == 502
    payload = json.loads(body)
    assert payload["error"] == "upstream_unavailable"
    assert payload["attempts"] == _MAX_UPSTREAM_ATTEMPTS
    assert "transport error" in payload["error_description"]
