# Transcript Contract

This contract applies to the GUI chat transcript ledger: the first prompt,
startup status, provider output, tool events, and live delivery into the
visible conversation. Viewport anchoring, deep links, historical pagination,
read position, and live-tail navigation are covered by
[Transcript Navigation](../transcript-navigation/contract.md).

## Product Model

The transcript is a durable conversation ledger with a live tail. It should
feel like a mature messaging surface: refresh may recover from a broken browser
state, but refresh must not be required for normal progress.

The browser DOM is not the source of truth. Provider streams are adapter input,
not the rendered protocol. The frontend renders the Tank conversation protocol
from durable events.

Compacted agent activity is a server-owned projection of the same durable
transcript ledger, not a second ledger. The Turn activity row is an activity/log
surface; the main transcript is the settled conversation surface. The main
transcript is promotion-only: provider activity, reasoning, tool output, and
provisional assistant prose must not default into it. Historical timeline reads
return Turn activity as primary collapsed rows, with child entries loaded on
expansion. The UI may duplicate assistant prose across those projections only
when a successful terminal event explicitly marks that prose as the final
answer; it must not visibly move a rendered row from one surface to the other.

## Sources Of Truth

- `session_events` owns transcript entries and ordering.
- `order_key` owns transcript order and cursor movement.
- `session.status` events own session lifecycle. Plain startup notices
  (`Session is loading.` / `Session is ready.`) are turn noise folded into the
  owning turn's Turn activity, not main-transcript rows; provider credential
  banners (`.../provider/.../status`, including the recovery "back online"
  ready) and any `failed` status stay promoted as top-level system messages.
- The Tank conversation protocol owns the projection rules for Turn activity
  versus settled transcript messages.
- A `turn_activity` shell carries the durable `turnNumber` stamped from
  `session_turns` during materialization. It is a read-only projection of the
  number, not a second source of truth; the number's owner is the
  [Transcript Navigation](../transcript-navigation/contract.md) contract.
- `turn.completed.payload.final_answer.timeline_ids` is the only durable fact
  that promotes assistant prose from activity/log material into a settled
  main-transcript assistant response.
- Provider SDK events are inputs that must be converted to Tank events before
  the UI depends on them.
- SSE is a live follower of durable events, not the transcript store.

## Migration Rules

- Browser-local startup drafts are not transcript rows.
- Client-only "loading" or "continuing" transcript placeholders are prohibited
  when a durable event exists or can be written.
- A first prompt typed on the splash screen must be written durably before
  startup status events.
- Service-created GUI chat sessions must include and durably write an initial
  prompt as part of creation. A promptless GUI session is invalid; there is no
  debug, operator, or pod-boot-smoke exception. CLI/config sessions may exist
  without a chat transcript because they are different products, not empty GUI
  transcripts.
- Old provider-specific transcript render paths must be deleted when replaced
  by Tank protocol rendering.
- Refresh-only recovery must not be accepted as proof that live transcript
  delivery works.
- Compactable activity must not be rendered first as a settled transcript row
  and later relocated into Turn activity.
- Previous-conversation loads must consume the server transcript projection;
  raw timeline events are not the authority for historical Turn activity
  grouping.

## Live Behavior

- A new transcript begins with the user's first durable message when the user
  provided one.
- Startup notices (`session.status` `loading`/`ready`) are durable, but they are
  not main-transcript rows: the server projection folds them into the owning
  turn's Turn activity — the turn whose `order_key` epoch contains them, with a
  notice preceding the first user message owned by that first turn. A startup
  notice with no owning turn produces no transcript row. They surface in the
  Turns view, not the conversation.
- An already-open transcript client must receive and render post-cursor durable
  events without reload.
- The live stream must keep draining persisted events until caught up whenever
  it wakes, reconnects, heartbeats, or resumes from visibility changes.
- A reconnect from an unknown cursor must trigger explicit resync instead of
  silently skipping a gap.
- Ready/load transitions must not reset, reorder, or replace the transcript.
- Active-turn assistant prose is provisional until a successful terminal event
  carries an explicit durable final-answer marker. The server projection uses
  `turn.completed.payload.final_answer.timeline_ids` as the only final-answer
  source; it must not infer finality from a trailing assistant message/run.
