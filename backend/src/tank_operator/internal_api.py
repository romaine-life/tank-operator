"""Internal endpoints for in-cluster MCP servers (#57 stage 3).

Service-to-service auth: the caller presents its projected K8s SA token in
``Authorization: Bearer <token>``; the orchestrator validates it via
``TokenReview`` and accepts callers whose SA name is in
``INTERNAL_API_ALLOWED_SUBJECTS`` (default: the mcp-github SA). No Entra
session cookie or app-level JWT — these endpoints are out-of-band of the
user-facing auth flow and intentionally not reachable from the SPA's
allowlist of users.

The first endpoint, ``/api/internal/resolve-caller``, exists so mcp-github
can mint per-user GitHub App installation tokens. mcp-github reads the
``X-Forwarded-For`` chain its kube-rbac-proxy stamps on inbound requests
to recover the source session pod's IP, then asks this endpoint to map
``pod_ip → (email, installation_id)`` via the pod's
``tank-operator/owner-email`` annotation and the Cosmos profile row. The
orchestrator already owns both pieces of state (the session pods are
stamped by ``sessions.py``; profiles live in the same Cosmos container
``profiles.py`` reads), so centralizing the lookup here keeps mcp-github
out of Cosmos and keeps the trust boundary inside this process.
"""

from __future__ import annotations

import logging
import os
from dataclasses import dataclass

from fastapi import APIRouter, Depends, Header, HTTPException, Query
from kubernetes_asyncio import client

from .profiles import ProfileStore
from .sessions import SessionManager

log = logging.getLogger(__name__)

# Comma-separated ``namespace/serviceaccount`` pairs allowed to call the
# internal endpoints. Default is just mcp-github; widen via env to add
# future MCP servers (mcp-azure-personal, etc.) that need the same lookup.
_DEFAULT_ALLOWED_SUBJECTS = "mcp-github/mcp-github"
ALLOWED_CALLER_SUBJECTS = frozenset(
    s.strip().lower()
    for s in os.environ.get(
        "INTERNAL_API_ALLOWED_SUBJECTS", _DEFAULT_ALLOWED_SUBJECTS
    ).split(",")
    if s.strip()
)

# Email of the cluster operator. Profile lookups for this email are flagged
# ``is_host=true`` so mcp-github knows it can use the host's existing
# romaine-life-app installation directly instead of routing through the
# user-facing tank-operator-romaine-life App.
HOST_EMAIL = os.environ.get("HOST_EMAIL", "").strip().lower()


@dataclass
class CallerSubject:
    """A validated SA caller. ``name`` is the bare SA name, ``namespace`` its namespace."""

    namespace: str
    name: str

    @property
    def qualified(self) -> str:
        return f"{self.namespace}/{self.name}"


async def _validate_sa_token(token: str) -> CallerSubject:
    """Validate a K8s SA bearer token and return its identity.

    Uses the cluster's TokenReview API (the same authn primitive
    kube-rbac-proxy uses on incoming MCP traffic). Requires the
    orchestrator SA to hold ``system:auth-delegator`` (granted in the
    Helm chart). Returns the caller's namespace+SA on success; raises
    401 on invalid/expired tokens.
    """
    api_client = client.ApiClient()
    try:
        authn = client.AuthenticationV1Api(api_client)
        review = await authn.create_token_review(
            body=client.V1TokenReview(spec=client.V1TokenReviewSpec(token=token)),
        )
    finally:
        await api_client.close()

    status_obj = getattr(review, "status", None)
    if not status_obj or not getattr(status_obj, "authenticated", False):
        reason = getattr(status_obj, "error", "") or "token not authenticated"
        raise HTTPException(status_code=401, detail=f"invalid SA token: {reason}")

    user = getattr(status_obj, "user", None)
    username = getattr(user, "username", "") if user else ""
    # K8s SA tokens come back as ``system:serviceaccount:<ns>:<sa>``. Anything
    # else (a real user, a node identity) is rejected — this endpoint is
    # only for SA-to-SA traffic.
    parts = username.split(":")
    if len(parts) != 4 or parts[0] != "system" or parts[1] != "serviceaccount":
        raise HTTPException(
            status_code=403,
            detail=f"caller is not a service account: {username or 'unknown'}",
        )
    return CallerSubject(namespace=parts[2], name=parts[3])


async def authorized_caller(
    authorization: str | None = Header(default=None),
) -> CallerSubject:
    """FastAPI dependency: validate + authorize the inbound SA token."""
    if not authorization or not authorization.lower().startswith("bearer "):
        raise HTTPException(status_code=401, detail="missing bearer token")
    token = authorization[7:].strip()
    if not token:
        raise HTTPException(status_code=401, detail="empty bearer token")
    subject = await _validate_sa_token(token)
    if subject.qualified.lower() not in ALLOWED_CALLER_SUBJECTS:
        raise HTTPException(
            status_code=403,
            detail=f"caller {subject.qualified} not allowed for internal API",
        )
    return subject


def build_router(
    sessions: SessionManager,
    profiles: ProfileStore,
) -> APIRouter:
    """Build a fresh router wired to the given dependencies.

    Constructs the router per call (not at module scope) so tests can
    spin up multiple isolated apps without route closures from prior
    builds leaking into later ones.
    """
    router = APIRouter(prefix="/api/internal", tags=["internal"])

    @router.get("/resolve-caller")
    async def resolve_caller(
        pod_ip: str = Query(..., description="Pod IP of the session pod calling the MCP server."),
        _: CallerSubject = Depends(authorized_caller),
    ) -> dict:
        """Map ``pod_ip`` to the session owner's email + GitHub installation.

        Returns 404 when no session pod has that IP (e.g. the caller is
        not a tank-operator session pod, or the lookup raced a pod
        deletion). Returns ``installation_id: null`` when the user has
        not completed the GitHub App install flow — mcp-github falls
        back to the host's installation in that case.
        """
        pod = await sessions.find_pod_by_ip(pod_ip)
        if pod is None:
            raise HTTPException(
                status_code=404,
                detail=f"no session pod with IP {pod_ip}",
            )
        annotations = (pod.metadata.annotations or {}) if pod.metadata else {}
        email = (annotations.get("tank-operator/owner-email") or "").strip().lower()
        if not email:
            raise HTTPException(
                status_code=404,
                detail=f"session pod {pod.metadata.name} missing owner-email annotation",
            )
        profile = await profiles.get(email)
        return {
            "email": email,
            "installation_id": profile.installation_id,
            "is_host": bool(HOST_EMAIL) and email == HOST_EMAIL,
            "host_email": HOST_EMAIL or None,
            "pod_name": pod.metadata.name,
        }

    return router
