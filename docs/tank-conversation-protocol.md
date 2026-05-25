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
producers, the Cosmos `session-events` ledger is the replay source of truth,
NATS JetStream is the durable live work fabric, and React renders a projection
of Tank conversation events. Provider-specific events are inputs to adapters;
they are not UI state.

## Current Architecture

The implementation has explicit durable and live boundaries:

- `agent-runner/src/runner.ts` and `codex-runner/src/runner.ts` publish
  canonical transcript events to the NATS JetStream session bus before the UI
  can observe them.
- `agent-runner/src/sessionEvents.ts` and `codex-runner/src/sessionEvents.ts`
  define canonical Tank event allowlists. All Tank events are durable.
- The backend session-bus persister writes bus events to the Postgres
  `session_events` table and wakes SSE streams only after the ledger write
  commits.
- `backend-go/internal/store/session_events.go` reads `session_events` by
  session and pages by canonical `order_key`.
- `frontend/src/App.tsx` renders Claude and Codex SDK events through the Tank
  conversation reducer/projection. It also owns status decisions such as
  stopped vs error, active tool state, and reconnect behavior directly in the
  pane.
- The GUI chat path publishes durable SDK commands to NATS JetStream. A future
  provider should map provider output into the stable Tank protocol before
  touching frontend sidebar and chat state logic.

This ADR is the live contract for the app-managed GUI chat path. Changes to
producer, backend, or UI behavior should update this document in the same PR.

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
`schemas/tank-conversation-event.schema.json`; the single TypeScript stub
lives at `runner-shared/conversation.{js,d.ts}` (consumed by codex-runner,
agent-runner, and the frontend); the Go stub lives at
`backend-go/internal/conversation`. The JSON Schema is the source of truth
for `actor`, `source`, `visibility`, and event `type` enums. Changes to
those enums must update the schema first;
`scripts/check-tank-conversation-contract.mjs` and the Go conversation
package test then verify the shared TS module and Go definitions match it.
The same script validates canonical fixtures in
`schemas/tank-conversation-event.fixtures.json`.

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
- `visibility`: `durable`. The `live-only` value was retired once the
  producer-side live channel never landed; the enum is kept single-valued so
  callers still tag durability explicitly rather than infer it.

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

User input:

- `user_message.created`

Turn lifecycle:

- `turn.submitted`
- `turn.started`
- `turn.completed`
- `turn.failed`
- `turn.command_failed`
- `turn.interrupt_requested`
- `turn.interrupted`

Item lifecycle:

- `item.started`
- `item.completed`
- `item.failed`

Background shell task lifecycle:

- `shell_task.started`
- `shell_task.updated`
- `shell_task.exited`

Tool and approval lifecycle:

- `tool.approval_requested`
- `tool.approval_resolved`

Session activity is computed server-side by the lifecycle emitter as
sessions evolve and published as `session.activity_changed` rows in the
durable session-list lifecycle ledger
(`session_lifecycle_events`); the same payload is delivered over the
sidebar SSE stream (`GET /api/sessions/events`) and joined into
`GET /api/sessions` for initial-state hydration. Per-conversation read
state lives at `/api/sessions/{id}/read_state` and is also derived from
the durable event ledger. Neither is a Tank chat event type — adding one
is a schema change, not a derived projection.

## State Machine

A conversation projection has these UI states:

- `ready`: no active turn needs attention.
- `submitted`: user input is durable and waiting for runner execution.
- `streaming`: a runner is executing a turn or emitting items.
- `needs_input`: the runner is paused for approval or other explicit client
  input.
- `stopping`: a user-initiated stop has landed on the durable ledger; the
  runner has not yet emitted a terminal event.
- `stopped`: the active turn ended by user interrupt or runner shutdown, not by
  provider failure.
- `error`: the session is in a failed state. Reached only via a durable
  turn-terminal failure (`turn.failed` or `turn.command_failed`) or by the
  session pod entering the Failed state (`failedFromPod`). `item.failed`
  is a per-item transcript marker for a single failed tool call; the agent
  routinely handles tool errors and continues, so it does not transition
  session-level state. The per-item error badge in the transcript renders
  from the same `item.failed` event independently.

