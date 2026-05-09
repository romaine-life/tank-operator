import asyncio
import json
import os
import logging
import re as _re
import time
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any, AsyncIterator
from urllib.parse import quote

import aiohttp
from fastapi import (
    Cookie,
    Depends,
    FastAPI,
    Header,
    HTTPException,
    Request,
    WebSocket,
    WebSocketDisconnect,
    status,
)
from fastapi import Response
from fastapi.responses import (
    FileResponse,
    JSONResponse,
    RedirectResponse,
    StreamingResponse,
)
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
    mint_session_token_for_k8s_subject,
    verify_install_state,
)
from .credentials_seed import (
    CredentialsSeedError,
    harvest_and_save,
    harvest_codex_and_save,
)
from .exec_proxy import (
    exec_capture,
    exec_launch_detached,
    exec_write_file,
    exec_stream_to_websocket,
)
from .internal_api import build_router as build_internal_router
from .profiles import ProfileStore, SessionRegistryStore
from .session_events import SessionEventBus
from .sessions import (
    CODEX_HEADLESS_MODE,
    DEFAULT_SESSION_MODE,
    HEADLESS_MODES,
    SANDBOX_AGENT_PORT,
    SESSION_MODES,
    SESSIONS_NAMESPACE,
    SUBSCRIPTION_HEADLESS_MODE,
    PodNotReady,
    SessionInfo,
    SessionManager,
    SessionNotFound,
    SessionNotOwned,
)

session_registry = SessionRegistryStore()
session_events = SessionEventBus()
sessions = SessionManager(registry=session_registry, events=session_events)
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
app.include_router(build_internal_router(sessions, profiles))


class LoginBody(BaseModel):
    credential: str


class LoginResponse(BaseModel):
    token: str
    user: dict[str, str]


MAX_HEADLESS_PROMPT_BYTES = int(
    os.environ.get("MAX_HEADLESS_PROMPT_BYTES", str(256 * 1024))
)

# headless-run.sh's positional args 4 + 5 (model, permission_mode) reach the
# claude/codex CLI verbatim. Constraining what gets there to a tight charset
# means we can splice them into bash command literals without a quoting layer.
import secrets as _secrets
import shlex as _shlex
_HEADLESS_ARG_PATTERN = _re.compile(r"^[A-Za-z0-9._-]{1,64}$")
_RUN_ID_PATTERN = _re.compile(r"^[A-Za-z0-9._-]{1,80}$")
_HEADLESS_PROMPT_DIR = "/tmp"
_HEADLESS_RUN_EXIT_MARKER = "__TANK_RUN_EXIT__:"
_RUN_PREFLIGHT_KEEPALIVE_SECONDS = 10
_SESSION_EVENTS_KEEPALIVE_SECONDS = 10


def _validate_headless_arg(value: str | None) -> str:
    if value is None:
        return ""
    if isinstance(value, str) and _HEADLESS_ARG_PATTERN.match(value):
        return value
    return ""


def _new_prompt_path() -> str:
    return f"{_HEADLESS_PROMPT_DIR}/tank-prompt-{_secrets.token_hex(8)}"


def _validate_run_id(value: str | None) -> str:
    if isinstance(value, str) and _RUN_ID_PATTERN.match(value):
        return value
    return _secrets.token_hex(12)


def _run_stream_path(run_id: str) -> str:
    return f"/tmp/tank-run-{run_id}.stream"


def _run_pid_path(run_id: str) -> str:
    return f"/tmp/tank-run-{run_id}.pid"


async def _wait_for_run_pod_name(owner: str, session_id: str, ws: WebSocket) -> str:
    """Wait for a run pod without leaving the browser WebSocket idle."""

    pod_task = asyncio.create_task(
        sessions.get_pod_name(owner=owner, session_id=session_id)
    )
    try:
        while True:
            done, _pending = await asyncio.wait(
                {pod_task},
                timeout=_RUN_PREFLIGHT_KEEPALIVE_SECONDS,
                return_when=asyncio.FIRST_COMPLETED,
            )
            if done:
                return await pod_task
            await ws.send_json({"keepalive": True, "phase": "waiting_for_pod"})
    except Exception:
        if not pod_task.done():
            pod_task.cancel()
        raise


def _build_headless_script(
    *,
    provider: str,
    prompt_path: str,
    follow_up: bool,
    model: str,
    permission_mode: str,
) -> str:
    """Bash one-liner that runs headless-run.sh against an on-pod prompt file.

    Used by both the live-stream WS endpoint (wrapped in `bash -lc` and
    streamed to the browser) and the fire-and-forget HTTP endpoints
    (wrapped in nohup+disown by exec_launch_detached). model and
    permission_mode are pre-validated against [A-Za-z0-9._-]{1,64}, so
    splicing them into the literal is safe.
    """
    quoted_path = _shlex.quote(prompt_path)
    return (
        f"bash /opt/tank/headless-run.sh {provider} {quoted_path} "
        f"{'true' if follow_up else 'false'} '{model}' '{permission_mode}'"
        f"; rc=$?; rm -f {quoted_path}; (exit $rc)"
    )


