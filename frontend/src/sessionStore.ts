// SessionStore is the client-side reconciler for the sidebar's
// session list — the layer the user asked for after PR #524/#526
// landed and bugs still leaked: "I never understand why the frontend
// wouldn't have its own layer to protect the user from that."
//
// Per docs/session-list-redesign.md Phase 3 it owns:
//
//   1. The row cache (id → row) — replace-by-id semantics, no
//      event-type discriminator, no placeholder synthesis.
//   2. The tombstone set (ids the user has explicitly deleted OR
//      ids the server told us to drop via visible=false). Subsequent
//      wire payloads for tombstoned ids are dropped at the store
//      boundary. The set is in-memory per tab; the next snapshot is
//      the authoritative reset.
//   3. The optimistic-delete handshake: user clicks X → the row is
//      tombstoned immediately and removed from the cache; the DELETE
//      API call is fire-and-forget; whether the server confirms
//      synchronously or via the row-update wire, the local view is
//      consistent because the tombstone holds.
//   4. The cursor: the latest row_version seen, used to seed the SSE
//      reconnect.
//   5. Snapshot replacement (refresh button or SSE reconnect): the
//      cache is rebuilt from the server's authoritative view;
//      tombstones for ids the server still considers visible are
//      cleared (recovery from a failed optimistic delete).
//
// The store is intentionally not a React reducer. It's a plain
// TypeScript class with mutation methods and a subscribe API; the
// App.tsx integration subscribes to changes and re-renders via
// useSyncExternalStore. This keeps the protective semantics testable
// without React, and lets the /_debug/session-list page render the
// raw store state without going through React lifecycle.

import {
  normalizeSessionActivity,
  type SessionActivitySummary,
} from "./sessionActivity";
import {
  getSessionListDebugSnapshot,
  recordSessionListDebugEvent,
  updateSessionListDebugStore,
  type SessionListDebugEvent,
} from "./sessionListDebug";
import { normalizeBugLabelDisplayName } from "./bugLabels";

export interface SessionBugLabel {
  id?: number;
  name: string;
  slug: string;
  display_name: string;
}

// SessionRow is the wire shape one row-update payload's `row` field
// carries. Field set mirrors the SessionRecord projection the backend
// emits in sessioncontroller.MarshalRowUpdate, and matches the
// snapshot's Info JSON one-for-one so /api/sessions and
// /api/sessions/events parse through the same shape.
export interface SessionRow {
  id: string;
  owner: string;
  mode: string;
  session_scope: string;
  pod_name?: string;
  name?: string | null;
  // Server-computed session title, always present on the wire. The trimmed
  // user `name` when set, else a backend-derived short id slug. The SPA
  // renders this verbatim rather than deriving a local fallback.
  display_name: string;
  visible: boolean;
  status: string;
  requested_at?: string;
  created_at?: string;
  updated_at?: string;
  ready_at?: string;
  terminating_at?: string;
  activity_summary?: Record<string, unknown>;
  test_state?: Record<string, unknown>;
  rollout_state?: Record<string, unknown>;
  // repos is the durable owner/name slug list the user picked at
  // session creation. Always present on the wire (empty array when
  // none picked); the splash chips and the per-session detail view
  // both read through this field so local SPA state never
  // contradicts the row (handlers_repos.go on the backend).
  repos: string[];
  // clone_state surfaces the per-repo outcome from the repo-cloner
  // init container (keyed by slug). Optional on the wire
  // until the cloner writes back; null/missing means
  // "no clone state yet" rather than "clone succeeded."
  clone_state?: Record<string, unknown>;
  capabilities: string[];
  bug_label?: SessionBugLabel | null;
  bug_labels?: SessionBugLabel[];
  model?: string;
  effort?: string;
  runtime_model?: string;
  runtime_effort?: string;
  runtime_configured_at?: string;
  // Provider-observed live context window for this session's model, plumbed
  // through the row from agent-runner. The composer fraction's denominator —
  // never a frontend-assumed model table.
  runtime_context_window_tokens?: number;
  runtime_context_window_source?: string;
  runtime_context_window_observed_at?: string;
  provider_rate_limit_info?: Record<string, unknown>;
  provider_rate_limit_observed_at?: string;
  // Durable per-session count of context.compacted events, projected onto the
  // row from the session_events ledger. Powers the composer's compaction metric;
  // stable across reload and identical in a fresh tab, like the window above.
  compaction_count?: number;
  agent_avatar_id?: string;
  system_avatar_id?: string;
  // Durable user-facing order for the sidebar. Larger values render
  // earlier. This is intentionally separate from row_version so
  // status/test/rollout/activity updates do not reshuffle rows.
  sidebar_position: number;
  row_version: number;
}

