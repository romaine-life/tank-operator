import os
import logging
import time
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any, AsyncIterator

from fastapi import (
    Cookie,
    Depends,
    FastAPI,
    HTTPException,
    Request,
    WebSocket,
    WebSocketDisconnect,
    status,
)
from fastapi.responses import FileResponse, JSONResponse, RedirectResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel

from .auth import (
    COOKIE_NAME,
    SESSION_TTL_SECONDS,
    User,
    _decode_session_token,
    current_user,
    current_user_ws,
    exchange_microsoft_token,
    gravatar_url,
    mint_install_state,
    verify_install_state,
)
from .credentials_seed import (
    CredentialsSeedError,
    harvest_and_save,
    harvest_codex_and_save,
)
from .exec_proxy import bridge, exec_stream_to_websocket, exec_write_file
from .profiles import ProfileStore, SessionRegistryStore
from .sessions import (
    CODEX_HEADLESS_MODE,
    DEFAULT_SESSION_MODE,
    HEADLESS_MODES,
    SESSION_MODES,
    SESSIONS_NAMESPACE,
    PodNotReady,
    SessionInfo,
    SessionManager,
    SessionNotFound,
    SessionNotOwned,
    SessionTerminalUnavailable,
)

session_registry = SessionRegistryStore()
sessions = SessionManager(registry=session_registry)
profiles = ProfileStore()
log = logging.getLogger(__name__)

PASTE_IMAGE_TYPES = {
    "image/png": "png",
    "image/jpeg": "jpg",
    "image/webp": "webp",
    "image/gif": "gif",
}
MAX_PASTE_IMAGE_BYTES = int(
    os.environ.get("MAX_PASTE_IMAGE_BYTES", str(8 * 1024 * 1024))
)


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncIterator[None]:
    await profiles.startup()
    await session_registry.startup()
    await sessions.startup()
    try:
        yield
    finally:
        await sessions.shutdown()
        await session_registry.shutdown()
        await profiles.shutdown()


app = FastAPI(lifespan=lifespan)


class LoginBody(BaseModel):
    credential: str


class LoginResponse(BaseModel):
    token: str
    user: dict[str, str]


class TerminalDebugBody(BaseModel):
    event: str
    session_id: str | None = None
    mode: str | None = None
    payload: dict[str, Any] = {}


MAX_HEADLESS_PROMPT_BYTES = int(
    os.environ.get("MAX_HEADLESS_PROMPT_BYTES", str(256 * 1024))
)


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/api/config")
async def config() -> dict[str, str]:
    """Public auth config consumed by the frontend to bootstrap MSAL."""
    return {
        "entra_client_id": os.environ.get("ENTRA_CLIENT_ID", ""),
        "entra_authority": "https://login.microsoftonline.com/common",
    }


@app.post("/api/auth/microsoft/login", response_model=LoginResponse)
async def microsoft_login(body: LoginBody, request: Request) -> JSONResponse:
    session_token, user = await exchange_microsoft_token(body.credential)
    # Ensure a profile row exists for the authenticated email. Cheap on
    # repeat logins (single-document read), creates the row only on first
    # login. Hooked here rather than in current_user so we don't add a
    # Cosmos round-trip to every request.
    profile = await profiles.get_or_create(user.email)
    secure = request.url.scheme == "https"
    response = JSONResponse(
        {
            "token": session_token,
            "user": {
                "sub": user.sub,
                "email": user.email,
                "name": user.name,
                "avatar_url": gravatar_url(user.email),
                "github_login": profile.github_login,
                "installation_id": profile.installation_id,
            },
        }
    )
    response.set_cookie(
        key=COOKIE_NAME,
        value=session_token,
        max_age=SESSION_TTL_SECONDS,
        httponly=True,
        secure=secure,
        samesite="lax",
        path="/",
    )
    return response


@app.post("/api/auth/logout")
async def logout() -> JSONResponse:
    response = JSONResponse({"status": "ok"})
    response.delete_cookie(COOKIE_NAME, path="/")
    return response


@app.get("/api/auth/me", response_model=dict)
async def me(user: User = Depends(current_user)) -> dict:
    """Identity + profile state for the signed-in user.

    `installation_id` is null until the user installs the GitHub App via the
    onboarding flow (#57 stage 2). The frontend uses its presence as the
    signal for whether to show the install wall.
    """
    profile = await profiles.get_or_create(user.email)
    return {
        "sub": user.sub,
        "email": user.email,
        "name": user.name,
        "avatar_url": gravatar_url(user.email),
        "github_login": profile.github_login,
        "installation_id": profile.installation_id,
    }


@app.post("/api/debug/terminal")
async def terminal_debug(
    body: TerminalDebugBody,
    user: User = Depends(current_user),
) -> dict[str, str]:
    log.info(
        "terminal debug event=%s user=%s session=%s mode=%s payload=%s",
        body.event,
        user.email,
        body.session_id,
        body.mode,
        body.payload,
    )
    return {"status": "ok"}


