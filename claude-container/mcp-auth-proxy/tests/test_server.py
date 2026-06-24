from __future__ import annotations

import asyncio
import base64
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
    ARGOCD_MCP_PORT,
    AUTH_ROMAINE_HEADER_PORTS,
    AuthRomaineServiceProvider,
    AZURE_MCP_PORT,
    GITHUB_MCP_PORT,
    GLIMMUNG_MCP_PORT,
    GRAFANA_MCP_PORT,
    JWT_BEARER_PORTS,
    K8S_MCP_PORT,
    TANK_OPERATOR_MCP_PORT,
    SPIRELENS_MCP_PORT,
    _append_ci_reminder,
    _append_tank_publish_tool,
    _append_azure_break_glass_tool,
    _break_glass_approval_url,
    _azure_break_glass_approval_url,
    _activate_break_glass_mcp_config,
    _checks_state,
    _effective_listeners,
    _first_pr_from_response,
    _grant_branch_allows,
    _sanitize_branch_scope_name,
    _filter_github_write_tools,
    _github_tool_block_response,
    _handle_tank_break_glass_tool,
    _handle_tank_azure_break_glass_tool,
    _handle_query_tank_db_tool,
    _handle_tank_merge_tool,
    _active_break_glass_grant_cached,
    _handle_break_glass_mcp,
    _handle_break_glass_mint_token,
    _break_glass_operations,
    _BREAK_GLASS_FULL_API_OPERATION,
    _json_objects_from_mcp_body,
    _make_handler,
    _mint_github_installation_token,
    _mint_github_installation_token_for_repos,
    _parse_mcp_tool_call,
    _post_tank_control_action,
    _push_head_with_token,
    _repo_slug_from_remote,
    _resolve_ci_state,
    _watch_published_commit,
)


@pytest.fixture(autouse=True)
def _reset_break_glass_grant_cache():
    # The grant-lookup cache is a module-level dict; clear it around every test so
    # a cached result from one test cannot leak into the next.
    import mcp_auth_proxy.server as _server

    _server._BREAK_GLASS_GRANT_CACHE.clear()
    yield
    _server._BREAK_GLASS_GRANT_CACHE.clear()


def test_tank_operator_uses_jwt_bearer_not_sa_token() -> None:
    # mcp-tank-operator lost its kube-rbac-proxy sidecar (mcp-tank-operator#31),
    # so the only thing that ever consumed the SA-token bearer is gone and its
    # Authorization bearer must be the auth.romaine.life JWT. mcp-github and the
    # SpireLens host verify the JWT directly, so they share the set.
    assert TANK_OPERATOR_MCP_PORT in JWT_BEARER_PORTS
    assert GITHUB_MCP_PORT in JWT_BEARER_PORTS
    assert SPIRELENS_MCP_PORT in JWT_BEARER_PORTS


def test_kube_rbac_proxy_upstreams_keep_sa_token_bearer() -> None:
    # Every other in-cluster MCP still sits behind a kube-rbac-proxy that
    # TokenReviews the SA-token bearer, so they must NOT be on the JWT bearer.
    for port in (AZURE_MCP_PORT, GLIMMUNG_MCP_PORT, GRAFANA_MCP_PORT, K8S_MCP_PORT, ARGOCD_MCP_PORT):
        assert port not in JWT_BEARER_PORTS


def test_in_cluster_mcps_receive_auth_romaine_side_header() -> None:
    # The kube-rbac-proxy-backed MCPs still need the SA bearer at the transport
    # gate, but forwarding the auth.romaine JWT side header gives their app
    # containers the migration path before that proxy is removed.
    for port in (
        AZURE_MCP_PORT,
        GITHUB_MCP_PORT,
        GLIMMUNG_MCP_PORT,
        GRAFANA_MCP_PORT,
        K8S_MCP_PORT,
        ARGOCD_MCP_PORT,
        TANK_OPERATOR_MCP_PORT,
    ):
        assert port in AUTH_ROMAINE_HEADER_PORTS


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