// SessionRowUpdatePayload is one frame on the row-update wire. Catch-up
// and live deliveries both carry this shape.
export interface SessionRowUpdatePayload {
  row: SessionRow;
  cursor: string;
}

// SessionStoreEvent is what subscribers receive. The kind tells the
// app what changed; row/id is the affected entity.
export type SessionStoreEvent =
  | { kind: "row-added"; row: SessionRow }
  | { kind: "row-replaced"; row: SessionRow }
  | { kind: "row-removed"; id: string }
  | { kind: "snapshot-replaced" }
  | { kind: "row-dropped-tombstoned"; id: string };

export type SessionStoreSubscriber = (event: SessionStoreEvent) => void;

export class SessionStore {
  private rows = new Map<string, SessionRow>();
  private tombstones = new Set<string>();
  private subscribers = new Set<SessionStoreSubscriber>();
  private cursor: string | null = null;

  // applySnapshot replaces the row cache from a server snapshot while keeping
  // the row-version cursor monotonic. HTTP snapshots can resolve after newer
  // SSE or point-read observations; those late snapshots must not regress an
  // already-observed row from Active back to Pending or drop a row created
  // after the snapshot cursor.
  applySnapshot(
    rows: SessionRow[],
    snapshotCursor: string | null,
    source = "snapshot",
  ): void {
    const previousRows = this.rows;
    const visibleByID = new Map<string, SessionRow>();
    for (const row of rows) {
      if (!row.visible) continue;
      const existing = previousRows.get(row.id);
      visibleByID.set(
        row.id,
        existing && existing.row_version > row.row_version ? existing : row,
      );
    }
    if (snapshotCursor) {
      for (const [id, row] of previousRows) {
        if (visibleByID.has(id)) continue;
        if (rowVersionAfterCursor(row.row_version, snapshotCursor)) {
          visibleByID.set(id, row);
        }
      }
    }
    this.rows = visibleByID;
    // Clear tombstones that the server now considers visible — a
    // failed optimistic delete recovers here. Tombstones for ids the
    // server didn't return stay so a stale wire delivery can't bring
    // them back.
    for (const id of [...this.tombstones]) {
      if (visibleByID.has(id)) {
        this.tombstones.delete(id);
      }
    }
    this.cursor = maxRowCursor(this.cursor, snapshotCursor);
    this.recordDebugEvent({
      kind: "snapshot-applied",
      source,
      cursor: this.cursor,
      rows: this.list(),
      tombstones: this.tombstonedIds(),
      detail: {
        snapshot_cursor: snapshotCursor,
        incoming_count: rows.length,
        visible_count: [...visibleByID.values()].length,
      },
    });
    this.publishDebugStoreState();
    this.emit({ kind: "snapshot-replaced" });
  }