# ----------------------------------------------------------------------------
# GitHub App install flow (#57 stage 2)
#
# Onboarding wall in the SPA → /api/github/install/url 302s to GitHub's
# install page → user grants on GitHub → GitHub redirects to the App's
# Setup URL (configured in the GitHub UI to point at /api/github/install/
# callback) → callback persists installation_id on the profile and 302s
# back to /. Whole flow is browser-driven; no orchestrator-side outbound
# calls to GitHub.
# ----------------------------------------------------------------------------

GITHUB_APP_SLUG = os.environ.get("GITHUB_APP_SLUG", "tank-operator")


@app.get("/api/github/install/url")
async def github_install_url(user: User = Depends(current_user)) -> RedirectResponse:
    """Redirect the caller to GitHub's install consent page.

    The state JWT binds the install flow to the caller's email so the
    callback can refuse a redirect that didn't originate from us. 10-min
    TTL — long enough for "click → grant → return" without leaving a stale
    token usable to retry later.
    """
    state = mint_install_state(user.email)
    target = (
        f"https://github.com/apps/{GITHUB_APP_SLUG}/installations/new?state={state}"
    )
    return RedirectResponse(url=target, status_code=302)


@app.get("/api/github/install/callback")
async def github_install_callback(
    request: Request,
    installation_id: int | None = None,
    setup_action: str | None = None,
    state: str | None = None,
    auth_token: str | None = Cookie(default=None),
) -> RedirectResponse:
    """GitHub's redirect target after install consent.

    Validates the state JWT we minted and the caller's session cookie agree
    on email — defense-in-depth against a phishing scenario where an
    attacker mints a state for their own email and tricks a victim into
    completing the install. Without the cookie check, the victim's
    installation_id would land under the attacker's profile.

    On any validation failure, redirect to /?install_error=<reason> so the
    SPA can render a banner instead of leaving the user on a backend 4xx.
    """

    def _err(reason: str) -> RedirectResponse:
        return RedirectResponse(url=f"/?install_error={reason}", status_code=302)

    if not state:
        return _err("missing_state")
    if not installation_id:
        # GitHub sends `setup_action=request` for org-controlled installs that
        # are pending admin approval; installation_id arrives later via the
        # webhook (out of scope for stage 2).
        return _err(
            "pending_approval"
            if setup_action == "request"
            else "missing_installation_id"
        )
    try:
        state_email = verify_install_state(state)
    except HTTPException:
        return _err("invalid_state")

    if not auth_token:
        return _err("session_expired")
    try:
        cookie_user = _decode_session_token(auth_token)
    except HTTPException:
        return _err("session_invalid")

    if cookie_user.email.lower() != state_email:
        return _err("email_mismatch")

    await profiles.update_installation(
        email=state_email, installation_id=installation_id, github_login=None
    )
    return RedirectResponse(url="/", status_code=302)


class CreateSessionBody(BaseModel):
    # Body is optional on the wire (POST with no JSON still works) so the
    # default-mode `+ new` button doesn't have to send anything.
    mode: str = DEFAULT_SESSION_MODE


class CreateSessionWithContextBody(BaseModel):
    glimmung_run_id: str
    glimmung_issue_id: str
    glimmung_pr_id: str | None = None
    validation_url: str | None = None
    caller_email: str | None = None
    mode: str = DEFAULT_SESSION_MODE


class CreateSessionWithContextResponse(BaseModel):
    session_url: str
    session: SessionInfo


@app.post("/api/sessions")
async def create_session(
    body: CreateSessionBody | None = None,
    user: User = Depends(current_user),
) -> SessionInfo:
    mode = body.mode if body else DEFAULT_SESSION_MODE
    if mode not in SESSION_MODES:
        raise HTTPException(status_code=400, detail=f"unknown mode: {mode}")
    return await sessions.create(owner=user.email, mode=mode)


@app.post("/api/sessions/with-context", response_model=CreateSessionWithContextResponse)
async def create_session_with_context(
    body: CreateSessionWithContextBody,
    request: Request,
    user: User = Depends(current_user),
) -> CreateSessionWithContextResponse:
    """Create a fresh session preloaded with canonical glimmung context.

    Glimmung passes ids, not rendered text. The session pod can then use
    mcp-glimmung to read the Issue / Run / PR details from the source of
    truth while still booting with enough context to orient the operator.
    """
    if body.mode not in SESSION_MODES:
        raise HTTPException(status_code=400, detail=f"unknown mode: {body.mode}")
    if body.caller_email and body.caller_email.lower() != user.email.lower():
        raise HTTPException(
            status_code=403, detail="caller_email does not match session user"
        )

    context = {
        "glimmung_run_id": body.glimmung_run_id,
        "glimmung_issue_id": body.glimmung_issue_id,
        "glimmung_pr_id": body.glimmung_pr_id,
        "validation_url": body.validation_url,
        "caller_email": user.email,
    }
    created = await sessions.create(
        owner=user.email,
        mode=body.mode,
        glimmung_context=context,
    )
    session_url = str(request.base_url).rstrip("/") + f"/?session={created.id}"
    return CreateSessionWithContextResponse(session_url=session_url, session=created)