class _GitHubAPIFake:
    def __init__(
        self,
        *,
        head_sha: str = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        prior_sha: str = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        changed_paths: list[str] | None = None,
        workflow_text: str | None = None,
        workflow_path: str = ".github/workflows/go-backend.yml",
        run_event: str = "pull_request",
        prior_conclusion: str = "success",
        head_runs: list[dict] | None = None,
    ) -> None:
        self.head_sha = head_sha
        self.prior_sha = prior_sha
        self.changed_paths = changed_paths or []
        self.workflow_text = workflow_text if workflow_text is not None else (
            "name: Go backend\n"
            "on:\n"
            "  pull_request:\n"
            "    paths:\n"
            "      - 'backend-go/**'\n"
            "jobs:\n"
            "  test:\n"
            "    runs-on: ubuntu-latest\n"
        )
        self.workflow_path = workflow_path
        self.run_event = run_event
        self.prior_conclusion = prior_conclusion
        self.head_runs = head_runs or []
        self.requests: list[dict] = []

    def request(self, method: str, url: str, *, headers: dict, json=None, **kwargs):
        self.requests.append({"method": method, "url": url, "headers": headers, "json": json, "kwargs": kwargs})
        path = url.split("https://api.github.com", 1)[-1]
        if path.endswith(f"/commits/{self.head_sha}/check-runs"):
            return _FakeRawResponse(200, json_module_dumps({"check_runs": self.head_runs}))
        if path.endswith(f"/commits/{self.prior_sha}/check-runs"):
            return _FakeRawResponse(
                200,
                json_module_dumps({
                    "check_runs": [
                        {
                            "name": "test",
                            "status": "completed",
                            "conclusion": self.prior_conclusion,
                            "started_at": "2026-06-16T05:18:05Z",
                            "id": 81586055969,
                            "details_url": "https://github.com/romaine-life/tank-operator/actions/runs/27595901622/job/81586055969",
                        }
                    ]
                }),
            )
        if path.endswith(f"/commits/{self.head_sha}/status") or path.endswith(f"/commits/{self.prior_sha}/status"):
            return _FakeRawResponse(200, json_module_dumps({"state": "pending", "statuses": []}))
        if path.endswith("/pulls/1245/commits?per_page=100"):
            return _FakeRawResponse(
                200,
                json_module_dumps([
                    {"sha": self.prior_sha},
                    {"sha": self.head_sha},
                ]),
            )
        if path.endswith("/actions/runs/27595901622"):
            return _FakeRawResponse(200, json_module_dumps({"path": self.workflow_path, "name": "Go backend", "event": self.run_event}))
        if f"/contents/{self.workflow_path}" in path:
            encoded = base64.b64encode(self.workflow_text.encode("utf-8")).decode("ascii")
            return _FakeRawResponse(200, json_module_dumps({"content": encoded}))
        if f"/compare/{self.prior_sha}...{self.head_sha}" in path:
            return _FakeRawResponse(
                200,
                json_module_dumps({"files": [{"filename": item} for item in self.changed_paths]}),
            )
        return _FakeRawResponse(404, b"{}")


class _GovernedMergeHTTP:
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

    # Governed GitHub API operations still use the App's full permission set.
    assert token == "full-token"
    assert http.calls[0]["json"]["params"]["arguments"] == {
        "repos": ["romaine-life/tank-operator"],
        "write": True,
        "full": True,
    }


def test_break_glass_mint_token_unlimited_mints_full_github_api_scope(monkeypatch) -> None:
    # An unlimited-branch grant (the only kind that reaches this handler — the
    # branch-restricted refusal fires before it) now mints the App's FULL
    # permission set so the raw break-glass token unlocks PR/issue/merge API
    # writes, not just contents.
    minted: list[dict] = []

    async def fake_active(_http, _service_token, repo_slug):
        assert repo_slug == "romaine-life/tank-operator"
        return {
            "event_id": "grant-1",
            "operations": ["mint_full_git_token", "full_github_api"],
            "branch_scope": {"kind": "unlimited"},
            "expires_at": "2026-06-12T23:00:00Z",
        }

    async def fake_mint(_http, _service_token, repo_slug, *, workflows=False, full=False):
        minted.append({"repo": repo_slug, "workflows": workflows, "full": full})
        return "full-token"

    monkeypatch.setattr("mcp_auth_proxy.server._active_break_glass_grant", fake_active)
    monkeypatch.setattr("mcp_auth_proxy.server._mint_github_installation_token", fake_mint)

    response = asyncio.run(
        _handle_break_glass_mint_token(
            _FakeRawHTTP(_FakeRawResponse(201, b'{"ok":true}')),
            _StaticTokenProvider("service-token"),
            88,
            {"repo": "romaine-life/tank-operator"},
        )
    )

    payload = json.loads(response.text)
    assert payload["result"]["structuredContent"]["token"] == "full-token"
    assert minted == [{"repo": "romaine-life/tank-operator", "workflows": False, "full": True}]


