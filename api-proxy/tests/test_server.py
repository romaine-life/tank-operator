import asyncio
import base64
import json
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import AsyncMock, patch


def install_proto_stubs() -> None:
    for name in (
        "envoy",
        "envoy.service",
        "envoy.service.ext_proc",
        "envoy.service.ext_proc.v3",
        "envoy.config",
        "envoy.config.core",
        "envoy.config.core.v3",
        "envoy.type",
        "envoy.type.v3",
    ):
        pkg = sys.modules.setdefault(name, types.ModuleType(name))
        pkg.__path__ = []

    ext_proc_pb2_grpc = types.ModuleType(
        "envoy.service.ext_proc.v3.external_processor_pb2_grpc"
    )

    class ExternalProcessorServicer:
        pass

    ext_proc_pb2_grpc.ExternalProcessorServicer = ExternalProcessorServicer
    ext_proc_pb2_grpc.add_ExternalProcessorServicer_to_server = (
        lambda *args, **kwargs: None
    )
    sys.modules["envoy.service.ext_proc.v3.external_processor_pb2_grpc"] = (
        ext_proc_pb2_grpc
    )
    sys.modules["envoy.service.ext_proc.v3.external_processor_pb2"] = types.ModuleType(
        "envoy.service.ext_proc.v3.external_processor_pb2"
    )
    sys.modules["envoy.config.core.v3.base_pb2"] = types.ModuleType(
        "envoy.config.core.v3.base_pb2"
    )
    sys.modules["envoy.type.v3.http_status_pb2"] = types.ModuleType(
        "envoy.type.v3.http_status_pb2"
    )


install_proto_stubs()

from tank_api_proxy.server import (
    AuthInjector,
    ProxyConfig,
    _classify_refresh_failure,
    _config_from_env,
    _patch_blob,
)


def jwt_with_claims(claims: dict) -> str:
    def encode(obj: dict) -> str:
        raw = json.dumps(obj, separators=(",", ":")).encode()
        return base64.urlsafe_b64encode(raw).decode().rstrip("=")

    return f"{encode({'alg': 'none', 'typ': 'JWT'})}.{encode(claims)}.signature"


def codex_config(credentials_file: str) -> ProxyConfig:
    return ProxyConfig(
        provider="codex",
        credentials_file=credentials_file,
        token_url="https://auth.openai.com/oauth/token",
        client_id="app_EMoamEEZ73f0CkXaXp7hrann",
        kv_secret_name="codex-credentials",
        account_header="ChatGPT-Account-ID",
        fedramp_header="X-OpenAI-Fedramp",
        patch_last_refresh=True,
    )


def claude_config(credentials_file: str) -> ProxyConfig:
    return ProxyConfig(
        provider="claude",
        credentials_file=credentials_file,
        token_url="https://console.anthropic.com/v1/oauth/token",
        client_id="9d1c250a-e61b-44d9-88ed-5944d1962f5e",
        kv_secret_name="claude-code-credentials",
        account_header="",
        fedramp_header="",
        patch_last_refresh=False,
    )


class _UsageResponse:
    def __init__(self, status_code: int, body: object) -> None:
        self.status_code = status_code
        self._body = body
        self.text = json.dumps(body) if isinstance(body, dict) else str(body)

    def json(self) -> object:
        if isinstance(self._body, Exception):
            raise self._body
        return self._body


class _UsageClient:
    responses: list[object] = []
    request_count: int = 0

    def __init__(self, *args: object, **kwargs: object) -> None:
        pass

    async def __aenter__(self) -> "_UsageClient":
        return self

    async def __aexit__(self, *args: object) -> None:
        return None

    async def get(self, url: str, headers: dict[str, str]) -> object:
        self.__class__.request_count += 1
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


