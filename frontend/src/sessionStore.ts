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

  // applySnapshot replaces the row cache wholesale from a server
  // snapshot. Clears tombstones for ids the server still considers
  // visible — that's the recovery path for a failed optimistic
  // delete. Tombstones for ids the server omits stay in place so a
  // post-delete row-update for that id is still dropped.
  applySnapshot(rows: SessionRow[], snapshotCursor: string | null): void {
    const visibleByID = new Map<string, SessionRow>();
    for (const row of rows) {
      if (row.visible) visibleByID.set(row.id, row);
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
    this.cursor = snapshotCursor;
    this.emit({ kind: "snapshot-replaced" });
  }

  // applyRowUpdate handles one wire payload (catch-up or live).
  // Returns true if the store state changed.
  applyRowUpdate(payload: SessionRowUpdatePayload): boolean {
    const row = payload.row;
    if (!row || !row.id) return false;

    // Cursor monotonicity: out-of-order deliveries are dropped at
    // the application layer. The cursor advance is done in App.tsx
    // when applying SSE events, not here, because the SSE handler
    // already drops payloads ≤ current cursor before they reach us.
    // applyRowUpdate trusts the payload is meant to be applied.

    if (this.tombstones.has(row.id)) {
      this.emit({ kind: "row-dropped-tombstoned", id: row.id });
      return false;
    }
    if (!row.visible) {
      // Server-initiated delete (registry.MarkDeleted or any other
      // mutation that flipped visible=false). Tombstone the id and
      // remove from cache.
      this.tombstones.add(row.id);
      if (this.rows.has(row.id)) {
        this.rows.delete(row.id);
        this.emit({ kind: "row-removed", id: row.id });
        return true;
      }
      return false;
    }
    const had = this.rows.has(row.id);
    this.rows.set(row.id, row);
    this.cursor = payload.cursor;
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
    if (this.rows.has(id)) {
      this.rows.delete(id);
      this.emit({ kind: "row-removed", id });
    }
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
  }

  // list returns the cached rows in row_version-descending order
  // (most recently changed first). The App-side ordering hook re-
  // orders by user-pinned position; this default order is just
  // stable + deterministic.
  list(): SessionRow[] {
    return [...this.rows.values()].sort((a, b) => b.row_version - a.row_version);
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
  } {
    return {
      cursor: this.cursor,
      rows: this.list(),
      tombstones: this.tombstonedIds(),
    };
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
  if (!id || !owner || !sessionScope) return null;
  const visible = rowRaw.visible === true;
  const rowVersion = numberField(rowRaw, "row_version");
  if (rowVersion === null) return null;
  return {
    cursor,
    row: {
      id,
      owner,
      mode: stringField(rowRaw, "mode") ?? "",
      session_scope: sessionScope,
      pod_name: stringField(rowRaw, "pod_name") ?? undefined,
      name: nullableStringField(rowRaw, "name"),
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
      row_version: rowVersion,
    },
  };
}

// --- helpers ---------------------------------------------------------------

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
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
