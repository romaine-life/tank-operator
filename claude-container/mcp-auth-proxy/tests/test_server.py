from __future__ import annotations

import asyncio
import json
import subprocess
from datetime import datetime, timedelta, timezone
from urllib.parse import parse_qs, urlparse

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
    _append_azure_break_glass_tool,
    _break_glass_approval_url,
    _azure_break_glass_approval_url,
    _activate_break_glass_mcp_config,
    _activate_azure_break_glass_mcp_config,
    _checks_state,
    _effective_listeners,
    _feature_contracts_body_status,
    _first_pr_from_response,
    _filter_github_write_tools,
    _github_tool_block_response,
    _prepare_glimmung_hot_swap_call,
    _handle_tank_break_glass_tool,
    _handle_tank_azure_break_glass_tool,
    _handle_tank_create_pr_lane_tool,
    _handle_tank_merge_tool,
    _handle_tank_rename_pr_tool,
    _handle_tank_update_pr_body_tool,
    _handle_tank_pr_lane_tool,
    _handle_break_glass_mcp,
    _json_objects_from_mcp_body,
    _make_handler,
    _mint_github_installation_token,
    _parse_mcp_tool_call,
    _push_head_with_token,
    _repo_slug_from_remote,
    _watch_published_commit,
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

    def request(self, method: str, url: str, *, headers: dict, **kwargs):
        self.requests.append({"method": method, "url": url, "headers": headers, "kwargs": kwargs})
        if method == "PUT" and "/pulls/1113/merge" in url:
            return _FakeRawResponse(200, json_module_dumps({"merged": True, "sha": "merge-sha"}))
        if method == "PATCH" and "/issues/1113" in url:
            return _FakeRawResponse(
                200,
                json_module_dumps({"html_url": "https://github.com/romaine-life/tank-operator/pull/1113"}),
            )
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
            return _FakeRawResponse(
                200,
                json_module_dumps({
                    "html_url": "https://github.com/romaine-life/tank-operator/pull/1113",
                    "mergeable": True,
                    "mergeable_state": "clean",
                    "draft": False,
                    "head": {"sha": self.sha, "ref": "tank/session/95/tank-operator"},
                }),
            )
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