class ServerTests(unittest.TestCase):
    def test_patch_blob_updates_codex_tokens(self) -> None:
        blob = {
            "auth_mode": "chatgptAuthTokens",
            "tokens": {
                "id_token": "old-id",
                "access_token": "old-access",
                "refresh_token": "old-refresh",
                "account_id": "acct",
            },
            "last_refresh": "2026-01-01T00:00:00Z",
        }

        patched = _patch_blob(
            blob,
            "new-access",
            "new-refresh",
            3600,
            new_id="new-id",
            patch_last_refresh=True,
        )

        self.assertEqual(patched["tokens"]["id_token"], "new-id")
        self.assertEqual(patched["tokens"]["access_token"], "new-access")
        self.assertEqual(patched["tokens"]["refresh_token"], "new-refresh")
        self.assertNotEqual(patched["last_refresh"], blob["last_refresh"])

    def test_reload_extracts_codex_account_headers_from_id_token(self) -> None:
        id_token = jwt_with_claims(
            {
                "https://api.openai.com/auth": {
                    "chatgpt_account_id": "acct_123",
                    "chatgpt_account_is_fedramp": True,
                }
            }
        )
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "auth.json"
            path.write_text(
                json.dumps(
                    {
                        "tokens": {
                            "id_token": id_token,
                            "access_token": "access",
                            "refresh_token": "refresh",
                        },
                        "last_refresh": "2026-05-12T00:00:00Z",
                    }
                ),
                encoding="utf-8",
            )
            injector = AuthInjector(codex_config(str(path)))

            injector._reload_from_file()

            self.assertEqual(injector._cached_access, "access")
            self.assertEqual(injector._cached_refresh, "refresh")
            self.assertEqual(injector._cached_account_id, "acct_123")
            self.assertTrue(injector._cached_fedramp)

    def test_reload_does_not_clobber_fresher_memory_with_stale_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "auth.json"
            path.write_text(
                json.dumps(
                    {
                        "tokens": {
                            "access_token": "old-access",
                            "refresh_token": "old-refresh",
                        },
                        "last_refresh": "2026-05-12T00:00:00Z",
                    }
                ),
                encoding="utf-8",
            )
            injector = AuthInjector(codex_config(str(path)))
            injector._cached_blob = {
                "tokens": {
                    "access_token": "new-access",
                    "refresh_token": "new-refresh",
                },
                "last_refresh": "2026-05-13T00:00:00Z",
            }
            injector._cached_access = "new-access"
            injector._cached_refresh = "new-refresh"

            injector._reload_from_file()

            self.assertEqual(injector._cached_access, "new-access")
            self.assertEqual(injector._cached_refresh, "new-refresh")

    def test_usage_headers_include_codex_account_metadata(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            injector = AuthInjector(codex_config(str(Path(tmp) / "auth.json")))
            injector._cached_account_id = "acct_123"
            injector._cached_fedramp = True

            headers = injector._usage_headers("access-token")

            self.assertEqual(headers["Authorization"], "Bearer access-token")
            self.assertEqual(headers["User-Agent"], "Codex CLI/0.131.0")
            self.assertEqual(headers["ChatGPT-Account-ID"], "acct_123")
            self.assertEqual(headers["X-OpenAI-Fedramp"], "true")

    def test_usage_urls_are_provider_specific(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            claude = AuthInjector(claude_config(str(Path(tmp) / "claude.json")))
            codex = AuthInjector(codex_config(str(Path(tmp) / "codex.json")))

            self.assertIn("anthropic.com/api/oauth/usage", claude._usage_urls()[0])
            self.assertTrue(any("chatgpt.com" in url for url in codex._usage_urls()))

    def test_usage_snapshot_serves_last_good_snapshot_on_rate_limit(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            injector = AuthInjector(claude_config(str(Path(tmp) / "auth.json")))
            _UsageClient.request_count = 0
            _UsageClient.responses = [
                _UsageResponse(200, {"five_hour": {"utilization": 18}}),
                _UsageResponse(429, {"error": "rate_limited"}),
            ]

            with (
                patch("tank_api_proxy.server.httpx.AsyncClient", _UsageClient),
                patch.object(
                    injector,
                    "_get_access_token",
                    new=AsyncMock(return_value="access-token"),
                ),
            ):
                first = asyncio.run(injector.usage_snapshot())
                second = asyncio.run(injector.usage_snapshot())

            self.assertEqual(first["status"], "ok")
            self.assertNotIn("cached", first)
            self.assertEqual(second["status"], "ok")
            self.assertEqual(_UsageClient.request_count, 2)
            self.assertTrue(second["cached"])
            self.assertTrue(second["stale"])
            self.assertEqual(second["source_status_code"], 429)
            self.assertEqual(second["usage"], first["usage"])
            self.assertEqual(second["observed_at"], first["observed_at"])

    def test_usage_snapshot_without_cache_reports_rate_limit_error(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            injector = AuthInjector(claude_config(str(Path(tmp) / "auth.json")))
            _UsageClient.request_count = 0
            _UsageClient.responses = [
                _UsageResponse(429, {"error": "rate_limited"}),
            ]

            with (
                patch("tank_api_proxy.server.httpx.AsyncClient", _UsageClient),
                patch.object(
                    injector,
                    "_get_access_token",
                    new=AsyncMock(return_value="access-token"),
                ),
            ):
                payload = asyncio.run(injector.usage_snapshot())

            self.assertEqual(payload["status"], "error")
            self.assertEqual(payload["status_code"], 429)
            self.assertEqual(_UsageClient.request_count, 1)

    def test_usage_snapshot_does_not_hide_credential_failure_with_cache(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            injector = AuthInjector(claude_config(str(Path(tmp) / "auth.json")))
            _UsageClient.responses = [
                _UsageResponse(200, {"five_hour": {"utilization": 18}}),
                _UsageResponse(401, {"error": "expired"}),
                _UsageResponse(401, {"error": "expired"}),
            ]

            with (
                patch("tank_api_proxy.server.httpx.AsyncClient", _UsageClient),
                patch.object(
                    injector,
                    "_get_access_token",
                    new=AsyncMock(return_value="access-token"),
                ),
                patch.object(injector, "_refresh", new=AsyncMock(return_value=None)),
            ):
                first = asyncio.run(injector.usage_snapshot())
                second = asyncio.run(injector.usage_snapshot())

            self.assertEqual(first["status"], "ok")
            self.assertEqual(second["status"], "error")
            self.assertEqual(second["status_code"], 401)
            self.assertNotIn("cached", second)


class ConfigFromEnvTests(unittest.TestCase):
    def test_missing_provider_env_fails_fast(self) -> None:
        with patch.dict("os.environ", {}, clear=True):
            with self.assertRaisesRegex(RuntimeError, "PROXY_PROVIDER is required"):
                _config_from_env()

    def test_unknown_provider_env_fails_fast(self) -> None:
        with patch.dict("os.environ", {"PROXY_PROVIDER": "bogus"}, clear=True):
            with self.assertRaisesRegex(RuntimeError, "unknown PROXY_PROVIDER"):
                _config_from_env()

    def test_codex_requires_explicit_kv_key_env(self) -> None:
        with patch.dict(
            "os.environ",
            {
                "PROXY_PROVIDER": "codex",
                "CODEX_CREDENTIALS_FILE": "/etc/codex-credentials/auth.json",
            },
            clear=True,
        ):
            with self.assertRaisesRegex(RuntimeError, "CODEX_CREDENTIALS_KV_KEY is required"):
                _config_from_env()

    def test_claude_requires_explicit_kv_key_env(self) -> None:
        with patch.dict(
            "os.environ",
            {
                "PROXY_PROVIDER": "claude",
                "CLAUDE_CREDENTIALS_FILE": "/etc/claude-credentials/claude-code-credentials",
            },
            clear=True,
        ):
            with self.assertRaisesRegex(RuntimeError, "CLAUDE_CREDENTIALS_KV_KEY is required"):
                _config_from_env()

    def test_codex_uses_explicit_slot_kv_key(self) -> None:
        with patch.dict(
            "os.environ",
            {
                "PROXY_PROVIDER": "codex",
                "CODEX_CREDENTIALS_FILE": "/etc/codex-credentials/auth.json",
                "CODEX_CREDENTIALS_KV_KEY": "tank-operator-slot-2-codex-credentials",
            },
            clear=True,
        ):
            config = _config_from_env()

        self.assertEqual(config.kv_secret_name, "tank-operator-slot-2-codex-credentials")

def _codex_injector(credentials_file: str) -> AuthInjector:
    return AuthInjector(
        ProxyConfig(
            provider="codex",
            credentials_file=credentials_file,
            token_url="https://auth.openai.com/oauth/token",
            client_id="codex-client",
            kv_secret_name="codex-credentials",
            account_header="ChatGPT-Account-ID",
            fedramp_header="X-OpenAI-Fedramp",
            patch_last_refresh=True,
        )
    )


class RefreshKeeperTests(unittest.TestCase):
    """The proactive keeper warms tokens outside cancellable request streams."""

    def test_should_refresh_false_without_refresh_token(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            inj = _codex_injector(str(Path(tmp) / "t"))
            inj._cached_refresh = None
            self.assertFalse(inj._should_refresh())

    def test_should_refresh_true_when_invalidated(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            inj = _codex_injector(str(Path(tmp) / "t"))
            inj._cached_refresh = "r"
            inj._access_invalidated = True
            self.assertTrue(inj._should_refresh())

    def test_should_refresh_true_when_access_missing(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            inj = _codex_injector(str(Path(tmp) / "t"))
            inj._cached_refresh = "r"
            inj._cached_access = None
            self.assertTrue(inj._should_refresh())

    def test_should_refresh_false_when_token_is_fresh(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            inj = _codex_injector(str(Path(tmp) / "t"))
            inj._cached_refresh = "r"
            inj._cached_access = "a"
            inj._access_invalidated = False
            inj._cached_blob = {"access_token": "a", "last_refresh": "2099-01-01T00:00:00Z"}
            self.assertFalse(inj._should_refresh())

    def test_should_refresh_true_when_near_expiry(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            inj = _codex_injector(str(Path(tmp) / "t"))
            inj._cached_refresh = "r"
            inj._cached_access = "a"
            inj._access_invalidated = False
            # Expired in the past -> within skew -> needs refresh.
            inj._cached_blob = {"access_token": "a", "last_refresh": "2000-01-01T00:00:00Z"}
            self.assertTrue(inj._should_refresh())

    def test_refresh_skips_redundant_rotation_when_fresh(self) -> None:
        # The keeper and the reactive 401 path can both call _refresh; when the
        # token is already fresh and not invalidated, _refresh must NOT burn the
        # provider round trip (no httpx client constructed).
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "auth.json"
            path.write_text(
                json.dumps(
                    {
                        "access_token": "a",
                        "refresh_token": "r",
                        "last_refresh": "2099-01-01T00:00:00Z",
                    }
                ),
                encoding="utf-8",
            )
            inj = _codex_injector(str(path))
            inj._access_invalidated = False
            with patch("tank_api_proxy.server.httpx.AsyncClient") as mock_client:
                asyncio.run(inj._refresh())
                mock_client.assert_not_called()


class HealthSnapshotTests(unittest.TestCase):
    """Pins the contract the orchestrator's provider-health poller depends on.

    See docs/features/transcript/contract.md (the session.status surface
    that consumes this snapshot) and the orchestrator's poller in
    backend-go/internal/providerhealth/. The poller reads /health/<provider>
    every 30s, debounces sustained failures, and writes
    provider_credential_health rows that drive the transcript banner.
    A regression in the snapshot fields would silently break the banner.
    """

    def _fresh_injector(self) -> AuthInjector:
        with tempfile.TemporaryDirectory() as tmp:
            return AuthInjector(codex_config(str(Path(tmp) / "auth.json")))

    def test_snapshot_default_state_is_unknown(self) -> None:
        # Before any refresh attempt, the snapshot's result is "unknown"
        # — the orchestrator treats this as "no data yet, do not flip
        # Layer 1." The proxy intentionally does not infer healthy from
        # the bare absence of a failure; the cached blob may still be
        # serving a long-lived access token whose refresh has never been
        # exercised.
        injector = self._fresh_injector()
        snapshot = injector.health_snapshot()
        self.assertEqual(snapshot["provider"], "codex")
        self.assertEqual(snapshot["result"], "unknown")
        self.assertEqual(snapshot["reason"], "")
        self.assertEqual(snapshot["text"], "")
        self.assertIsNone(snapshot["last_attempted_at"])
        self.assertIsNone(snapshot["last_succeeded_at"])
        self.assertEqual(snapshot["attempt_id"], 0)

    def test_snapshot_after_success_records_success_state(self) -> None:
        injector = self._fresh_injector()
        injector._record_health_result("success", "", "")
        injector._health_attempt_id = 1
        injector._health_last_succeeded_at = 1700000000.0
        injector._health_last_attempted_at = 1700000000.0
        snapshot = injector.health_snapshot()
        self.assertEqual(snapshot["result"], "success")
        self.assertEqual(snapshot["last_succeeded_at"], 1700000000.0)

    def test_snapshot_after_failure_carries_reason_and_text(self) -> None:
        injector = self._fresh_injector()
        injector._record_health_result(
            "http_error",
            "refresh_token_reused",
            "Sign-in expired. The refresh token has already been used; re-authenticate to restore service.",
        )
        injector._health_attempt_id = 2
        injector._health_last_attempted_at = 1700000100.0
        snapshot = injector.health_snapshot()
        self.assertEqual(snapshot["result"], "http_error")
        self.assertEqual(snapshot["reason"], "refresh_token_reused")
        self.assertIn("re-authenticate", snapshot["text"].lower())
        # last_succeeded_at must remain None — a later failure does not
        # invalidate a never-observed success.
        self.assertIsNone(snapshot["last_succeeded_at"])


class ClassifyRefreshFailureTests(unittest.TestCase):
    """Pins how upstream OAuth /token error bodies become (reason, text).

    The refresh_token_reused incident that motivated the transcript
    banner: upstream returns {"error":{"code":"refresh_token_reused",
    "message":"Your refresh token has already been used..."}} and the
    SPA sees a banner explaining what to do. If this classifier drifts,
    the banner becomes content-free again.
    """

    class _StubResponse:
        def __init__(self, status_code: int, body: object) -> None:
            self.status_code = status_code
            self._body = body
            self.text = json.dumps(body) if isinstance(body, dict) else str(body)

        def json(self) -> object:
            if isinstance(self._body, dict):
                return self._body
            raise ValueError("non-json body")

    def test_classify_refresh_token_reused_returns_canonical_text(self) -> None:
        resp = self._StubResponse(
            401,
            {
                "error": {
                    "code": "refresh_token_reused",
                    "message": "Your refresh token has already been used to generate a new access token. Please try signing in again.",
                }
            },
        )
        reason, text = _classify_refresh_failure(resp)  # type: ignore[arg-type]
        self.assertEqual(reason, "refresh_token_reused")
        # Canonical copy preferred over the upstream message — the
        # upstream "Please try signing in again." reads awkwardly in
        # the SPA banner.
        self.assertIn("re-authenticate", text.lower())

    def test_classify_unknown_code_falls_back_to_status(self) -> None:
        resp = self._StubResponse(401, {"error": "bad_things"})
        reason, _ = _classify_refresh_failure(resp)  # type: ignore[arg-type]
        self.assertEqual(reason, "bad_things")

    def test_classify_non_json_body_uses_http_status(self) -> None:
        resp = self._StubResponse(500, "Internal Server Error")
        reason, text = _classify_refresh_failure(resp)  # type: ignore[arg-type]
        self.assertEqual(reason, "http_500")
        self.assertTrue(text)


if __name__ == "__main__":
    unittest.main()