def _build_live_run_script(script: str, pid_path: str) -> str:
    marker = _shlex.quote(_HEADLESS_RUN_EXIT_MARKER)
    quoted_pid_path = _shlex.quote(pid_path)
    # Use bash variables so the trap body doesn't need nested single-quotes.
    # The trap writes the exit marker even on SIGTERM so the tail script's
    # grep loop always terminates rather than hanging after a cancel.
    return (
        f"echo $$ > {quoted_pid_path}; "
        f"_tank_pid={quoted_pid_path}; _tank_marker={marker}; "
        "trap 'rc=$?; rm -f \"$_tank_pid\"; "
        "printf \"\\n%s%s\\n\" \"$_tank_marker\" \"$rc\"; exit $rc' TERM INT; "
        f"{script}; rc=$?; "
        f"rm -f {quoted_pid_path}; "
        f"printf '\\n%s%s\\n' {marker} \"$rc\"; "
        f"exit $rc"
    )


def _build_cancel_run_command(pid_path: str) -> list[str]:
    quoted_pid_path = _shlex.quote(pid_path)
    return [
        "bash",
        "-lc",
        (
            f"pid=$(cat {quoted_pid_path} 2>/dev/null || true); "
            "if [ -n \"$pid\" ]; then "
            "pkill -TERM -P \"$pid\" 2>/dev/null || true; "
            "kill -TERM \"$pid\" 2>/dev/null || true; "
            "fi"
        ),
    ]