@app.get("/api/sessions")
async def list_sessions(user: User = Depends(current_user)) -> list[SessionInfo]:
    return await sessions.list(owner=user.email)


@app.delete("/api/sessions/{session_id}")
async def delete_session(
    session_id: str, user: User = Depends(current_user)
) -> dict[str, str]:
    try:
        await sessions.delete(owner=user.email, session_id=session_id)
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    return {"id": session_id, "status": "deleted"}


@app.post("/api/sessions/{session_id}/touch")
async def touch_session(
    session_id: str, user: User = Depends(current_user)
) -> dict[str, str]:
    try:
        await sessions.touch(owner=user.email, session_id=session_id)
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    return {"id": session_id, "status": "touched"}


class PatchSessionBody(BaseModel):
    # Empty string / null clears the name; otherwise stored verbatim (trimmed
    # + length-capped server-side).
    name: str | None = None


@app.patch("/api/sessions/{session_id}")
async def patch_session(
    session_id: str,
    body: PatchSessionBody,
    user: User = Depends(current_user),
) -> SessionInfo:
    try:
        return await sessions.set_name(
            owner=user.email, session_id=session_id, name=body.name
        )
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")


@app.post("/api/sessions/{session_id}/save-credentials")
async def save_credentials(
    session_id: str, user: User = Depends(current_user)
) -> dict[str, str]:
    """Capture credentials from a config session and seed KV.

    Mode dispatch:
      - `config`        → ~/.claude/.credentials.json → KV `claude-code-credentials`
      - `codex_config`  → ~/.codex/auth.json          → KV `codex-credentials`

    Both paths only valid for their respective config modes — both as a
    UX guard (the button only shows on those tabs) and as defense-in-depth
    so a misconfigured caller can't dump credentials out of a regular
    session pod's mounted Secret.
    """
    try:
        session = await sessions.get_session(owner=user.email, session_id=session_id)
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    if session.mode not in ("config", "codex_config"):
        raise HTTPException(
            status_code=400,
            detail="save-credentials is only valid for config / codex_config sessions",
        )
    try:
        pod_name = await sessions.get_pod_name(owner=user.email, session_id=session_id)
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    try:
        if session.mode == "config":
            await harvest_and_save(namespace=SESSIONS_NAMESPACE, pod_name=pod_name)
        else:
            await harvest_codex_and_save(
                namespace=SESSIONS_NAMESPACE, pod_name=pod_name
            )
    except CredentialsSeedError as e:
        raise HTTPException(status_code=400, detail=str(e))
    return {"id": session_id, "status": "saved"}


@app.post("/api/sessions/{session_id}/paste-image")
async def paste_image(
    session_id: str,
    request: Request,
    user: User = Depends(current_user),
) -> dict[str, str]:
    content_type = request.headers.get("content-type", "").split(";", 1)[0].lower()
    extension = PASTE_IMAGE_TYPES.get(content_type)
    if extension is None:
        raise HTTPException(
            status_code=415, detail="clipboard item is not a supported image"
        )

    body = await request.body()
    if not body:
        raise HTTPException(status_code=400, detail="empty image")
    if len(body) > MAX_PASTE_IMAGE_BYTES:
        raise HTTPException(status_code=413, detail="image is too large")

    try:
        pod_name = await sessions.get_pod_name(owner=user.email, session_id=session_id)
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")

    timestamp_ms = int(time.time() * 1000)
    path = f"/workspace/.tank-pastes/{session_id}/clipboard-{timestamp_ms}.{extension}"
    try:
        await exec_write_file(
            namespace=SESSIONS_NAMESPACE, pod_name=pod_name, path=path, data=body
        )
    except RuntimeError as e:
        log.warning("failed to write pasted image for session %s: %s", session_id, e)
        raise HTTPException(
            status_code=502, detail="failed to write image into session pod"
        )
    return {"path": path}