- Background-task wake turns are continuation mechanics, not standalone chat
  turns. The backend must not persist the wake prompt as a main-transcript
  `user_message.created` row; it carries the text on the backend-owned
  `turn.submitted.payload.prompt` so the server projection can render the same
  `authorKind=system` user-side message inside Turn activity. When the
  originating turn is derivable, the projection keeps the wake turn's activity
  shell out of the settled transcript and folds its body — in durable
  order-key order — into the originating turn's shell; a wake turn whose
  lineage cannot be derived keeps its own shell instead, because projected
  content must never be dropped without a surviving container. Wake
  activity remains inspectable in the Turns view as part of the originating turn,
  not as a second user-visible turn. If the wake-chain reaches a true final answer, that
  assistant prose may enter the main transcript only through the same explicit
  `turn.completed.payload.final_answer.timeline_ids` promotion path as any other
  successful turn, and the projected row is owned by the originating turn while
  retaining the wake backend turn id for audit/debug detail. Across the folded
  chain, the LAST completed terminal owns the turn-detail final answer; a
  parked origin turn's promoted ack is superseded by its continuation and must
  not resurface as the final answer — not even via the no-marker fallback.
- A completed SDK turn that leaves a background task running is not a
  user-final assistant response. Its assistant prose and background-task row
  remain Turn activity material, and the main transcript keeps the
  user's message open until the background-wake continuation reaches a true
  final answer or otherwise terminates — but the parked turn KEEPS its
  `turn_activity` shell in the settled projection, carrying
  `activity.continuation: true`. The shell is the durable home of the
  compacted body and the carrier of the stamped turn number; suppressing it
  annihilates the turn's content from the durable read model (the
  session-161 "Current turn / bare prompts" failure, replayed in
  `transcript_projection_replay_test.go`).
- Failed, interrupted, and otherwise non-successful turns do not have a final
  assistant answer. Their non-user activity stays in Turn activity, with terminal
  context surfaced by the Turn activity disclosure row and the terminal meta
  line, not by expanding child provider rows into the main transcript.
- A server-projected active `turn_activity` shell owns the visible running
  placeholder for that turn. `turn.submitted` alone is enough to project that
  shell, so the user sees immediate durable progress before provider output or
  runner-owned `turn.claimed` arrives. The browser must not hide the `...` row
  while waiting for a separately-delivered activity summary to set the same
  active turn id.
- The running placeholder's active state comes from that shell, but its chat
  placement is resolved from durable `order_key`, not from a structural
  "latest row carrying this turnId" rule. The placeholder sorts at the turn's
  live-tail order key — the furthest order key the turn has reached across both
  the shell's compacted activity (`endOrderKey`) and any turn-tagged row that
  stays in the main transcript. Companion rows anchored to a later order key
  must not be overtaken by the placeholder. The shell's own start key is
  anchored to the turn's first post-message event, so folded session-startup
  notices — whose durable order keys can precede the user message — never drag
  the placeholder (or the settled activity shell) above the message that opened
  the turn.
- Token usage updates are durable backend plumbing for the context/cost
  indicator, not visible transcript UI. The runners may emit `turn.usage`
  snapshots and terminal usage payloads, and the backend/admin math may
  continue to use those durable events for accounting, diagnostics, and
  reports. The projection may carry a data-only `turn_usage` meta row and
  usage fields on Turn activity shells so the pre-regression composer chip can
  render, but the frontend must render `turn_usage` rows as `null`; the
  confusing "Token usage updated" transcript message must not be visible.
- Cost/context math remains provider-observed, not guessed. Per-message
  snapshots (`usage_observation.usage_source = "claude.message"` for Claude;
  `thread.tokenUsage.updated` for Codex) and cumulative terminals
  (`claude.result`) have different semantics and are still validated by math
  tests. The product UI must not render token/cost accounting messages in the
  transcript; the composer restores the pre-regression `run-cost-estimate`
  chip with `ctx` and `usd` metrics.
- Provider-observed context window remains durable session metadata persisted
  as `runtime_context_window_tokens`, reported by the runners through
  `PUT /api/internal/sessions/{id}/runtime-config` (Codex app-server token
  usage; the Claude Agent SDK per-turn `modelUsage.contextWindow`). The
  composer context indicator is a context-pressure affordance, not a billing
  surface: it renders a `used/window` fraction from durable `turn.usage`
  snapshots plus the durable session-row window, never from a frontend
  model-window table or guessed denominator. The row value is durable and
  first-observed-wins: the first positive window the runner reports is persisted
  and not overwritten by later reports, so the composer indicator is stable
  across reloads and matches a fresh tab.
