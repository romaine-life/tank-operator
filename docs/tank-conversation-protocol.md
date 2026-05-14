# Tank Conversation Protocol

Status: draft ADR for issue #402.

## Durability Boundary

For this document, "session pod" means the Kubernetes pod backing one Tank
session, including its workspace `emptyDir` and the pod-side Claude/Codex
runner containers.

Tank's messaging durability target is the live-session case: browser
disconnects, frontend reloads, orchestrator restarts/rollouts, and
runner-process restarts while that same session pod is still alive. Session-pod
deletion or death is a terminal session lifecycle event and is intentionally
not a messaging durability target. The protocol must not promise session-pod
resurrection or preservation of in-flight agent work after the pod is gone.

Tank sessions should behave like durable conversations with live event
delivery layered on top. Browser tabs are clients, pod-side runners are
producers, Cosmos and the backend are the source of truth, and React renders a
projection of Tank conversation events. Provider-specific events are inputs to
adapters; they are not UI state.

## Current Code Review

The current implementation already has useful pieces, but they are implicit:

- `agent-runner/src/runner.ts` and `codex-runner/src/runner.ts` both write
  canonical transcript events to Cosmos before the UI can observe them.
- `agent-runner/src/cosmos.ts` and `codex-runner/src/cosmos.ts` define
  canonical-vs-live-only event allowlists, but the allowlists are provider
  shaped rather than Tank shaped.
- `backend-go/internal/store/session_events.go` reads `session-events` by
  session and pages by canonical `order_key`.
- `frontend/src/App.tsx` renders Claude and Codex SDK events through the Tank
  conversation reducer/projection. It also owns status decisions such as
  stopped vs error, active tool state, and reconnect behavior directly in the
  pane.
- The GUI chat path persists `session-events` and queues work in
  `turn-queue`. A future provider should map provider output into the stable
  Tank protocol before touching frontend sidebar and chat state logic.

This ADR makes the missing contract explicit before the next implementation
step rewires producers, backend APIs, and UI projection.

## Borrowed Constraints

Re-checked on 2026-05-12:

- CloudCLI centers persistent cloud environments, cross-device continuity, and
  agent choice. Tank should preserve agent-agnostic session pods and make
  browser disconnects non-terminal for agent work.
- Slack separates durable history from event delivery. Tank needs history APIs
  that can reconstruct the transcript and sidebar state before a live socket is
  attached.
- Zulip separates narrow event streams from the durable message history a
  client can fetch again after reconnect. Tank should keep live delivery as an
  event notification layer over the ledger, not as the only place state exists.
- Matrix Synapse and Element Web use incremental sync tokens over persisted
  room state. Tank should resume from a durable per-session cursor and force a
  timeline reload when the cursor is no longer valid.
- Mattermost and Rocket.Chat treat websocket/SSE-style delivery as a wakeup
  channel for stored posts and events. Tank should follow that shape: providers
  write first, clients observe second.
- Discord Gateway uses event envelopes, sequence numbers, heartbeat/ack, and
  resume from the last sequence. Tank should make connection state separate
  from agent state and resume from a cursor.
- AI SDK UI exposes chat lifecycle states `submitted`, `streaming`, `ready`,
  and `error`, and resumable streams require app-owned persistence of messages
  and active streams. Tank should use that vocabulary, extending it only for
  `stopped` and `needs_input`.
- The OpenAI Codex App Server uses thread, turn, and item primitives. It also
  translates low-level core events into stable UI-ready notifications and can
  pause a turn for approval or other client input. Tank should follow the same
  thread/turn/item split.

References:

- CloudCLI product and API docs: <https://cloudcli.ai/> and
  <https://developer.cloudcli.ai/>
- Zulip server events:
  <https://zulip.com/api/get-events> and
  <https://zulip.com/api/register-queue>
- Matrix Client-Server sync:
  <https://spec.matrix.org/latest/client-server-api/#syncing>
- Mattermost WebSocket and posts APIs:
  <https://api.mattermost.com/#tag/WebSocket> and
  <https://api.mattermost.com/#tag/posts>