def _build_tail_run_script(stream_path: str, offset: int = 0) -> str:
    quoted_path = _shlex.quote(stream_path)
    marker = _shlex.quote(_HEADLESS_RUN_EXIT_MARKER)
    start_byte = max(1, offset + 1)
    return (
        "set -euo pipefail; "
        # Wait for the stream file with a 30s deadline so a pod crash or a
        # launch failure doesn't leave the tail exec hanging indefinitely.
        "deadline=$((SECONDS+30)); "
        f"while [ ! -f {quoted_path} ]; do "
        "sleep 0.2; "
        "[ $SECONDS -lt $deadline ] || "
        "{ echo 'timed out waiting for run stream' >&2; exit 1; }; "
        "done; "
        f"tail -c +{start_byte} -F {quoted_path} & tail_pid=$!; "
        # Guard the file-existence condition: if a concurrent tail script already
        # found __TANK_RUN_EXIT__ and deleted the stream file, grep returns exit 2
        # (file not found), ! inverts to 0, and the while would spin forever.
        # Adding [ -f ... ] makes the loop exit cleanly when the file disappears.
        f"while [ -f {quoted_path} ] && ! grep -q {marker} {quoted_path}; do sleep 0.5; done; "
        "sleep 0.2; "
        "kill \"$tail_pid\" 2>/dev/null || true; "
        "wait \"$tail_pid\" 2>/dev/null || true; "
        f"rc=$(sed -n 's/^{_HEADLESS_RUN_EXIT_MARKER}//p' {quoted_path} 2>/dev/null | tail -1) || rc=; "
        f"rm -f {quoted_path}; "
        "exit \"${rc:-0}\""
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


@app.post("/api/internal/auth/k8s")
async def auth_via_k8s_sa(authorization: str | None = Header(default=None)) -> JSONResponse:
    """Cluster-native auth path. Caller presents a kubernetes
    ServiceAccount projected token in `Authorization: Bearer <token>`;
    the orchestrator validates it via TokenReview and, if the SA
    subject is in K8S_AUTH_ALLOWED_SUBJECTS, returns a session JWT
    bound to the mapped email.

    Lets in-cluster automation (smoke tests, headless-browser probes,
    health-check sidecars) authenticate without going through MSAL.
    """
    if not authorization or not authorization.lower().startswith("bearer "):
        raise HTTPException(status_code=401, detail="missing bearer SA token")
    sa_token = authorization[7:]
    session_token, user = await mint_session_token_for_k8s_subject(sa_token)
    return JSONResponse(
        {
            "token": session_token,
            "user": {"email": user.email, "name": user.name, "sub": user.sub},
        }
    )


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


@app.get("/api/sessions/events")
async def session_events_stream(
    request: Request, user: User = Depends(current_user)
) -> StreamingResponse:
    async def events() -> AsyncIterator[str]:
        async with session_events.subscribe(user.email) as queue:
            yield "event: ready\ndata: {}\n\n"
            while not await request.is_disconnected():
                try:
                    await asyncio.wait_for(
                        queue.get(), timeout=_SESSION_EVENTS_KEEPALIVE_SECONDS
                    )
                except asyncio.TimeoutError:
                    yield ": keep-alive\n\n"
                    continue
                yield "event: sessions-changed\ndata: {}\n\n"

    return StreamingResponse(events(), media_type="text/event-stream")


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


class TestStateBody(BaseModel):
    active: bool = True
    slot_index: int | None = None
    url: str | None = None
    lease_id: str | None = None


# ---------------------------------------------------------------------------
# Files API — read-only browsing of /workspace inside the session pod.
# Powers the Files tab in the chat pane. Edits are out of scope for now.

WORKSPACE_ROOT = "/workspace"
MAX_FILE_BYTES = 256 * 1024  # 256 KiB cap; UI shows a "truncated" hint above this.


def _safe_workspace_path(path: str) -> str:
    """Resolve a user-supplied path under /workspace, rejecting escapes.

    Returns the absolute path. The orchestrator hands this to `find` /
    `head` over `kubectl exec`, so we filter aggressively to avoid handing
    a shell anything weird.
    """
    if path is None:
        path = ""
    if "\0" in path:
        raise HTTPException(status_code=400, detail="invalid path (null byte)")
    candidate = os.path.normpath(os.path.join(WORKSPACE_ROOT, path.lstrip("/")))
    if candidate != WORKSPACE_ROOT and not candidate.startswith(WORKSPACE_ROOT + "/"):
        raise HTTPException(status_code=400, detail="path outside /workspace")
    return candidate


class FileEntry(BaseModel):
    name: str
    type: str  # "file" | "dir" | "symlink" | "other"
    size: int


class FileListing(BaseModel):
    path: str
    entries: list[FileEntry]


class FileContent(BaseModel):
    path: str
    size: int
    truncated: bool
    text: str
    binary: bool


class SkillEntry(BaseModel):
    name: str
    path: str
    source: str
    description: str
    body_preview: str


class SkillListing(BaseModel):
    entries: list[SkillEntry]


class McpServerEntry(BaseModel):
    name: str
    transport: str
    target: str
    source: str
    enabled: bool


class McpServerListing(BaseModel):
    entries: list[McpServerEntry]


class WriteFileBody(BaseModel):
    text: str


@app.get("/api/sessions/{session_id}/skills", response_model=SkillListing)
async def list_session_skills(
    session_id: str,
    user: User = Depends(current_user),
) -> SkillListing:
    """List installed SKILL.md files inside the session pod.

    GUI/headless sessions hide the terminal's startup skill inventory, so the
    web UI needs its own view over the same on-pod directories Codex and
    Claude read from.
    """
    try:
        session = await sessions.get_session(owner=user.email, session_id=session_id)
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")

    if session.mode.startswith("codex") or session.mode.startswith("pi"):
        roots = ["/home/node/.codex/skills"]
    elif session.mode.startswith("subscription") or session.mode in ("api_key", "config"):
        roots = ["/home/node/.claude/skills"]
    else:
        roots = ["/home/node/.codex/skills", "/home/node/.claude/skills"]

    scan_script = (
        "import json, os, sys\n"
        "roots = sys.argv[1:]\n"
        "def parse_skill(text):\n"
        "    meta = {}\n"
        "    body = text\n"
        "    if text.startswith('---\\n'):\n"
        "        end = text.find('\\n---', 4)\n"
        "        if end != -1:\n"
        "            raw = text[4:end]\n"
        "            body = text[text.find('\\n', end + 1) + 1:]\n"
        "            for line in raw.splitlines():\n"
        "                if ':' not in line or line.startswith((' ', '\\t', '-')):\n"
        "                    continue\n"
        "                k, v = line.split(':', 1)\n"
        "                meta[k.strip()] = v.strip().strip('\\\"\\'')\n"
        "    preview = ' '.join(body.strip().split())[:240]\n"
        "    return meta, preview\n"
        "out = []\n"
        "for root in roots:\n"
        "    if not os.path.isdir(root):\n"
        "        continue\n"
        "    for dirpath, dirs, files in os.walk(root):\n"
        "        dirs[:] = sorted(dirs)\n"
        "        if 'SKILL.md' not in files:\n"
        "            continue\n"
        "        path = os.path.join(dirpath, 'SKILL.md')\n"
        "        try:\n"
        "            text = open(path, encoding='utf-8').read(65536)\n"
        "        except OSError:\n"
        "            continue\n"
        "        meta, preview = parse_skill(text)\n"
        "        rel = os.path.relpath(dirpath, root)\n"
        "        fallback = os.path.basename(dirpath) if rel == '.' else rel\n"
        "        name = str(meta.get('name') or fallback).replace(os.sep, '/')\n"
        "        source = 'codex' if '/.codex/' in path else 'claude'\n"
        "        out.append({\n"
        "            'name': name,\n"
        "            'path': path,\n"
        "            'source': source,\n"
        "            'description': str(meta.get('description') or ''),\n"
        "            'body_preview': preview,\n"
        "        })\n"
        "out.sort(key=lambda x: (x['source'], x['name'].lower()))\n"
        "print(json.dumps(out))\n"
    )
    cmd = ["python3", "-c", scan_script, *roots]
    try:
        out = await exec_capture(SESSIONS_NAMESPACE, pod_name, cmd)
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=f"skill scan failed: {exc}")

    import json as _json
    try:
        raw_entries = _json.loads(out.decode("utf-8", errors="replace") or "[]")
    except _json.JSONDecodeError:
        raw_entries = []
    entries: list[SkillEntry] = []
    for item in raw_entries:
        if not isinstance(item, dict):
            continue
        entries.append(
            SkillEntry(
                name=str(item.get("name") or ""),
                path=str(item.get("path") or ""),
                source=str(item.get("source") or ""),
                description=str(item.get("description") or ""),
                body_preview=str(item.get("body_preview") or ""),
            )
        )
    return SkillListing(entries=entries)