Turn transitions:

1. `user_message.created` records the user's input and `client_nonce`.
2. `turn.submitted` moves the composer to `submitted`.
3. `turn.started` moves the composer and sidebar to `streaming`.
4. `tool.approval_requested` moves the projection to `needs_input` until
   `tool.approval_resolved`.
5. `turn.interrupt_requested` moves the projection from `submitted`,
   `streaming`, or `needs_input` to `stopping`; `activeTurnId` is preserved
   because the turn is still mid-flight. A late-arriving request after a
   terminal event records the chip but does not downgrade the terminal
   state.
6. `turn.completed` returns to `ready` (also from `stopping` when the stop
   lost the race to a clean completion).
7. `turn.interrupted` returns to `stopped`.
8. `turn.failed` returns to `error`.

`item.*` events update transcript units under a turn. The event type is
the lifecycle/plumbing axis:

- `item.completed`: the provider item reached a durable result.
- `item.failed`: the provider item failed before a usable result existed
  (adapter/provider execution failure).

Completed-but-unsuccessful tool results stay `item.completed` and carry
`payload.outcome`. Supported outcome kinds are:

- `{kind:"ok"}`: normal completion.
- `{kind:"result_failed", reason:"exit_code" | "claude_tool_result_is_error" | "codex_item_status_failed", code?}`:
  the tool ran and returned a bad result.
- `{kind:"execution_failed", reason:"provider_item_error"}`: execution
  failed; the adapter emits `item.failed`.

The frontend renders `result_failed` items with a failed/error tone,
matching shell and CI conventions for nonzero exit codes. It does not
derive session/sidebar failure from item outcomes; only turn-terminal
failures and pod failure affect session-level error state.

### Transcript Compaction

The durable ledger remains fully replayable. Transcript compaction is a
server-owned transcript projection layered on top of `session_events`, not a
producer or storage behavior and not a browser-local reconstruction pass. The
projection has two distinct surfaces:

- The Turn activity row is an activity/log projection of what happened during a
  turn.
- The main transcript is the settled conversation projection.

Rows must not visibly bounce between those surfaces. If an event is eligible
for Turn activity, the server transcript projection classifies it before first
paint; the frontend must not render the event as a standalone main-transcript
row and later move that same rendered row into Turn activity. Conversely,
content shown inside Turn activity while a turn is active is provisional
activity output, not a settled transcript row being promoted later.

Historical timeline reads return first-class `turn_activity` rows. These rows
load collapsed by default and carry summary metadata only: turn id, activity
counts, compacted child ids, order range, timestamps, status, and error count.
The child entries for a Turn activity row are fetched only when the row is
expanded through the turn activity endpoint. This keeps previous-conversation
navigation bounded while preserving a durable replay path for deep links.

For an active turn, the client may condense assistant progress notes,
provisional assistant text, tool rows, reasoning blocks, background-task rows,
and meta rows into a single Turn activity disclosure row as they arrive. A
normal assistant message does not by itself declare that it is the final answer;
without an explicit durable final-answer marker, the final answer is identified
only after a successful terminal event.

For a turn that ended with `turn.completed` and produced at least one assistant
message, the client identifies the final answer as the trailing assistant
message/run for that turn. The final assistant answer is rendered in the main
transcript as the settled assistant response. Pre-final activity may remain
condensed into the same Turn activity row. The activity row may also contain an
activity/log copy of assistant prose, including prose that later becomes the
final answer, but that copy is not a second settled transcript message.

Failed turns, interrupted turns, and turns that never produce a final assistant
message stay expanded so failure and stop context is not hidden.

Deep links still target durable `timeline_id` values. Opening a link to a
compacted activity item expands the Turn activity row around that item. When the
same assistant prose is projected both as Turn activity and as the settled final
answer, the settled main-transcript answer is the canonical message target for
copy links, unread counts, latest-message state, and fork-from-message actions;
the activity copy is evidence for the turn log, not another conversation
message.