  // applyRowUpdate handles one wire payload (catch-up or live).
  // Returns true if the store state changed.
  applyRowUpdate(payload: SessionRowUpdatePayload): boolean {
    const row = payload.row;
    if (!row || !row.id) {
      this.recordDebugEvent({
        kind: "row-dropped-malformed",
        source: "SessionStore",
        cursor: payload?.cursor ?? null,
        detail: { has_row: row != null },
      });
      this.publishDebugStoreState();
      return false;
    }

    // Keep the resume cursor monotonic even when an older frame is ignored.
    // The row itself is still version-gated below so stale payloads cannot
    // replace newer row state.
    const previousCursor = this.cursor;
    this.cursor = maxRowCursor(this.cursor, payload.cursor);

    if (this.tombstones.has(row.id)) {
      this.recordDebugEvent({
        kind: "row-dropped-tombstoned",
        source: "SessionStore",
        session_id: row.id,
        cursor: this.cursor,
        row,
        rows: this.list(),
        tombstones: this.tombstonedIds(),
        detail: { payload_cursor: payload.cursor, previous_cursor: previousCursor },
      });
      this.publishDebugStoreState();
      this.emit({ kind: "row-dropped-tombstoned", id: row.id });
      return false;
    }
    const existing = this.rows.get(row.id);
    if (existing && existing.row_version > row.row_version) {
      this.recordDebugEvent({
        kind: "row-dropped-stale",
        source: "SessionStore",
        reason: "row_version_regression",
        session_id: row.id,
        cursor: this.cursor,
        row,
        rows: this.list(),
        tombstones: this.tombstonedIds(),
        detail: {
          payload_cursor: payload.cursor,
          existing_row_version: existing.row_version,
          incoming_row_version: row.row_version,
          previous_cursor: previousCursor,
        },
      });
      this.publishDebugStoreState();
      return false;
    }
    if (!row.visible) {
      // Server-initiated delete (registry.MarkDeleted or any other
      // mutation that flipped visible=false). Tombstone the id and
      // remove from cache.
      this.tombstones.add(row.id);
      const wasPresent = this.rows.has(row.id);
      if (this.rows.has(row.id)) {
        this.rows.delete(row.id);
      }
      this.recordDebugEvent({
        kind: "row-removed-server",
        source: "SessionStore",
        reason: "visible_false",
        session_id: row.id,
        cursor: this.cursor,
        row,
        rows: this.list(),
        tombstones: this.tombstonedIds(),
        detail: { payload_cursor: payload.cursor, was_present: wasPresent },
      });
      this.publishDebugStoreState();
      if (wasPresent) {
        this.emit({ kind: "row-removed", id: row.id });
        return true;
      }
      return false;
    }
    const had = existing != null;
    this.rows.set(row.id, row);
    this.recordDebugEvent({
      kind: had ? "row-replaced" : "row-added",
      source: "SessionStore",
      session_id: row.id,
      cursor: this.cursor,
      row,
      rows: this.list(),
      tombstones: this.tombstonedIds(),
      detail: { payload_cursor: payload.cursor, previous_cursor: previousCursor },
    });
    this.publishDebugStoreState();
    this.emit({ kind: had ? "row-replaced" : "row-added", row });
    return true;
  }

  // optimisticDelete is the user-clicked-X handshake. Tombstones the
  // id and removes the row from cache BEFORE the DELETE API call
  // returns. The server's eventual visible=false row update is a
  // no-op (already tombstoned). If the DELETE call fails, refresh()
  // will rehydrate visible rows including this id and clear the
  // tombstone.
  optimisticDelete(id: string): void {
    this.tombstones.add(id);
    const wasPresent = this.rows.has(id);
    if (this.rows.has(id)) {
      this.rows.delete(id);
    }
    this.recordDebugEvent({
      kind: "optimistic-delete",
      source: "SessionStore",
      session_id: id,
      cursor: this.cursor,
      rows: this.list(),
      tombstones: this.tombstonedIds(),
      detail: { was_present: wasPresent },
    });
    this.publishDebugStoreState();
    if (wasPresent) {
      this.emit({ kind: "row-removed", id });
    }
  }