- Already-open Turn activity details are a cached view of the server projection,
  not a second browser-owned ledger. A live `transcript_rows` batch for a turn
  whose details are already loaded must invalidate that cache and re-read
  `/turns/{id}/activity`; the browser must not synthesize child activity rows
  from the live shell.
- The dedicated Turns view renders the turn's initiating instruction as
  server-projected turn context above the paged activity body. Human turns
  source this context from durable `user_message.created`; backend-owned
  background-task wake turns source it from durable
  `turn.submitted.payload.prompt` and mark it system-authored. This context is
  not an activity child row, so it stays visible while the reader moves between
  activity pages.
- Synthetic AskUserQuestion turns may render the asking turn's durable
  final-answer candidate on each question page before the answer card. The
  candidate is snapshotted by the runner on
  `turn.awaiting_input.payload.asking_turn_final_answer` using the same
  `final_answer.timeline_ids` shape as `turn.completed`; the backend then reads
  those exact assistant `item.completed` rows from the asking turn. This is a
  page-context copy only: it must not create an extra activity page, change the
  question turn's lifecycle/event counts, or become the synthetic turn's final
  answer.
- The dedicated Turns view renders successful final assistant prose from the
  server-projected `/turns/{id}/activity` `final_answer.entries` section, not by
  inferring finality from the currently selected activity page. Agent activity
  is compacted by default so the Turns surface follows the settled chat
  projection: an active compacted turn derives liveness from the server-owned
  active shell and keeps context plus the generic running `Thinking...`
  affordance visible while hiding self-talk/tool rows as they arrive; a
  completed compacted turn keeps the durable final answer visible even when the
  final-answer event belongs to a different activity page. Expanding the turn
  reveals the execution trace for that turn. Failed, interrupted, and no-final
  completed turns do not expose a compacted final-answer projection because
  there is no durable assistant result to show, except the AskUserQuestion /
  ExitPlanMode hand-off turn described next.
- An asking turn that paused on AskUserQuestion / ExitPlanMode never carries a
  durable `turn.completed.final_answer`: the answer rotates execution onto a
  separate continuation turn. The hand-off itself plays the final-answer role, so
  `/turns/{id}/activity` `final_answer.entries` for that turn is the agent's
  preamble (the `asking_turn_final_answer` assistant prose the runner
  snapshotted, named with the same `final_answer.timeline_ids` shape) followed by
  the AskUserQuestion card, whose `awaitingInput` carries the shortcut to the
  question turn. This makes the asking turn collapsible to that bundle instead of
  rendering "No turn activity", and is the same snapshot the question page copies
  as page context — but only the asking turn promotes it to a final answer; the
  synthetic question turn's copy stays a page-context copy (above) and never
  becomes that turn's final answer. The promotion projects existing durable rows;
  it must not add an activity page or change either turn's event counts.
- AskUserQuestion answer input never traps the user. A pending question is a
  conversation the user steers, not a form that must be satisfied before the UI
  releases them. Four invariants hold for every question regardless of what the
  agent sent:
  - No nothing-selected state. Every question carries a synthetic "Something
    else" choice, selected by default; the answer reader
    (`effectiveAskUserQuestionSelection`) resolves an empty stored selection to
    "Something else", so Submit is always live and Enter always advances or
    submits. An agent's option list is a set of shortcuts, never a fence.
  - Companion text is a valid answer on ANY selection. The composer free-form
    text is never gated on `allowFreeForm` and never silently dropped: it rides
    as `annotations.notes` whether the selection is a real option or "Something
    else" (where it becomes the whole answer). The agent may hint that it
    expects a pick; it cannot revoke the user's ability to answer outside the
    menu.
  - An empty pass is acceptable. Submitting with nothing typed and nothing
    picked sends "Something else" with no elaboration — an honest "I'm not
    answering this", not a forced choice and not a blocked Submit. On the wire
    that is `answers:{question:["Something else"]}`, which satisfies the
    backend's >=1-non-empty-label gate by construction; the backend does not
    validate answer labels against the question's offered options.
  - A visible exit is always present. The Stop control (also bound to Esc)
    renders ALONGSIDE the answer Submit while a question is pending, never
    swapped out for it. Stopping the turn is the durable dismiss path
    (`turn.interrupt_requested` -> `turn.interrupted` -> card `dismissed`); it is
    the emergency hatch, not the primary flow, and Esc is advertised rather than
    the only way out.
  In a multi-question set, Enter (or the button) advances to the next question
  mid-set and submits on the last page; per-question free-form text is persisted
  on page change and restored on return, so advancing never discards a typed
  answer.