def test_break_glass_mint_token_refuses_branch_restricted_grant(monkeypatch) -> None:
    # A branch/count-scoped grant stays least-privilege on the governed push
    # path: the raw full-token mint must be refused entirely, never elevated.
    async def fake_active(_http, _service_token, _repo_slug):
        return {
            "event_id": "grant-1",
            "operations": ["mint_full_git_token", "push_current_head"],
            "branch_scope": {"kind": "count", "count": 2},
        }

    async def fake_mint(*_args, **_kwargs):
        raise AssertionError("branch-restricted grant must not mint a raw token")

    monkeypatch.setattr("mcp_auth_proxy.server._active_break_glass_grant", fake_active)
    monkeypatch.setattr("mcp_auth_proxy.server._mint_github_installation_token", fake_mint)

    response = asyncio.run(
        _handle_break_glass_mint_token(
            _FakeRawHTTP(_FakeRawResponse(201, b'{"ok":true}')),
            _StaticTokenProvider("service-token"),
            88,
            {"repo": "romaine-life/tank-operator"},
        )
    )

    payload = json.loads(response.text)
    assert payload["error"]["code"] == -32020
    assert "branch-scoped" in payload["error"]["message"]


def test_break_glass_mint_token_rejects_workflows_without_operation(monkeypatch) -> None:
    async def fake_active(_http, _service_token, _repo_slug):
        return {
            "event_id": "grant-1",
            "operations": ["mint_full_git_token"],
            "branch_scope": {"kind": "unlimited"},
        }

    async def fake_mint(*_args, **_kwargs):
        raise AssertionError("workflow token mint should not run without workflow grant")

    monkeypatch.setattr("mcp_auth_proxy.server._active_break_glass_grant", fake_active)
    monkeypatch.setattr("mcp_auth_proxy.server._mint_github_installation_token", fake_mint)

    response = asyncio.run(
        _handle_break_glass_mint_token(
            _FakeRawHTTP(_FakeRawResponse(201, b'{"ok":true}')),
            _StaticTokenProvider("service-token"),
            89,
            {"repo": "romaine-life/tank-operator", "workflows": True},
        )
    )

    payload = json.loads(response.text)
    assert payload["error"]["code"] == -32020
    assert "workflow-file writes" in payload["error"]["message"]


def test_break_glass_mint_token_allows_workflows_with_operation(monkeypatch) -> None:
    minted: list[dict] = []

    async def fake_active(_http, _service_token, _repo_slug):
        return {
            "event_id": "grant-1",
            "operations": ["mint_full_git_token", "workflows"],
            "branch_scope": {"kind": "unlimited"},
            "expires_at": "2026-06-12T23:00:00Z",
        }

    async def fake_mint(_http, _service_token, repo_slug, *, workflows=False, full=False):
        minted.append({"repo": repo_slug, "workflows": workflows, "full": full})
        return "workflow-token"

    monkeypatch.setattr("mcp_auth_proxy.server._active_break_glass_grant", fake_active)
    monkeypatch.setattr("mcp_auth_proxy.server._mint_github_installation_token", fake_mint)

    response = asyncio.run(
        _handle_break_glass_mint_token(
            _FakeRawHTTP(_FakeRawResponse(201, b'{"ok":true}')),
            _StaticTokenProvider("service-token"),
            90,
            {"repo": "romaine-life/tank-operator", "workflows": True},
        )
    )

    payload = json.loads(response.text)
    assert payload["result"]["structuredContent"]["token"] == "workflow-token"
    assert payload["result"]["structuredContent"]["workflows"] is True
    # Unlimited-branch grant -> full=True (full GitHub API), workflows requested.
    assert minted == [{"repo": "romaine-life/tank-operator", "workflows": True, "full": True}]


def test_break_glass_operations_appends_full_api_only_when_unlimited() -> None:
    assert _BREAK_GLASS_FULL_API_OPERATION not in _break_glass_operations(workflows=False)
    assert _BREAK_GLASS_FULL_API_OPERATION not in _break_glass_operations(workflows=True, unlimited=False)
    assert _BREAK_GLASS_FULL_API_OPERATION in _break_glass_operations(workflows=False, unlimited=True)
    assert _BREAK_GLASS_FULL_API_OPERATION in _break_glass_operations(workflows=True, unlimited=True)


