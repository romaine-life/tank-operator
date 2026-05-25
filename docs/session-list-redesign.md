# Session List Redesign

The sidebar's "what sessions does this owner have?" state pipeline has
produced three production bugs in a row across `#524`, `#526`, and the
not-yet-merged live-stream resurrection case. Each PR cleaned up one
glue point between independent state pipes; the remaining glue points
still produced the next bug. This document is the full plan for cutting
over to a shape where the bug class is structurally absent, not
defended against.

It binds the work to [docs/migration-policy.md](migration-policy.md)
(the old shape is deleted end-to-end, no compatibility, no fallback)
and [docs/quality-timeframes.md](quality-timeframes.md) (heavy
long-term solution by default; full plan written first when the
solution spans multiple PRs; each PR must leave the system coherent;
observability, tests, and migration guards are completion gates, not
follow-ups).

## Problem Statement

The current sidebar state pipeline is **event-sourcing without entity
ownership**. Five structural properties produce the bug class:

1. **No single owner per session.** Three independent writers append
   to `session_lifecycle_events`: `Manager`
   ([sessions/manager.go:646](../backend-go/internal/sessions/manager.go))
   for user-action transitions, `podinformer`
   ([podinformer.go:308](../backend-go/internal/podinformer/podinformer.go))
   for K8s pod transitions, `lifecycle_emitter`
   ([cmd/tank-operator/lifecycle_emitter.go:69](../backend-go/cmd/tank-operator/lifecycle_emitter.go))
   for chat-activity deltas. None of them knows what the others are
   doing. When `Manager.Delete` marks session 52 `visible=false`,
   `podinformer` keeps emitting `session.pod_terminating` and
   `session.pod_failed` because nothing in its watch loop checks the
   registry. There is no place in the architecture where "session 52
   is dead" is a fact that all producers respect.

2. **The wire carries event types, not row state.** SSE on
   `/api/sessions/events` delivers eleven discriminated event types
   ([sessionListEvents.ts:23](../frontend/src/sessionListEvents.ts)).
   The SPA reduces them with a `switch` on type
   ([sessionListEvents.ts:117](../frontend/src/sessionListEvents.ts)).
   Every new event type is a new code branch. The wire cannot say
   "session 52's current row is X" — it can only say "this transition
   happened."

3. **Deletion is not terminal at the ledger.** The unique index on
   `session_lifecycle_events` is keyed on `(session_scope, session_id,
   event_id)`
   ([pgstore/migrations.go:120](../backend-go/internal/pgstore/migrations.go)).
   The schema lets you append `session.pod_terminating` AFTER
   `session.deleted` for the same `(scope, id)`. The SSE handler
   forwards the events in `order_key` order without semantic guard.
   The reducer cannot tell post-delete events from pre-delete events.

4. **The snapshot and the event-fold are parallel computations.**
   `GET /api/sessions` runs registry visible-rows + K8s pod-fallback +
   lifecycle-store `LatestPodStatus`/`LatestActivity` hydration
   ([sessions/sessions.go:96](../backend-go/internal/sessions/sessions.go)).
   Folding the lifecycle ledger from `order_key=0` produces a
   different answer — it includes the historical lifecycle of any
   since-deleted session. Both are presented to the SPA as "the
   current state." Reader.List's pod-fallback bug and the cold-open
   replay bug were both "the two computations disagreed at this glue
   point."

5. **The SPA optimistically mutates state it later receives deltas
   about.** Click delete → SPA removes row locally → server emits
   `session.deleted` → reducer no-ops (already removed) →
   pod-informer emits `session.pod_terminating` → reducer cannot find
   row → `applyPodStatusEvent` synthesizes a placeholder
   ([sessionListEvents.ts:244](../frontend/src/sessionListEvents.ts)).
   The SPA has no record that the session was deleted; every arriving
   event is treated as a fresh mutation to apply.

The chat-window pipeline (`session_events` table, per-session SSE)
works because it has a single writer (the runner), one durable store,
and fully deterministic replay-from-cursor. The sidebar tried to copy
the shape but added multiple writers, a parallel snapshot table, and
a non-terminal deletion. It is event-sourcing-shaped without the
coherence properties that make event-sourcing work.