`shell_task.*` events are session-level background shell processes spawned
by a tool call. They are not normal tool items: they can continue after the
foreground turn has completed, they render as their own transcript artifact,
and they are listed in the session shell-task ledger. A background task is
owned by the turn that spawned it (`turn_id`, `timeline_id`, `task_id`), but
its lifecycle does not transition the conversation run state. This mirrors
the product contract in Claude Code and Codex: background shell work is
visible and session-owned, while Stop terminates the active foreground turn
rather than pretending every descendant process is chat prose.

Per-token typewriter deltas are intentionally not on the Tank event
surface: the `item.delta` event type and the `live-only` visibility were
retired together once it became clear no consumer subscribed to either.
Items are snapshotted via `item.started` → `item.completed`; if a future
live channel for partial tokens lands, restore both the event type and
the visibility together.

## Provider Mappings

Claude SDK adapter:

| Provider event | Tank event | Notes |
| --- | --- | --- |
| JetStream `submit_turn` command | `user_message.created`, `turn.submitted` | Backend publishes these events at the submit boundary; runner duplicate publishes are deduped by event id. `client_nonce` is required. |
| First SDK output for a turn | `turn.started` | Current Claude SDK stream does not always expose a clean turn marker; adapter may synthesize this after the durable user message. |
| `assistant` text block | `item.completed` | `actor=assistant`, item kind `message`; tool-use blocks become tool items. |
| `assistant` tool_use block | `item.started` | `actor=tool`; include tool name/input in payload. |
| `user` tool_result block | `item.completed` | Completes the matching tool item by `timeline_id`; `is_error=true` maps to `payload.outcome.kind="result_failed"`, not `item.failed`. |
| `system/task_started` | `shell_task.started` | `actor=tool`; `task_id` identifies the background shell task. The runner records task ownership so later notifications still attach to the spawning turn. |
| `system/task_progress`, `system/task_updated` | `shell_task.updated` | Progress/status snapshots for an already-owned background task. |
| `system/task_notification` terminal status | `shell_task.exited` | Terminal background task result (`completed`, `failed`, `stopped`, etc.) without changing session run status. |
| `result` success | `turn.completed` | Include usage when present. |
| `result` error | `turn.failed` | Provider error, not user interrupt. |
| SDK interrupt acknowledgement | `turn.interrupted` | Must not render as provider error. |
| `stream_event`, status, hooks, plugin changes | ignored | Per-token deltas are not on the Tank surface; restoring requires re-adding `item.delta` + `live-only` together. |

Codex SDK adapter:

| Provider event | Tank event | Notes |
| --- | --- | --- |
| JetStream `submit_turn` command | `user_message.created`, `turn.submitted` | Backend publishes these events at the submit boundary; runner duplicate publishes are deduped by event id. `client_nonce` is required. |
| `turn.started` | `turn.started` | Preserve provider turn id when available. |
| `item.started` | `item.started` | Tool-like items drive active item state. |
| `item.updated` | ignored (no Tank event) | Adapter still observes ordinary frames so `item.completed` can fall back to the last running text; no Tank event reaches the bus. Codex unified-exec background terminal updates are the exception and map to `shell_task.updated`. |
| `userMessage` item echo | ignored (no Tank event) | Tank owns submitted user input through the backend-published `user_message.created` event. Provider echoes of that input must not enter the durable item stream or render as tool calls. |
| `item.completed` message/reasoning/tool | `item.completed` or `item.failed` | Map command, file change, MCP, and web search to tool item payloads. Nonzero exit codes and provider status `failed` with no execution error map to `payload.outcome.kind="result_failed"`. A non-null provider item error maps to `item.failed` with `outcome.kind="execution_failed"`. |
| `commandExecution` with `source=unifiedExecStartup` or `source=unifiedExecInteraction` | `shell_task.started`, `shell_task.updated`, `shell_task.exited` | Codex App Server background terminals are session-owned processes. `processId` is the preferred `task_id`; `thread/backgroundTerminals/clean` is the explicit action that stops them. |
| `turn.completed` | `turn.completed` | Include usage. |
| `turn.completed` with provider status `interrupted` | `turn.interrupted` | Codex App Server documents `turn/interrupt` as cancelling the active turn without terminating background terminals. |
| `turn.failed` or `error` | `turn.failed` | Unless adapter classifies it as abort/interrupt. |
| Abort from user interrupt | `turn.interrupted` | Distinct from provider failure. |