@app.get("/api/sessions/{session_id}/mcp-servers", response_model=McpServerListing)
async def list_session_mcp_servers(
    session_id: str,
    user: User = Depends(current_user),
) -> McpServerListing:
    """List MCP servers configured inside the session pod.

    GUI/headless sessions hide the agent startup inventory, so the web UI needs
    its own view over the mounted MCP config just like it does for skills.
    """
    try:
        await sessions.get_session(owner=user.email, session_id=session_id)
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")

    scan_script = (
        "import json\n"
        "path = '/workspace/.mcp.json'\n"
        "out = []\n"
        "try:\n"
        "    config = json.load(open(path, encoding='utf-8'))\n"
        "except Exception:\n"
        "    config = {}\n"
        "servers = config.get('mcpServers') or {}\n"
        "if isinstance(servers, dict):\n"
        "    for name, value in servers.items():\n"
        "        if not isinstance(value, dict):\n"
        "            continue\n"
        "        transport = str(value.get('type') or "
        "('stdio' if value.get('command') else 'unknown'))\n"
        "        target = str(value.get('url') or value.get('command') or '')\n"
        "        out.append({\n"
        "            'name': str(name),\n"
        "            'transport': transport,\n"
        "            'target': target,\n"
        "            'source': path,\n"
        "            'enabled': True,\n"
        "        })\n"
        "out.sort(key=lambda x: x['name'].lower())\n"
        "print(json.dumps(out))\n"
    )
    try:
        out = await exec_capture(
            SESSIONS_NAMESPACE, pod_name, ["python3", "-c", scan_script]
        )
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=f"mcp scan failed: {exc}")

    import json as _json
    try:
        raw_entries = _json.loads(out.decode("utf-8", errors="replace") or "[]")
    except _json.JSONDecodeError:
        raw_entries = []
    entries: list[McpServerEntry] = []
    for item in raw_entries:
        if not isinstance(item, dict):
            continue
        entries.append(
            McpServerEntry(
                name=str(item.get("name") or ""),
                transport=str(item.get("transport") or ""),
                target=str(item.get("target") or ""),
                source=str(item.get("source") or ""),
                enabled=bool(item.get("enabled")),
            )
        )
    return McpServerListing(entries=entries)


@app.get("/api/sessions/{session_id}/files", response_model=FileListing)
async def list_session_files(
    session_id: str,
    path: str = "",
    user: User = Depends(current_user),
) -> FileListing:
    """Directory listing under /workspace inside the session pod.

    `path` is relative to /workspace; empty / "/" lists the root.
    """
    abs_path = _safe_workspace_path(path)
    try:
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    # BusyBox `find` (alpine) lacks GNU `-printf`, so we drop into python3
    # inside the pod for a JSON listing. The claude-container image bakes
    # python3 in via apk for orchestration helpers.
    listing_script = (
        "import os, json, sys\n"
        "p = sys.argv[1]\n"
        "out = []\n"
        "for name in sorted(os.listdir(p)):\n"
        "    full = os.path.join(p, name)\n"
        "    try:\n"
        "        st = os.lstat(full)\n"
        "    except OSError:\n"
        "        continue\n"
        "    if os.path.islink(full):\n"
        "        t = 'symlink'\n"
        "    elif os.path.isdir(full):\n"
        "        t = 'dir'\n"
        "    elif os.path.isfile(full):\n"
        "        t = 'file'\n"
        "    else:\n"
        "        t = 'other'\n"
        "    out.append({'name': name, 'type': t, 'size': st.st_size if t == 'file' else 0})\n"
        "print(json.dumps(out))\n"
    )
    cmd = ["python3", "-c", listing_script, abs_path]
    try:
        out = await exec_capture(SESSIONS_NAMESPACE, pod_name, cmd)
    except RuntimeError as exc:
        raise HTTPException(status_code=404, detail=f"path not found: {exc}")
    import json as _json
    try:
        listing = _json.loads(out.decode("utf-8", errors="replace") or "[]")
    except _json.JSONDecodeError:
        listing = []
    entries: list[FileEntry] = []
    for item in listing:
        if not isinstance(item, dict):
            continue
        entries.append(
            FileEntry(
                name=str(item.get("name", "")),
                type=str(item.get("type", "other")),
                size=int(item.get("size", 0) or 0),
            )
        )
    # Directories first, then files, alphabetical within each group.
    entries.sort(key=lambda e: (e.type != "dir", e.name.lower()))
    rel = abs_path[len(WORKSPACE_ROOT):].lstrip("/") if abs_path != WORKSPACE_ROOT else ""
    return FileListing(path=rel, entries=entries)