  // applyLocalOrder is the short optimistic window after a user drag
  // and before PUT /api/sessions/order confirms. It updates the same
  // row field the server owns, so any following row update preserves
  // the visible order unless the persistence call fails and App.tsx
  // refreshes from the authoritative snapshot.
  applyLocalOrder(orderedIds: string[]): boolean {
    if (orderedIds.length !== this.rows.size) {
      this.recordDebugEvent({
        kind: "local-order-rejected",
        source: "SessionStore",
        cursor: this.cursor,
        rows: this.list(),
        tombstones: this.tombstonedIds(),
        detail: { ordered_ids: orderedIds, row_count: this.rows.size },
      });
      this.publishDebugStoreState();
      return false;
    }
    const seen = new Set<string>();
    for (const id of orderedIds) {
      if (seen.has(id) || !this.rows.has(id)) {
        this.recordDebugEvent({
          kind: "local-order-rejected",
          source: "SessionStore",
          cursor: this.cursor,
          rows: this.list(),
          tombstones: this.tombstonedIds(),
          detail: { ordered_ids: orderedIds, rejected_id: id },
        });
        this.publishDebugStoreState();
        return false;
      }
      seen.add(id);
    }
    for (let index = 0; index < orderedIds.length; index += 1) {
      const id = orderedIds[index];
      const row = this.rows.get(id);
      if (!row) return false;
      this.rows.set(id, {
        ...row,
        sidebar_position: orderedIds.length - index,
      });
    }
    this.recordDebugEvent({
      kind: "local-order-applied",
      source: "SessionStore",
      cursor: this.cursor,
      rows: this.list(),
      tombstones: this.tombstonedIds(),
      detail: { ordered_ids: orderedIds },
    });
    this.publishDebugStoreState();
    this.emit({ kind: "snapshot-replaced" });
    return true;
  }

  // getCursor returns the latest row_version applied. Used by
  // App.tsx to seed the EventSource Last-Event-ID on reconnect.
  getCursor(): string | null {
    return this.cursor;
  }

  // setCursor is used after the snapshot fetch to seed the cursor
  // from Tank-Sessions-Snapshot-Cursor before SSE opens. Distinct
  // from applySnapshot() so the snapshot replay can stage state
  // changes without prematurely advancing the cursor.
  setCursor(cursor: string | null): void {
    this.cursor = cursor;
    this.recordDebugEvent({
      kind: "cursor-set",
      source: "SessionStore",
      cursor: this.cursor,
      rows: this.list(),
      tombstones: this.tombstonedIds(),
    });
    this.publishDebugStoreState();
  }

  // list returns the cached rows in durable sidebar order. RowVersion
  // is only the live-update cursor; sorting by it would move sessions
  // whenever test/rollout/activity state changes.
  list(): SessionRow[] {
    return [...this.rows.values()].sort(compareSessionRowsForSidebar);
  }

  get(id: string): SessionRow | undefined {
    return this.rows.get(id);
  }

  isTombstoned(id: string): boolean {
    return this.tombstones.has(id);
  }

  tombstonedIds(): string[] {
    return [...this.tombstones];
  }

  // activityForRender extracts the SessionActivitySummary the
  // sidebar's chips/labels render against. Returns null when the
  // row has no activity_summary yet.
  activityForRender(id: string): SessionActivitySummary | null {
    const row = this.rows.get(id);
    if (!row || !row.activity_summary) return null;
    return normalizeSessionActivity({
      session_id: id,
      ...row.activity_summary,
    });
  }

  subscribe(fn: SessionStoreSubscriber): () => void {
    this.subscribers.add(fn);
    return () => this.subscribers.delete(fn);
  }

  // debugDump returns a structural snapshot of the store. Used by
  // the /_debug/session-list page to render the SPA's state without
  // browser devtools.
  debugDump(): {
    cursor: string | null;
    rows: SessionRow[];
    tombstones: string[];
    recent_events: SessionListDebugEvent[];
  } {
    return {
      cursor: this.cursor,
      rows: this.list(),
      tombstones: this.tombstonedIds(),
      recent_events: getSessionListDebugSnapshot().events,
    };
  }

