"""Internal endpoints for in-cluster MCP servers (#57 stage 3).

Service-to-service auth: the caller presents its projected K8s SA token in
``Authorization: Bearer <token>``; the orchestrator validates it via
``TokenReview`` and accepts callers whose SA name is in
``INTERNAL_API_ALLOWED_SUBJECTS`` (default: the mcp-github SA). No Entra
session cookie or app-level JWT — these endpoints are out-of-band of the
user-facing auth flow and intentionally not reachable from the SPA's
allowlist of users.

Two families of endpoints live here:

``/api/internal/resolve-caller``
    Maps pod_ip → (email, installation_id). Used by mcp-github to mint
    per-user GitHub App installation tokens (#57 stage 3).

``/api/internal/sessions/*``
    Session CRUD on behalf of a caller identified by pod IP. Used by
    mcp-tank-operator so any session pod can manage sessions without
    holding a user-facing JWT. The orchestrator resolves caller_pod_ip →
    owner email via the same find_pod_by_ip path as resolve-caller. Fail-
    open: if the pod IP is unknown, the endpoint returns a descriptive 422
    rather than a 500 so the MCP tool can surface a clean error message.
"""

from __future__ import annotations

import dataclasses
import logging
import os
from dataclasses import dataclass

from fastapi import APIRouter, Depends, Header, HTTPException, Query
from kubernetes_asyncio import client
from pydantic import BaseModel

from .profiles import ProfileStore
from .sessions import (
    HEADLESS_MODES,
    SUBSCRIPTION_HEADLESS_MODE,
    PodNotReady,
    SessionManager,
    SessionNotFound,
    SessionNotOwned,
)

log = logging.getLogger(__name__)

# Comma-separated ``namespace/serviceaccount`` pairs allowed to call the
# internal endpoints. Default is just mcp-github; widen via env to add
# future MCP servers (mcp-azure-personal, etc.) that need the same lookup.
_DEFAULT_ALLOWED_SUBJECTS = "mcp-github/mcp-github,mcp-tank-operator/mcp-tank-operator"
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

# Public hostname for the tank UI — used to build session URLs returned by
# internal session endpoints. Override via env in non-prod slots.
TANK_UI_HOST = os.environ.get("TANK_UI_HOST", "https://tank.romaine.life")


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


async def _resolve_email_from_pod_ip(
    caller_pod_ip: str, sessions: SessionManager
) -> str:
    """Map caller_pod_ip to owner email. Raises 422 with a user-visible message if not found."""
    pod = await sessions.find_pod_by_ip(caller_pod_ip)
    if pod is None:
        raise HTTPException(
            status_code=422,
            detail=(
                f"could not identify caller from pod IP {caller_pod_ip} — "
                "make sure you're calling from inside a tank-operator session pod"
            ),
        )
    annotations = (pod.metadata.annotations or {}) if pod.metadata else {}
    email = (annotations.get("tank-operator/owner-email") or "").strip().lower()
    if not email:
        pod_name = pod.metadata.name if pod.metadata else "unknown"
        raise HTTPException(
            status_code=422,
            detail=f"session pod {pod_name} missing owner-email annotation",
        )
    return email


def _session_url(session_id: str) -> str:
    return f"{TANK_UI_HOST.rstrip('/')}/?session={session_id}"


# ---------------------------------------------------------------------------
# Pydantic request bodies for internal session endpoints
# ---------------------------------------------------------------------------


class InternalCreateSessionBody(BaseModel):
    mode: str = SUBSCRIPTION_HEADLESS_MODE


class InternalPatchSessionBody(BaseModel):
    name: str | None = None


class InternalSendMessageBody(BaseModel):
    prompt: str
    model: str | None = None
    permission_mode: str | None = None


class InternalSpawnRunBody(BaseModel):
    prompt: str
    mode: str = SUBSCRIPTION_HEADLESS_MODE
    name: str | None = None
    model: str | None = None
    permission_mode: str | None = None