def test_active_break_glass_grant_cached_serves_positive_within_ttl(monkeypatch) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server._BREAK_GLASS_GRANT_CACHE", {})
    calls = {"n": 0}

    async def fake_active(_http, _service_token, _repo_slug):
        calls["n"] += 1
        return {
            "event_id": "grant-1",
            "operations": ["mint_full_git_token", "full_github_api"],
            "branch_scope": {"kind": "unlimited"},
            "expires_at": "2099-01-01T00:00:00Z",
        }

    monkeypatch.setattr("mcp_auth_proxy.server._active_break_glass_grant", fake_active)

    http = _FakeRawHTTP(_FakeRawResponse(200, b"{}"))

    async def run():
        a = await _active_break_glass_grant_cached(http, "svc", "romaine-life/tank-operator")
        b = await _active_break_glass_grant_cached(http, "svc", "romaine-life/tank-operator")
        return a, b

    a, b = asyncio.run(run())
    assert a is not None and b is not None
    assert calls["n"] == 1  # second served from cache (grant not yet expired)


def test_active_break_glass_grant_cached_does_not_serve_positive_past_expiry(monkeypatch) -> None:
    # A cached positive must never outlive the grant's own expires_at: an expired
    # entry forces a re-fetch (which in production re-locks, since real Tank
    # filters expiry).
    monkeypatch.setattr("mcp_auth_proxy.server._BREAK_GLASS_GRANT_CACHE", {})
    calls = {"n": 0}

    async def fake_active(_http, _service_token, _repo_slug):
        calls["n"] += 1
        return {
            "event_id": "grant-1",
            "operations": ["mint_full_git_token", "full_github_api"],
            "branch_scope": {"kind": "unlimited"},
            "expires_at": "2000-01-01T00:00:00Z",
        }

    monkeypatch.setattr("mcp_auth_proxy.server._active_break_glass_grant", fake_active)

    http = _FakeRawHTTP(_FakeRawResponse(200, b"{}"))

    async def run():
        await _active_break_glass_grant_cached(http, "svc", "romaine-life/tank-operator")
        await _active_break_glass_grant_cached(http, "svc", "romaine-life/tank-operator")

    asyncio.run(run())
    assert calls["n"] == 2  # expired positive is not served stale from cache


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

    http = _GovernedMergeHTTP(sha)
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
    # The governed-merge gate is the single readiness authority for the merge
    # tool: it must be hit at the renamed route, scoped to the session, with the
    # exact branch/HEAD the merge will land. (Route rename regression guard.)
    verify_posts = [call for call in http.posts if call["url"].endswith("/governed-merge/verify")]
    assert len(verify_posts) == 1
    assert verify_posts[0]["url"].endswith("/api/internal/sessions/95/governed-merge/verify")
    assert verify_posts[0]["json"]["repo"] == "romaine-life/tank-operator"
    assert verify_posts[0]["json"]["branch"] == "tank/session/95/tank-operator"
    assert verify_posts[0]["json"]["sha"] == sha
    merge_requests = [call for call in http.requests if call["method"] == "PUT" and "/pulls/1113/merge" in call["url"]]
    assert merge_requests[0]["kwargs"]["json"] == {"sha": sha, "merge_method": "squash"}
    actions = [call["json"]["action"] for call in http.posts if "control-actions" in call["url"]]
    assert actions == ["github.pull_request.merge", "github.pull_request.merge"]


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

    http = _GovernedMergeHTTP("unused")
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
    assert "request_git_break_glass" in augmented
    assert "watch_current_session_pr" in augmented
    # The in-pod governed-publish / PR-mutation tools were retired once the
    # agent-egress proxy (the wall) became the GitHub boundary: a plain
    # git push / gh edits the governed PR. (Token fragments are joined at
    # runtime so these absence assertions do not themselves trip the
    # scripts/check-removed-* reintroduction guards.)
    retired_tools = [
        "_".join(["publish", "current", "head"]),
        "_".join(["rename", "current", "session", "pr"]),
        "_".join(["update", "current", "session", "pr", "body"]),
        "_".join(["request", "pr", "lane"]),
        "_".join(["create", "pr", "lane"]),
    ]
    for retired in retired_tools:
        assert retired not in augmented
    assert "merge_current_session_pr" in augmented