## Backend API Sketch

History reads:

- Normal navigation opens the live tail:
  `GET /api/sessions/{session_id}/timeline?anchor=newest&limit=200`
- Explicit message links open a bounded page around a durable transcript
  identity:
  `GET /api/sessions/{session_id}/timeline?timeline_id=<timeline_id>&num_before=100&num_after=100`
- Manual upward pagination reads older events:
  `GET /api/sessions/{session_id}/timeline?before_order_key=<cursor>&limit=100`
- Managed Codex background terminal stop:
  `POST /api/sessions/{session_id}/background-tasks/{task_id}/stop`
  publishes a `stop_background_task` control-plane command. The Codex
  app-server runner maps that to `thread/backgroundTerminals/clean` and emits
  `shell_task.exited{status:"stopped"}` for the selected task so the Background
  page drops it from the active list. Detached shell candidates remain
  observational because Tank does not own their PID lifecycle.

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

The frontend must attach the live SSE stream only after the initial `/timeline`
read has established a cursor. Browser-local scroll position is not a supported
timeline anchor; reopening or switching sessions uses `anchor=newest` unless the
URL carries an explicit `message`/`timeline_id` target.

This also applies when the React chat pane stayed mounted while hidden. Mounted
component state may preserve local controls, but it is not a transcript anchor:
when the pane becomes visible again, the SPA resets its local timeline window
and performs the same durable bootstrap (`anchor=newest`, or the explicit
message target if the URL carries one) before reopening the per-session SSE
stream.

Read state write:

`PUT /api/sessions/{session_id}/read-state`

Body:

```json
{ "last_read_order_key": "001..." }
```

Launch-time durable chat turn submission:

`POST /api/sessions`

Body:

```json
{
  "mode": "claude_gui",
  "repos": ["owner/repo"],
  "initial_turn": {
    "client_nonce": "turn_abc123",
    "prompt": "Implement the change",
    "model": "claude-sonnet-4-6",
    "permission_mode": "bypassPermissions",
    "skill_name": "test"
  }
}
```

When `initial_turn` is present for an SDK chat session, the backend validates
the turn before creating the session. After `manager.Create` returns, the
backend writes the `user_message.created` and `turn.submitted` boundary events
directly to `session_events` with timestamps that sort before the session
startup `session.status` row, then publishes the durable JetStream
`submit_turn` command without waiting for the pod to become Ready. JetStream is
the readiness buffer; the runner consumes the command after it starts. This is
the only path for a no-attachment first prompt from the splash composer, so the
first visible transcript row is the user's launch message, followed by durable
startup status and then runner output.

Hermes/no-pod chat sessions use the same `initial_turn` create boundary, but
the bridge writes the boundary events and starts the Hermes run directly rather
than publishing a JetStream command.