@app.get("/api/sessions/{session_id}/files/content", response_model=FileContent)
async def get_session_file_content(
    session_id: str,
    path: str,
    user: User = Depends(current_user),
) -> FileContent:
    """Read a file under /workspace, capped at MAX_FILE_BYTES."""
    abs_path = _safe_workspace_path(path)
    if abs_path == WORKSPACE_ROOT:
        raise HTTPException(status_code=400, detail="cannot read directory")
    try:
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    # Read 1 byte over the cap so we can tell whether the file was truncated.
    cmd = ["head", "-c", str(MAX_FILE_BYTES + 1), "--", abs_path]
    try:
        data = await exec_capture(SESSIONS_NAMESPACE, pod_name, cmd)
    except RuntimeError as exc:
        raise HTTPException(status_code=404, detail=f"file not found: {exc}")
    truncated = len(data) > MAX_FILE_BYTES
    if truncated:
        data = data[:MAX_FILE_BYTES]
    rel = abs_path[len(WORKSPACE_ROOT):].lstrip("/")
    try:
        text = data.decode("utf-8")
        return FileContent(
            path=rel, size=len(data), truncated=truncated, text=text, binary=False
        )
    except UnicodeDecodeError:
        return FileContent(
            path=rel, size=len(data), truncated=truncated, text="", binary=True
        )


MAX_UPLOAD_BYTES = 8 * 1024 * 1024  # 8 MiB cap on a single attachment.
ATTACHMENTS_REL_DIR = ".attachments"


class UploadResponse(BaseModel):
    path: str  # Relative to /workspace
    abs_path: str  # Full path in the session pod, what claude reads
    name: str
    size: int


@app.post("/api/sessions/{session_id}/files/upload", response_model=UploadResponse)
async def upload_session_file(
    session_id: str,
    request: Request,
    name: str = "",
    user: User = Depends(current_user),
) -> UploadResponse:
    """Save an uploaded file to /workspace/.attachments inside the session pod.

    Used by the chat composer to attach images / files; the prompt is
    then sent with explicit path references so claude can `Read` them.

    Body is raw bytes (Content-Type: image/png, etc); the file name comes
    in via the `name` query param. Avoids the multipart parser so we
    don't need python-multipart in the orchestrator image.
    """
    import re as _re_up
    raw_name = name or "file"
    safe_name = _re_up.sub(r"[^A-Za-z0-9._-]", "_", raw_name)[:100] or "file"
    stamp = int(time.time() * 1000)
    rel_path = f"{ATTACHMENTS_REL_DIR}/{stamp}-{safe_name}"
    abs_path = _safe_workspace_path(rel_path)
    contents = await request.body()
    if len(contents) > MAX_UPLOAD_BYTES:
        raise HTTPException(
            status_code=413,
            detail=f"file too large (max {MAX_UPLOAD_BYTES} bytes)",
        )
    try:
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    await exec_write_file(SESSIONS_NAMESPACE, pod_name, abs_path, contents)
    return UploadResponse(
        path=rel_path, abs_path=abs_path, name=safe_name, size=len(contents)
    )


class FileWalkResponse(BaseModel):
    paths: list[str]
    truncated: bool


MAX_WALK_RESULTS = 5000


@app.get("/api/sessions/{session_id}/files/walk", response_model=FileWalkResponse)
async def walk_session_files(
    session_id: str,
    user: User = Depends(current_user),
) -> FileWalkResponse:
    """Recursive walk of /workspace; returns relative file paths capped at
    MAX_WALK_RESULTS. Powers `@filename` mention autocomplete in the
    composer.
    """
    try:
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    walk_script = (
        "import os, json, sys\n"
        f"limit = {MAX_WALK_RESULTS}\n"
        "root = sys.argv[1]\n"
        "out = []\n"
        "trunc = False\n"
        "for dirpath, dirs, files in os.walk(root):\n"
        # Skip dot-dirs (node_modules etc), but keep .attachments visible.
        "    dirs[:] = [d for d in dirs if d == '.attachments' or not d.startswith('.')]\n"
        "    dirs[:] = [d for d in dirs if d not in ('node_modules', '.git', 'dist', 'build', '__pycache__', '.venv')]\n"
        "    for name in files:\n"
        "        if name.startswith('.') and not dirpath.endswith('/.attachments'):\n"
        "            continue\n"
        "        rel = os.path.relpath(os.path.join(dirpath, name), root)\n"
        "        out.append(rel)\n"
        "        if len(out) >= limit:\n"
        "            trunc = True\n"
        "            break\n"
        "    if trunc:\n"
        "        break\n"
        "print(json.dumps({'paths': out, 'truncated': trunc}))\n"
    )
    cmd = ["python3", "-c", walk_script, WORKSPACE_ROOT]
    try:
        out = await exec_capture(SESSIONS_NAMESPACE, pod_name, cmd)
    except RuntimeError as exc:
        raise HTTPException(status_code=500, detail=f"walk failed: {exc}")
    import json as _json
    try:
        body = _json.loads(out.decode("utf-8", errors="replace") or "{}")
    except _json.JSONDecodeError:
        body = {"paths": [], "truncated": False}
    return FileWalkResponse(
        paths=list(body.get("paths") or []),
        truncated=bool(body.get("truncated")),
    )


