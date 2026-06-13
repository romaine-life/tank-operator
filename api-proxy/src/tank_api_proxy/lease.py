"""Refresh-scoped Kubernetes Lease for cross-replica rotation exclusivity.

Why this exists (#1079 item 6): the OAuth refresh single-flight in
``server.py`` is per-process. With more than one api-proxy replica, two pods
hitting token expiry concurrently would both rotate the SAME single-use
refresh_token; providers with reuse detection can revoke the whole grant
family, killing the chain for every pod until a human re-seeds via the
credential wizard (docs/api-proxy-auth.md → "Single-flight refresh",
"multi-deployment hazards"). Until this lease landed, that risk was why the
proxy deployments were pinned to replicas: 1.

Shape: this is NOT standing leader election. A pod tries to take the lease
only for the duration of one rotation; a pod that loses waits for the
winner's rotation to propagate through the existing KV → ESO → file pipeline
(the loser's ``_reload_from_file`` freshness guard picks it up) instead of
calling the provider.

Failure posture: FAIL OPEN. If the Lease API is unreachable, RBAC is
missing, or anything else goes wrong, the caller proceeds exactly as the
pre-lease code did — a lease outage must degrade to "the old risk level",
never to "no rotations". Every outcome is counted so the flip to
replicas > 1 can be gated on observed lease health.
"""

from __future__ import annotations

import datetime as _dt
import logging
import os

import httpx

log = logging.getLogger("tank-api-proxy.lease")

_SA_DIR = "/var/run/secrets/kubernetes.io/serviceaccount"

# A rotation is seconds of work; the TTL only has to outlive a wedged winner.
LEASE_TTL_SECONDS = 120


class LeaseUnavailable(Exception):
    """The Lease API could not answer — callers fail open."""


def _now() -> _dt.datetime:
    return _dt.datetime.now(_dt.timezone.utc)


def _rfc3339_micro(ts: _dt.datetime) -> str:
    # Lease spec times are MicroTime.
    return ts.strftime("%Y-%m-%dT%H:%M:%S.%f") + "Z"


def _parse_micro(raw: str | None) -> _dt.datetime | None:
    if not raw:
        return None
    try:
        return _dt.datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        return None


class RefreshLease:
    """One named coordination.k8s.io Lease, spoken over the in-cluster API."""

    def __init__(self, name: str, holder: str | None = None) -> None:
        self.name = name
        self.holder = holder or os.environ.get("HOSTNAME") or "api-proxy"
        host = os.environ.get("KUBERNETES_SERVICE_HOST")
        port = os.environ.get("KUBERNETES_SERVICE_PORT", "443")
        if not host:
            raise LeaseUnavailable("not running in-cluster (no KUBERNETES_SERVICE_HOST)")
        try:
            with open(f"{_SA_DIR}/namespace", encoding="utf-8") as f:
                self.namespace = f.read().strip()
        except OSError as exc:
            raise LeaseUnavailable(f"service account namespace unreadable: {exc}") from exc
        self._base = f"https://{host}:{port}/apis/coordination.k8s.io/v1/namespaces/{self.namespace}/leases"
        self._ca = f"{_SA_DIR}/ca.crt"

    def _token(self) -> str:
        # Re-read per call: projected SA tokens rotate.
        try:
            with open(f"{_SA_DIR}/token", encoding="utf-8") as f:
                return f.read().strip()
        except OSError as exc:
            raise LeaseUnavailable(f"service account token unreadable: {exc}") from exc

    async def _request(self, client: httpx.AsyncClient, method: str, url: str, **kwargs) -> httpx.Response:
        try:
            return await client.request(
                method,
                url,
                headers={"Authorization": f"Bearer {self._token()}"},
                **kwargs,
            )
        except httpx.HTTPError as exc:
            raise LeaseUnavailable(f"lease API request failed: {exc}") from exc

    async def try_acquire(self) -> bool:
        """Take the lease for one rotation. True = this pod rotates;
        False = a live peer holds it; LeaseUnavailable = fail open."""
        now = _now()
        async with httpx.AsyncClient(timeout=5.0, verify=self._ca) as client:
            resp = await self._request(client, "GET", f"{self._base}/{self.name}")
            if resp.status_code == 404:
                body = {
                    "apiVersion": "coordination.k8s.io/v1",
                    "kind": "Lease",
                    "metadata": {"name": self.name, "namespace": self.namespace},
                    "spec": {
                        "holderIdentity": self.holder,
                        "leaseDurationSeconds": LEASE_TTL_SECONDS,
                        "acquireTime": _rfc3339_micro(now),
                        "renewTime": _rfc3339_micro(now),
                    },
                }
                created = await self._request(client, "POST", self._base, json=body)
                if created.status_code == 201:
                    return True
                if created.status_code == 409:
                    return False  # raced another pod's create; they rotate
                raise LeaseUnavailable(f"lease create returned {created.status_code}: {created.text[:200]}")
            if resp.status_code != 200:
                raise LeaseUnavailable(f"lease get returned {resp.status_code}: {resp.text[:200]}")

            lease = resp.json()
            spec = lease.get("spec") or {}
            holder = spec.get("holderIdentity") or ""
            renew = _parse_micro(spec.get("renewTime")) or _parse_micro(spec.get("acquireTime"))
            duration = int(spec.get("leaseDurationSeconds") or LEASE_TTL_SECONDS)
            held = bool(holder) and renew is not None and (now - renew).total_seconds() < duration
            if held and holder != self.holder:
                return False
            # Free, expired, or ours from a crashed prior attempt: claim it.
            lease.setdefault("spec", {})
            lease["spec"].update(
                {
                    "holderIdentity": self.holder,
                    "leaseDurationSeconds": LEASE_TTL_SECONDS,
                    "acquireTime": _rfc3339_micro(now),
                    "renewTime": _rfc3339_micro(now),
                }
            )
            updated = await self._request(client, "PUT", f"{self._base}/{self.name}", json=lease)
            if updated.status_code == 200:
                return True
            if updated.status_code == 409:
                return False  # optimistic-concurrency loss; the winner rotates
            raise LeaseUnavailable(f"lease update returned {updated.status_code}: {updated.text[:200]}")

    async def release(self) -> None:
        """Best-effort: clear the holder so peers don't wait out the TTL."""
        try:
            async with httpx.AsyncClient(timeout=5.0, verify=self._ca) as client:
                resp = await self._request(client, "GET", f"{self._base}/{self.name}")
                if resp.status_code != 200:
                    return
                lease = resp.json()
                spec = lease.get("spec") or {}
                if spec.get("holderIdentity") != self.holder:
                    return
                lease["spec"]["holderIdentity"] = ""
                lease["spec"]["renewTime"] = _rfc3339_micro(_now())
                await self._request(client, "PUT", f"{self._base}/{self.name}", json=lease)
        except LeaseUnavailable:
            log.warning("lease release failed for %s; peers wait out the TTL", self.name)