def test_mint_github_installation_token_full_requests_full_scope() -> None:
    body = (
        b"event: message\n"
        b'data: {"jsonrpc":"2.0","result":{"structuredContent":{"token":"full-token"}}}\n\n'
    )
    http = _FakeRawHTTP(_FakeRawResponse(200, body))

    token = asyncio.run(
        _mint_github_installation_token(
            http, "auth-token", "romaine-life/tank-operator", full=True
        )
    )

    # Break-glass mint_full_git_token must ask mcp-github for the App's full
    # permission set (full=True), not the contents-only clone scope.
    assert token == "full-token"
    assert http.calls[0]["json"]["params"]["arguments"] == {
        "repos": ["romaine-life/tank-operator"],
        "write": True,
        "full": True,
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


def test_tank_merge_tool_merges_verified_session_branch(monkeypatch, tmp_path) -> None:
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

    http = _HotSwapHTTP(sha)
    response = asyncio.run(
        _handle_tank_merge_tool(
            http,
            _StaticTokenProvider("service-token"),
            14,
            {"repo_path": str(repo), "pr_number": 1113, "merge_method": "squash"},
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    assert structured["merged"] is True
    assert structured["pr_number"] == 1113
    assert structured["branch"] == "tank/session/95/tank-operator"
    token_posts = [call for call in http.posts if "mcp-github" in call["url"]]
    assert token_posts[0]["json"]["params"]["arguments"] == {
        "repos": ["romaine-life/tank-operator"],
        "write": True,
        "full": True,
    }
    merge_requests = [call for call in http.requests if call["method"] == "PUT" and "/pulls/1113/merge" in call["url"]]
    assert merge_requests[0]["kwargs"]["json"] == {"sha": sha, "merge_method": "squash"}
    actions = [call["json"]["action"] for call in http.posts if "control-actions" in call["url"]]
    assert actions == ["github.pull_request.merge", "github.pull_request.merge"]


def test_tank_rename_pr_tool_renames_verified_session_pr(monkeypatch, tmp_path) -> None:
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

    http = _HotSwapHTTP(sha)
    response = asyncio.run(
        _handle_tank_rename_pr_tool(
            http,
            _StaticTokenProvider("service-token"),
            17,
            {"repo_path": str(repo), "pr_number": 1113, "title": "Tank session 95: governed rename"},
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    assert structured["title"] == "Tank session 95: governed rename"
    assert structured["pr_number"] == 1113
    patch_requests = [call for call in http.requests if call["method"] == "PATCH" and "/issues/1113" in call["url"]]
    assert patch_requests[0]["kwargs"]["json"] == {"title": "Tank session 95: governed rename"}
    actions = [call["json"]["action"] for call in http.posts if "control-actions" in call["url"]]
    assert actions == ["github.pull_request.rename", "github.pull_request.rename"]


def test_tank_update_pr_body_tool_updates_verified_session_pr(monkeypatch, tmp_path) -> None:
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
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", workspace)
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    body = (
        "## Summary\n\n- Add governed PR body tool.\n\n"
        "## Feature Contracts\n\nAffected contracts:\n"
        "- [x] Session Lifecycle\n- [ ] Observability\n\n"
        "Evidence:\n- New control-action ledger entry github.pull_request.update_body.\n"
    )
    http = _HotSwapHTTP("unused")
    response = asyncio.run(
        _handle_tank_update_pr_body_tool(
            http,
            _StaticTokenProvider("service-token"),
            18,
            {"repo_path": str(repo), "pr_number": 1113, "body": body},
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    assert structured["pr_number"] == 1113
    assert structured["body_length"] == len(body)
    assert structured["feature_contracts_ready"] is True
    assert structured["feature_contracts_missing"] == []
    patch_requests = [call for call in http.requests if call["method"] == "PATCH" and "/issues/1113" in call["url"]]
    assert patch_requests[0]["kwargs"]["json"] == {"body": body}
    posts = [call for call in http.posts if "control-actions" in call["url"]]
    actions = [call["json"]["action"] for call in posts]
    assert actions == ["github.pull_request.update_body", "github.pull_request.update_body"]
    # The governed ledger payload must stay small (backend caps it at 16 KiB):
    # it records body metadata, not the full body text.
    assert posts[0]["json"]["payload"]["body_length"] == len(body)
    assert posts[0]["json"]["payload"]["feature_contracts_ready"] is True
    assert "body" not in posts[0]["json"]["payload"]


def test_tank_update_pr_body_tool_flags_incomplete_feature_contracts(monkeypatch, tmp_path) -> None:
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
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", workspace)
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    # A body the agent might write that does NOT satisfy check-pr-body.
    http = _HotSwapHTTP("unused")
    response = asyncio.run(
        _handle_tank_update_pr_body_tool(
            http,
            _StaticTokenProvider("service-token"),
            19,
            {"repo_path": str(repo), "pr_number": 1113, "body": "Just a plain description.\n"},
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    # The body is still updated (the tool is a general PR-body editor), but the
    # advisory flags it as not satisfying the gate.
    assert structured["feature_contracts_ready"] is False
    assert structured["feature_contracts_missing"]
    posts = [call for call in http.posts if "control-actions" in call["url"]]
    assert posts[0]["json"]["payload"]["feature_contracts_ready"] is False


def test_tank_update_pr_body_tool_requires_body(monkeypatch, tmp_path) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")
    http = _HotSwapHTTP("unused")
    response = asyncio.run(
        _handle_tank_update_pr_body_tool(
            http,
            _StaticTokenProvider("service-token"),
            20,
            {"repo_path": str(tmp_path), "body": "   "},
        )
    )
    payload = json.loads(response.text)
    assert payload["error"]["code"] == -32017
    assert "body is required" in payload["error"]["message"]
    assert http.posts == []
    assert http.requests == []


def test_feature_contracts_body_status_accepts_filled_section() -> None:
    body = (
        "## Feature Contracts\n\nAffected contracts:\n"
        "- [x] Session Lifecycle\n- [ ] None\n\n"
        "Evidence:\n- Concrete evidence line.\n"
    )
    ready, missing = _feature_contracts_body_status(body)
    assert ready is True
    assert missing == []


def test_feature_contracts_body_status_rejects_default_template() -> None:
    # Mirrors .github/pull_request_template.md with nothing filled in.
    body = (
        "## Summary\n\n-\n\n## Feature Contracts\n\nAffected contracts:\n"
        "- [ ] Transcript\n- [ ] None\n\nEvidence:\n-\n"
    )
    ready, missing = _feature_contracts_body_status(body)
    assert ready is False
    assert any("affected contract" in reason for reason in missing)
    assert any("Evidence" in reason for reason in missing)


def test_feature_contracts_body_status_reports_missing_markers() -> None:
    ready, missing = _feature_contracts_body_status("no contracts here")
    assert ready is False
    assert any("## Feature Contracts" in reason for reason in missing)
    assert any("Affected contracts:" in reason for reason in missing)
    assert any("Evidence:" in reason for reason in missing)


def test_tank_merge_tool_rejects_non_session_branch(monkeypatch, tmp_path) -> None:
    workspace = tmp_path
    repo = workspace / "tank-operator"
    repo.mkdir()
    subprocess.run(["git", "init"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(["git", "config", "user.email", "agent@example.test"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.name", "Agent"], cwd=repo, check=True)
    (repo / "README.md").write_text("test\n", encoding="utf-8")
    subprocess.run(["git", "add", "README.md"], cwd=repo, check=True)
    subprocess.run(["git", "commit", "-m", "initial"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(["git", "checkout", "-b", "feature/outside-lane"], cwd=repo, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(["git", "remote", "add", "origin", "https://github.com/romaine-life/tank-operator.git"], cwd=repo, check=True)
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", workspace)
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    http = _HotSwapHTTP("unused")
    response = asyncio.run(
        _handle_tank_merge_tool(
            http,
            _StaticTokenProvider("service-token"),
            15,
            {"repo_path": str(repo), "pr_number": 1113},
        )
    )

    payload = json.loads(response.text)
    assert payload["error"]["code"] == -32015
    assert "expected Tank session branch" in payload["error"]["message"]
    assert http.posts == []
    assert http.requests == []


def test_append_tank_publish_tool_augments_event_prefixed_sse_tools_list() -> None:
    raw = (
        b"event: message\n"
        b'data: {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_transcript"}]}}\n\n'
    )

    augmented = _append_tank_publish_tool(raw).decode()

    assert "event: message" in augmented
    assert "publish_current_head" in augmented
    assert "request_git_break_glass" in augmented
    assert "request_pr_lane" in augmented
    assert "merge_current_session_pr" in augmented
    assert "rename_current_session_pr" in augmented
    assert "update_current_session_pr_body" in augmented


def test_tank_publish_tool_is_added_to_tools_list() -> None:
    raw = b'{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_transcript"}]}}'

    augmented = json.loads(_append_tank_publish_tool(raw))

    names = [tool["name"] for tool in augmented["result"]["tools"]]
    assert names == [
        "read_transcript",
        "publish_current_head",
        "request_pr_lane",
        "create_pr_lane",
        "merge_current_session_pr",
        "rename_current_session_pr",
        "update_current_session_pr_body",
        "request_git_break_glass",
    ]
    publish = augmented["result"]["tools"][1]
    assert publish["inputSchema"]["properties"]["repo_path"]["type"] == "string"
    pr_lane = augmented["result"]["tools"][2]
    assert pr_lane["inputSchema"]["required"] == ["repo_scope", "reason"]
    assert "repo_scope" in pr_lane["inputSchema"]["properties"]
    assert "branch_scope" in pr_lane["inputSchema"]["properties"]
    assert "lane_names" not in pr_lane["inputSchema"]["properties"]
    assert "requested_count" not in pr_lane["inputSchema"]["properties"]
    assert "pull request" in pr_lane["description"]
    create_lane = augmented["result"]["tools"][3]
    assert create_lane["inputSchema"]["required"] == ["request_event_id"]
    merge = augmented["result"]["tools"][4]
    assert "pr_number" in merge["inputSchema"]["properties"]
    assert "session branch" in merge["description"]
    rename = augmented["result"]["tools"][5]
    assert rename["inputSchema"]["required"] == ["title"]
    update_body = augmented["result"]["tools"][6]
    assert update_body["inputSchema"]["required"] == ["body"]
    assert "body" in update_body["inputSchema"]["properties"]
    assert "Feature Contracts" in update_body["description"]
    break_glass = augmented["result"]["tools"][7]
    assert "approval URL" in break_glass["description"]
    assert break_glass["inputSchema"]["required"] == ["repo_scope", "branch_scope", "reason"]
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
    url = _break_glass_approval_url("95", "request-123")

    assert url == "https://tank.romaine.life/sessions/95?break_glass_request=request-123"
    query = parse_qs(urlparse(url).query)
    assert query["break_glass_request"] == ["request-123"]
    assert "repo_scope" not in query
    assert "branch_scope" not in query
    assert "reason" not in query


def test_break_glass_approval_url_carries_slot_scope() -> None:
    url = _break_glass_approval_url("slot/session", "request-123")

    assert url == "https://tank.romaine.life/sessions/slot%2Fsession?break_glass_request=request-123"


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
            {
                "repo_scope": {"kind": "current_repo", "repo": "romaine-life/tank-operator"},
                "branch_scope": {"kind": "unlimited"},
                "reason": "need branch repair",
            },
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    assert structured["approval_url"].startswith("https://tank.romaine.life/sessions/95?break_glass_request=")
    assert payload["result"]["structuredContent"]["privileged_tools_visible"] is False
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    recorded = recorded_call["json"]
    assert recorded["action"] == "github.break_glass.request"
    assert recorded["event_id"] == structured["request_event_id"]
    assert recorded["source_tool"] == "request_git_break_glass"
    assert recorded["target_ref"] == "https://github.com/romaine-life/tank-operator"
    assert recorded["payload"]["reason"] == "need branch repair"
    assert recorded["payload"]["approval_url"] == structured["approval_url"]
    assert recorded["payload"]["request_event_id"] == recorded["event_id"]
    assert recorded["payload"]["repo_scope"] == {"kind": "current_repo", "repo": "romaine-life/tank-operator"}
    assert recorded["payload"]["branch_scope"] == {"kind": "unlimited"}
    approval_query = parse_qs(urlparse(structured["approval_url"]).query)
    assert "repo_scope" not in approval_query
    assert "branch_scope" not in approval_query
    assert "reason" not in approval_query
    assert recorded_call["headers"]["Authorization"] == "Bearer service-token"


def test_azure_break_glass_approval_url_carries_intent_without_repo() -> None:
    url = _azure_break_glass_approval_url("95", "request-abc")

    assert url == "https://tank.romaine.life/sessions/95?break_glass_request=request-abc"
    assert "intent=azure-break-glass" not in url
    assert "repo=" not in url
    assert "reason=" not in url


def test_append_azure_break_glass_tool_adds_request_tool() -> None:
    raw = b'{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_transcript"}]}}'

    augmented = json.loads(_append_azure_break_glass_tool(raw))

    names = [tool["name"] for tool in augmented["result"]["tools"]]
    assert names == ["read_transcript", "request_azure_break_glass"]
    tool = augmented["result"]["tools"][1]
    assert "locked by default" in tool["description"]
    assert "token" not in tool["inputSchema"]["properties"]
    # Idempotent: a second pass must not duplicate the tool.
    again = json.loads(_append_azure_break_glass_tool(json.dumps(augmented).encode()))
    assert [t["name"] for t in again["result"]["tools"]] == names


def test_tank_azure_break_glass_tool_records_request_without_granting(monkeypatch) -> None:
    # GET (grant lookup) returns no active grant; POST (control-action) is the
    # recorded request. The tool must not grant access or reveal a token.
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b'{"active":false}'),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_azure_break_glass_tool(
            http,
            _StaticTokenProvider("service-token"),
            9,
            {"reason": "inspect ledger"},
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    assert structured["resource"] == "azure-personal"
    assert structured["status"] == "approval_required"
    assert structured["privileged_tools_visible"] is False
    assert structured["approval_url"].startswith("https://tank.romaine.life/sessions/95?break_glass_request=")
    assert "token" not in structured
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    recorded = recorded_call["json"]
    assert recorded["action"] == "azure.break_glass.request"
    assert recorded["event_id"] == structured["request_event_id"]
    assert recorded["source_tool"] == "request_azure_break_glass"
    assert recorded["target_kind"] == "azure_mcp"
    assert recorded["target_ref"] == "azure-personal"
    assert recorded["payload"]["reason"] == "inspect ledger"
    assert recorded["payload"]["approval_url"] == structured["approval_url"]
    assert recorded["payload"]["request_event_id"] == recorded["event_id"]


def test_tank_azure_break_glass_tool_reports_active_grant(monkeypatch, tmp_path) -> None:
    # When a grant is already active, the tool activates azure-personal into the
    # workspace MCP config (the harness reconnect trigger) and reports approved.
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", tmp_path)
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b'{"active":true,"event_id":"azg-1","expires_at":"2999-01-01T00:00:00Z"}'),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_azure_break_glass_tool(
            http,
            _StaticTokenProvider("service-token"),
            9,
            {"reason": "inspect ledger"},
        )
    )

    structured = json.loads(response.text)["result"]["structuredContent"]
    assert structured["status"] == "approved"
    assert structured["privileged_tools_visible"] is True
    assert structured["expires_at"] == "2999-01-01T00:00:00Z"
    assert structured["activation"]["server_name"] == "azure-personal"
    mcp_config = json.loads((tmp_path / ".mcp.json").read_text())
    assert mcp_config["mcpServers"]["azure-personal"]["url"] == "http://127.0.0.1:9991/"


def test_azure_break_glass_activation_adds_mcp_entry(monkeypatch, tmp_path) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", tmp_path)
    result = _activate_azure_break_glass_mcp_config({"event_id": "g1", "expires_at": "2999-01-01T00:00:00Z"})
    assert result["server_name"] == "azure-personal"
    assert result["reload_required"] is True
    config = json.loads((tmp_path / ".mcp.json").read_text())
    assert config["mcpServers"]["azure-personal"] == {"type": "http", "url": "http://127.0.0.1:9991/"}
    marker = json.loads((tmp_path / ".tank" / "azure-break-glass-active.json").read_text())
    assert marker["grant_event_id"] == "g1"


def test_tank_break_glass_tool_records_request_when_active_grant_misses_branch_scope(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(
            200,
            b'{"active":true,"event_id":"grant-1","repo_scope":{"kind":"current_repo","repo":"romaine-life/tank-operator"},"branch_scope":{"kind":"named","branches":["branch-a"]}}',
        ),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_break_glass_tool(
            http,
            _StaticTokenProvider("service-token"),
            17,
            {
                "repo_scope": {"kind": "current_repo", "repo": "romaine-life/tank-operator"},
                "branch_scope": {"kind": "named", "branches": ["branch-b"]},
                "reason": "need another branch",
            },
        )
    )

    payload = json.loads(response.text)
    assert payload["result"]["structuredContent"]["status"] == "approval_required"
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    assert recorded_call["json"]["payload"]["branch_scope"] == {"kind": "named", "branches": ["branch-b"]}


def test_tank_break_glass_tool_records_all_repo_branch_scope(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b"not-json"),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_break_glass_tool(
            http,
            _StaticTokenProvider("service-token"),
            19,
            {
                "repo_scope": {"kind": "all_repos"},
                "branch_scope": {"kind": "named", "branches": ["refs/heads/branch-a", "branch-b"]},
                "reason": "planned migration",
            },
        )
    )

    payload = json.loads(response.text)
    assert payload["result"]["structuredContent"]["repo_scope"] == {"kind": "all_repos"}
    assert payload["result"]["structuredContent"]["branch_scope"] == {"kind": "named", "branches": ["branch-a", "branch-b"]}
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    recorded = recorded_call["json"]
    assert recorded["target_ref"] == "tank://session/95/git-break-glass/all-repos"
    assert recorded["payload"]["repo_scope"] == {"kind": "all_repos"}
    assert recorded["payload"]["branch_scope"] == {"kind": "named", "branches": ["branch-a", "branch-b"]}
    query = parse_qs(urlparse(recorded["payload"]["approval_url"]).query)
    assert query["break_glass_request"] == [recorded["event_id"]]
    assert "repo_scope" not in query
    assert "branch_scope" not in query


def test_tank_pr_lane_tool_records_approval_request(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b'{"active":false}'),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_pr_lane_tool(
            http,
            _StaticTokenProvider("service-token"),
            10,
            {
                "repo_scope": {"kind": "current_repo", "repo": "romaine-life/tank-operator"},
                "lane_name": "docs",
                "relationship": "parallel",
                "base": "main",
                "scope": "docs/",
                "reason": "split docs-only review from backend policy",
            },
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    assert structured["status"] == "approval_required"
    assert structured["request_event_id"].startswith("tank-pr-lane-request-95-")
    assert structured["proposed_branch"] == "tank/session/95/tank-operator/docs"
    assert structured["approval_url"].startswith("https://tank.romaine.life/?session=95&pr_lane_request=")
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    recorded = recorded_call["json"]
    assert recorded["action"] == "github.pr_lane.request"
    assert recorded["status"] == "started"
    assert recorded["source_tool"] == "request_pr_lane"
    assert recorded["payload"]["lane_name"] == "docs"
    assert recorded["payload"]["relationship"] == "parallel"
    assert recorded["payload"]["auto_approved"] is False
    get_call = next(call for call in http.calls if call.get("method") == "GET")
    assert "lane_name=docs" in get_call["url"]
    assert "proposed_branch=tank%2Fsession%2F95%2Ftank-operator%2Fdocs" in get_call["url"]


def test_tank_pr_lane_tool_records_allocation_request(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b'{"active":false}'),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_pr_lane_tool(
            http,
            _StaticTokenProvider("service-token"),
            16,
            {
                "repo_scope": {"kind": "current_repo", "repo": "romaine-life/tank-operator"},
                "branch_scope": {"kind": "named", "branches": ["docs", "backend"]},
                "reason": "split review into named lanes",
            },
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    assert structured["status"] == "approval_required"
    assert structured["allocation_request"] is True
    assert structured["repo_scope"] == {"kind": "current_repo", "repo": "romaine-life/tank-operator"}
    assert structured["branch_scope"] == {"kind": "named", "branches": ["docs", "backend"]}
    assert structured["approval_url"].startswith("https://tank.romaine.life/?session=95&pr_lane_request=")
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    recorded = recorded_call["json"]
    assert recorded["action"] == "github.pr_lane.request"
    assert recorded["status"] == "started"
    assert recorded["payload"]["allocation_request"] is True
    assert recorded["payload"]["repo_scope"] == {"kind": "current_repo", "repo": "romaine-life/tank-operator"}
    assert recorded["payload"]["branch_scope"] == {"kind": "named", "branches": ["docs", "backend"]}


def test_tank_pr_lane_tool_records_multi_repo_allocation_request(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b'{"active":false}'),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_pr_lane_tool(
            http,
            _StaticTokenProvider("service-token"),
            20,
            {
                "repo_scope": {"kind": "repos", "repos": ["romaine-life/tank-operator", "romaine-life/auth"]},
                "branch_scope": {"kind": "count", "count": 5},
                "reason": "split multi-repo work",
            },
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    assert structured["status"] == "approval_required"
    assert structured["repo_scope"] == {"kind": "repos", "repos": ["romaine-life/tank-operator", "romaine-life/auth"]}
    assert structured["branch_scope"] == {"kind": "count", "count": 5}
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    recorded = recorded_call["json"]
    assert recorded["target_ref"] == "tank://session/95/pr-lanes/repos"
    assert recorded["repo_owner"] == ""
    assert recorded["repo_name"] == ""
    assert recorded["payload"]["repo_scope"] == {"kind": "repos", "repos": ["romaine-life/tank-operator", "romaine-life/auth"]}
    assert recorded["payload"]["branch_scope"] == {"kind": "count", "count": 5}


def test_tank_pr_lane_tool_rejects_conflicting_branch_scope(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b'{"active":false}'),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_pr_lane_tool(
            http,
            _StaticTokenProvider("service-token"),
            21,
            {
                "repo_scope": {"kind": "current_repo", "repo": "romaine-life/tank-operator"},
                "branch_scope": {"kind": "unlimited", "branches": ["docs"]},
                "reason": "split review",
            },
        )
    )

    payload = json.loads(response.text)
    assert payload["error"]["code"] == -32013
    assert "unlimited rejects branches and count" in payload["error"]["message"]
    assert all(call.get("method") != "POST" for call in http.calls)


def test_tank_pr_lane_tool_marks_session_auto_approved(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b'{"active":true,"event_id":"auto-1","limit":10}'),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_pr_lane_tool(
            http,
            _StaticTokenProvider("service-token"),
            11,
            {
                "repo_scope": {"kind": "current_repo", "repo": "romaine-life/tank-operator"},
                "lane_name": "mcp-proxy",
                "relationship": "stacked",
                "reason": "depends on backend lane endpoint",
            },
        )
    )

    payload = json.loads(response.text)
    assert payload["result"]["structuredContent"]["status"] == "approved"
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    recorded = recorded_call["json"]
    assert recorded["status"] == "succeeded"
    assert recorded["payload"]["auto_approved"] is True
    assert recorded["payload"]["auto_approval_event_id"] == "auto-1"


def test_tank_create_pr_lane_tool_creates_governed_worktree_and_pr(monkeypatch, tmp_path) -> None:
    repo_path = tmp_path / "tank-operator"
    repo_path.mkdir()
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(
            200,
            json_module_dumps(
                {
                    "allowed": True,
                    "request_event_id": "lane-request-1",
                    "approval_event_id": "lane-approve-1",
                    "repo": "romaine-life/tank-operator",
                    "lane_name": "docs",
                    "relationship": "parallel",
                    "base": "main",
                    "scope": "docs/",
                    "reason": "split docs",
                    "proposed_branch": "tank/session/95/tank-operator/docs",
                }
            ),
        ),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")
    monkeypatch.setattr("mcp_auth_proxy.server.WORKSPACE_ROOT", tmp_path)

    worktrees: list[dict] = []
    pushes: list[dict] = []
    github_calls: list[dict] = []

    async def fake_git_output(path, *args):
        if args == ("config", "--get", "remote.origin.url"):
            return "https://github.com/romaine-life/tank-operator.git"
        if args == ("rev-parse", "HEAD"):
            return "a" * 40
        raise AssertionError(f"unexpected git output call: {path} {args}")

    async def fake_ensure(source_repo_path, *, branch, base, lane_name, worktree_path):
        worktrees.append(
            {
                "source": source_repo_path,
                "branch": branch,
                "base": base,
                "lane_name": lane_name,
                "worktree_path": worktree_path,
            }
        )
        worktree_path.mkdir(parents=True)

    async def fake_mint(_http, _service_token, repo_slug, *, workflows=False):
        assert repo_slug == "romaine-life/tank-operator"
        assert workflows is False
        return "github-token"

    async def fake_push(path, branch, token):
        pushes.append({"path": path, "branch": branch, "token": token})

    async def fake_call(_http, _service_token, name, arguments):
        github_calls.append({"name": name, "arguments": arguments})
        return [
            {
                "result": {
                    "content": [
                        {
                            "type": "text",
                            "text": "Created https://github.com/romaine-life/tank-operator/pull/123",
                        }
                    ]
                }
            }
        ]

    created_tasks = []
    monkeypatch.setattr("mcp_auth_proxy.server._git_output", fake_git_output)
    monkeypatch.setattr("mcp_auth_proxy.server._ensure_pr_lane_worktree", fake_ensure)
    monkeypatch.setattr("mcp_auth_proxy.server._mint_github_installation_token", fake_mint)
    monkeypatch.setattr("mcp_auth_proxy.server._push_head_with_token", fake_push)
    monkeypatch.setattr("mcp_auth_proxy.server._call_mcp_github_tool", fake_call)
    monkeypatch.setattr("mcp_auth_proxy.server.asyncio.create_task", lambda coro: created_tasks.append(coro))

    try:
        response = asyncio.run(
            _handle_tank_create_pr_lane_tool(
                http,
                _StaticTokenProvider("service-token"),
                12,
                {"request_event_id": "lane-request-1", "repo_path": str(repo_path)},
            )
        )
    finally:
        for coro in created_tasks:
            coro.close()

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    assert structured["branch"] == "tank/session/95/tank-operator/docs"
    assert structured["pr_url"] == "https://github.com/romaine-life/tank-operator/pull/123"
    assert structured["worktree_path"] == str(tmp_path / ".tank" / "pr-lanes" / "romaine-life" / "tank-operator" / "docs")
    assert worktrees[0]["source"] == repo_path.resolve()
    assert pushes == [
        {
            "path": tmp_path / ".tank" / "pr-lanes" / "romaine-life" / "tank-operator" / "docs",
            "branch": "tank/session/95/tank-operator/docs",
            "token": "github-token",
        }
    ]
    assert github_calls[0]["name"] == "create_pull_request"
    assert github_calls[0]["arguments"]["draft"] is True
    assert github_calls[0]["arguments"]["head"] == "tank/session/95/tank-operator/docs"
    recorded_actions = [call["json"]["action"] for call in http.calls if call.get("method") == "POST"]
    assert recorded_actions == ["github.pr_lane.create", "github.pull_request.open", "github.pr_lane.create"]


def test_tank_create_pr_lane_tool_rejects_unapproved_request(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(409, json_module_dumps({"allowed": False, "reasons": ["pending approval"]})),
        post_response=_FakeRawResponse(201, b'{"ok":true}'),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_tank_create_pr_lane_tool(
            http,
            _StaticTokenProvider("service-token"),
            13,
            {"request_event_id": "lane-request-1"},
        )
    )

    payload = json.loads(response.text)
    assert payload["error"]["code"] == -32014
    assert "pending approval" in payload["error"]["message"]


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
    assert "restricted Git mode" in payload["error"]["message"]
    assert payload["error"]["data"]["replacement_tool"] == "publish_current_head"
    assert payload["error"]["data"]["break_glass_tool"] == "request_git_break_glass"


def test_github_merge_tool_block_response_points_to_governed_merge() -> None:
    body = json.dumps({
        "jsonrpc": "2.0",
        "id": 8,
        "method": "tools/call",
        "params": {"name": "merge_pull_request", "arguments": {"owner": "romaine-life", "name": "tank-operator", "pullNumber": 1}},
    }).encode()

    response = _github_tool_block_response(body, "merge_pull_request")

    assert response is not None
    payload = json.loads(response.text)
    assert payload["error"]["data"]["replacement_tool"] == "merge_current_session_pr"
    assert "merge_current_session_pr" in payload["error"]["message"]


def test_github_update_issue_block_response_points_to_governed_rename() -> None:
    body = json.dumps({
        "jsonrpc": "2.0",
        "id": 18,
        "method": "tools/call",
        "params": {"name": "update_issue", "arguments": {"owner": "romaine-life", "name": "tank-operator", "number": 1, "title": "new"}},
    }).encode()

    response = _github_tool_block_response(body, "update_issue")

    assert response is not None
    payload = json.loads(response.text)
    assert payload["error"]["data"]["replacement_tool"] == "rename_current_session_pr"
    assert payload["error"]["data"]["body_tool"] == "update_current_session_pr_body"
    assert "rename_current_session_pr" in payload["error"]["message"]
    assert "update_current_session_pr_body" in payload["error"]["message"]
    assert "Feature Contracts" in payload["error"]["message"]


def test_filter_github_write_tools_removes_denied_tools_from_sse_list() -> None:
    raw = (
        b"event: message\n"
        b'data: {"jsonrpc":"2.0","id":1,"result":{"tools":['
        b'{"name":"get_pull_request"},'
        b'{"name":"create_pull_request"},'
        b'{"name":"commit_to_branch"},'
        b'{"name":"merge_pull_request"},'
        b'{"name":"update_issue"},'
        b'{"name":"list_pull_requests"}]}}\n\n'
    )

    filtered = _filter_github_write_tools(raw)
    payload = json.loads(filtered.decode().split("data: ", 1)[1])
    names = [tool["name"] for tool in payload["result"]["tools"]]

    assert names == ["get_pull_request", "list_pull_requests"]


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


def test_checks_state_uses_latest_run_per_name() -> None:
    # A stale failed run plus a newer success run for the SAME check name must
    # read as succeeded — GitHub branch protection evaluates the latest run per
    # name, and a check re-run from failure->success must not block forever.
    runs = [
        {"name": "check-pr-body", "status": "completed", "conclusion": "failure", "started_at": "2026-06-14T09:50:00Z", "id": 1},
        {"name": "check-pr-body", "status": "completed", "conclusion": "success", "started_at": "2026-06-14T10:31:00Z", "id": 2},
    ]
    status, error, payload = _checks_state(runs, None)
    assert status == "succeeded", (status, error)
    assert payload["failed"] == []
    assert payload["completed"] == 1
    # Order-independent (GitHub does not guarantee ordering in the response).
    assert _checks_state(list(reversed(runs)), None)[0] == "succeeded"


def test_checks_state_latest_pending_run_overrides_old_success() -> None:
    # If the newest run for a name is still running, the check is pending even
    # when an older run for that name succeeded.
    runs = [
        {"name": "test", "status": "completed", "conclusion": "success", "started_at": "2026-06-14T09:00:00Z", "id": 1},
        {"name": "test", "status": "in_progress", "conclusion": None, "started_at": "2026-06-14T10:00:00Z", "id": 2},
    ]
    status, _, payload = _checks_state(runs, None)
    assert status == "started"
    assert "test" in payload["pending"]


def test_watch_published_commit_records_clean_mergeability_via_single_pr(monkeypatch) -> None:
    # The /pulls?head= list endpoint returns mergeable=null (the mock PR in the
    # list carries no mergeable field); the watcher must fetch the single PR
    # (/pulls/{n}, which the mock reports clean) so it records a *succeeded*
    # mergeability observation instead of looping on "unknown" forever.
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")
    http = _HotSwapHTTP("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
    asyncio.run(
        asyncio.wait_for(
            _watch_published_commit(
                http,
                _StaticTokenProvider("service-token"),
                "romaine-life",
                "tank-operator",
                "tank/session/95/tank-operator",
                "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
                "tank-publish-test",
            ),
            timeout=10,
        )
    )
    actions = [(c["json"]["action"], c["json"]["status"]) for c in http.posts if "control-actions" in c["url"]]
    assert ("github.commit.ci", "succeeded") in actions
    assert ("github.pull_request.mergeability", "succeeded") in actions
    # The single-PR GET (not just the list) was issued for mergeability.
    assert any(call["method"] == "GET" and call["url"].endswith("/pulls/1113") for call in http.requests)


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