- The authenticated Turns view is a chat-capable continuation surface. Its
  composer uses the same `POST /api/sessions/{session_id}/turns` durable
  boundary as the main transcript composer; it does not create a second submit
  route or browser-local turn ledger. The submit response carries the durable
  `turn_id` and, when Postgres turn numbering is active, `turn_number`; the
  browser selects and routes the new Turns detail from that durable identity
  while waiting for the server-projected row to arrive.
- Turn activity may show a log copy of assistant prose, including prose that
  later becomes the final answer, but that copy is not a second settled
  transcript message.
- Copy links, unread counts, latest-message state, and fork-from-message actions
  must target the settled transcript projection, not duplicate activity-log
  copies or the Turns view's context copy of the initiating instruction.

## Failure And Recovery

- Browser reload replays from durable history and may repair local display
  state, but reload is not part of the happy path.
- Browser disconnect resumes from a durable cursor.
- Orchestrator rollout and runner-process restart must not lose already
  persisted transcript events.
- Session-pod death is the lifecycle boundary. Tank does not promise to
  resurrect the `emptyDir` workspace or continue a dead pod's conversation.

## Observability

- There must be a way to compare an open client's last applied cursor with the
  durable tail for the session.
- Stream open, reconnect, resync, heartbeat, emitted-event, and error counters
  must distinguish auth failure from live cursor lag.
- User-trust bugs should be diagnosable by querying `session_events` first,
  then live stream telemetry.
- A durable terminal event that exists but is not visible in an open transcript
  must leave enough telemetry to localize the miss.
- Provider context-window reports on the runtime-config PUT must be counted by
  bounded labels so missing or ignored runner reports are diagnosable without
  reading runner logs. `tank_session_context_window_report_total{provider,
  source,result}` records one outcome per call around `SetRuntimeContextWindow`
  (`ok` / `not_found` / `update_failed`, and `ignored` when the call carried no
  positive window); `source` is bounded to the known observation tags plus an
  `other` bucket.
- A live Turn activity detail refresh that cannot re-read the server projection
  must be counted by bounded client telemetry and leave a visible, retryable
  state in the Turns detail instead of silently leaving stale activity on
  screen.
- A server-projected active Turn activity shell that does not produce a visible
  `...` row must emit bounded client telemetry so the missing placeholder can be
  diagnosed without guessing from screenshots. The telemetry must count active
  shells from transcript entries, not only from rendered rows, because the shell
  may be projection-only.
- A report that a message bounced between Turn activity and the main transcript
  should be diagnosable from the durable event order plus the frontend
  projection chosen before first paint.

## Acceptance Checks

- Creating a session from a splash prompt writes the user message before
  startup status.
- `session.status` loading/ready notices fold into the owning turn's Turn
  activity and render there from durable events; they are absent from the main
  transcript. A `failed` status and provider credential banners render as
  top-level system messages.
- With an SSE stream already open at cursor X, persisting later transcript
  events causes the browser to render them without refresh.
- Reconnect from a valid cursor replays missed events exactly once.
- Reconnect from an unknown cursor triggers explicit resync.
- A browser or integration check proves that load/ready notices do not appear
  as main-transcript rows and do not reorder the conversation; they are
  reachable in the turn's Turns view.
- An active turn that emits assistant prose and then later emits more work does
  not show that prose as a settled main-transcript row before moving it into
  Turn activity.
- With a Turns detail already loaded and an SSE stream already open, a later
  durable activity row for the same turn causes the browser to re-read
  `/turns/{id}/activity` without refresh; repeated refresh failures surface a
  retryable Turns detail error and `tank_session_event_client_events_total`
  labels for the failure.
- Opening a numbered turn route (`/sessions/{id}/turns/{n}`) renders the
  initiating instruction at the top of the Turns view from the server
  projection. Switching activity pages keeps that same context visible and does
  not duplicate the human user message or system wake prompt inside the activity
  page body.