class ActiveRunResponse(BaseModel):
    run_id: str
    stream_offset: int


@app.get("/api/sessions/{session_id}/run/active")
async def get_active_run(
    session_id: str,
    user: User = Depends(current_user),
) -> ActiveRunResponse | None:
    """Check whether the session pod has an in-progress run.

    Returns run_id and current stream byte offset when a pid file exists
    (i.e. the agent process hasn't finished), null otherwise. The caller
    can open the run WebSocket with resume=true + the returned run_id/offset
    to attach to the live stream.
    """
    try:
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")

    # Single exec: find the newest pid file, verify the process is still alive
    # via kill -0 (guards against stale files from SIGKILL/OOM deaths where the
    # trap didn't run), then return run_id and current stream byte offset.
    # Outputs "<run_id> <stream_bytes>" on success, empty string if idle.
    check_script = (
        "pid_file=$(ls /tmp/tank-run-*.pid 2>/dev/null | head -1); "
        "[ -z \"$pid_file\" ] && exit 0; "
        "pid=$(cat \"$pid_file\" 2>/dev/null || true); "
        "[ -z \"$pid\" ] && exit 0; "
        "kill -0 \"$pid\" 2>/dev/null || { rm -f \"$pid_file\"; exit 0; }; "
        "run_id=${pid_file#/tmp/tank-run-}; run_id=${run_id%.pid}; "
        "stream_path=\"/tmp/tank-run-${run_id}.stream\"; "
        "bytes=$(wc -c < \"$stream_path\" 2>/dev/null || echo 0); "
        "echo \"$run_id $bytes\""
    )
    try:
        out = await exec_capture(SESSIONS_NAMESPACE, pod_name, ["bash", "-c", check_script])
    except RuntimeError:
        return None

    line = out.decode().strip()
    if not line:
        return None

    parts = line.split()
    if len(parts) != 2:
        return None
    run_id, stream_bytes = parts[0], parts[1]
    if not run_id or "/" in run_id:
        return None
    try:
        stream_offset = int(stream_bytes)
    except ValueError:
        stream_offset = 0

    return ActiveRunResponse(run_id=run_id, stream_offset=stream_offset)


@app.get("/api/sessions/{session_id}/run/history")
async def get_run_history(
    session_id: str,
    user: User = Depends(current_user),
) -> Response:
    """Stream the most recent claude-code session JSONL out of the session
    pod. Used by HeadlessRun on mount as a fallback when localStorage is
    empty (different browser, cleared cache, etc).

    Returns ndjson body. Empty body when no history exists yet — frontend
    treats that as "no prior conversation".
    """
    try:
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    try:
        session = await sessions.get_session(owner=user.email, session_id=session_id)
    except (SessionNotOwned, SessionNotFound):
        session = None
    if session and session.mode == CODEX_HEADLESS_MODE:
        cmd = ["bash", "-lc", "cat /tmp/tank-run-history.ndjson 2>/dev/null || true"]
    else:
        # claude-code persists each session at
        # ~/.claude/projects/<encoded-cwd>/<uuid>.jsonl. We don't track the
        # uuid → tank session mapping yet; for now return the most recently
        # modified jsonl in any project subdir, which corresponds to the last
        # `claude -p` invocation on the pod.
        cmd = [
            "bash",
            "-lc",
            "ls -t /home/node/.claude/projects/*/*.jsonl 2>/dev/null | head -1 | xargs -I{} cat {} 2>/dev/null",
        ]
    try:
        out = await exec_capture(SESSIONS_NAMESPACE, pod_name, cmd)
    except RuntimeError:
        out = b""
    return Response(content=out, media_type="application/x-ndjson")