def test_tank_publish_tool_is_added_to_tools_list() -> None:
    raw = b'{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_transcript"}]}}'

    augmented = json.loads(_append_tank_publish_tool(raw))

    names = [tool["name"] for tool in augmented["result"]["tools"]]
    # In-pod governed-publish (publish_current_head) and the governed PR-mutation
    # tools (rename/update_body) are no longer injected: the wall governs pushes
    # and gh PR edits. watch + merge + provision + break-glass remain. The
    # exact-list assertion below is the absence proof.
    assert names == [
        "read_transcript",
        "watch_current_session_pr",
        "merge_current_session_pr",
        "provision_test_slot",
        "request_git_break_glass",
    ]
    for retired in [
        "_".join(["publish", "current", "head"]),
        "_".join(["rename", "current", "session", "pr"]),
        "_".join(["update", "current", "session", "pr", "body"]),
        "_".join(["request", "pr", "lane"]),
        "_".join(["create", "pr", "lane"]),
    ]:
        assert retired not in names
    watch = augmented["result"]["tools"][1]
    assert watch["inputSchema"]["properties"]["pr_number"]["type"] == "integer"
    merge = augmented["result"]["tools"][2]
    assert "pr_number" in merge["inputSchema"]["properties"]
    assert "session branch" in merge["description"]
    provision_test_slot = augmented["result"]["tools"][3]
    # Optional disambiguation/selection knobs, none required (the backend resolves
    # the governed coordinates from durable session state).
    assert provision_test_slot["inputSchema"].get("required", []) == []
    assert provision_test_slot["inputSchema"]["properties"]["repo"]["type"] == "string"
    assert provision_test_slot["inputSchema"]["properties"]["pr"]["type"] == "integer"
    assert provision_test_slot["inputSchema"]["properties"]["drive"]["type"] == "boolean"
    assert provision_test_slot["inputSchema"]["properties"]["ref"]["type"] == "string"
    assert "test slot" in provision_test_slot["description"].lower()
    break_glass = augmented["result"]["tools"][4]
    assert "approval URL" in break_glass["description"]
    assert break_glass["inputSchema"]["required"] == ["repo_scope", "branch_scope", "reason"]
    assert "token" not in break_glass["inputSchema"]["properties"]
    assert break_glass["inputSchema"]["properties"]["workflows"]["type"] == "boolean"


def test_break_glass_approval_url_carries_request_context() -> None:
    url = _break_glass_approval_url("95", "request-123")

    assert url == "https://tank.romaine.life/sessions/95/break-glass/request-123"
    query = parse_qs(urlparse(url).query)
    assert "repo_scope" not in query
    assert "branch_scope" not in query
    assert "reason" not in query


def test_break_glass_approval_url_carries_slot_scope(monkeypatch) -> None:
    monkeypatch.setattr("mcp_auth_proxy.server.TANK_UI_HOST", "https://tank.romaine.life")
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_SCOPE", "tank-operator-slot-2")

    url = _break_glass_approval_url("slot/session", "request-123")

    assert (
        url
        == "https://tank-operator-slot-2.tank.dev.romaine.life/sessions/slot%2Fsession/break-glass/request-123"
    )


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
    assert structured["approval_url"].startswith("https://tank.romaine.life/sessions/95/break-glass/")
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
    # Unlimited-branch request advertises full GitHub API write up front.
    assert recorded["payload"]["operations"] == ["mint_full_git_token", "push_current_head", "full_github_api"]
    assert recorded["payload"]["workflows"] is False
    parsed_approval = urlparse(structured["approval_url"])
    approval_query = parse_qs(parsed_approval.query)
    assert parsed_approval.path.startswith("/sessions/95/break-glass/")
    assert "repo_scope" not in approval_query
    assert "branch_scope" not in approval_query
    assert "reason" not in approval_query
    assert recorded_call["headers"]["Authorization"] == "Bearer service-token"


def test_tank_break_glass_tool_records_workflows_operation(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b'{"active":false}'),
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
                "reason": "need workflow repair",
                "workflows": True,
            },
        )
    )

    payload = json.loads(response.text)
    structured = payload["result"]["structuredContent"]
    recorded_call = next(call for call in http.calls if call.get("method") == "POST")
    recorded = recorded_call["json"]
    assert structured["workflows"] is True
    assert structured["operations"] == ["mint_full_git_token", "push_current_head", "workflows", "full_github_api"]
    assert recorded["payload"]["workflows"] is True
    assert recorded["payload"]["operations"] == ["mint_full_git_token", "push_current_head", "workflows", "full_github_api"]


def test_azure_break_glass_approval_url_carries_intent_without_repo() -> None:
    url = _azure_break_glass_approval_url("95", "request-abc")

    assert url == "https://tank.romaine.life/sessions/95/break-glass/request-abc"
    assert "intent=azure-break-glass" not in url
    assert "repo=" not in url
    assert "reason=" not in url


def test_append_azure_break_glass_tool_adds_request_tool() -> None:
    raw = b'{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_transcript"}]}}'

    augmented = json.loads(_append_azure_break_glass_tool(raw))

    names = [tool["name"] for tool in augmented["result"]["tools"]]
    assert names[0] == "read_transcript"
    assert "request_azure_break_glass" in names
    azure_tool = next(t for t in augmented["result"]["tools"] if t["name"] == "request_azure_break_glass")
    assert "locked by default" in azure_tool["description"]
    assert "token" not in azure_tool["inputSchema"]["properties"]
    # Idempotent: a second pass must not duplicate any tool.
    again = json.loads(_append_azure_break_glass_tool(json.dumps(augmented).encode()))
    assert [t["name"] for t in again["result"]["tools"]] == names