- Opening a synthetic AskUserQuestion turn route renders the linked asking
  turn's final-answer candidate above the answer card when the awaiting-input
  payload names one, keeps the page kind as `question`, and invalidates the
  cached page when the asking turn's durable high-water mark changes.
- A pending AskUserQuestion renders a visible Stop/cancel control in the
  composer alongside Submit; the exit is not removed while a question is active.
- An AskUserQuestion with no option picked and nothing typed is submittable and
  posts `answers:{question:["Something else"]}` (an honest pass), not a blocked
  Submit.
- Free-form composer text is included as the answer note for any selection —
  including a real-option pick and an options-only (`allowFreeForm:false`)
  question — and is never discarded on submit or on multi-question page change.
- `frontend/src/askUserQuestionSelection.test.ts` proves the never-empty default
  ("Something else"), the sentinel's mutual exclusion with real options, and the
  answer-payload builder's empty-pass + companion-text-on-any-selection
  invariants.
- A stopped or dismissed synthetic AskUserQuestion / ExitPlanMode turn keeps
  exactly one `question` page per question: the Stop sequence
  (`turn.interrupt_requested` followed by the dismissing `turn.interrupted`) and
  any other non-answer terminal fold onto the question page, never a spurious
  trailing `activity` page. Because a non-`needs_input` turn opens on its last
  page, that extra page would land the Turns view on a contextless activity page
  and strand the prompt slot on "Prompt context unavailable"; instead the turn
  opens on its question page and the slot shows the "Question N of M" heading.
- Collapsing agent activity in the Turns view keeps the server-projected final
  answer visible, hides ordinary tool/reasoning/progress rows, keeps
  server-owned always-visible context such as background-wake prompts visible,
  and stays disabled when no durable final answer exists.
- Submitting from the authenticated Turns view writes the normal durable
  `user_message.created` / `turn.submitted` boundary, keeps public message-link
  views read-only, routes the browser to `/sessions/{id}/turns/{n}` when the
  response includes a durable number, and does not infer that number from the
  loaded transcript window.
- A completed turn may show the final assistant prose in the main transcript
  while also retaining a log copy in Turn activity, without counting it as two
  transcript messages.
- A background-task wake continuation writes no durable main-transcript user
  message, renders no wake activity shell in the main transcript, folds the
  system-user wake prompt into the originating turn in the Turns view, and still
  promotes a true final answer when the successful terminal event names its
  final-answer item IDs.
- A turn whose background task outlives its own `turn.completed` does not promote
  that turn's assistant final-answer item into the main transcript. The item is
  compacted into Turn activity alongside the background-task row; a later
  background-wake turn may promote the actual final assistant answer.
- Failed or interrupted turns keep their non-user rows in Turn activity and show
  only the user message plus terminal context in the main transcript unless a
  later successful `turn.completed` with explicit final-answer ids wins the race.
- A session whose row carries no provider-observed window
  (`runtime_context_window_tokens = 0`) must not cause the frontend to guess a
  model window or render a context fraction. Once both a provider-observed
  window and a durable usage snapshot exist, the composer renders the
  `used/window` context fraction. `scripts/check-context-window-table-migration.mjs`
  proves no `CONTEXT_WINDOW_BY_MODEL` / `getContextWindow` model table remains
  under `frontend/src`; the usage UI guard proves the composer chip exists
  while the visible token-usage transcript message does not return.
- The composer context indicator also surfaces a durable per-session compaction
  count as a third `cmp` metric, including the zero state before the first
  compaction. The pre-session splash composer renders that metric at `0`
  rather than adding it only after a session starts; chat-box controls keep a
  stable shape and use disabled or empty values when the backing session fact is
  not available yet. In active sessions it is durable session metadata
  (`sessions.compaction_count`), maintained server-side as a projection over the
  append-only `session_events` ledger (count of `context.compacted` events) and
  carried on the session row, so it is reload/fresh-tab stable and never
  inferred from whatever transcript entries the browser has loaded. Unlike the
  `ctx` occupancy numerator — which self-resets after a compaction because the
  next prompt is summary + recent turns — the count is cumulative and monotonic.
  The projection is idempotent under at-least-once delivery (recompute-and-
  compare; the row is written only when the total advances), and the bounded
  activity-summary fold is explicitly not its source. See the Composer
  Compaction Count capability in `capabilities.md`.