## Target Architecture

The end state is **row-centric, single-writer-per-row, per-row UPDATE
on the wire, with a frontend reconciler that owns the user-facing
view**. Coder's workspace-list shape, applied to tank's sidebar.

### Backend: SessionController (single writer per row)

One process is the only writer to the `sessions` table for fields the
sidebar cares about. It folds three input streams into row updates:

- **User actions** (the Manager API surface: create, delete, set name,
  set test state, set rollout state).
- **K8s pod state** (subsumes `podinformer` — the controller's own
  watch loop, not a separate publisher).
- **Chat activity deltas** (subsumes `lifecycle_emitter` — the
  controller's own subscription to the chat session-bus, not a
  separate publisher).

The `sessions` row absorbs every field the sidebar renders today:
`status`, `ready_at`, `terminating_at`, `activity_summary jsonb`,
`unread_count`, `test_state jsonb`, `rollout_state jsonb`, and
`sidebar_position`. `row_version` is only the update cursor; it is
not a render-order key. `test_state.active` means the test workflow
has started; it is not proof that a Glimmung slot still exists.
`rollout_state.active` means the rollout workflow has started, which
ends the test workflow. The two states are mutually exclusive at the
row: at most one can carry `{"active": true}`. There is no
`session_lifecycle_events` read on the sidebar path. The K8s pod is
the SessionController's input, not the SPA's input.

Deletion is terminal at the row: once `visible=false` is written, the
controller emits exactly one "session deleted" notification on the
wire and stops publishing for that id. The K8s pod's subsequent
Terminating/Failed/Removed transitions update the row's
`terminating_at` only if the row is still visible; for invisible rows
they are dropped at the controller.

### Wire shape: per-row UPDATE, not typed events

NATS and Postgres have non-overlapping roles, mirroring the chat-window
pipeline:

| | Postgres `sessions` table | NATS row subject |
| --- | --- | --- |
| **Role** | Durable source of truth | Live wake / push |
| **Holds** | Current row state per session | Nothing — not durable, no replay |
| **Read by** | Snapshot endpoint, SSE catch-up on reconnect | SSE live subscriber, while connection is open |
| **Written by** | SessionController on every mutation | SessionController, immediately after the SQL write |
| **Loss tolerance** | None — this is the truth | High — on NATS outage, SSE catch-up reads the same rows from SQL |

NATS payload is one of two shapes:

```
{"row": { ...full current row state... }, "cursor": "<row_version>"}
{"deleted": true, "session_id": "52", "cursor": "<row_version>"}
```

No event types. No replay-from-zero semantics. The SPA receives a
payload and either replaces its cache entry for that id, or removes
it. The "deleted" shape is the visibility-false signal on the wire;
catch-up returns rows including `visible=false` so a client that
disconnected mid-delete still sees the deletion on reconnect.

Catch-up reads directly from the `sessions` table: `SELECT * FROM
sessions WHERE email=$1 AND session_scope=$2 AND row_version > $cursor
ORDER BY row_version`. No separate `session_row_updates` audit table —
the `sessions` table is the catch-up source, the snapshot source, and
the live source's durable backing all at once. `row_version` is a
per-(email, scope) monotonic int (same shape as today's
`session_lifecycle_events.order_key`, just hung on the row).

The `Tank-Lifecycle-Tip-Order-Key` header is replaced by an analogous
`Tank-Sessions-Snapshot-Cursor` representing the `MAX(row_version)`
for (owner, scope) at snapshot time.

### Frontend: SessionStore (the user-facing reconciler)

The frontend has its own protective layer that owns the
user-facing view. This is the layer that has been missing — events
have been applied to React state directly with no intermediate model,
which is why every server-side wonk has produced a user-visible bug.

`SessionStore` is the canonical client-side reducer. Responsibilities:

1. **Cache rows by id**, replaced wholesale on every server update.
   No event-type switch. No placeholder synthesis. If an update
   arrives for an unknown id, it's a new row — add it. If a delete
   arrives, remove it. The list renders by the row's durable
   `sidebar_position`, so row updates for test, rollout, status, or
   activity state cannot reorder sessions.
