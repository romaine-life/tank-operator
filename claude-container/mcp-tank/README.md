# mcp-tank

Legacy stdio MCP server baked into tank-operator session containers. Exposes
session-orchestration tools so an agent in one session can hand work off to
another — either by spawning a fresh run pod or by appending a follow-up
to an existing one.

Session pods no longer register this server by default. The canonical
session-management MCP surface is the in-cluster HTTP `tank-operator` server
from `nelsong6/mcp-tank-operator`, mounted through `k8s/session-config/mcp.json`
as `tank-operator`. Keep this package only for explicit local/manual use.

Lives here (rather than its own repo) because the surface is one-to-one
with tank-operator's `/api/sessions/*` endpoints; splitting them would just
make the two drift apart.

When run manually, the server starts as a subprocess and calls the
orchestrator's HTTP API at `$TANK_OPERATOR_URL` using the per-pod
`$TANK_API_TOKEN` JWT.

## Tools

- `spawn_run_session(prompt, mode?, name?, model?, permission_mode?)` —
  create a new headless run session and dispatch the first prompt.
- `send_to_session(session_id, prompt, model?, permission_mode?)` —
  append a follow-up prompt to an existing headless session.
- `list_sessions()` — list the caller's sessions; surfaces the calling
  pod's own `$TANK_SESSION_ID` so the agent knows which row is itself.
- `get_run_history(session_id)` — read the run transcript ndjson back.

## Auth

`TANK_API_TOKEN` is a tank-operator session JWT (HS256, signed with the
orchestrator's `JWT_SECRET`) bound to the pod's owning user. Validated
the same way as a browser session cookie. The shared `claude-session` SA
token is **not** used here — this surface is "user did X", not "the
cluster did X".