def test_query_tank_db_tool_injected_only_for_non_restricted(monkeypatch) -> None:
    raw = b'{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_transcript"}]}}'

    monkeypatch.setattr("mcp_auth_proxy.server.RESTRICTED_GIT_ENABLED", False)
    non_restricted = json.loads(_append_azure_break_glass_tool(raw))
    nr_names = [t["name"] for t in non_restricted["result"]["tools"]]
    assert "query_tank_db" in nr_names
    qtool = next(t for t in non_restricted["result"]["tools"] if t["name"] == "query_tank_db")
    assert "READ-ONLY" in qtool["description"]
    assert qtool["inputSchema"]["required"] == ["sql"]

    monkeypatch.setattr("mcp_auth_proxy.server.RESTRICTED_GIT_ENABLED", True)
    restricted = json.loads(_append_azure_break_glass_tool(raw))
    assert "query_tank_db" not in [t["name"] for t in restricted["result"]["tools"]]


def test_handle_query_tank_db_tool_runs_read_query(monkeypatch) -> None:
    http = _FakeRawHTTPByMethod(
        get_response=_FakeRawResponse(200, b"{}"),
        post_response=_FakeRawResponse(
            200, b'{"columns":["id","status"],"rows":[["1","ok"]],"row_count":1,"truncated":false}'
        ),
    )
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")

    response = asyncio.run(
        _handle_query_tank_db_tool(
            http,
            _StaticTokenProvider("service-token"),
            7,
            {"sql": "SELECT id, status FROM session_events LIMIT 1"},
        )
    )

    payload = json.loads(response.text)["result"]
    structured = payload["structuredContent"]
    assert structured["columns"] == ["id", "status"]
    assert structured["rows"] == [["1", "ok"]]
    assert "id\tstatus" in payload["content"][0]["text"]
    grant_posts = [
        c for c in http.calls
        if c.get("method") == "POST" and "/db-read-query" in c.get("url", "")
    ]
    assert grant_posts and grant_posts[0]["json"]["sql"].startswith("SELECT id, status")


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
    assert structured["approval_url"].startswith("https://tank.romaine.life/sessions/95/break-glass/")
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


def test_tank_azure_break_glass_tool_reports_active_grant(monkeypatch) -> None:
    # When a grant is already active, the tool reports approved + expiry but does
    # NOT write any MCP config. Surfacing is now automatic (B-auto): the
    # orchestrator enqueues an approval turn and the pod-side runner adds the
    # server + rebuilds. The proxy never touches the (read-only) .mcp.json.
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
    assert "activation" not in structured


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
    parsed_approval = urlparse(recorded["payload"]["approval_url"])
    query = parse_qs(parsed_approval.query)
    assert parsed_approval.path == f"/sessions/95/break-glass/{recorded['event_id']}"
    assert "repo_scope" not in query
    assert "branch_scope" not in query


def test_github_write_tool_block_response_returns_mcp_error() -> None:
    body = json.dumps({
        "jsonrpc": "2.0",
        "id": 7,
        "method": "tools/call",
        "params": {"name": "mint_clone_token", "arguments": {"repos": ["romaine-life/tank-operator"], "write": True}},
    }).encode()

    response = _github_tool_block_response(body, "mint_clone_token")

    assert response is not None
    payload = json.loads(response.text)
    assert payload["id"] == 7
    assert "restricted Git mode" in payload["error"]["message"]
    # No in-pod governed-publish tool to point at anymore: the message tells the
    # agent to use normal git/gh (governed by the wall). No replacement_tool key.
    assert "replacement_tool" not in payload["error"]["data"]
    assert "git push" in payload["error"]["message"]
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


def test_github_update_issue_block_response_points_to_wall_governed_gh() -> None:
    body = json.dumps({
        "jsonrpc": "2.0",
        "id": 18,
        "method": "tools/call",
        "params": {"name": "update_issue", "arguments": {"owner": "romaine-life", "name": "tank-operator", "number": 1, "title": "new"}},
    }).encode()

    response = _github_tool_block_response(body, "update_issue")

    assert response is not None
    payload = json.loads(response.text)
    # PR title/body edits are no longer governed by an in-pod MCP tool: the agent
    # uses `gh pr edit`, which the wall governs. No replacement_tool / body_tool.
    assert "replacement_tool" not in payload["error"]["data"]
    assert "body_tool" not in payload["error"]["data"]
    assert "gh pr create|edit|ready|comment" in payload["error"]["message"]
    assert payload["error"]["data"]["break_glass_tool"] == "request_git_break_glass"


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


