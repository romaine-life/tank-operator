import base64
import json
import sys
import tempfile
import types
import unittest
from pathlib import Path


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

from tank_api_proxy.server import AuthInjector, ProxyConfig, _patch_blob


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


if __name__ == "__main__":
    unittest.main()