2. **Tombstone set**: every id that has been deleted (by local user
   action OR by server notification) is added to the tombstone set.
   Subsequent server updates for tombstoned ids are dropped at the
   store boundary. The tombstone set is in-memory per tab; the next
   snapshot is the authoritative reset.
3. **Optimistic delete**: user clicks X → store tombstones the id and
   removes the row immediately. The DELETE request is fire-and-await
   in the background; whether it succeeds or fails, the local view
   stays consistent because the tombstone holds.
4. **Snapshot is the authority**: on `refresh()` (manual button, SSE
   reconnect, or explicit reload), the store replaces its row cache
   from the snapshot response and clears tombstones for ids the
   server still considers visible (recovery path: if the user
   tombstoned locally but the server didn't receive the delete, the
   snapshot brings it back).
5. **In-memory ring buffer of recent updates** for diagnosis — the
   `/_debug/session-list` page renders it without the user needing
   browser devtools. The SPA mirrors this bounded ring to
   `sessionStorage` and exposes `window.__tankSessionListDebug()` so a
   reload of the debug route can still inspect the latest row, store,
   render, and avatar transitions from the current tab.

The frontend SessionStore makes the user-visible view robust against
any backend wonk by construction: the SPA cannot resurrect a deleted
session because the tombstone set rejects updates for it; the SPA
cannot show a phantom row because there is no synthesis path; the SPA
cannot drift from the server because refresh replaces the cache.

## Migration Phases

Per quality-timeframes.md planning rule, each phase leaves the system
coherent on its own. Per migration-policy.md, the old shape is deleted
end-to-end by the end of Phase 4. Compatibility layers are prohibited
in every phase.

### Phase 0 — This plan doc

Doc PR only. No code changes. Merging this doc is the shared
reference for everything that follows.

**Completion gate**: doc merged on main, linked from CLAUDE.md under
the existing "Quality timeframe" section.

### Phase 1 — Schema absorbs sidebar fields; single SessionController consolidates the writers

Database:
- Add columns to `sessions`: `status text NOT NULL DEFAULT 'Pending'`,
  `ready_at timestamptz`, `terminating_at timestamptz`,
  `activity_summary jsonb`, `sidebar_position bigint NOT NULL`,
  `row_version bigint NOT NULL DEFAULT 0`.
  `row_version` is the per-row monotonic update counter that replaces
  the lifecycle ledger's `order_key` for the row-update wire.
  `sidebar_position` is the durable render-order key and does not
  change when row_version advances for state updates.