def _mint_clone_token_body(**arguments: object) -> bytes:
    return json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": "mint_clone_token",
            "arguments": {"repos": ["romaine-life/tank-operator"], **arguments},
        },
    }).encode()


async def _run_github_write_block_proxy(call_body: bytes) -> tuple[int, str, int]:
    """POST `call_body` through a proxy built with block_github_write_tools=True
    (the restricted-git GitHub-port wiring) against a recording upstream.
    Returns (status, response_text, upstream_call_count)."""
    upstream_calls = [0]

    async def upstream_handler(request: web.Request) -> web.Response:
        upstream_calls[0] += 1
        return web.json_response(
            {"jsonrpc": "2.0", "id": 1, "result": {"structuredContent": {"token": "ro-token"}}}
        )

    upstream_app = web.Application()
    upstream_app.router.add_route("*", "/{tail:.*}", upstream_handler)
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
                    block_github_write_tools=True,
                ),
            )
            proxy_server = TestServer(proxy_app)
            client = TestClient(proxy_server)
            await client.start_server()
            try:
                resp = await client.post("/", data=call_body)
                text = await resp.text()
                return resp.status, text, upstream_calls[0]
            finally:
                await client.close()
        finally:
            await http.close()
    finally:
        await upstream_server.close()


def test_read_only_mint_clone_token_is_blocked_in_restricted_mode() -> None:
    # The read-only carve-out is gone: with the agent-egress proxy (the wall)
    # fronting every session's GitHub traffic, the in-pod credential helper no
    # longer mints tokens, so mint_clone_token stays blocked in any shape and
    # never reaches mcp-github.
    status, body, upstream_calls = asyncio.run(
        _run_github_write_block_proxy(_mint_clone_token_body())
    )
    assert status == 200  # JSON-RPC error rides an HTTP 200
    assert upstream_calls == 0
    payload = json.loads(body)
    assert payload["error"]["code"] == -32010
    assert "restricted Git mode" in payload["error"]["message"]


def test_write_mint_clone_token_is_blocked_in_restricted_mode() -> None:
    # write / workflows / full mints would hand the shell a push-capable token —
    # blocked at the proxy, never reaching mcp-github.
    for args in ({"write": True}, {"write": True, "workflows": True}, {"full": True}):
        status, body, upstream_calls = asyncio.run(
            _run_github_write_block_proxy(_mint_clone_token_body(**args))
        )
        assert status == 200, args  # JSON-RPC error rides an HTTP 200
        assert upstream_calls == 0, args
        payload = json.loads(body)
        assert payload["error"]["code"] == -32010, args
        assert "restricted Git mode" in payload["error"]["message"], args


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


def test_resolve_ci_state_satisfies_missing_path_filtered_check_from_prior_green() -> None:
    http = _GitHubAPIFake(changed_paths=["docs/features/ci-watch/capabilities.md"])

    status, error, payload = asyncio.run(
        _resolve_ci_state(
            http,
            "github-token",
            "romaine-life",
            "tank-operator",
            http.head_sha,
            pr_number=1245,
            branch="tank/session/989/tank-operator",
        )
    )

    assert status == "succeeded", error
    evidence = payload["evidence"]
    assert evidence[-1]["check"] == "test"
    assert evidence[-1]["reason"] == "paths_unchanged_since_success"
    assert evidence[-1]["satisfied_by_sha"] == http.prior_sha


def test_resolve_ci_state_blocks_missing_check_when_trigger_paths_changed() -> None:
    http = _GitHubAPIFake(changed_paths=["backend-go/cmd/tank-operator/ci_merge.go"])

    status, error, payload = asyncio.run(
        _resolve_ci_state(
            http,
            "github-token",
            "romaine-life",
            "tank-operator",
            http.head_sha,
            pr_number=1245,
            branch="tank/session/989/tank-operator",
        )
    )

    assert status == "started"
    assert "inputs changed" in error
    assert payload["evidence"][-1]["status"] == "missing_changed_inputs"


