"""Cross-replica refresh-lease behavior (issue #1079 item 6).

Two layers:
  - RefreshLease against a mocked Kubernetes Lease API (acquire semantics);
  - AuthInjector._refresh's defer / fail-open arms with the lease stubbed.
"""

from __future__ import annotations

import asyncio
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import AsyncMock, patch

import httpx

from test_server import install_proto_stubs

install_proto_stubs()

import tank_api_proxy.lease as lease_mod
from tank_api_proxy.lease import LeaseUnavailable, RefreshLease
from tank_api_proxy.server import AuthInjector, ProxyConfig


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


class FakeLeaseAPI:
    """Minimal coordination.k8s.io Lease server for MockTransport."""

    def __init__(self, existing: dict | None = None) -> None:
        self.lease = existing
        self.puts = 0

    def handler(self, request: httpx.Request) -> httpx.Response:
        if request.method == "GET":
            if self.lease is None:
                return httpx.Response(404, json={"reason": "NotFound"})
            return httpx.Response(200, json=self.lease)
        if request.method == "POST":
            if self.lease is not None:
                return httpx.Response(409, json={"reason": "AlreadyExists"})
            self.lease = json.loads(request.content)
            return httpx.Response(201, json=self.lease)
        if request.method == "PUT":
            self.puts += 1
            self.lease = json.loads(request.content)
            return httpx.Response(200, json=self.lease)
        return httpx.Response(405)


class RefreshLeaseTests(unittest.TestCase):
    def _lease(self, tmp: str, api: FakeLeaseAPI) -> RefreshLease:
        sa = Path(tmp) / "sa"
        sa.mkdir(exist_ok=True)
        (sa / "namespace").write_text("tank-operator", encoding="utf-8")
        (sa / "token").write_text("sa-token", encoding="utf-8")
        (sa / "ca.crt").write_text("ca", encoding="utf-8")

        real_client = httpx.AsyncClient
        self._patches = [
            patch.object(lease_mod, "_SA_DIR", str(sa)),
            patch.dict(
                "os.environ",
                {"KUBERNETES_SERVICE_HOST": "k8s.invalid", "KUBERNETES_SERVICE_PORT": "443", "HOSTNAME": "proxy-a"},
            ),
            patch.object(
                lease_mod.httpx,
                "AsyncClient",
                lambda **kwargs: real_client(transport=httpx.MockTransport(api.handler)),
            ),
        ]
        for p in self._patches:
            p.start()
            self.addCleanup(p.stop)
        return RefreshLease("api-proxy-refresh-codex")

    def test_acquires_when_absent_via_create(self) -> None:
        api = FakeLeaseAPI()
        with tempfile.TemporaryDirectory() as tmp:
            lease = self._lease(tmp, api)
            self.assertTrue(asyncio.run(lease.try_acquire()))
            self.assertEqual(api.lease["spec"]["holderIdentity"], "proxy-a")

    def test_defers_when_peer_holds_unexpired(self) -> None:
        now = lease_mod._rfc3339_micro(lease_mod._now())
        api = FakeLeaseAPI(
            existing={
                "metadata": {"name": "api-proxy-refresh-codex"},
                "spec": {
                    "holderIdentity": "proxy-b",
                    "leaseDurationSeconds": 120,
                    "renewTime": now,
                },
            }
        )
        with tempfile.TemporaryDirectory() as tmp:
            lease = self._lease(tmp, api)
            self.assertFalse(asyncio.run(lease.try_acquire()))
            self.assertEqual(api.puts, 0, "must not steal a live peer's lease")

    def test_takes_over_expired_lease(self) -> None:
        api = FakeLeaseAPI(
            existing={
                "metadata": {"name": "api-proxy-refresh-codex"},
                "spec": {
                    "holderIdentity": "proxy-b",
                    "leaseDurationSeconds": 1,
                    "renewTime": "2000-01-01T00:00:00.000000Z",
                },
            }
        )
        with tempfile.TemporaryDirectory() as tmp:
            lease = self._lease(tmp, api)
            self.assertTrue(asyncio.run(lease.try_acquire()))
            self.assertEqual(api.lease["spec"]["holderIdentity"], "proxy-a")

    def test_release_clears_only_own_holder(self) -> None:
        api = FakeLeaseAPI()
        with tempfile.TemporaryDirectory() as tmp:
            lease = self._lease(tmp, api)
            self.assertTrue(asyncio.run(lease.try_acquire()))
            asyncio.run(lease.release())
            self.assertEqual(api.lease["spec"]["holderIdentity"], "")
            # A peer's lease is never cleared.
            api.lease["spec"]["holderIdentity"] = "proxy-b"
            asyncio.run(lease.release())
            self.assertEqual(api.lease["spec"]["holderIdentity"], "proxy-b")

    def test_out_of_cluster_raises_unavailable(self) -> None:
        with patch.dict("os.environ", {}, clear=False):
            import os

            os.environ.pop("KUBERNETES_SERVICE_HOST", None)
            with self.assertRaises(LeaseUnavailable):
                RefreshLease("api-proxy-refresh-codex")