- Rocket.Chat realtime API:
  <https://developer.rocket.chat/apidocs/realtime-api>
- Slack Events API and history API:
  <https://docs.slack.dev/apis/events-api/> and
  <https://docs.slack.dev/reference/methods/conversations.history/>
- Discord Gateway: <https://docs.discord.com/developers/events/gateway>
- AI SDK UI: <https://ai-sdk.dev/docs/reference/ai-sdk-ui/use-chat> and
  <https://ai-sdk.dev/docs/ai-sdk-ui/chatbot-resume-streams>
- OpenAI Codex App Server:
  <https://openai.com/index/unlocking-the-codex-harness/>

## Event Envelope

Every Tank event has a stable envelope. The shared JSON Schema lives at
`schemas/tank-conversation-event.schema.json`; TypeScript and Go stubs live at
`frontend/src/tankConversation.ts` and `backend-go/internal/conversation`.
The JSON Schema is the source of truth for `actor`, `source`, `visibility`,
and event `type` enums. Changes to those enums must update the schema first;
`scripts/check-tank-conversation-contract.mjs` and the Go conversation package
test then verify the frontend, agent-runner, codex-runner, and Go definitions
match it. The same script also validates representative canonical fixtures in
`schemas/tank-conversation-event.fixtures.json`, including runner-stamped and
persisted timeline shapes.

Required fields:

- `event_id`: unique replay/dedupe id.
- `order_key` or `sequence`: strict per-session render cursor. New events
  should write both. Consumers sort by `order_key`, then `sequence`, then
  `event_id`.
- `session_id`: Tank session id.
- `turn_id`: required for turn and item events.
- `actor`: `user`, `assistant`, `system`, `tool`, or `runner`.
- `source`: `tank`, `claude`, or `codex`.
- `type`: stable Tank event type.
- `created_at`: producer timestamp in RFC3339 format.
- `visibility`: `durable`, `live-only`, or `audit-only`.

Optional fields:

- `conversation_id`: alias for future non-session conversations. Defaults to
  `session_id` for current Tank sessions.
- `timeline_id`: Tank-owned durable identity for a rendered timeline unit.
- `provider_item_id`: raw provider-scoped item id, preserved only as metadata.
- `parent_id`: causal linkage, such as an item under a turn or approval under a
  tool call.
- `client_nonce`: idempotency key for user submissions.
- `producer`: metadata such as adapter name, version, runtime, and raw provider
  event id.
- `payload`: type-specific data. Keep provider raw payloads under
  `payload.provider` only when needed for a specialized renderer.

Per-event requirements are enforced by
`schemas/tank-conversation-event.schema.json` and runtime validators. The
general envelope is not enough for validity. For example,
`user_message.created` requires `turn_id`, `timeline_id`, `client_nonce`, and
`payload.text`; `item.*` requires `turn_id`, `timeline_id`, and `payload.kind`.
Malformed Tank-owned events must be rejected before persistence or websocket
fanout rather than rendered with fallback ids or empty text.

User-originated display semantics live in the durable user message payload.
Plain user text uses:

```json
{ "display": { "kind": "plain" } }
```

Skill invocations use:

```json
{
  "display": {
    "kind": "skill_invocation",
    "skill_name": "test",
    "supplemental_text": "optional user text"
  }
}
```

The frontend must render skill invocation UI from this durable metadata. It
must not infer skill cards from raw `/skill` or `$skill` prompt text.

## Event Families

Conversation lifecycle:

- `conversation.started`
- `conversation.archived`

User input:

- `user_message.created`

Turn lifecycle:

- `turn.submitted`
- `turn.started`
- `turn.completed`
- `turn.failed`
- `turn.interrupted`

Item lifecycle:

- `item.started`
- `item.delta`
- `item.completed`
- `item.failed`

Tool and approval lifecycle:

- `tool.approval_requested`
- `tool.approval_resolved`

Session/read activity:

- `session.activity_updated`
- `read_state.updated`