def test_resolve_ci_state_blocks_missing_unfiltered_workflow() -> None:
    http = _GitHubAPIFake(
        changed_paths=["docs/features/ci-watch/capabilities.md"],
        workflow_text=(
            "name: Always runs\n"
            "on:\n"
            "  pull_request:\n"
            "jobs:\n"
            "  test:\n"
            "    runs-on: ubuntu-latest\n"
        ),
    )

    status, error, payload = asyncio.run(
        _resolve_ci_state(
            http,
            "github-token",
            "romaine-life",
            "tank-operator",
            http.head_sha,
            pr_number=1245,
            branch="tank/session/989/tank-operator",
        )
    )

    assert status == "started"
    assert "has no pull_request path filter" in error
    assert payload["evidence"][-1]["status"] == "missing_unfiltered_workflow"


def test_resolve_ci_state_does_not_expect_prior_manual_workflow_dispatch_run() -> None:
    http = _GitHubAPIFake(
        changed_paths=["docs/features/ci-watch/capabilities.md"],
        workflow_text=(
            "name: Manual proof image build\n"
            "on:\n"
            "  workflow_dispatch:\n"
            "jobs:\n"
            "  test:\n"
            "    runs-on: ubuntu-latest\n"
        ),
        run_event="workflow_dispatch",
    )

    status, error, payload = asyncio.run(
        _resolve_ci_state(
            http,
            "github-token",
            "romaine-life",
            "tank-operator",
            http.head_sha,
            pr_number=1245,
            branch="tank/session/989/tank-operator",
        )
    )

    assert status == "started"
    assert error == "checks are pending or have not appeared yet"
    assert payload["evidence"] == []


def test_watch_published_commit_records_clean_mergeability_via_single_pr(monkeypatch) -> None:
    # The /pulls?head= list endpoint returns mergeable=null (the mock PR in the
    # list carries no mergeable field); the watcher must fetch the single PR
    # (/pulls/{n}, which the mock reports clean) so it records a *succeeded*
    # mergeability observation instead of looping on "unknown" forever.
    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "95")
    http = _GovernedMergeHTTP("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
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
                "push_current_head",
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


def test_post_tank_control_action_adds_caller_session_headers(monkeypatch) -> None:
    async def run() -> list[dict]:
        http = _FakeHTTP(_FakeResponse(201, {"ok": True}))
        await _post_tank_control_action(
            http,
            {"Authorization": "Bearer service-token", "Content-Type": "application/json"},
            {
                "event_id": "ctrl-1",
                "invocation_id": "inv-1",
                "source_service": "mcp-github",
                "source_tool": "request_git_break_glass",
                "action": "github.break_glass.request",
                "status": "started",
                "target_kind": "github_repository",
                "target_ref": "https://github.com/romaine-life/auth",
            },
        )
        return http.calls

    monkeypatch.setattr("mcp_auth_proxy.server.ORIGIN_SESSION_ID", "954")
    monkeypatch.setattr("mcp_auth_proxy.server.SESSION_SCOPE", "tank-operator-slot-2")

    calls = asyncio.run(run())
    assert len(calls) == 1
    headers = calls[0]["headers"]
    assert headers["Authorization"] == "Bearer service-token"
    assert headers["X-Tank-Caller-System"] == "tank-operator"
    assert headers["X-Tank-Caller-Kind"] == "session"
    assert headers["X-Tank-Caller-Session-Id"] == "954"
    assert headers["X-Tank-Caller-Session-Scope"] == "tank-operator-slot-2"


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


def test_branch_scope_named_preserves_slashes() -> None:
    # Regression (found by a live restricted-agent smoke test): branch names
    # contain "/" (feature/x, fix/y, smoke/z), so the scoped-grant name sanitizer
    # must NOT drop the slash segment. The logic was inherited from the retired
    # single-token PR-lane labels and did rsplit("/")[-1] + slash-stripping, so a
    # grant for "feature/x" recorded "x"; _grant_branch_allows then compared the
    # raw pushed ref against it and refused the legitimate push
    # (branch_out_of_scope) — the "grant a branch, push fails" bug. Earlier tests
    # only used single-segment branch names, so it slipped through.
    for branch in (
        "smoke/branch-lane-grants",
        "feature/login",
        "fix/auth-bug",
        "release/v2.1",
    ):
        assert _sanitize_branch_scope_name(branch) == branch
        grant = {"branch_scope": {"kind": "named", "branches": [_sanitize_branch_scope_name(branch)]}}
        assert _grant_branch_allows(grant, branch) is True
    # refs/heads/ is still normalized away on both sides.
    assert _sanitize_branch_scope_name("refs/heads/feature/x") == "feature/x"
    # An out-of-scope branch is still refused (scope enforcement intact).
    out_of_scope = {"branch_scope": {"kind": "named", "branches": [_sanitize_branch_scope_name("feature/login")]}}
    assert _grant_branch_allows(out_of_scope, "feature/other") is False