  private publishDebugStoreState(): void {
    updateSessionListDebugStore({
      cursor: this.cursor,
      rows: this.list(),
      tombstones: this.tombstonedIds(),
    });
  }

  private recordDebugEvent(input: Omit<SessionListDebugEvent, "seq" | "at">): void {
    recordSessionListDebugEvent(input);
  }

  private emit(event: SessionStoreEvent): void {
    for (const sub of this.subscribers) {
      try {
        sub(event);
      } catch {
        // Subscriber errors must not bring down the store.
      }
    }
  }
}

// normalizeSessionRowUpdate parses one wire frame from
// /api/sessions/events. Returns null on malformed input; the SSE
// handler logs and drops.
export function normalizeSessionRowUpdate(value: unknown): SessionRowUpdatePayload | null {
  if (!isRecord(value)) return null;
  const cursor = stringField(value, "cursor");
  const rowRaw = value.row;
  if (!cursor || !isRecord(rowRaw)) return null;
  const id = stringField(rowRaw, "id");
  const owner = stringField(rowRaw, "owner");
  const sessionScope = stringField(rowRaw, "session_scope");
  // display_name is a required server field on every row-update frame; a
  // frame missing it is a contract violation, so drop it like any other
  // malformed frame rather than locally re-deriving a title fallback.
  const displayName = stringField(rowRaw, "display_name");
  if (!id || !owner || !sessionScope || !displayName) return null;
  const visible = rowRaw.visible === true;
  const rowVersion = numberField(rowRaw, "row_version");
  const sidebarPosition = numberField(rowRaw, "sidebar_position");
  if (rowVersion === null || sidebarPosition === null) return null;
  return {
    cursor,
    row: {
      id,
      owner,
      mode: stringField(rowRaw, "mode") ?? "",
      session_scope: sessionScope,
      pod_name: stringField(rowRaw, "pod_name") ?? undefined,
      name: nullableStringField(rowRaw, "name"),
      display_name: displayName,
      visible,
      status: stringField(rowRaw, "status") ?? "Pending",
      requested_at: stringField(rowRaw, "requested_at") ?? undefined,
      created_at: stringField(rowRaw, "created_at") ?? undefined,
      updated_at: stringField(rowRaw, "updated_at") ?? undefined,
      ready_at: stringField(rowRaw, "ready_at") ?? undefined,
      terminating_at: stringField(rowRaw, "terminating_at") ?? undefined,
      activity_summary: isRecord(rowRaw.activity_summary)
        ? (rowRaw.activity_summary as Record<string, unknown>)
        : undefined,
      test_state: isRecord(rowRaw.test_state)
        ? (rowRaw.test_state as Record<string, unknown>)
        : undefined,
      rollout_state: isRecord(rowRaw.rollout_state)
        ? (rowRaw.rollout_state as Record<string, unknown>)
        : undefined,
      repos: Array.isArray(rowRaw.repos)
        ? (rowRaw.repos as unknown[]).filter(
            (entry): entry is string => typeof entry === "string",
          )
        : [],
      clone_state: isRecord(rowRaw.clone_state)
        ? (rowRaw.clone_state as Record<string, unknown>)
        : undefined,
      capabilities: Array.isArray(rowRaw.capabilities)
        ? (rowRaw.capabilities as unknown[]).filter(
            (entry): entry is string => typeof entry === "string",
          )
        : [],
      bug_label: normalizeSessionBugLabel(rowRaw.bug_label),
      bug_labels: normalizeSessionBugLabels(rowRaw.bug_labels),
      model: stringField(rowRaw, "model") ?? undefined,
      effort: stringField(rowRaw, "effort") ?? undefined,
      runtime_model: stringField(rowRaw, "runtime_model") ?? undefined,
      runtime_effort: stringField(rowRaw, "runtime_effort") ?? undefined,
      runtime_configured_at: stringField(rowRaw, "runtime_configured_at") ?? undefined,
      runtime_context_window_tokens: nonNegativeNumberField(
        rowRaw,
        "runtime_context_window_tokens",
      ) ?? undefined,
      runtime_context_window_source:
        stringField(rowRaw, "runtime_context_window_source") ?? undefined,
      runtime_context_window_observed_at:
        stringField(rowRaw, "runtime_context_window_observed_at") ?? undefined,
      provider_rate_limit_info: isRecord(rowRaw.provider_rate_limit_info)
        ? (rowRaw.provider_rate_limit_info as Record<string, unknown>)
        : undefined,
      provider_rate_limit_observed_at:
        stringField(rowRaw, "provider_rate_limit_observed_at") ?? undefined,
      compaction_count: nonNegativeNumberField(rowRaw, "compaction_count") ?? undefined,
      agent_avatar_id: stringField(rowRaw, "agent_avatar_id") ?? undefined,
      system_avatar_id: stringField(rowRaw, "system_avatar_id") ?? undefined,
      sidebar_position: sidebarPosition,
      row_version: rowVersion,
    },
  };
}