# ---------------------------------------------------------------------------


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

    # -----------------------------------------------------------------------
    # Internal session CRUD — used by mcp-tank-operator
    #
    # Every endpoint takes caller_pod_ip and resolves it to owner email.
    # The SA token in Authorization identifies the MCP server (e.g.
    # mcp-tank-operator/mcp-tank-operator); caller_pod_ip identifies the
    # session that owns the request. This keeps identity locked to the
    # source-IP chain and never lets callers claim arbitrary emails.
    # -----------------------------------------------------------------------

    @router.get("/sessions")
    async def internal_list_sessions(
        caller_pod_ip: str = Query(...),
        _: CallerSubject = Depends(authorized_caller),
    ) -> list:
        """List sessions owned by the caller identified by pod IP."""
        email = await _resolve_email_from_pod_ip(caller_pod_ip, sessions)
        rows = await sessions.list(owner=email)
        return [
            {**dataclasses.asdict(s), "url": _session_url(s.id)}
            for s in rows
        ]

    @router.post("/sessions", status_code=201)
    async def internal_create_session(
        body: InternalCreateSessionBody,
        caller_pod_ip: str = Query(...),
        _: CallerSubject = Depends(authorized_caller),
    ) -> dict:
        """Create a session owned by the caller identified by pod IP."""
        from .sessions import SESSION_MODES

        email = await _resolve_email_from_pod_ip(caller_pod_ip, sessions)
        if body.mode not in SESSION_MODES:
            raise HTTPException(status_code=400, detail=f"unknown mode: {body.mode}")
        session = await sessions.create(owner=email, mode=body.mode)
        return {**dataclasses.asdict(session), "url": _session_url(session.id)}

    @router.delete("/sessions/{session_id}")
    async def internal_delete_session(
        session_id: str,
        caller_pod_ip: str = Query(...),
        _: CallerSubject = Depends(authorized_caller),
    ) -> dict:
        """Delete a session owned by the caller identified by pod IP."""
        email = await _resolve_email_from_pod_ip(caller_pod_ip, sessions)
        try:
            await sessions.delete(owner=email, session_id=session_id)
        except SessionNotFound:
            raise HTTPException(status_code=404, detail="session not found")
        except SessionNotOwned:
            raise HTTPException(status_code=403, detail="session not owned by caller")
        return {"id": session_id, "status": "deleted"}

    @router.patch("/sessions/{session_id}")
    async def internal_patch_session(
        session_id: str,
        body: InternalPatchSessionBody,
        caller_pod_ip: str = Query(...),
        _: CallerSubject = Depends(authorized_caller),
    ) -> dict:
        """Set a friendly display name on a session."""
        email = await _resolve_email_from_pod_ip(caller_pod_ip, sessions)
        try:
            session = await sessions.set_name(
                owner=email, session_id=session_id, name=body.name
            )
        except SessionNotFound:
            raise HTTPException(status_code=404, detail="session not found")
        except SessionNotOwned:
            raise HTTPException(status_code=403, detail="session not owned by caller")
        return {**dataclasses.asdict(session), "url": _session_url(session.id)}

    @router.post("/sessions/{session_id}/messages", status_code=202)
    async def internal_send_message(
        session_id: str,
        body: InternalSendMessageBody,
        caller_pod_ip: str = Query(...),
        _: CallerSubject = Depends(authorized_caller),
    ) -> dict:
        """Dispatch a follow-up prompt to an existing headless session.

        The target session must be in a headless mode. Fire-and-forget:
        returns 202 once the run has been launched on the pod.
        """
        if not body.prompt or not body.prompt.strip():
            raise HTTPException(status_code=400, detail="missing prompt")
        email = await _resolve_email_from_pod_ip(caller_pod_ip, sessions)
        try:
            await sessions.dispatch_headless(
                owner=email,
                session_id=session_id,
                prompt=body.prompt,
                follow_up=True,
                model=body.model or "",
                permission_mode=body.permission_mode or "",
            )
        except SessionNotFound:
            raise HTTPException(status_code=404, detail="session not found")
        except SessionNotOwned:
            raise HTTPException(status_code=403, detail="session not owned by caller")
        except PodNotReady:
            raise HTTPException(status_code=503, detail="session pod not ready")
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc))
        return {"session_id": session_id, "status": "dispatched"}

    @router.post("/sessions/run", status_code=202)
    async def internal_spawn_run(
        body: InternalSpawnRunBody,
        caller_pod_ip: str = Query(...),
        _: CallerSubject = Depends(authorized_caller),
    ) -> dict:
        """Create a headless session and dispatch the first prompt to it.

        Equivalent to the user-facing POST /api/sessions/run but
        resolved to the caller's identity via pod IP. Returns 202 once
        the run has been launched.
        """
        if not body.prompt or not body.prompt.strip():
            raise HTTPException(status_code=400, detail="missing prompt")
        if body.mode not in HEADLESS_MODES:
            raise HTTPException(
                status_code=400,
                detail=f"mode {body.mode!r} does not support headless runs",
            )
        email = await _resolve_email_from_pod_ip(caller_pod_ip, sessions)
        session = await sessions.create(owner=email, mode=body.mode)
        if body.name:
            try:
                session = await sessions.set_name(
                    owner=email, session_id=session.id, name=body.name
                )
            except (SessionNotFound, SessionNotOwned):
                pass
        try:
            await sessions.dispatch_headless(
                owner=email,
                session_id=session.id,
                prompt=body.prompt,
                follow_up=False,
                model=body.model or "",
                permission_mode=body.permission_mode or "",
            )
        except PodNotReady:
            raise HTTPException(status_code=503, detail="session pod not ready")
        except ValueError as exc:
            raise HTTPException(status_code=400, detail=str(exc))
        return {
            "session": {**dataclasses.asdict(session), "url": _session_url(session.id)},
            "status": "dispatched",
        }

    return router