Attachment-backed SDK launches set `initial_turn.deferred=true`. The create
request still writes `user_message.created` before startup status, using the
user's text plus attachment names as the durable display text. After the pod is
ready and files are uploaded into the workspace, the SPA submits the same
`client_nonce` to `POST /api/sessions/{session_id}/turns` with
`existing_user_message=true`; the backend writes only `turn.submitted` and
publishes the runnable command whose prompt contains the pod-local attachment
paths. This preserves one user bubble and one turn id while keeping file bytes
on the existing workspace upload path.

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
  "existing_user_message": false,
  "follow_up": true
}
```

The backend validates session ownership and SDK runtime, requires the session
pod to be ready, writes `user_message.created` and `turn.submitted` boundary
events directly to `session_events`, then publishes a durable JetStream
`submit_turn` command keyed by `client_nonce`. The runner consumes commands
through a durable per-session/per-provider consumer and calls JetStream
`working()` while a long turn is in flight.
When `existing_user_message=true`, the user row must already have been written
by the launch-time create boundary, so this endpoint writes `turn.submitted`
only.
Command ack happens only after the corresponding durable terminal event is
published. Claude `ScheduleWakeup` is handled pod-side: the agent-runner extracts
the tool_use, holds a `setTimeout` for `delaySeconds`, and at fire time publishes
a normal `submit_turn` command (`source=schedule-wakeup`) to the command subject.
The scheduler is in-process state inside the runner; it does not survive runner-
process death and does not need to, per the durability boundary in
docs/product-inspirations.md (the pod owns runtime scheduler state, not Cosmos).

The UI consumes durable transcript delivery from
`GET /api/sessions/{session_id}/events`, where SSE event ids are canonical
`order_key` values and `Last-Event-ID` is the resume cursor. Unknown cursors
produce `resync_required`; clients reload `/timeline` instead of silently
skipping a gap. Open SSE streams do not poll any side endpoint for
indicator state. Because browser-native EventSource cannot attach an
`Authorization` header, the SPA first calls `POST /api/auth/stream-ticket`
with its auth.romaine.life bearer JWT and then opens EventSource with the
short-lived opaque `stream_ticket` query carrier. The ticket is scoped to
stream kind, session scope, and session id, stored in Postgres so replica
rollouts do not strand reconnects, and accepted only by SSE handlers. The
backend event persister wakes SSE streams through NATS only after the
Postgres `session_events` write commits. There is no ledger sweep or browser
polling fallback for live transcript delivery.

Copied transcript links are also machine-readable. A browser link such as
`/?session=<id>&message=<timeline_id>` still serves the SPA for humans, but
the HTML shell includes a `<script id="tank-message-link"
type="application/json">` contract, `<link rel="alternate"
type="application/json">`, and HTTP `Link` headers that name the session,
`timeline_id`, and canonical `timeline_url`. Non-browser fetches (no `Accept`
header, `Accept: */*`, `Accept: application/json`, or `?format=json`) get JSON
directly; unauthenticated callers get the contract plus auth instructions,
while authenticated callers get the resolved timeline payload inline. The
payload is the same durable `/timeline` response: `target_timeline_id`,
`target_order_key`, and a bounded `events` window around the persisted cursor.
The JSON contract carries an `agent_recipe` array with copyable curl commands:
send the projected service-account token to auth.romaine.life as
`Authorization: Bearer <token>`, exchange the returned `auth_jwt` at this Tank
origin, fetch the `json_url`, and page older context with
`before_order_key=<prev_order_key>` when `found_oldest=false`.

Durable turn interruption:

`POST /api/sessions/{session_id}/turns/{turn_id}/interrupt`

The backend validates ownership, then performs two writes in this order:

1. **Persist `turn.interrupt_requested`** to `session_events` via the same
   `persistBackendEvent` path the submit boundary uses for
   `user_message.created` / `turn.submitted`. Event_id is deterministic in
   `target_turn_id` (`<turnID>:turn.interrupt_requested`) so a double-click
   POST collapses to one durable row at the Postgres UNIQUE constraint.
2. **Publish a durable JetStream `interrupt_turn` command** on the
   per-session/per-provider **control-plane subject**
   (`tank.session.<scope-token>.<session-token>.control.<provider>`), not the command subject
   used for `submit_turn` / `input_reply`. Runners consume the command
   from a dedicated control-plane JetStream consumer (separate
   `durable_name`, separate `filter_subject`, higher `max_ack_pending`)
   and abort the matching active turn from inside the session pod.

The data plane (`tank.session.<scope-token>.<session-token>.commands.<provider>`) and the
control plane (`tank.session.<scope-token>.<session-token>.control.<provider>`) are
deliberately separate JetStream subjects with separate durable consumers.
The data-plane consumer runs `max_ack_pending=1` so a long-running
`submit_turn` is processed end-to-end before the next one starts; that's
correct for turn serialization but fatal for stop semantics if interrupts
shared the same consumer — a queued interrupt would sit behind the
in-flight submit's ack window (sustained by `working()` heartbeats for
the full duration of the turn) and only be delivered after the turn
naturally completed. The split is the load-bearing fix for the "Stop
doesn't interrupt deep tool-use loops" regression; see
`scripts/check-removed-chat-runtime.mjs` and
`backend-go/internal/sessionbus/SubjectForCommand` for the regression
guards on either side of the wire.

Runner-produced events use the sibling event subject
`tank.session.<scope-token>.<session-token>.events`, and each orchestrator
runs its persister on `tank.session.<scope-token>.*.events` with a
scope-specific durable consumer name. Prod and test slots share the NATS
stream, so the scope token is part of the physical subject rather than a
post-delivery filter; a slot completion cannot be claimed by a different
scope's persister and lose the sidebar activity-summary side effect.

If step 1 fails, the handler returns 500 and step 2 does not execute — the
ledger never carries a side-effect that wasn't accompanied by a durable
record. If step 1 succeeds and step 2 fails, the existing
`turn.command_failed` durable marker is written after the
`turn.interrupt_requested` row. The reducer resolves the chain to `error`;
the `turn.interrupt_requested` chip stays on the transcript as honest
evidence that the user did press Stop.

**Four-outcome contract on the runner side (post-#532).** Once an
`interrupt_turn` command lands on the runner's control-plane consumer,
the runner MUST produce exactly one of these terminal-outcome
increments on `tank_runner_interrupt_outcome_total` within bounded
time, and a corresponding durable terminal event:

- `terminated_via_sdk` — interrupt arrived during an in-flight turn.
  The runner signals the provider immediately, then publishes
  `turn.interrupted` with bounded retry without waiting for provider
  acknowledgement. Claude first asks the SDK to background in-flight
  foreground Bash/subagent tasks, but a short grace deadline prevents
  that control call from holding Stop hostage. Codex App Server receives
  `turn/interrupt` and, per its protocol, does not terminate background
  terminals. Late foreground SDK frames after Tank has emitted the
  terminal are ignored; background task lifecycle frames remain visible
  via `shell_task.*`.
- `terminated_pre_sdk` — interrupt arrived before the matching
  `submit_turn` had been dispatched on this runner. The control plane
  and data plane don't synchronize past JetStream-level delivery (by
  #511's design), so an early-stop click can race the submit. The
  runner holds such interrupts in an in-process `pendingInterrupts`
  buffer, drains them when the matching `submit_turn` lands, and
  synthesizes `turn.interrupted{reason:"client_interrupt_before_start"}`
  without ever feeding the prompt to the SDK.
- `orphaned` — a buffered interrupt's matching `submit_turn` never
  arrived within `SESSION_INTERRUPT_BUFFER_MS` (default 30s). The
  runner synthesizes `turn.failed{reason:"interrupt_orphaned"}` so the
  UI's "stopping" projection resolves to a durable terminal rather
  than hanging.
- `publish_failed` — `sdkQuery.interrupt()` was attempted, every retry
  to publish `turn.interrupted` failed, and the fallback
  `turn.failed{reason:"publish_interrupt_failed"}` also failed. The
  JetStream `interrupt_turn` command is NAK'd; redelivery on the next
  `ack_wait` expiry retries the whole flow. Alert-worthy if it
  persists.
- `turn_already_terminal` — interrupt arrived after the targeted turn
  had already emitted its own terminal (`turn.completed` /
  `turn.failed`). The race is legitimate; the durable ledger shows the
  natural terminal; the UI resolves via the existing race-resolution
  arm in `conversationReducer.ts`.
- `invalid_target` — `interrupt_turn` arrived with neither
  `target_turn_id` nor `client_nonce`. Backend bug; should be zero in
  production.
- `buffered` is a transient arrival counter (not a terminal). Every
  `buffered` increment must drain to one of the terminal-outcome
  buckets above; the difference is the alert surface.

There is no other valid outcome. Returning silently from
`acceptInterrupt` or `interruptActiveTurn` without producing a durable
terminal AND a counter increment is the bug class #532 closed — see
the issue for the post-mortem evidence (Postgres `session_events` rows
for session 19 showing 20 of 24 item completions landing AFTER a stop
click, with no `turn.interrupted` ever emitted).

**Oversized-event truncation contract (post-#532 Stage 3).** Tank
conversation events whose JSON-encoded body would exceed NATS's
`max_payload` (1 MiB default) are truncated by
`runner-shared/sessionBus.js::truncateEventIfOversized` before reaching
the JetStream publish. The default per-runner budget is 900 KiB
(`SESSION_EVENT_MAX_BYTES`); strings longer than 50 KiB are eligible
for replacement (`SESSION_EVENT_STRING_THRESHOLD`). Two truncation
shapes apply, in order:

1. **`strings-truncated`** — one or more string fields are replaced
   with a typed marker: `[truncated: <N> bytes original;
   sha256_16=<16 hex chars>; reason=event-too-large for transport]`.
   Schema shape is preserved (the field stays a string); downstream
   adapters/persister/SPA need no special handling. The marker carries
   the original byte count and a SHA256 prefix for forensic recovery
   from upstream caches (Claude/Codex CLI's on-disk JSONL transcript).
2. **`payload-dropped`** — even after aggressive string truncation the
   event was still over budget; the entire `payload` is replaced with
   `{__payload_dropped: true, original_bytes, reason: "event_oversized_after_truncation"}`.
   The event envelope (type, turn_id, event_id, conversation_id,
   producer, etc.) stays intact so the durable ledger still records
   "an event of this type existed for this turn at this order_key" —
   the body is unrecoverable from the wire path but the structural
   event survives.

Each truncation increments `tank_runner_event_truncated_total
{event_type, severity}`. Severity `strings-truncated` is informational;
`payload-dropped` is alert-worthy because sustained `payload-dropped`
traffic means a producer (typically a `tool_result.output` from `Read`
of a large file or `Bash` with massive stdout) needs to chunk or
stream rather than emit one giant Tank event. Pre-#532 the same
oversized payload would throw `payload max_payload size exceeded`
synchronously inside the NATS client, the runner's `dispatch()` would
catch it, and the event would silently vanish — Session 19's seven
publish failures across the pod's lifetime were exactly this shape.

Subjective rule for renderers: an `__payload_dropped` marker should
render as a "[content too large to display]" affordance, ideally with
the `original_bytes` field shown so the user has a forensic breadcrumb.
A `[truncated: …]` string inside a normal text field should render
inline as the string itself (it already reads as a marker).

The UI's `stopping` run status is **strictly a projection** of the durable
`turn.interrupt_requested` event. No client-side flag, no UI-local mirror.
A refresh after pressing Stop replays the chip from `/timeline` and the
projection reconstructs the stopping state without further work.
`scripts/check-removed-chat-runtime.mjs` blocks reintroduction of the
retired `stopRequested` / `stoppingTargetRef` UI-mirror; the cancelRun
function body is pinned free of `setRunStatus("stopping")` by
`frontend/src/migrationPolicy.test.ts`. Failed interrupt publish is a
visible control error, not a local state transition.

Provider mapping for the new event: there is no provider mapping.
`turn.interrupt_requested` is produced by the orchestrator at the `/interrupt`
boundary, regardless of provider. `actor=system`, `source=tank`. Runners
remain the sole producers of `turn.interrupted`.

Durable AskUserQuestion answer (`input_reply` command):

`POST /api/sessions/{session_id}/turns/{turn_id}/input-reply`

Body:

```json
{
  "provider_item_id": "toolu_...",
  "timeline_id": "item_...",
  "answers": {
    "Which auth method should we use?": ["OAuth"]
  },
  "annotations": {
    "Which auth method should we use?": { "notes": "matches the existing IdP" }
  }
}
```

`answers` is `Record<questionText, answerLabel[]>` — always an array so
multi-select questions and single-select questions share one shape.
`annotations` is optional `Record<questionText, { preview?, notes? }>` from
the Claude Agent SDK's AskUserQuestion schema and carries free-text notes
the user attached to a selected option.

The backend validates ownership, that the session mode supports durable
AskUserQuestion replies, target ids, that the answers map is non-empty, and
total payload size, then publishes a durable JetStream `input_reply` command
with `target_turn_id=<turn_id>`,
`target_provider_item_id=<provider_item_id>`,
`target_timeline_id=<timeline_id>`, `answers`, and (optionally)
`annotations`.

The Claude runner accepts the command only when the target turn is active
and the matching AskUserQuestion tool call is waiting on a `canUseTool`
permission decision. The runner does not synthesize a `tool_result` user
message: AskUserQuestion answer delivery rides on the SDK's
`canUseTool({behavior:"allow", updatedInput:{questions, answers,
annotations}})` contract, and the SDK's own tool definition formats the
canonical `tool_result` content from `updatedInput`. The runner acks the
durable command only after publishing `tool.approval_resolved` whose payload
mirrors the answers (and annotations, if any) that resolved the call.

Codex GUI uses the Codex App Server transport as its primary path. In
`codex_gui` and the backwards-compatible `codex_app_server` mode, Codex App
Server emits an `item/tool/requestUserInput` server request; the codex-runner
publishes the same durable `tool.approval_requested` event as Claude and
resolves the provider request after receiving this durable `input_reply`
control command.

The explicit `codex_exec_gui` fallback preserves the old SDK / `codex exec`
transport. That fallback does not support durable AskUserQuestion replies:
`codex exec` rejects `request_user_input` at the provider layer, and
codex-runner rejects `input_reply` commands when it is not running the
app-server transport. Browser tabs must not send AskUserQuestion answers
through a runner socket or any other non-durable control channel.

Durability scope: session commands are intended to survive browser
disconnects, orchestrator restarts/rollouts, and runner-process restarts while
the session pod itself is still live. Session-pod deletion or death is terminal
for the session and its `emptyDir` workspace; recovering a dead session pod is
an explicit non-goal for this protocol.

Activity summary (per-session sidebar indicators):

Activity summaries are durable rows of type `session.activity_changed`
in the per-owner `session_lifecycle_events` ledger. The lifecycle emitter
folds chat events into a summary (status, active_turn_id, needs_input,
failed, unread_count) on each chat event upsert and emits a new row only
when an indicator-visible field changes. The sidebar consumes the same
payload two ways:

- Initial state: the latest `session.activity_changed` payload per
  session is joined into `GET /api/sessions` as the `activity` field.
- Live updates: the typed-event SSE stream on `GET /api/sessions/events`
  delivers each new row to the sidebar in real time.

`GET /api/sessions/timeline` returns a paginated, cursor-resumable slice
of the ledger for post-resync recovery.

Storage:

- Keep the Postgres `session_events` table as the durable ledger, partitioned
  by `tank_session_id`.
- Use NATS JetStream as the durable command/event fabric, not as the product
  history database. Commands are acked only after durable terminal events are
  published; events are acknowledged by the backend persister after the
  Postgres upsert succeeds.
- Add or materialize per-user read state keyed by `email + session_id`.
- Use `order_key` as the pagination cursor. Document id is only a dedupe key.
- Compute unread and attention state server-side from durable events plus read
  state.

Operational counters:

- `/metrics` exposes the `tank_session_event_stream_*` counters
  (opens, reconnects, `resync_required`, stream errors, timeline read
  failures, emitted events, heartbeats, wake-subscribe failures) and the
  `tank_session_event_stream_lag_seconds` histogram. See
  `docs/observability.md` for the full taxonomy and the Grafana panels.
- `tank_stream_auth_ticket_total` covers the EventSource auth boundary; store
  failures there explain the failure mode where `/timeline` refresh works but
  live transcript or sidebar SSE never opens.
- Missing-message investigations should start from those metrics and the
  durable Postgres `session_events` ledger cursor, not from browser-local state.

## Migration Guardrails

- New provider events must map to a Tank event or be explicitly ignored with
  a test. The `live-only` visibility was retired; resurrecting it requires
  re-introducing the producer-side live channel in the same change.
- New canonical Tank event types must be added to the JSON Schema first, then
  to the Go and TypeScript contract constants in the same change. The contract
  check is expected to fail until every package agrees.
- Timeline replay and SSE delivery must produce the same reducer state for the
  same canonical event sequence.
- `client_nonce` is the idempotency boundary for user submission. The durable
  store should reject or return the existing event for duplicate nonces.
- Browser connection state is never agent state. Disconnect, reconnecting, and
  resumed are transport states layered beside the conversation projection.
