import os
from typing import Any

import httpx
from mcp.server.fastmcp import FastMCP


# Set on every session pod by the orchestrator (backend/src/tank_operator/sessions.py).
# TANK_OPERATOR_URL points at the orchestrator Service in-cluster; TANK_API_TOKEN
# is a session JWT bound to the owning user, minted at pod creation.
ORCHESTRATOR_URL_ENV = "TANK_OPERATOR_URL"
API_TOKEN_ENV = "TANK_API_TOKEN"
SESSION_ID_ENV = "TANK_SESSION_ID"

DEFAULT_TIMEOUT_SECONDS = 60.0


def _orchestrator_url() -> str:
    url = os.environ.get(ORCHESTRATOR_URL_ENV, "").rstrip("/")
    if not url:
        raise RuntimeError(
            f"{ORCHESTRATOR_URL_ENV} is not set in the pod env; the orchestrator "
            "did not stamp a callback URL onto this session"
        )
    return url


def _api_token() -> str:
    token = os.environ.get(API_TOKEN_ENV, "")
    if not token:
        raise RuntimeError(
            f"{API_TOKEN_ENV} is not set in the pod env; the orchestrator did "
            "not mint a session JWT for this pod (likely JWT_SECRET unconfigured)"
        )
    return token


def _client(timeout: float = DEFAULT_TIMEOUT_SECONDS) -> httpx.Client:
    return httpx.Client(
        base_url=_orchestrator_url(),
        headers={"Authorization": f"Bearer {_api_token()}"},
        timeout=timeout,
    )


def register_tools(mcp: FastMCP) -> None:
    @mcp.tool()
    def spawn_run_session(
        prompt: str,
        mode: str = "subscription_headless",
        name: str | None = None,
        model: str | None = None,
        permission_mode: str | None = None,
    ) -> dict[str, Any]:
        """Create a fresh tank-operator headless run session and dispatch the first prompt.

        Use this to hand off work to a brand-new agent — the receiver starts
        with no prior context, only the prompt you supply. Returns the new
        session id and a session URL the user can open in a browser to watch
        the run.

        - `prompt`: instructions for the receiving agent (required, non-empty).
        - `mode`: `subscription_headless` (Claude, default) or `codex_headless`.
        - `name`: optional friendly label, shown in the operator UI.
        - `model`, `permission_mode`: optional, forwarded to headless-run.sh.
          Pre-validated to [A-Za-z0-9._-]{1,64} server-side; anything else is
          silently dropped.

        Fire-and-forget: the call returns once the run has been launched on
        the new pod. Output lands in the session's run history; poll with
        `get_run_history(session_id)` if the caller wants to wait on a
        result.
        """
        body: dict[str, Any] = {"prompt": prompt, "mode": mode}
        if name:
            body["name"] = name
        if model:
            body["model"] = model
        if permission_mode:
            body["permission_mode"] = permission_mode
        with _client() as c:
            r = c.post("/api/sessions/run", json=body)
        if r.status_code >= 400:
            return {"error": r.text, "status_code": r.status_code}
        return r.json()

    @mcp.tool()
    def send_to_session(
        session_id: str,
        prompt: str,
        model: str | None = None,
        permission_mode: str | None = None,
    ) -> dict[str, Any]:
        """Append a follow-up prompt to an existing tank-operator run session.

        Use this to continue work in an already-running session — the
        receiving agent picks up with its prior conversation transcript
        (claude --continue or equivalent). The target session must be in a
        headless mode and owned by the same user as this pod.

        Fire-and-forget: returns once the prompt has been queued on the pod.
        """
        body: dict[str, Any] = {"prompt": prompt}
        if model:
            body["model"] = model
        if permission_mode:
            body["permission_mode"] = permission_mode
        with _client() as c:
            r = c.post(f"/api/sessions/{session_id}/messages", json=body)
        if r.status_code >= 400:
            return {"error": r.text, "status_code": r.status_code}
        return r.json()

    @mcp.tool()
    def list_sessions() -> dict[str, Any]:
        """List the caller's tank-operator sessions.

        Useful for discovery before calling `send_to_session` — find the id
        of the sibling run pod by name or mode. Returns the full session
        list visible to the owning user (terminal + headless).
        """
        with _client() as c:
            r = c.get("/api/sessions")
        if r.status_code >= 400:
            return {"error": r.text, "status_code": r.status_code}
        return {"sessions": r.json(), "self": os.environ.get(SESSION_ID_ENV) or None}

    @mcp.tool()
    def whoami() -> dict[str, Any]:
        """Return the user this pod is acting on behalf of, plus the pod's session id.

        Useful before deciding whether to spawn a sibling vs. continue in
        place. The user identity comes from /api/auth/me, which decodes the
        same TANK_API_TOKEN JWT this server uses for every other call — so
        the answer reflects exactly which user the orchestrator will
        attribute subsequent spawn / send actions to.
        """
        with _client() as c:
            r = c.get("/api/auth/me")
        if r.status_code >= 400:
            return {"error": r.text, "status_code": r.status_code}
        body = r.json()
        return {
            "email": body.get("email"),
            "session_id": os.environ.get(SESSION_ID_ENV) or None,
        }

    @mcp.tool()
    def get_run_history(session_id: str) -> dict[str, Any]:
        """Read the claude-code conversation transcript for a headless run.

        Returns the ndjson body served by /api/sessions/{id}/run/history as a
        single string in `transcript`. Empty when the run has not yet
        produced output. Use to poll for completion of a session you spawned
        or messaged.
        """
        with _client(timeout=30.0) as c:
            r = c.get(f"/api/sessions/{session_id}/run/history")
        if r.status_code >= 400:
            return {"error": r.text, "status_code": r.status_code}
        return {"transcript": r.text}