## State Machine

A conversation projection has these UI states:

- `ready`: no active turn needs attention.
- `submitted`: user input is durable and waiting for runner execution.
- `streaming`: a runner is executing a turn or emitting items.
- `needs_input`: the runner is paused for approval or other explicit client
  input.
- `stopped`: the active turn ended by user interrupt or runner shutdown, not by
  provider failure.
- `error`: a turn or item failed.

Turn transitions:

1. `user_message.created` records the user's input and `client_nonce`.
2. `turn.submitted` moves the composer to `submitted`.
3. `turn.started` moves the composer and sidebar to `streaming`.
4. `tool.approval_requested` moves the projection to `needs_input` until
   `tool.approval_resolved`.
5. `turn.completed` returns to `ready`.
6. `turn.interrupted` returns to `stopped`.
7. `turn.failed` returns to `error`.

`item.*` events update transcript units under a turn. `item.delta` is durable
only when it is needed to replay the item. Pure typewriter deltas may remain
`live-only` and must not be required to reconstruct final UI state.

## Provider Mappings

Claude SDK adapter:

| Provider event | Tank event | Notes |
| --- | --- | --- |
| Claimed `turn-queue` row | `user_message.created`, `turn.submitted` | Must persist before pushing to the SDK queue. `client_nonce` is required. |
| First SDK output for a turn | `turn.started` | Current Claude SDK stream does not always expose a clean turn marker; adapter may synthesize this after the durable user message. |
| `assistant` text block | `item.completed` | `actor=assistant`, item kind `message`; tool-use blocks become tool items. |
| `assistant` tool_use block | `item.started` | `actor=tool`; include tool name/input in payload. |
| `user` tool_result block | `item.completed` or `item.failed` | Completes the matching tool item by `timeline_id`; provider ids remain metadata. |
| `result` success | `turn.completed` | Include usage when present. |
| `result` error | `turn.failed` | Provider error, not user interrupt. |
| SDK interrupt acknowledgement | `turn.interrupted` | Must not render as provider error. |
| `stream_event`, status, hook/task progress | `item.delta` or live-only ignored | Durable only if needed for replay. |

Codex SDK adapter:

| Provider event | Tank event | Notes |
| --- | --- | --- |
| Claimed `turn-queue` row | `user_message.created`, `turn.submitted` | `client_nonce` required; current `tank_turn_seq` becomes `turn_id` input. |
| `turn.started` | `turn.started` | Preserve provider turn id when available. |
| `item.started` | `item.started` | Tool-like items drive active item state. |
| `item.updated` | `item.delta` | Durable only when final `item.completed` lacks enough state to replay. |
| `item.completed` message/reasoning/tool | `item.completed` | Map command, file change, MCP, and web search to tool item payloads. |
| `turn.completed` | `turn.completed` | Include usage. |
| `turn.failed` or `error` | `turn.failed` | Unless adapter classifies it as abort/interrupt. |
| Abort from user interrupt | `turn.interrupted` | Distinct from provider failure. |

## Backend API Sketch

History read:

`GET /api/sessions/{session_id}/timeline?after_order_key=<cursor>&limit=200`

Returns:

```json
{
  "session_id": "63",
  "events": [],
  "next_order_key": "001...",
  "has_more": false,
  "activity": {
    "status": "streaming",
    "last_order_key": "001...",
    "unread_count": 3,
    "needs_input": false,
    "failed": false,
    "active_turn_id": "turn_..."
  },
  "read_state": {
    "last_read_order_key": "001..."
  }
}
```

Read state write:

`PUT /api/sessions/{session_id}/read-state`

Body:

```json
{ "last_read_order_key": "001..." }
```

Durable SDK turn submission:

`POST /api/sessions/{session_id}/turns`

Body:

```json
{
  "client_nonce": "turn_abc123",
  "prompt": "Implement the change",
  "model": "claude-sonnet-4-6",
  "permission_mode": "bypassPermissions",
  "skill_name": "test",
  "follow_up": true
}
```

