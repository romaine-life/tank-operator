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
producers, the Postgres `session_events` ledger is the replay source of truth,
NATS JetStream is the durable live work fabric, and React renders a projection
of Tank conversation events. Provider-specific events are inputs to adapters;
they are not UI state.

## Current Architecture

The implementation has explicit durable and live boundaries:

- `claude-runner/src/runner.ts` and `codex-runner/src/runner.ts` publish
  canonical transcript events to the NATS JetStream session bus before the UI
  can observe them.
- `claude-runner/src/sessionEvents.ts` and `codex-runner/src/sessionEvents.ts`
  define canonical Tank event allowlists. All Tank events are durable.
- The backend session-bus persister writes bus events to the Postgres
  `session_events` table and wakes SSE streams only after the ledger write
  commits.
- `backend-go/internal/store/session_events.go` reads `session_events` by
  session and pages by canonical `order_key` for audit/debug and Turn activity
  detail.
- `backend-go/internal/store/session_transcript_rows.go` materializes the
  visible transcript read model from `session_events`. Both `/timeline` reads
  and the per-session browser SSE stream use this projection.
- `frontend/src/App.tsx` renders server-owned transcript rows. Provider raw
  item/tool events stay behind Turn activity detail and debug surfaces.
- The GUI chat path publishes durable SDK commands to NATS JetStream. A future
  provider should map provider output into the stable Tank protocol before
  touching frontend sidebar and chat state logic.

This ADR is the live contract for the app-managed GUI chat path. Changes to
producer, backend, or UI behavior should update this document in the same PR.

## New Event Vocabulary Requires A Consumer Audit

Any PR that adds an event type, a `turn.submitted` `source` value, a terminal
payload field, or a new turn-id shape MUST enumerate every read-side consumer
and state for each whether it consumes the new shape or is intentionally
inert: the transcript projection (`transcript_projection.go`), the transcript
row materializer, the turn pager, chat activity, the Background-activity
screen, read-state cursors, deep links, and the stranded-turn sweep
(`FindStrandedTurns` — any turn shape that legitimately never receives a
terminal under its own id, like the AskUserQuestion question shell, paused
asking turn, and rotated continuation, must be excluded from its candidate
model or the sweep writes false `turn.command_failed` terminals onto it;
this happened in production on the sweep's first day, 2026-06-12). Producer-side completeness is not
turns (a new source, a new no-user-message turn shape, a new terminal flag)
with zero read-model changes; the relay rendered as a standalone turn and the
pending task was invisible at rest until tank-operator#1035 published the
durable `shell_task.*` fold edge and taught `isBackgroundTaskWakeTurnEvent`
the new source. The unwritten invariant "every turn is user-anchored" lived
only in read-side assumptions; this section exists so the next new turn shape
fails loudly at review time instead.

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
claude-runner, and the frontend); the Go stub lives at
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
- `turn.claimed`
- `turn.started`
- `turn.completed`
- `turn.failed`
- `turn.command_failed`
- `turn.interrupt_requested`
- `turn.interrupted`

Context lifecycle:

- `context.compacted`

Item lifecycle:

- `item.started`
- `item.completed`
- `item.failed`

Background shell task lifecycle:

- `shell_task.started`
- `shell_task.updated`
- `shell_task.exited`

AskUserQuestion handoff/answer:

- `turn.awaiting_input`
- `turn.input_answered`

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
- `needs_input`: the agent asked the user a question (AskUserQuestion). In the
  Tank-visible product model, the question set is the assistant response for
  the submitted turn. The provider callback may still be paused internally,
  but the transcript turn boundary is owned by Tank.