def _expired_codex_blob() -> dict:
    return {
        "auth_mode": "chatgptAuthTokens",
        "tokens": {"access_token": "old-access", "refresh_token": "r1", "account_id": "acct"},
        "last_refresh": "2000-01-01T00:00:00Z",
    }


class RefreshLeaseIntegrationTests(unittest.TestCase):
    """_refresh's lease arms, with RefreshLease stubbed at the server seam."""

    def _injector(self, tmp: str) -> AuthInjector:
        path = Path(tmp) / "auth.json"
        path.write_text(json.dumps(_expired_codex_blob()), encoding="utf-8")
        inj = AuthInjector(codex_config(str(path)))
        inj._access_invalidated = True
        return inj

    def test_defers_to_peer_and_skips_provider(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            inj = self._injector(tmp)
            fake_lease = AsyncMock()
            fake_lease.try_acquire.return_value = False
            with (
                patch("tank_api_proxy.server.RefreshLease", return_value=fake_lease),
                patch.object(inj, "_await_peer_rotation", AsyncMock(return_value=True)),
                patch("tank_api_proxy.server.httpx.AsyncClient") as mock_client,
            ):
                asyncio.run(inj._refresh())
                mock_client.assert_not_called()

    def test_lease_unavailable_fails_open_to_rotation(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            inj = self._injector(tmp)
            with (
                patch("tank_api_proxy.server.RefreshLease", side_effect=LeaseUnavailable("no cluster")),
                patch.object(inj, "_rotate_with_provider", AsyncMock()) as rotate,
            ):
                asyncio.run(inj._refresh())
                rotate.assert_awaited_once()

    def test_acquired_lease_rotates_and_releases(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            inj = self._injector(tmp)
            fake_lease = AsyncMock()
            fake_lease.try_acquire.return_value = True
            with (
                patch("tank_api_proxy.server.RefreshLease", return_value=fake_lease) as ctor,
                patch.object(inj, "_rotate_with_provider", AsyncMock()) as rotate,
            ):
                asyncio.run(inj._refresh())
                rotate.assert_awaited_once()
                fake_lease.release.assert_awaited_once()
                # Lease identity is the credential CHAIN (kv secret), not the
                # provider: both claude deployments run provider=claude but
                # rotate unrelated chains and must not share a lease.
                ctor.assert_called_once_with("api-proxy-refresh-codex-credentials")

    def test_peer_timeout_rotates_anyway(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            inj = self._injector(tmp)
            fake_lease = AsyncMock()
            fake_lease.try_acquire.return_value = False
            with (
                patch("tank_api_proxy.server.RefreshLease", return_value=fake_lease),
                patch.object(inj, "_await_peer_rotation", AsyncMock(return_value=False)),
                patch.object(inj, "_rotate_with_provider", AsyncMock()) as rotate,
            ):
                asyncio.run(inj._refresh())
                rotate.assert_awaited_once()
                fake_lease.release.assert_not_awaited()


if __name__ == "__main__":
    unittest.main()