@app.get("/api/sessions/{session_id}/files/raw")
async def get_session_file_raw(
    session_id: str,
    path: str,
    user: User = Depends(current_user),
) -> Response:
    """Stream a raw file from /workspace as bytes — used by the file
    viewer to render images. Capped at MAX_UPLOAD_BYTES (8 MiB).
    """
    abs_path = _safe_workspace_path(path)
    if abs_path == WORKSPACE_ROOT:
        raise HTTPException(status_code=400, detail="cannot read directory")
    try:
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    cmd = ["head", "-c", str(MAX_UPLOAD_BYTES), "--", abs_path]
    try:
        data = await exec_capture(SESSIONS_NAMESPACE, pod_name, cmd)
    except RuntimeError as exc:
        raise HTTPException(status_code=404, detail=f"file not found: {exc}")
    # Pick a content-type from the extension. Defensively narrow to the
    # set the file viewer actually requests; everything else gets octet.
    ext = abs_path.rsplit(".", 1)[-1].lower() if "." in abs_path else ""
    ctype = {
        "png": "image/png",
        "jpg": "image/jpeg",
        "jpeg": "image/jpeg",
        "webp": "image/webp",
        "gif": "image/gif",
        "svg": "image/svg+xml",
        "bmp": "image/bmp",
    }.get(ext, "application/octet-stream")
    return Response(content=data, media_type=ctype)


