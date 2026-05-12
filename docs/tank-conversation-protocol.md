# Tank Conversation Protocol

Status: draft ADR for issue #402.

Tank sessions should behave like durable conversations with live event
delivery layered on top. Browser tabs are clients, pod-side runners are
producers, Cosmos and the backend are the source of truth, and React renders a
projection of Tank conversation events. Provider-specific events are inputs to
adapters; they are not UI state.

## Current Code Review

The current implementation already has useful pieces, but they are implicit:

- `agent-runner/src/runner.ts` and `codex-runner/src/runner.ts` both dispatch
  Cosmos-first, then WebSocket, which is the right read-your-writes ordering.
- `agent-runner/src/cosmos.ts` and `codex-runner/src/cosmos.ts` define
  canonical-vs-live-only event allowlists, but the allowlists are provider
  shaped rather than Tank shaped.
- `backend-go/internal/store/session_events.go` reads `session-events` by
  session and sorts by `tank_order_key`, then timestamps, then ids. Its public
  API still paginates by `after` document id, so the requested cursor does not
  yet match render order in every legacy case.
- `frontend/src/App.tsx` has provider-specific projection helpers for Claude,
  Codex, and legacy run events. It also owns status decisions such as stopped
  vs error, active tool state, and reconnect behavior directly in the pane.
- The legacy run path persists `active-runs` and `run-events`; the SDK path
  persists `session-events`. A future provider would still need to touch
  frontend sidebar and chat state logic unless the provider output is first
  mapped into a stable Tank protocol.

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

Required fields:

- `event_id`: unique replay/dedupe id.
- `order_key` or `sequence`: strict per-session render cursor. New events
  should write both. Consumers sort by `order_key`, then `sequence`, then
  `event_id`.
- `session_id`: Tank session id.
- `turn_id`: required for turn and item events.
- `actor`: `user`, `assistant`, `system`, `tool`, or `runner`.
- `source`: `tank`, `claude`, `codex`, or `legacy-run`.
- `type`: stable Tank event type.
- `created_at`: producer timestamp in RFC3339 format.
- `visibility`: `durable`, `live-only`, or `audit-only`.

Optional fields:

- `conversation_id`: alias for future non-session conversations. Defaults to
  `session_id` for current Tank sessions.
- `item_id`: required when the event concerns a durable unit inside a turn.
- `parent_id`: causal linkage, such as an item under a turn or approval under a
  tool call.
- `client_nonce`: idempotency key for user submissions.
- `producer`: metadata such as adapter name, version, runtime, and raw provider
  event id.
- `payload`: type-specific data. Keep provider raw payloads under
  `payload.provider` only when needed for a specialized renderer.

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
| Browser submit frame | `user_message.created`, `turn.submitted` | Must persist before pushing to the SDK queue. `client_nonce` is required. |
| First SDK output for a turn | `turn.started` | Current Claude SDK stream does not always expose a clean turn marker; adapter may synthesize this after the durable user message. |
| `assistant` text block | `item.completed` | `actor=assistant`, item kind `message`; tool-use blocks become tool items. |
| `assistant` tool_use block | `item.started` | `actor=tool`; include tool name/input in payload. |
| `user` tool_result block | `item.completed` or `item.failed` | Completes the matching tool item by `item_id`. |
| `result` success | `turn.completed` | Include usage when present. |
| `result` error | `turn.failed` | Provider error, not user interrupt. |
| SDK interrupt acknowledgement | `turn.interrupted` | Must not render as provider error. |
| `stream_event`, status, hook/task progress | `item.delta` or live-only ignored | Durable only if needed for replay. |

Codex SDK adapter:

| Provider event | Tank event | Notes |
| --- | --- | --- |
| Browser submit frame | `user_message.created`, `turn.submitted` | `client_nonce` required; current `tank_turn_seq` becomes `turn_id` input. |
| `turn.started` | `turn.started` | Preserve provider turn id when available. |
| `item.started` | `item.started` | Tool-like items drive active item state. |
| `item.updated` | `item.delta` | Durable only when final `item.completed` lacks enough state to replay. |
| `item.completed` message/reasoning/tool | `item.completed` | Map command, file change, MCP, and web search to tool item payloads. |
| `turn.completed` | `turn.completed` | Include usage. |
| `turn.failed` or `error` | `turn.failed` | Unless adapter classifies it as abort/interrupt. |
| Abort from user interrupt | `turn.interrupted` | Distinct from provider failure. |

Legacy run adapter:

| Legacy event | Tank event | Notes |
| --- | --- | --- |
| `run.message.created` user | `user_message.created` | Use run id as `turn_id` until the turn queue owns ids. |
| `run.output.started` | `turn.started` | Synthetic start marker. |
| `run.tool.started` | `item.started` | Tool item with `item_id=tool_use_id`. |
| `run.tool.completed` | `item.completed` or `item.failed` | Failure when `is_error=true`. |
| `run.message.created` assistant | `item.completed` | Assistant message item. |
| `run.completed` | `turn.completed` | |
| `run.failed` or `run.stale` | `turn.failed` | Stale is a distinct reason in payload. |

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

Activity summary:

`GET /api/sessions/activity`

Returns per-session activity summaries for the sidebar so unopened sessions can
show running, unread, failed, and needs-input states.

Storage:

- Keep `session-events` as the durable ledger, partitioned by `tank_session_id`.
- Add or materialize per-user read state keyed by `email + session_id`.
- Use `order_key` as the pagination cursor. Document id is only a dedupe key.
- Compute unread and attention state server-side from durable events plus read
  state.

## Migration Guardrails

- New provider events must map to Tank events, be marked `live-only`, or be
  explicitly ignored with a test.
- Replay and live delivery must produce the same reducer state for the same
  canonical event sequence.
- `client_nonce` is the idempotency boundary for user submission. The durable
  store should reject or return the existing event for duplicate nonces.
- Browser connection state is never agent state. Disconnect, reconnecting, and
  resumed are transport states layered beside the conversation projection.