@app.websocket("/api/sessions/{session_id}/exec")
async def session_exec(ws: WebSocket, session_id: str) -> None:
    # Accept up front so we can send a close frame the browser can read
    # (`reason` is dropped by Starlette/most browsers when close is called
    # before accept — the tab just sees code 1006, no detail).
    await ws.accept()
    try:
        user = current_user_ws(ws)
    except HTTPException as e:
        await ws.close(code=status.WS_1008_POLICY_VIOLATION, reason=e.detail)
        return

    try:
        pod_ip, terminal_port = await sessions.get_terminal_endpoint(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        await ws.close(code=status.WS_1008_POLICY_VIOLATION, reason="not owner")
        return
    except SessionNotFound:
        await ws.close(code=status.WS_1011_INTERNAL_ERROR, reason="session not found")
        return
    except SessionTerminalUnavailable:
        await ws.close(
            code=status.WS_1011_INTERNAL_ERROR, reason="session needs restart"
        )
        return
    except PodNotReady:
        await ws.close(code=status.WS_1011_INTERNAL_ERROR, reason="pod not ready")
        return

    async with sessions.track_ws(session_id):
        try:
            await bridge(ws, pod_ip=pod_ip, terminal_port=terminal_port)
        except WebSocketDisconnect:
            pass


@app.websocket("/api/sessions/{session_id}/run")
async def session_run(ws: WebSocket, session_id: str) -> None:
    await ws.accept()
    try:
        user = current_user_ws(ws)
    except HTTPException as e:
        await ws.close(code=status.WS_1008_POLICY_VIOLATION, reason=e.detail)
        return

    try:
        session = await sessions.get_session(owner=user.email, session_id=session_id)
    except SessionNotOwned:
        await ws.close(code=status.WS_1008_POLICY_VIOLATION, reason="not owner")
        return
    except SessionNotFound:
        await ws.close(code=status.WS_1011_INTERNAL_ERROR, reason="session not found")
        return

    if session.mode not in HEADLESS_MODES:
        await ws.close(
            code=status.WS_1008_POLICY_VIOLATION,
            reason="session mode does not support headless runs",
        )
        return

    try:
        first = await ws.receive_json()
    except Exception:
        await ws.close(code=status.WS_1003_UNSUPPORTED_DATA, reason="expected JSON")
        return
    prompt = first.get("prompt") if isinstance(first, dict) else None
    if not isinstance(prompt, str) or not prompt.strip():
        await ws.close(code=status.WS_1003_UNSUPPORTED_DATA, reason="missing prompt")
        return
    prompt_bytes = prompt.encode()
    if len(prompt_bytes) > MAX_HEADLESS_PROMPT_BYTES:
        await ws.close(code=status.WS_1009_MESSAGE_TOO_BIG, reason="prompt too large")
        return

    try:
        pod_name = await sessions.get_pod_name(owner=user.email, session_id=session_id)
    except SessionNotOwned:
        await ws.close(code=status.WS_1008_POLICY_VIOLATION, reason="not owner")
        return
    except SessionNotFound:
        await ws.close(code=status.WS_1011_INTERNAL_ERROR, reason="session not found")
        return
    except PodNotReady:
        await ws.close(code=status.WS_1011_INTERNAL_ERROR, reason="pod not ready")
        return

    provider = "codex" if session.mode == CODEX_HEADLESS_MODE else "claude"
    command = [
        "bash",
        "-lc",
        (
            "set -uo pipefail; "
            "prompt_file=$(mktemp); "
            "status=0; "
            f"head -c {len(prompt_bytes)} > \"$prompt_file\" || status=$?; "
            "if [ \"$status\" -eq 0 ]; then "
            f"bash /opt/tank/headless-run.sh {provider} \"$prompt_file\" </dev/null || status=$?; "
            "fi; "
            "rm -f \"$prompt_file\"; "
            "exit $status"
        ),
    ]
    async with sessions.track_ws(session_id):
        try:
            await exec_stream_to_websocket(
                ws,
                namespace=SESSIONS_NAMESPACE,
                pod_name=pod_name,
                command=command,
                stdin=prompt_bytes,
            )
        except WebSocketDisconnect:
            pass


_static_env = os.environ.get("TANK_OPERATOR_STATIC_DIR")
_static = (
    Path(_static_env) if _static_env else Path(__file__).resolve().parent / "static"
)
if _static.exists():
    app.mount("/assets", StaticFiles(directory=_static / "assets"), name="assets")
    app.mount("/fonts", StaticFiles(directory=_static / "fonts"), name="fonts")

    @app.get("/manifest.webmanifest")
    async def web_app_manifest() -> FileResponse:
        return FileResponse(
            _static / "manifest.webmanifest",
            media_type="application/manifest+json",
        )

    @app.get("/")
    async def index() -> FileResponse:
        return FileResponse(_static / "index.html")

    @app.get("/_styleguide")
    async def styleguide() -> FileResponse:
        # SPA-served. The Vite bundle's main.tsx routes to StyleguideView
        # when window.location.pathname matches; we just need to serve
        # the same index.html so the bundle loads. Glimmung's UI pilot
        # contract — see frontend/src/StyleguideView.tsx and the
        # docs/styleguide-contract.md in the glimmung repo.
        return FileResponse(_static / "index.html")