@app.put("/api/sessions/{session_id}/files/content", response_model=FileContent)
async def put_session_file_content(
    session_id: str,
    path: str,
    body: WriteFileBody,
    user: User = Depends(current_user),
) -> FileContent:
    """Write `body.text` to a file under /workspace. Refuses to write
    over a directory or outside /workspace. Capped at MAX_FILE_BYTES.
    """
    abs_path = _safe_workspace_path(path)
    if abs_path == WORKSPACE_ROOT:
        raise HTTPException(status_code=400, detail="cannot write to root")
    data = body.text.encode("utf-8")
    if len(data) > MAX_FILE_BYTES:
        raise HTTPException(
            status_code=413,
            detail=f"file too large (max {MAX_FILE_BYTES} bytes)",
        )
    try:
        pod_name = await sessions.get_pod_name(
            owner=user.email, session_id=session_id
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    await exec_write_file(SESSIONS_NAMESPACE, pod_name, abs_path, data)
    rel = abs_path[len(WORKSPACE_ROOT):].lstrip("/")
    return FileContent(
        path=rel, size=len(data), truncated=False, text=body.text, binary=False
    )


# ---------------------------------------------------------------------------


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


@app.post("/api/sessions/{session_id}/test-state")
async def update_test_state(
    session_id: str,
    body: TestStateBody,
    user: User = Depends(current_user),
) -> SessionInfo:
    try:
        return await sessions.set_test_state(
            owner=user.email,
            session_id=session_id,
            active=body.active,
            slot_index=body.slot_index,
            url=body.url,
            lease_id=body.lease_id,
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


class SpawnRunSessionBody(BaseModel):
    """Spawn a fresh headless session and dispatch one prompt to it."""

    prompt: str
    mode: str = SUBSCRIPTION_HEADLESS_MODE
    name: str | None = None
    model: str | None = None
    permission_mode: str | None = None


class SpawnRunSessionResponse(BaseModel):
    session: SessionInfo
    session_url: str


class SessionMessageBody(BaseModel):
    """Append a follow-up prompt to an existing headless session."""

    prompt: str
    model: str | None = None
    permission_mode: str | None = None


class SessionMessageResponse(BaseModel):
    session_id: str
    status: str  # "dispatched"


def _validate_prompt(prompt: str) -> bytes:
    if not prompt or not prompt.strip():
        raise HTTPException(status_code=400, detail="missing prompt")
    encoded = prompt.encode()
    if len(encoded) > MAX_HEADLESS_PROMPT_BYTES:
        raise HTTPException(status_code=413, detail="prompt too large")
    return encoded


@app.post("/api/sessions/run", response_model=SpawnRunSessionResponse, status_code=202)
async def spawn_run_session(
    body: SpawnRunSessionBody,
    request: Request,
    user: User = Depends(current_user),
) -> SpawnRunSessionResponse:
    """Create a new headless session and dispatch a first prompt to it.

    Returns 202 once the run has been launched on the pod
    (fire-and-forget); poll /api/sessions/{id}/run/history for output.
    """
    if body.mode not in HEADLESS_MODES:
        raise HTTPException(
            status_code=400,
            detail=f"mode {body.mode!r} does not support headless runs",
        )
    _validate_prompt(body.prompt)
    created = await sessions.create(owner=user.email, mode=body.mode)
    if body.name:
        try:
            created = await sessions.set_name(
                owner=user.email, session_id=created.id, name=body.name
            )
        except (SessionNotFound, SessionNotOwned):
            # Race: pod was deleted between create and rename. Continue with
            # the unnamed session — caller can rename later if it cares.
            pass
    try:
        await sessions.dispatch_headless(
            owner=user.email,
            session_id=created.id,
            prompt=body.prompt,
            follow_up=False,
            model=body.model or "",
            permission_mode=body.permission_mode or "",
        )
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    session_url = str(request.base_url).rstrip("/") + f"/?session={created.id}"
    return SpawnRunSessionResponse(session=created, session_url=session_url)


@app.post(
    "/api/sessions/{session_id}/messages",
    response_model=SessionMessageResponse,
    status_code=202,
)
async def send_session_message(
    session_id: str,
    body: SessionMessageBody,
    user: User = Depends(current_user),
) -> SessionMessageResponse:
    """Append a follow-up prompt to an existing headless session.

    Equivalent to opening the existing /run WebSocket with follow_up=true,
    but as a fire-and-forget HTTP call so an agent in another session can
    deliver a message without holding a stream open. Receiving session must
    be in a headless mode.
    """
    _validate_prompt(body.prompt)
    try:
        await sessions.dispatch_headless(
            owner=user.email,
            session_id=session_id,
            prompt=body.prompt,
            follow_up=True,
            model=body.model or "",
            permission_mode=body.permission_mode or "",
        )
    except SessionNotOwned:
        raise HTTPException(status_code=403, detail="session not owned by caller")
    except SessionNotFound:
        raise HTTPException(status_code=404, detail="session not found")
    except PodNotReady:
        raise HTTPException(status_code=503, detail="pod not ready")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    return SessionMessageResponse(session_id=session_id, status="dispatched")


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
    resume = bool(first.get("resume")) if isinstance(first, dict) else False
    run_id = _validate_run_id(first.get("run_id") if isinstance(first, dict) else None)
    prompt = first.get("prompt") if isinstance(first, dict) else None
    prompt_bytes = b""
    if not resume:
        if not isinstance(prompt, str) or not prompt.strip():
            await ws.close(code=status.WS_1003_UNSUPPORTED_DATA, reason="missing prompt")
            return
        prompt_bytes = prompt.encode()
        if len(prompt_bytes) > MAX_HEADLESS_PROMPT_BYTES:
            await ws.close(code=status.WS_1009_MESSAGE_TOO_BIG, reason="prompt too large")
            return
    follow_up = bool(first.get("follow_up")) if isinstance(first, dict) else False
    raw_model = first.get("model") if isinstance(first, dict) else None
    raw_pm = first.get("permission_mode") if isinstance(first, dict) else None
    raw_offset = first.get("offset") if isinstance(first, dict) else 0
    tail_offset = raw_offset if isinstance(raw_offset, int) and raw_offset > 0 else 0
    model = _validate_headless_arg(raw_model if isinstance(raw_model, str) else None)
    permission_mode = _validate_headless_arg(
        raw_pm if isinstance(raw_pm, str) else None
    )

    try:
        pod_name = await _wait_for_run_pod_name(
            owner=user.email, session_id=session_id, ws=ws
        )
    except SessionNotOwned:
        await ws.close(code=status.WS_1008_POLICY_VIOLATION, reason="not owner")
        return
    except SessionNotFound:
        await ws.close(code=status.WS_1011_INTERNAL_ERROR, reason="session not found")
        return
    except PodNotReady:
        await ws.close(code=status.WS_1011_INTERNAL_ERROR, reason="pod not ready")
        return
    except WebSocketDisconnect:
        return

    provider = "codex" if session.mode == CODEX_HEADLESS_MODE else "claude"
    stream_path = _run_stream_path(run_id)
    pid_path = _run_pid_path(run_id)
    if not resume:
        # Best-effort removal of stream/pid files older than 60 minutes.
        # Runs fire-and-forget so it doesn't block the run launch.
        asyncio.create_task(
            exec_capture(
                SESSIONS_NAMESPACE,
                pod_name,
                ["bash", "-c",
                 "find /tmp -maxdepth 1 -name 'tank-run-*.stream' -mmin +60 -delete 2>/dev/null; "
                 "find /tmp -maxdepth 1 -name 'tank-run-*.pid' -mmin +60 -delete 2>/dev/null; true"],
            )
        )
        prompt_path = _new_prompt_path()
        try:
            await exec_write_file(
                SESSIONS_NAMESPACE, pod_name, prompt_path, prompt_bytes
            )
        except RuntimeError as exc:
            await ws.send_json({"status": "error", "detail": f"failed to stage prompt: {exc}"})
            await ws.close(code=status.WS_1011_INTERNAL_ERROR, reason="prompt write failed")
            return
        script = _build_headless_script(
            provider=provider,
            prompt_path=prompt_path,
            follow_up=follow_up,
            model=model,
            permission_mode=permission_mode,
        )
        try:
            await exec_launch_detached(
                namespace=SESSIONS_NAMESPACE,
                pod_name=pod_name,
                command=_build_live_run_script(script, pid_path),
                log_path=stream_path,
            )
        except RuntimeError as exc:
            await ws.send_json({"status": "error", "detail": f"failed to launch run: {exc}"})
            await ws.close(code=status.WS_1011_INTERNAL_ERROR, reason="run launch failed")
            return
    await ws.send_json({"status": "attached", "run_id": run_id})
    command = ["bash", "-lc", _build_tail_run_script(stream_path, tail_offset)]
    async with sessions.track_ws(session_id):
        try:
            await exec_stream_to_websocket(
                ws,
                namespace=SESSIONS_NAMESPACE,
                pod_name=pod_name,
                command=command,
                stdin=b"",
                cancel_command=_build_cancel_run_command(pid_path),
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