- `scheduled`: the agent parked itself with pending time-bound work (a Claude
  The sibling of
  `needs_input` — a non-terminal pause-phase of a live (simulated) turn that
  resumes on the clock/event, not on the user, so it does **not** summon. A turn
  terminal with a pending wake folds here instead of `ready`; the chat-activity
  emitter applies it from the durable wake tables (`HasPending`), not from the
  chat-event stream. It resolves to `streaming` (the wake fired a new turn), to
  `ready` (cancel or a prompt-mid-sleep take-over — a direct `scheduled -> ready`
  that does not ring), or to `error` with `away_error=true` (the wake durably
  failed — publish bounce, dead session, or fire-attempt cap exhausted — a
  broken self-resume that rings the summon). A session that is transiently not
  Active at fire time (a probe blip flipping the row to Pending) defers the
  wake instead of failing it, bounded by the fire-attempt cap.
  See [scheduled-turn-continuity.md](scheduled-turn-continuity.md).
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
2. `turn.submitted` moves the composer to `submitted` and must project a
   visible active Turn activity shell even before provider output exists.
3. `turn.claimed` means the runner accepted the `submit_turn` command and is
   about to feed, or has just fed, the provider SDK. It keeps the UI in the
   submitted/starting state and advances the active shell's durable cursor
   without becoming a turn boundary.
4. `turn.started` means provider output has begun; it moves the composer and
   sidebar to `streaming`.
5. `turn.awaiting_input` records AskUserQuestion as the Tank-visible response
   for the submitted turn and moves the projection to `needs_input`. The
   underlying submit command may stay in flight with runner heartbeats so a
   runner restart can recreate the provider callback before the user answers.
6. `turn.interrupt_requested` moves the projection from `submitted`,
   `streaming`, or `needs_input` to `stopping`; `activeTurnId` is preserved
   because the turn is still mid-flight. A late-arriving request after a
   terminal event records the chip but does not downgrade the terminal
   state.
7. `turn.completed` returns to `ready` (also from `stopping` when the stop
   lost the race to a clean completion).
8. `turn.interrupted` returns to `stopped`.
9. `turn.failed` returns to `error`.

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

The main transcript is promotion-only. User messages, durable session/system
notices, terminal meta rows, and explicitly promoted final-answer assistant rows
belong there. AskUserQuestion promotes a derived
`assistant_message.created` question message on the asking turn; that message
links to the numbered question turn. The Turn question page owns the
interactive answer form for the same durable question set. Answering the form
writes `turn.input_answered` for the question-set state and also writes a normal `user_message.created` plus
`turn.submitted` pair whose client nonce owns the continuation turn. If the
provider callback resumes from the same underlying harness run, the runner
rotates subsequent provider events onto that continuation turn. Provider
activity, reasoning, tool output,
background-task rows, assistant progress notes, provisional assistant prose, and
failure/stop context belong to Turn activity by default. Rows must not visibly
bounce between those surfaces. If an event is eligible for Turn activity, the
server transcript projection classifies it before first paint; the frontend
must not render the event as a full main-transcript activity surface and later
move that same rendered row into Turn activity. Conversely, content shown inside
Turn activity while a turn is active is provisional activity output, not a
settled transcript row being promoted later.

Historical timeline reads return first-class `turn_activity` rows. These rows
load collapsed by default and carry summary metadata only: turn id, activity
counts, compacted child ids, order range, timestamps, status, and error count.
The child entries for a Turn activity row are fetched only when the row is
expanded through the turn activity endpoint. This keeps previous-conversation
navigation bounded while preserving a durable replay path for deep links.

The shell's `active`/terminal status, counts, and `completedAt` are folded from
the **complete** set of a turn's `session_events`, never from a fixed-size
prefix. A turn that accumulates many events — most commonly one that crosses a
`context.compacted` boundary and keeps running — must still report its durable
terminal: the terminal is the last event, and a bounded oldest-first per-turn
read used to drop it, leaving a finished turn rendered as perpetually active.
The turn activity endpoint (`server_turn_activity_v3`) therefore **paginates**
the expansion body: a turn splits into pages sealed at `turnPageEventLimit`
events and at AskUserQuestion boundaries. A `turn.awaiting_input` event starts
a sequence of semantic `question_set` pages, one per question in that tool
invocation, while preserving one durable answer set. Each question page carries
the shared `questionSet` number plus `questionIndex`/`questionCount` metadata,
so the Turns UI can label the set and provide previous/next question shortcuts
through ordinary page selection. The immediately preceding activity page
carries a compact AskUserQuestion invocation marker derived from that same
durable event, so a question-first turn still has an audit page before the
harness-owned question surface. A later matching `turn.input_answered` seals
those question pages before resumed provider activity continues on a normal
activity page. The endpoint
returns the page directory (`page`, `page_count`, `pages[]`) and defaults to the
first pending unanswered `question_set` page while the turn is `needs_input`,
and to the **last** page otherwise (`?page=N` selects another); the Turns view always
shows a page selector (disabled at a single-page boundary) and lets the reader
move between sealed activity pages and question pages. Sealing is a durable
`order_key`-range concept so deep links and reloads are stable. The
endpoint also returns `turn_context`, a server-projected copy of the initiating
instruction for the turn. Human turns source it from the durable
`user_message.created` row. Backend-owned continuations that intentionally do
not create a main-transcript user row, such as background-task wakes, source it
from the durable `turn.submitted.payload.prompt` and mark it system-authored.
This context is not an activity page entry; it gives
`/sessions/{id}/turns/{n}` an orienting prompt header on every selected page
while the canonical user message, when one exists, remains the main transcript
row.
The
`tank_transcript_materialization_invariant_violation_total{invariant="active_shell_after_terminal"}`
counter and `TankTurnActiveWithDurableTerminal` alert guard against a regression
to a window that can't see the terminal.
When a projected shell carries `active: true` or `status: "active"`, that shell
also owns the main transcript's running `...` placeholder. The browser may also
learn the active turn from the session activity summary, but the shell's durable
active flag is sufficient; timeline refresh ordering must not briefly remove
the placeholder while another activity payload catches up.

For an active turn, the server may condense assistant progress notes,
provisional assistant text, tool rows, reasoning blocks, background-task rows,
and meta rows into a single Turn activity disclosure row as they arrive. A
normal assistant message does not by itself declare that it is the final answer.

For a turn that ended with `turn.completed`, `payload.final_answer.timeline_ids`
is the durable final-answer fact. Those Tank timeline IDs are rendered in the
main transcript as the settled assistant response. Every other non-user row for
that completed turn remains activity/log material. The projection must not infer
finality from provider ordering, adjacency, or a trailing assistant message/run.
If a completed turn has assistant prose but no final-answer marker, the prose is
kept in Turn activity rather than promoted to a settled transcript message.

Failed turns, interrupted turns, and turns that never produce a successful
`turn.completed` final-answer marker do not have settled assistant responses.
Their non-user rows remain Turn activity material. The main transcript may show
the user message and terminal meta context, while the Turn activity row may
default open or carry failure/stop summary metadata so context is visible
without dumping child activity rows into the settled conversation surface.

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

### Context Compaction Notice

Context compaction — the provider summarizing earlier conversation to reclaim
context-window space — is a durable `context.compacted` event, not a silent
provider-internal action. This is distinct from *Transcript Compaction* above:
that folds a turn's activity rows into a collapsed shell for display; this
records that the agent's working memory of earlier turns was condensed.

`context.compacted` is `actor=runner`, `source` is the provider that compacted
(`claude` or `codex`), and the payload carries `trigger` (`auto` when the
context filled, `manual` for an explicit `/compact`) plus optional `pre_tokens`
(the token count before compaction). When a provider does not expose trigger or
token metadata, the runner still emits the durable notice and defaults
`trigger` to `auto` rather than inventing unsupported metadata. The server
projection (`applyContextCompacted`) records it as an ordinary mid-turn
Turn-activity row (`meta`, `metaKind: context_compacted`): compaction is
intra-turn system noise — the same tier as tool calls and reasoning, not part
of the settled conversation a reader scans — so it is folded into the turn's
collapsed activity shell like any other non-final-answer row and is absent from
the settled transcript, surfacing only when the Turn-activity disclosure is
opened. It is rendered there through the existing `RunMetaBlock` primitive.
AskUserQuestion is also intra-turn state, but it is not merely another inline
activity child: the turn-page projection gives it a semantic `question_set`
page. The main transcript renders the durable button to that page; the Turns
question page is where the user answers and reviews the set.

This placement is what the Transcript contract's no-bounce invariant requires
(*"compactable activity must not be rendered first as a settled transcript row
and later relocated into Turn activity"*). An earlier implementation instead
promoted compaction into the settled transcript and excluded it from the
activity compact; on the per-turn detail screen that produced a flash-then-
vanish, because the row showed before the turn's activity children loaded and
was dropped once they did. Recording it as a normal activity child removes that
bounce by construction.

A provider event the runner adapter neither maps to a Tank event nor explicitly
ignores increments `tank_runner_unmapped_provider_event_total{type,subtype}`
(`claude-runner/src/runner.ts` → `logUnhandledSdkMessage`). This counter is the
backstop for the silent-drop class that hid compaction in the first place:
`compact_boundary` used to fall through to `return []` with no durable event and
no metric. Steady state is zero; a spike names the next provider event to map or
explicitly ignore — the discipline this protocol's migration guardrails require.

## Provider Mappings

Claude SDK adapter:

| Provider event | Tank event | Notes |
| --- | --- | --- |
| JetStream `submit_turn` command | `user_message.created`, `turn.submitted` | Backend publishes these events at the submit boundary; runner duplicate publishes are deduped by event id. `client_nonce` is required. |
| Runner accepts `submit_turn` | `turn.claimed` | Runner-side progress marker. The backend `turn.submitted` row already owns the open boundary; `turn.claimed` exists so pre-provider stalls are visible and measurable. |
| First SDK output for a turn | `turn.started` | Current Claude SDK stream does not always expose a clean turn marker; adapter may synthesize this after the durable user message. |
| `assistant` text block | `item.completed` | `actor=assistant`, item kind `message`; tool-use blocks become tool items. |
| `assistant` tool_use block | `item.started` | `actor=tool`; include tool name/input in payload. |
| `user` tool_result block | `item.completed` | Completes the matching tool item by `timeline_id`; `is_error=true` maps to `payload.outcome.kind="result_failed"`, not `item.failed`. |
| `system/task_started` | `shell_task.started` | `actor=tool`; `task_id` identifies the background shell task. The runner records task ownership so later notifications still attach to the spawning turn. |
| `system/task_progress`, `system/task_updated` | `shell_task.updated` | Progress/status snapshots for an already-owned background task. |
| `system/task_notification` terminal status | `shell_task.exited` | Terminal background task result (`completed`, `failed`, `stopped`, etc.) without changing session run status. |
| `result` success | `turn.completed` | Include usage when present. Include `payload.final_answer` when the turn emitted a final assistant text item. |
| `result` error | `turn.failed` | Provider error, not user interrupt. |
| SDK interrupt acknowledgement | `turn.interrupted` | Must not render as provider error. |
| `system/compact_boundary` | `context.compacted` | `actor=runner`, `source=claude`. Carries `payload.trigger` (`auto`/`manual`) and optional `pre_tokens` from the SDK's `compact_metadata`. Promoted to the main transcript as a system notice. |
| `system/init` | ignored | Session-init metadata, not a transcript event. Any OTHER unmapped `system/*` subtype increments `tank_runner_unmapped_provider_event_total` rather than vanishing silently. |
| `stream_event`, status, hooks, plugin changes | ignored | Per-token deltas are not on the Tank surface; restoring requires re-adding `item.delta` + `live-only` together. |

Codex SDK adapter:

| Provider event | Tank event | Notes |
| --- | --- | --- |
| JetStream `submit_turn` command | `user_message.created`, `turn.submitted` | Backend publishes these events at the submit boundary; runner duplicate publishes are deduped by event id. `client_nonce` is required. |
| `turn.started` | `turn.started` | Preserve provider turn id when available. |
| `item.started` | `item.started` | Tool-like items drive active item state. |
| `item.updated` | ignored (no Tank event) | Adapter still observes ordinary frames so `item.completed` can fall back to the last running text; no Tank event reaches the bus. Codex unified-exec background terminal updates are the exception and map to `shell_task.updated`. |
| `thread/compacted` or `contextCompaction` item | `context.compacted` | `actor=runner`, `source=codex`. `thread/compacted` is the Codex App Server notification shape generated from `@openai/codex@0.130.0`; it carries `threadId` and provider `turnId`. The generated protocol marks it deprecated in favor of a `contextCompaction` thread item, so the transport maps both surfaces and dedupes by provider turn id. Codex does not expose trigger or pre-token metadata here, so the payload defaults to `trigger="auto"`. |
| `userMessage` item echo | ignored (no Tank event) | Tank owns submitted user input through the backend-published `user_message.created` event. Provider echoes of that input must not enter the durable item stream or render as tool calls. |
| `item.completed` message/reasoning/tool | `item.completed` or `item.failed` | Map command, file change, MCP, and web search to tool item payloads. Nonzero exit codes and provider status `failed` with no execution error map to `payload.outcome.kind="result_failed"`. A non-null provider item error maps to `item.failed` with `outcome.kind="execution_failed"`. |
| `commandExecution` with `source=unifiedExecStartup` or `source=unifiedExecInteraction` | `shell_task.started`, `shell_task.updated`, `shell_task.exited` | Codex App Server background terminals are session-owned processes. `processId` is the preferred `task_id`; `thread/backgroundTerminals/clean` is the explicit action that stops them. |
| `turn.completed` | `turn.completed` | Include usage. Include `payload.final_answer` when the turn emitted a final assistant message item. |
| `turn.completed` with provider status `interrupted` | `turn.interrupted` | Codex App Server documents `turn/interrupt` as cancelling the active turn without terminating background terminals. |
| `turn.failed` or `error` | `turn.failed` | Unless adapter classifies it as abort/interrupt. |
| Abort from user interrupt | `turn.interrupted` | Distinct from provider failure. |


| Provider transcript step | Tank event | Notes |
| --- | --- | --- |
| JetStream `submit_turn` command | `user_message.created`, `turn.submitted` | Backend publishes these events at the submit boundary; runner duplicate publishes are deduped by event id. `client_nonce` is required. |
| `source=USER_EXPLICIT` | ignored | Tank owns the durable user row. Provider echoes must not enter the transcript. |
| `source=SYSTEM` history/context steps | ignored | Injected context is not a user-visible transcript item. |
| `source=MODEL` tool result step with `status=ERROR` | `item.failed` | Tool execution failure is item-scoped and does not fail the whole session. |
| `source=MODEL`, `type=PLANNER_RESPONSE` prose without tool calls | `item.completed` | Assistant message item; the latest such message is the final-answer candidate for `turn.completed`. |
| User interrupt / runner shutdown | `turn.interrupted` | Distinct from provider failure. |

## Backend API Sketch

History reads:

- Normal navigation opens the live tail:
  `GET /api/sessions/{session_id}/timeline?anchor=newest&rows=24`
- Explicit message links open a bounded page around a durable transcript
  identity:
  `GET /api/sessions/{session_id}/timeline?timeline_id=<timeline_id>&rows_before=12&rows_after=12`
- Public copied-message shares use an opaque bearer token and the same
  transcript-row read model:
  `GET /api/public/message-links/{token}/timeline?timeline_id=<timeline_id>&rows_before=12&rows_after=12`
- Manual upward pagination reads older transcript rows:
  `GET /api/sessions/{session_id}/timeline?before_cursor=<row_cursor>&rows=8`
- `/timeline` pages `session_transcript_rows`, the server-owned visible
  transcript read model. Raw `session_events` remain the Turn activity detail
  and audit source, but raw event counts are not a `/timeline` or main
  transcript live-stream API contract.
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
  "rows": [],
  "prev_cursor": "opaque-row-cursor",
  "next_cursor": "opaque-row-cursor",
  "found_oldest": false,
  "found_newest": true,
  "live_order_key": "001...",
  "cursor_semantic": "transcript_row",
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
read has established `live_order_key` for the SSE resume cursor. Browser-local
scroll position is not a supported timeline anchor; reopening or switching
sessions uses `anchor=newest` unless the URL carries an explicit
`message`/`timeline_id` target.

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
first visible transcript row is the user's launch message; the durable startup
status folds into that turn's Turn activity (it is not a separate main-transcript
row), and runner output follows.

Attachment-backed SDK launches set `initial_turn.deferred=true`. The create
request still writes `user_message.created` before startup status, using the
user's text as the durable display text and `payload.attachments` as structured
transcript metadata. After the pod is
ready and files are uploaded into the workspace, the SPA submits the same
`client_nonce` to `POST /api/sessions/{session_id}/turns` with
`existing_user_message=true`; the backend writes only `turn.submitted` and
publishes the runnable command whose prompt contains the pod-local attachment
paths. This preserves one user bubble and one turn id while keeping file bytes
on the existing workspace upload path.

This deferred second step is browser-driven, so a tab that goes away after
create (close, reload, navigation, a dropped upload) can leave the turn durably
recorded as `user_message.created` with no `turn.submitted` ever published — a
silently stranded launch that the runner waits on forever. The orchestrator's
stranded-launch sweep (`cmd/tank-operator/stranded_launch_sweep.go`, backed by
`store.FindStrandedLaunchTurns`) is the durable backstop: it periodically finds
launch turns whose `turn_id` carries only `user_message.created` and, once past
a generous age floor, emits a durable `turn.command_failed` so the SPA renders
the launch as failed instead of hanging. It does not re-dispatch — the file
bytes only ever existed in the originating browser — so surfacing the failure
is the terminal. `turn.command_failed` is itself terminal, so a late browser
dispatch is dropped by the runner's already-terminal guard; the deterministic
`event_id` collapses concurrent emits across replicas.

Durable SDK turn submission:

`POST /api/sessions/{session_id}/turns`

Body:

```json
{
  "client_nonce": "turn_abc123",
  "prompt": "Compare these\n\nAttachments:\n- /workspace/screenshots/1.png",
  "display_text": "Compare these",
  "display_attachments": [
    {
      "label": "Screenshot 1",
      "name": "image.png",
      "kind": "image",
      "path": "screenshots/1.png",
      "abs_path": "/workspace/screenshots/1.png",
      "size": 12345
    }
  ],
  "model": "claude-sonnet-4-6",
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
`prompt` is the executable runner input. `display_text` is optional and, when
present, is the durable `user_message.created` transcript text.
`display_attachments` is optional structured display metadata for user
attachments. The SPA uses this split for attachment-backed follow-up turns: the
user-visible row renders attachments as transcript UI, while the runner prompt
carries workspace paths.
When `existing_user_message=true`, the user row must already have been written
by the launch-time create boundary, so this endpoint writes `turn.submitted`
only.
Command ack happens only after the corresponding durable terminal event is
published. Provider self-scheduled wakeups are backend-owned durable state:
runner extracts `schedule` tool calls, then registers them through
`POST /api/internal/sessions/{session_id}/scheduled-wakeups` with the provider
item id as the idempotency key. The orchestrator claims due rows from
`session_scheduled_wakeups`, writes the normal `user_message.created` and
`turn.submitted` boundary events, then publishes a normal `submit_turn` command
with `source=schedule-wakeup`. The synthetic `user_message.created` row is
authored by the session's system identity and carries
`payload.source=schedule-wakeup`. Its visible text is the timer-fired
announcement; the original wake prompt stays on `payload.prompt` and
`turn.submitted.payload.prompt` so the UI can key the announcement while the
agent still receives the requested continuation prompt. Every scheduled-wakeup
lifecycle transition also persists `scheduled_wakeup.updated` with
`actor=system`, `source=tank`, `timeline_id=scheduled-wakeup:{wakeup_id}`, and
the stable `client_nonce`. The timeline bootstrap includes
`scheduled_background_tasks` projected from durable rows, and the session event
stream carries later projections through `transcript-rows`, so the SPA can keep
Background -> Scheduled current without polling `GET
/api/sessions/{session_id}/scheduled-wakeups`. A scheduled continuation is
visually confirmable even when its original tool row has scrolled out of the
loaded transcript window.

A Claude background task (`run_in_background`) that finishes while the session
has no active turn is resumed the same backend-owned way. A task-lifecycle SDK
frame never starts a turn, so without this the base Bash tool's "re-invokes you
when it exits" follow-up is silently stranded once the launching turn has ended.
The claude-runner registers the natural terminal (`completed`/`failed`/`exited` —
never a user-cancelled `stopped`/`cancelled`) through
`POST /api/internal/sessions/{session_id}/background-task-wakes`. The task id is
the idempotency key: the durable row `session_background_task_wakes` is keyed by
`sha256(tank_session_id, provider, task_id)`, so an SDK frame repeat or a runner
restart cannot double-wake. The orchestrator claims due rows and — unlike a
scheduled wakeup — re-checks session liveness before firing: it skips and
retries while the session is awaiting an AskUserQuestion answer (`needs_input`),
so the wake never clobbers a pending question; it fails the wake durably if the
session is no longer `Active`; otherwise it writes a normal `turn.submitted`
boundary event without a synthetic `user_message.created` prompt and publishes
`submit_turn` with `source=background-task`. The `turn.submitted` event remains
`source=tank` per the event schema and carries `payload.source=background-task`
as provenance plus `payload.prompt` as the existing system-authored wake text
for Turn activity projection and Turn-page prompt context. The wake turn is a
continuation mechanic inside the
simulated turn: its activity shell stays in the Turns view, not the settled main
transcript, and its wake prompt is folded into the originating Turn activity as
an `authorKind=system` user-side message. The earlier turn that parked on the still
running background task is also continuation material: its assistant prose and
background-task row stay in Turn activity rather than becoming a main transcript
answer. If the resumed agent reaches a true final answer, that assistant prose
is promoted only through the normal
`turn.completed.payload.final_answer.timeline_ids` marker. Because each enqueue stamps a fresh
`order_key` (`event_id` is indexed, not unique, so separate enqueues do not
collapse), the durable wake row — not the event ledger — is what makes the wake
fire exactly once.

The UI consumes durable transcript delivery from
`GET /api/sessions/{session_id}/events`. The stream emits `transcript-rows`
SSE events whose payload is `{order_key, rows}` from `session_transcript_rows`;
raw `item.*` and tool events are not sent to the main transcript pane. SSE
event ids are canonical `order_key` values and `Last-Event-ID` is the resume
cursor. Unknown cursors produce `resync_required`; clients reload `/timeline`
instead of silently skipping a gap. Open SSE streams do not poll any side
endpoint for indicator state. Because browser-native EventSource cannot attach an
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
`target_cursor`, and a bounded `rows` window around the persisted row cursor.
When the URL includes a minted `share=<token>` parameter, the JSON contract
also names `public_api.timeline_url`; that route resolves a read-only transcript
window without Tank authentication, gated by the opaque share token rather than
the guessable session/message query pair. Browser navigation to such a link
renders the public transcript surface without the authenticated sidebar,
composer, Files, Settings, or Background controls.
`sessions.visible=false` is a sidebar tombstone, not a transcript-retention or
access-control boundary: owned/admin transcript reads continue to resolve as
long as the durable session row and transcript ledger remain in Postgres.
The JSON contract carries an `agent_recipe` array with copyable curl commands:
send the projected service-account token to auth.romaine.life as
`Authorization: Bearer <token>`, exchange the returned `auth_jwt` at this Tank
origin, fetch the `json_url`, and page older context with
`before_cursor=<prev_cursor>` when `found_oldest=false`.

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
   (`tank.cmd.<scope-token>.<session-token>.control.<provider>`), not the command subject
   used for `submit_turn`. Runners consume the command
   from a dedicated control-plane JetStream consumer (separate
   `durable_name`, separate `filter_subject`, higher `max_ack_pending`)
   and abort the matching active turn from inside the session pod.

The data plane (`tank.cmd.<scope-token>.<session-token>.commands.<provider>`) and the
control plane (`tank.cmd.<scope-token>.<session-token>.control.<provider>`) are
deliberately separate JetStream subjects with separate durable consumers,
and both live on the dedicated `TANK_SESSION_COMMANDS` WorkQueue stream
(issue #1076 item 2): events stay on the `tank.session.>` bus stream, so a
flood session's events can never evict another session's undelivered
commands, and command-stream limit pressure REJECTS new publishes
(`DiscardNew` — a loud `turn.command_failed` at the submit boundary)
instead of silently evicting. During the cutover the backend dual-publishes
every command to the legacy `tank.session.…` command subjects too, so
pre-split session pods' durable consumers keep a byte-identical wire until
those pods age out.
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
  arm in `conversationReducer.ts`. Both runners also consult the
  durable ledger before the `orphaned` arm writes its synthetic
  terminal, and stale buffered interrupts cleared after their turn
  ended drain to this bucket — every accepted interrupt still lands in
  exactly one bucket (issue #1078).
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

### Stop while AskUserQuestion is pending

While a question is pending, the activity fold points
`active_turn_id` at the QUESTION turn, so the SPA's Stop targets an id
no `PendingTurn`/`AcceptedTurn` carries. Both runners resolve question
identifiers (`question_turn_id` / question client-nonce) back to the
ASKING turn (issue #1078 item 2). The interrupt then:

1. settles the pending provider callback without an answer (Claude:
   `isError` tool result; Codex: empty `answers` — a rejection would be
   an unhandled rejection in the app-server transport), so the provider
   pause unwinds instead of wedging until pod restart;
2. publishes `turn.interrupted{reason:"question_dismissed_by_stop"}` on
   the QUESTION turn — `turnAwaitingQuestionTarget` treats any terminal
   as not-awaiting, so the card stops accepting answers at the backend
   boundary (the answer POST 409s);
3. interrupts the asking turn through the normal arm above.

`tank_runner_ask_user_question_dismissed_total` counts dismissals.

### Durable answers across runner restarts

An `input_reply` that arrives while no question pause is registered
(runner restart: the redelivered `submit_turn` replays the turn for
minutes before the provider re-asks with a NEW item id) PARKS under a
JetStream heartbeat instead of nak-looping away the control plane's
`max_deliver` budget (issue #1078 item 3). When a pause (re)registers,
parked answers drain into it; matching falls back from the exact
(turn, timeline, item) key to the stable ASKING turn id, and a
fallback-matched re-asked shell closes with
`turn.interrupted{reason:"superseded_by_answer"}`. Unmatched parks
expire to a durable command failure after
`SESSION_PARKED_INPUT_REPLY_MS` (default 15m).
`tank_runner_input_reply_recovery_total{path}` counts every non-exact
path.

### Natural-terminal publish exhaustion parks for redelivery

A turn terminal whose publish exhausts retries no longer wedges the
data plane (issue #1078 item 1 — pre-fix the `working()` heartbeat
extended the un-acked `submit_turn` forever and `max_ack_pending=1`
silently blocked every later turn). The runner parks the terminal
event on the turn, stops the heartbeat, and NAKs the command;
JetStream redelivery retries the PUBLISH through the redelivery
reattach path — never the prompt. Redelivery of a still-running turn
reattaches the fresh delivery for the same reason (in-memory dedup by
turn identity, including pre-rotation identities of AskUserQuestion
continuations). `tank_runner_terminal_publish_deferred_total` counts
parks.

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

AskUserQuestion pauses the same turn (`turn.awaiting_input`):

When the in-pod agent invokes the AskUserQuestion tool, the asking turn
pauses while waiting for user input. The runner captures the Tank-canonical
questions and publishes durable `turn.awaiting_input` **on a freshly minted
question turn** (the JSON example previously showed the asking turn here —
that was never the wire shape):

```json
{
  "type": "turn.awaiting_input",
  "actor": "runner",
  "source": "claude",
  "turn_id": "<question turn: turn_question-<hash>>",
  "client_nonce": "question-<hash>",
  "payload": {
    "asking_turn_id": "<asking turn>",
    "question_turn_id": "<question turn>",
    "questions": [ { "question": "Which auth method should we use?", "...": "Tank-canonical shape" } ],
    "provider_item_id": "toolu_...",
    "timeline_id": "item_..."
  }
}
```

### Question turns are first-class numbered turns (frontend-facing contract)

Every AskUserQuestion handoff mints a UNIQUE question turn:
`questionClientNonce(askingTurnID, providerTimelineID)` →
`turn_question-<hash>` (deterministic per asking turn × provider item, so a
redelivered handoff re-derives the same id, while a provider re-ask — new
item id — mints a new one). The runner publishes that turn's own
`turn.submitted` followed by `turn.awaiting_input`; the durable
turn-number allocator numbers it like any user-visible turn (only
`turn_bgtask-` continuations are excluded — migration 0139), so the
question turn is navigable at `/sessions/{id}/turns/{n}` and renders its
own `question_set` page. While the question is pending, the activity fold
points `active_turn_id` at the QUESTION turn (this is what the SPA's Stop
targets). The ASKING turn separately records
`turn.awaiting_input.invocation` plus the derived
`assistant_message.created` question card; the main transcript shows only
that card, with an affordance to open the question set.

`turn.awaiting_input` is not a turn terminal, but the question turn's
lifecycle is closed — it always ends in exactly one of:

| terminal on the question turn | meaning | written by |
|---|---|---|
| `turn.input_answered` | the user answered; the answer also becomes the user's continuation turn (`answer-<hash>` nonce) | backend answer handler (`actor=user source=tank`) |
| `turn.interrupted{reason:"question_dismissed_by_stop"}` | the user clicked Stop instead of answering; the runner settled the provider pause without an answer | runner (issue #1078 item 2) |
| `turn.interrupted{reason:"superseded_by_answer"}` | a restart re-ask was resolved by the durable answer to the ORIGINAL card; this re-asked shell closes unanswered | runner (issue #1078 item 3) |

Any terminal on the question turn makes the backend's
`turnAwaitingQuestionTarget` reject further answers (HTTP 409 "question
turn is not awaiting input") — the durable ledger, not the SPA's render
state, is the answerability boundary. Known render edge: an already-open
tab keeps showing a dismissed card as answerable until the projection
refreshes, because the awaiting card's `answered` flag flips without an
`end_order_key` advance — tracked as issue #1077 item 4. The stranded-turn
sweep excludes awaiting-input turns (#1069), so an un-terminated question
turn is never false-failed; the terminals above exist so question turns
do not sit open forever in the first place.

- **Claude**: the runner exposes a Tank-owned SDK MCP server named `tank` and
  aliases provider `AskUserQuestion` calls to `mcp__tank__AskUserQuestion`.
  The MCP tool input accepts either the canonical `questions` array or a
  top-level `{question, options, ...}` shorthand for a single question. The
  runner normalizes both forms into the durable Tank conversation protocol's
  `questions[]` payload. The MCP handler publishes `turn.awaiting_input` and
  keeps that tool call pending. When `input_reply` arrives, the runner resolves
  the MCP call with the user's answers so the provider turn continues; any image
  the user attached to the answer is read from `/workspace` and returned as an
  inline image content block on that tool result (see `display_attachments`
  below). Claude SDK permissions run in bypass mode; AskUserQuestion is not
  implemented through permission interception.
- **Codex** (`codex_gui`): on the App Server's `item/tool/requestUserInput`,
  the runner publishes `turn.awaiting_input`, keeps the JSON-RPC request
  pending, then responds with the submitted answers when `input_reply` arrives.
- Historical `codex_app_server` rows used the same App Server AskUserQuestion
  path, but that mode is retired for create-time use; new sessions must use
  `codex_gui`.
- `codex_exec_gui` never produces `turn.awaiting_input` — the `codex exec`
  transport rejects `request_user_input`, and the mode is retired for
  create-time use.

The user's answer resumes the same turn:

`POST /api/sessions/{session_id}/turns/{turn_id}/answer` — `{turn_id}` is the
asking (awaiting-input) turn.

Body:

```json
{
  "provider_item_id": "toolu_...",
  "timeline_id": "item_...",
  "answers": { "Which auth method should we use?": ["OAuth"] },
  "annotations": { "Which auth method should we use?": { "notes": "matches the existing IdP" } },
  "display_attachments": [
    { "label": "Screenshot 1", "name": "screenshot.png", "kind": "image",
      "path": "screenshots/3.png", "abs_path": "/workspace/screenshots/3.png", "size": 40213 }
  ]
}
```

The handler validates ownership/mode/target ids, then requires the asking turn
to have a matching `turn.awaiting_input` event and no final terminal
(`turn.completed`, `turn.failed`, `turn.command_failed`, or `turn.interrupted`).
It persists durable `turn.input_answered` with a deterministic `client_nonce`
so repeated submits dedupe at the `(tank_session_id, event_id)` unique
constraint, then publishes a durable `input_reply` command on the control-plane
subject. If command publish fails after the answer event is persisted, the
deterministic command id lets a retry republish the same reply.

`answers` is `Record<questionText, answerLabel[]>` — always an array so single-
and multi-select share one shape. `annotations` is optional
`Record<questionText, { preview?, notes? }>` carrying free-text the user
attached. A free-form-only answer uses the synthetic label `Other` to keep the
answer map non-empty, but runners deliver the attached `notes` to the provider
instead of the synthetic label. When the user selects a real option and also
adds notes, runners deliver both the selected label and the notes. The question
page's answered state is derived durably — the projection
marks it answered by finding a later `turn.input_answered` event whose
`payload.question_timeline_id` matches the question — never from browser-local
optimism.

`display_attachments` is optional and carries any files the user attached to
the answer (a pasted screenshot is the main case), the same shape and upload
plumbing as a normal turn's `display_attachments` — the SPA uploads bytes to
`/files/upload` (landing at `/workspace/screenshots/…`) and posts only the
attachment metadata here. The handler normalizes it and threads it into all
three answer sinks: the durable `turn.input_answered` payload (`attachments`),
the Tank-visible `user_message.created` continuation turn (so the transcript
renders the attachment chip on the answer bubble), and the `input_reply`
command's `attachments`. The bytes never travel the bus — only path metadata
does, and the runner reads the file from the shared `/workspace` at delivery.
The Claude runner returns an image as an inline image content block in the
resolved AskUserQuestion tool result (a non-image as a path line, a
missing/oversize/unreadable image as a visible note) so the screenshot is in
the model's context, not silently dropped. `tank_runner_input_reply_attachment_total{kind,result}`
counts the outcome.

Tank-canonical AskUserQuestion question shape:

Both runner adapters normalize the provider's question payload into a single
Tank-canonical shape before publishing it in the `turn.awaiting_input`
`questions` payload. The frontend renders the Tank shape only —
provider-specific fields never reach the renderer directly. Per
[docs/product-inspirations.md](product-inspirations.md): "Provider-specific
event streams are adapter inputs. The frontend renders the Tank conversation
protocol, not raw provider wire formats."

```json
{
  "question":      "Which auth method should we use?",
  "header":        "Auth method",
  "multiSelect":   false,
  "options":       [{ "label": "OAuth", "description": "...", "preview": "..." }],
  "allowFreeForm": true,
  "secret":        false
}
```

- `allowFreeForm` — when true, the question page surfaces an always-on
  textarea for a "say something else" reply, and submit accepts text in lieu
  of an option pick. Claude SDK questions: always `true` (mirrors Claude
  Code's host-UI "Other" affordance). Codex app-server questions: mirrors the
  raw `isOther` flag.
- `secret` — when true, the textarea/input disables spell-check and
  autocomplete. Codex app-server `isSecret` maps here; the Claude SDK has no
  secret-input primitive today and always emits `secret: false`.
- `options` — empty array allowed (codex permits pure free-form questions
  with `options=null`; the codex adapter maps that to `[]`).
- `multiSelect` — codex has no multi-select primitive today; codex adapter
  emits `false`. If codex grows one, route the flag through the adapter so
  Tank's shape stays the single rendered authority.

Older durable rows (pre-cutover Claude events, pre-cutover codex events)
do not carry `allowFreeForm` / `secret`; the renderer treats absent fields
as `false`. Per [docs/migration-policy.md](migration-policy.md) -> "Old
data does not justify runtime support," there is no runtime fallback that
infers free-form support from the absence of those fields. A future
backfill projects the existing durable rows into the canonical shape; the
runtime read path stays Tank-shape-only.

AskUserQuestion question set owns a Turn page:

The transcript projection in `backend-go/cmd/tank-operator/transcript_projection.go`
emits one `metaKind: "awaiting_input"` meta row per `turn.awaiting_input`
pause, anchored at the asking turn's tail (`orderKey` + `~awaiting_input`
suffix). The row carries an `awaitingInput` payload — `askingTurnId`,
`providerItemId`, `timelineId`, `questions`, `questionSet`, `questionIndex`,
`questionCount`, `answered`, `answers`, and `annotations` — sourced entirely
from durable state (`answered` is true once a later `turn.input_answered` event
references the question). The
turn page splitter projects a compact `AskUserQuestion` tool marker onto the
preceding activity page and starts one semantic `question_set` page per
question at that row. Those pages share the same durable `awaitingInput`
payload, expose a shared set number with per-page question position, and keep
one set-level answer draft; the Submit action posts the whole answer set once
every question page has a response. The marker is sourced from the same durable
pause, not from provider-specific raw tool rows. The SPA renders the
interactive answer surface from the durable row in the Turns question pages;
the main transcript renders the same durable row as an assistant handoff
message. That whole row navigates to the pending question page, because
`turn.awaiting_input` is the point where the assistant has stopped speaking and
handed communication back to the user. This is a conversation-projection
boundary, not a runner terminal: the same provider turn still resumes through
`/answer`.
Submitting the form posts `/answer`, which resumes the same active turn.

The Turn-activity placement follows
[docs/features/transcript/contract.md](features/transcript/contract.md):
AskUserQuestion is part of the provider turn's work loop, and the user's answer
continues that same turn rather than becoming settled chat content.

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
- Keep the Postgres `session_transcript_rows` table as the `/timeline` read
  model and main transcript SSE source, keyed by opaque transcript row cursors
  and refreshed from `session_events` on each durable event write.
- Mark completed transcript-row backfills in
  `session_transcript_row_backfills`. Migration-created status rows are
  visible transcript rows, but they are not proof that historical turns have
  been projected. Projection-version catch-up is per requested session at
  `/timeline` and transcript SSE open, under the session materialization lock;
  serving pods must not run fleet-wide transcript-row backfills at startup.
- Use NATS JetStream as the durable command/event fabric, not as the product
  history database. Commands are acked only after durable terminal events are
  published; events are acknowledged by the backend persister after the
  Postgres upsert succeeds.
- Add or materialize per-user read state keyed by `email + session_id`.
- Use `order_key` as the live stream cursor. Use transcript row cursors as the
  history pagination cursor. Document id is only a dedupe key.
- Compute unread and attention state server-side from durable events plus read
  state.

Operational counters:

- `/metrics` exposes the `tank_session_event_stream_*` counters
  (opens, reconnects, `resync_required`, stream errors, timeline read
  failures, emitted projected rows, heartbeats, wake-subscribe failures) and the
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
- Timeline replay and per-session SSE delivery must consume the same
  `session_transcript_rows` projection. The main transcript frontend must not
  reduce raw provider/Tank item events as a parallel runtime path.
- `client_nonce` is the idempotency boundary for user submission. The durable
  store should reject or return the existing event for duplicate nonces.
- Browser connection state is never agent state. Disconnect, reconnecting, and
  resumed are transport states layered beside the conversation projection.