// --- helpers ---------------------------------------------------------------

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function normalizeSessionBugLabel(value: unknown): SessionBugLabel | null {
  if (!isRecord(value)) return null;
  const name = stringField(value, "name");
  const slug = stringField(value, "slug");
  const displayName = normalizeBugLabelDisplayName(stringField(value, "display_name") ?? name);
  if (!name || !slug || !displayName) return null;
  const id = numberField(value, "id");
  return {
    ...(id !== null ? { id } : {}),
    name,
    slug,
    display_name: displayName,
  };
}

function normalizeSessionBugLabels(value: unknown): SessionBugLabel[] {
  if (!Array.isArray(value)) return [];
  return value
    .map((entry) => normalizeSessionBugLabel(entry))
    .filter((label): label is SessionBugLabel => Boolean(label));
}

function stringField(value: Record<string, unknown>, key: string): string | null {
  const field = value[key];
  return typeof field === "string" && field ? field : null;
}

function nullableStringField(value: Record<string, unknown>, key: string): string | null {
  const field = value[key];
  if (typeof field === "string") return field;
  return null;
}

function numberField(value: Record<string, unknown>, key: string): number | null {
  const field = value[key];
  if (typeof field === "number" && Number.isFinite(field)) return field;
  if (typeof field === "string" && field !== "") {
    const n = Number(field);
    if (Number.isFinite(n)) return n;
  }
  return null;
}

function nonNegativeNumberField(value: Record<string, unknown>, key: string): number | null {
  const n = numberField(value, key);
  return n === null ? null : Math.max(0, Math.floor(n));
}

function rowCursorNumber(cursor: string | null): number | null {
  if (!cursor) return null;
  const n = Number(cursor);
  return Number.isFinite(n) ? n : null;
}

function maxRowCursor(a: string | null, b: string | null): string | null {
  if (!a) return b;
  if (!b) return a;
  const an = rowCursorNumber(a);
  const bn = rowCursorNumber(b);
  if (an !== null && bn !== null) return an >= bn ? a : b;
  return a >= b ? a : b;
}

function rowVersionAfterCursor(rowVersion: number, cursor: string): boolean {
  const cursorNumber = rowCursorNumber(cursor);
  if (cursorNumber === null) return false;
  return rowVersion > cursorNumber;
}

function compareSessionRowsForSidebar(a: SessionRow, b: SessionRow): number {
  if (a.sidebar_position !== b.sidebar_position) {
    return b.sidebar_position - a.sidebar_position;
  }
  const aCreated = Date.parse(a.created_at ?? "");
  const bCreated = Date.parse(b.created_at ?? "");
  const aCreatedSafe = Number.isFinite(aCreated) ? aCreated : 0;
  const bCreatedSafe = Number.isFinite(bCreated) ? bCreated : 0;
  if (aCreatedSafe !== bCreatedSafe) return bCreatedSafe - aCreatedSafe;
  return b.id.localeCompare(a.id, undefined, { numeric: true });
}