Code:
- New package `backend-go/internal/sessioncontroller/`. A single
  process that owns row writes. Takes over from `podinformer`
  (subsumed; the K8s watch loop becomes the controller's loop) and
  from `lifecycle_emitter` (subsumed; the chat-bus subscription
  becomes the controller's subscription). `Manager` no longer emits
  lifecycle events directly — it calls into the controller.
- The controller writes the new row columns on every transition,
  bumping `row_version`.
- The controller is the only place that decides whether to publish a
  row update on NATS. Invisible rows publish one "deleted"
  notification then go silent.

Wire: unchanged in this phase. The lifecycle-event SSE path keeps
running unchanged off the existing producers (for now — Phase 3
retires it). The new controller writes the new columns; the existing
publishers continue publishing typed events. Snapshot still hydrates
from `LatestPodStatus`/`LatestActivity`.

This is the "preparatory refactor with its own verification" that
quality-timeframes.md explicitly allows. It is not a compat layer:
the new columns are not yet read by anyone, the old reads are
unchanged, and Phase 2 atomically cuts reads over.

**Deletion targets in this phase** (per migration-policy.md "search
for the old system's names ... and remove every live path"):
- `internal/podinformer/` — moved into `internal/sessioncontroller/`
  during the phase; old import path is gone by phase end.
- `cmd/tank-operator/lifecycle_emitter.go` — moved into the
  controller; old file gone by phase end.

**Completion gates**:
- Tests assert: every row mutation produces a corresponding row write
  with the new columns set correctly.
- A `tank_session_row_writes_total{source}` counter labeled by
  trigger source (user_action, k8s_watch, chat_activity).
- `helm template` renders the new `SessionController` Deployment (if
  separate) or the existing orchestrator Deployment (if folded in).
- Migration guard: any new code that imports
  `internal/podinformer` or `cmd/tank-operator/lifecycle_emitter`
  fails the build.

### Phase 2 — Snapshot reads from the row

Cutover:
- `Reader.List` ([sessions/sessions.go:96](../backend-go/internal/sessions/sessions.go))
  drops `LatestPodStatus`/`LatestActivity` calls. Reads `status`,
  `ready_at`, `activity_summary` directly from the row.
- `Reader.List` drops the K8s pod-fallback loop entirely. The
  registry is the authoritative "what sessions exist for this owner."
  `Manager.Create` writes the registry row before creating the pod
  (the legitimate race that the pod-fallback covered is closed by
  inverting the write order).
- `Manager.Create` order change: `registry.Upsert` THEN
  `client.Pods.Create`. If pod create fails, the row is marked
  `visible=false` and the failure surfaces on the next snapshot.
- `handleListSessions` emits the new `Tank-Sessions-Snapshot-Cursor`
  header (`row_version` as of snapshot time, computed via
  `SELECT MAX(row_version) FROM sessions WHERE email = $1 AND
  session_scope = $2`).

**Deletion targets**:
- `Reader.List` pod-fallback loop (lines 126-134 today)
- `Reader.hydrateLifecycle`
- `LifecycleStatusSource` interface
- `LatestPodStatus` and `LatestActivity` callers in the snapshot
  path (lifecycle store still keeps the methods for now — retired
  in Phase 4)
- `Tank-Lifecycle-Tip-Order-Key` header (replaced by
  `Tank-Sessions-Snapshot-Cursor`)

**Completion gates**:
- Snapshot tests: visible-only output; no pod-fallback rows.
- Snapshot p99 latency benchmark: must be ≤ current (the join removal
  should make it faster).
- Migration guard: `Reader.List` no longer references K8s pods at
  all; new pattern blocks reintroduction of `pods.List` in
  `sessions.go`.

### Phase 3 — Live wire becomes per-row UPDATE

Cutover:
- New NATS subject `tank.live.sessions.<email_token>.<scope_token>.rows`
  for row-update payloads. The old `.events` subject is retired
  atomically (no parallel run; migration-policy.md prohibits the
  fallback).
- `SessionController` publishes row updates on every mutation.
  Payload is `{"row": {...full row...}, "cursor": "<row_version>"}`
  or `{"deleted": true, "session_id": "...", "cursor": "..."}`.
- New SSE catch-up endpoint reads from a new `session_row_updates`
  audit table (or queries the `sessions` table directly with
  `WHERE row_version > $cursor` ordered ascending — cheaper and avoids
  a new table). The latter is preferred unless we need true append-
  only semantics for the wire.
- Frontend `SessionStore` ships. The old reducer
  (`applySessionListEvent`, `normalizeSessionListEvent`,
  `SessionListEvent`) is deleted in this phase, not deferred.
- Optimistic delete + tombstone set + refresh button + in-memory
  update ring buffer all ship.
- The `/_debug/session-list` page ships (renders SessionStore state +
  copy button).

**Deletion targets**:
- `lifecycleevents.Event` and all `EventType*` constants
- `SessionListEventSubject`,
  `PublishSessionListEvent`,
  `SubscribeSessionListEvents` (or rename to `Row*` equivalents — but
  rename per migration-policy.md means the old name doesn't survive)
- Frontend: `sessionListEvents.ts` entire file → renamed to
  `sessionRowUpdates.ts` (or similar), with no carryover of
  event-type-switch logic
- `applySessionListEvent`, `normalizeSessionListEvent`,
  `applyPodStatusEvent`, `applyActivityEvent`, `createSession`,
  `deleteSession`, `patchSessionField` — all deleted, replaced by
  `SessionStore` methods
- `sessionListTelemetry.notePlaceholderSynthesized` — no synthesis
  path, counter retired
- `tank_session_list_client_placeholder_synthesized_total` and the
  `TankSessionListClientPlaceholderSynthesis` alert — retired
- `Tank-Sessions-Snapshot-Cursor` is the only cursor; the prior
  `Last-Event-ID`-on-ledger semantics is gone

**Completion gates**:
- SessionStore unit tests for every protective behavior: tombstone
  rejection, optimistic delete persistence, snapshot replacement,
  unknown-id row-add path.
- Backend integration test: delete a session → no subsequent NATS
  payload arrives for that id, even if K8s pod transitions through
  Terminating/Failed.
- Manual repro: the bug the user reported — create session, see
  previously-deleted session reappear — must be reproducible against
  the old branch and must NOT reproduce on the new branch.
- Migration guards: new code adding `applySessionListEvent` or
  `EventTypePod*` symbols fails CI.

### Phase 4 — Delete `session_lifecycle_events` end-to-end

Cutover:
- All remaining `lifecycleevents.Store` callers removed.
- `lifecycleevents.Store` interface deleted, package deleted.
- `session_lifecycle_events` table dropped via a new migration
  statement in `pgstore/migrations.go`. The five indexes on it
  (`_owner_order`, `_event_id`, `_session_order`,
  `_activity_latest`, `_pod_latest`) are dropped with it.
- Per migration-policy.md, no forensic-audit carve-out. "Just in
  case" runtime reads are explicit deletion targets in the doctrine's
  review heuristics. If post-incident debugging needs a controller
  audit trail later, add it as a SessionController-owned mechanism
  with a clean schema, scoped to a concrete operator need — do not
  preserve the old table on speculation.
- Documentation: `CLAUDE.md` updated to point at this doc; the
  current "the live workflow shape is the Workflow row registered
  in Glimmung's Cosmos database, not …" pattern of authoritative
  pointer is replicated for sessions.

**Deletion targets** (final sweep — every name from the old shape must
be gone from live code per migration-policy.md):
- `lifecycleevents` package (entire directory)
- `Append`, `ListByOwner`, `HasOrderKey`, `LatestOrderKey`,
  `LatestActivity`, `LatestPodStatus` interface methods
- `EventTypeCreated`, `EventTypeDeleted`, `EventTypePodScheduled`,
  `EventTypePodReady`, `EventTypePodNotReady`, `EventTypePodFailed`,
  `EventTypePodTerminating`, `EventTypeNameChanged`,
  `EventTypeTestStateChanged`, `EventTypeRolloutStateChanged`,
  `EventTypeActivityChanged`
- `session_lifecycle_events_owner_order`,
  `session_lifecycle_events_event_id`,
  `session_lifecycle_events_session_order`,
  `session_lifecycle_events_activity_latest`,
  `session_lifecycle_events_pod_latest` indexes (kept only if the
  audit endpoint queries benefit from them)
- All migration guard patterns in
  `scripts/check-removed-chat-runtime.mjs` that pinned the lifecycle
  shape are themselves retired; new patterns block reintroduction of
  any `apply*Event`, `EventType*`, or single-subject typed-event
  publishing

**Completion gates**:
- `grep -r 'lifecycleevents' backend-go/ frontend/` returns nothing
  in non-audit code.
- `tank_session_lifecycle_event_writes_total` and the
  `TankPodInformerLeaderMissing` alert are removed (they referred to
  the old leader-elected informer; the SessionController in Phase 1
  has its own leader-election with its own metric and alert).
- The user can hard-refresh after deleting a session and the
  sidebar shows zero phantom rows under all conditions: 75s pod-
  termination window, restart of the SessionController, browser
  reconnect, snapshot-during-delete.
- `/api/debug/session-list-state` and `/_debug/session-list` (from
  Phase 3) confirm the post-state matches what the SPA renders.

## Definition Of Done

The migration is complete when, per quality-timeframes.md's done
standard:

- **Durable data model explicit**: `sessions` row carries every
  sidebar-visible field; `session_scope`, `email`, `session_id`,
  `visible`, `sidebar_position`, and `row_version` define the
  partition, render order, and update cursor.
- **Runtime behavior survives**: pod-informer restart, SessionController
  restart, browser reconnect, SSE drop, NATS reconnect — none produce
  user-visible state corruption. Tests assert each of these.
- **User-visible state not inferred from optimism when durable
  source exists**: tombstones cover the local-vs-server window;
  snapshot is always authoritative on refresh.
- **Failure, timeout, cancellation, retry visible**: each row
  carries `terminating_at`; SessionController failure surfaces as
  `tank_session_controller_reconcile_failure_total` with an alert.
- **Observability**: `tank_session_row_writes_total{source}`,
  `tank_session_controller_reconcile_*`, `/api/debug/session-list-state`,
  `/_debug/session-list`. The user can verify any sidebar bug
  without browser devtools (per the standing constraint —
  feedback_no_devtools_build_surfaces_instead).
- **Tests cover the contract**: SessionStore behavior, controller
  reconcile loop, row-update wire shape, snapshot cursor semantics.
- **Docs describe final behavior, obsolete docs removed**: this doc
  becomes the reference; `tank-operator#83`'s description of the
  typed-event shape is annotated as retired.
- **Migration guards prevent old paths from returning**: per
  migration-policy.md, every old name is grep-blocked.
- **Cost/scaling understood**: per-row UPDATE payloads are larger
  than typed-event payloads; the change to per-row also reduces
  publish frequency (typed events were one per K8s transition; row
  updates coalesce multiple transitions into one payload when the
  controller's reconcile loop debounces). Benchmark expected: net
  NATS bandwidth reduction.

## Operational Risk

**Rollout**: phases roll independently. Phase 1's dual-write does
not produce wire changes; the SPA continues using the old shape.
Phase 2 cuts the snapshot's read source over to the row; if the
controller has not written the row correctly, the snapshot returns
stale state — mitigated by Phase 1's verification gate. Phase 3
cuts the wire over; SPAs roll alongside the orchestrator so old
SPAs hit old-shape SSE briefly during deploy — bounded by
`terminationGracePeriodSeconds: 75`, and the SPA's auto-reconnect
picks up the new wire on first reload after the orchestrator rolls.

**Rollback**: each phase has a rollback shape, but per
migration-policy.md no parallel runs. If Phase 3 produces a
production regression, the rollback is to revert the phase-3 PR,
not to "flip a flag." This is the cost the doctrine asks us to
take in exchange for not carrying compat layers.

**Cost**: SessionController is one new process (or one new
goroutine in the orchestrator). The K8s watch and chat-bus
subscription are already running today inside the pod-informer and
the persister; consolidating them does not add load. The new row
columns add a few bytes per row; sessions table cardinality is
bounded by user count.

**Stuck states**: the SessionController's reconcile loop must be
idempotent. If a transition is missed (process crash, watch drop),
the next reconcile observes current pod state and writes whatever
the row should be. The row's `row_version` is bumped on actual
mutations only — a re-observe that finds nothing new doesn't
generate spurious wire updates.

## Doctrine Alignment Summary

| Doctrine point | This plan |
| --- | --- |
| migration-policy.md "no compatibility, no fallback, no parallel runs" | Phases cut over atomically; old shape deleted by Phase 4; no flags. |
| migration-policy.md "treat `legacy`, `compatibility`, `fallback` as deletion targets" | No such layers introduced; old shape's `lifecycle*` names are grep-blocked. |
| quality-timeframes.md "Default to the long-term solution" | Row-centric controller-reconciler, not another glue layer on the event-sourced shape. |
| quality-timeframes.md "Write the full plan first" | This document. |
| quality-timeframes.md "Each PR leaves the system coherent" | Phases 1-4 are individually shippable; verification gate per phase. |
| quality-timeframes.md "Observability is a completion gate" | New metrics, debug page, debug endpoint, alerts all in the same phases as the code. |
| product-inspirations.md "Live transport should wake clients … should not be the only place product state exists" | Row is the durable state; wire just delivers updates. |
| product-inspirations.md "User-visible run state comes from durable turn events, not local optimism" | Frontend SessionStore tombstones local optimism, but the snapshot is always authoritative on refresh. |
| product-inspirations.md "Coder" inspiration | Workspace-list shape applied to sidebar: row-centric, single controller, per-row update wire. |