The backend validates session ownership and SDK runtime, writes a `turn-queue`
row with `source=sdk`, and returns `202 Accepted`. Runners claim due rows by
session/provider before producing canonical events. Claimed rows carry
`claim_id`, `claimed_by`, `claim_expires_at`, and `attempt_count`; terminal
updates require the current `claim_id`, and expired claims can be reclaimed by
a restarted runner in the same live session pod. Claude `ScheduleWakeup`
re-enqueues as a delayed `turn-queue` row with `source=schedule-wakeup` and
`available_at`. The UI consumes durable transcript delivery from
`GET /api/sessions/{session_id}/events`, where SSE event ids are canonical
`order_key` values and `Last-Event-ID` is the resume cursor. Unknown cursors
produce `resync_required`; clients reload `/timeline` instead of silently
skipping a gap. Open SSE streams do not poll `GET /api/sessions/activity`.
Runners call the internal session-event notify route after durable
`session-events` writes, and the backend wakes only streams for that session.
The stream can still sweep the ledger on a slow interval to handle missed
in-process notifications, but that sweep is not a live UI compatibility path
and cannot replace durable writes.

Durable turn interruption:

`POST /api/sessions/{session_id}/turns/{turn_id}/interrupt`

The backend validates ownership and writes a `turn-queue` row with
`source=interrupt` and `target_turn_id=<turn_id>`. Runners claim the row and
abort the matching active turn from inside the session pod. The UI may show
`stopping` after the enqueue succeeds, but it must not mark the run stopped or
clear the active turn until the durable `turn.interrupted` event appears in
timeline/SSE. Failed interrupt enqueue is a visible control error, not a local
state transition.

Durable Claude input reply:

`POST /api/sessions/{session_id}/turns/{turn_id}/input-reply`

Body:

```json
{
  "provider_item_id": "toolu_...",
  "timeline_id": "item_...",
  "text": "Use option A"
}
```

The backend validates ownership, Claude GUI mode, target ids, and text size,
then writes a `turn-queue` row with `source=input-reply`,
`target_turn_id=<turn_id>`, `target_provider_item_id=<provider_item_id>`,
`target_item_id=<timeline_id>`, and `input_reply=<text>`. The Claude runner
claims the row only when the target turn is active and waiting for that
provider item, pushes a provider tool-result message, and marks the queue row
completed only after the durable `tool.approval_resolved` event is produced.
Codex does not support this control row and fails it explicitly. Browser tabs
must not send AskUserQuestion answers through a runner socket or live-only
control channel.

Durability scope: queued SDK turns are intended to survive browser disconnects,
orchestrator restarts/rollouts, and runner-process restarts while the session
pod itself is still live. Session-pod deletion or death is terminal for the
session and its `emptyDir` workspace; recovering a dead session pod is an
explicit non-goal for this protocol.

Activity summary:

`GET /api/sessions/activity`

Returns per-session activity summaries for the sidebar so unopened sessions can
show running, unread, failed, and needs-input states. This endpoint is a
snapshot API for session lists and initial paint; it is not a transcript-live
polling fallback for an open session.

Storage:

- Keep `session-events` as the durable ledger, partitioned by `tank_session_id`.
- Add or materialize per-user read state keyed by `email + session_id`.
- Use `order_key` as the pagination cursor. Document id is only a dedupe key.
- Compute unread and attention state server-side from durable events plus read
  state.

## Migration Guardrails

- New provider events must map to Tank events, be marked `live-only`, or be
  explicitly ignored with a test.
- New canonical Tank event types must be added to the JSON Schema first, then
  to the Go and TypeScript contract constants in the same change. The contract
  check is expected to fail until every package agrees.
- Timeline replay and SSE delivery must produce the same reducer state for the
  same canonical event sequence.
- `client_nonce` is the idempotency boundary for user submission. The durable
  store should reject or return the existing event for duplicate nonces.
- Browser connection state is never agent state. Disconnect, reconnecting, and
  resumed are transport states layered beside the conversation projection.
