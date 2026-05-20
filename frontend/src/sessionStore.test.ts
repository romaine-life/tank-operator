import { test } from "node:test";
import assert from "node:assert/strict";

import {
  SessionStore,
  normalizeSessionRowUpdate,
  type SessionRow,
} from "./sessionStore";

function row(id: string, overrides: Partial<SessionRow> = {}): SessionRow {
  return {
    id,
    owner: "u@example.com",
    mode: "claude_gui",
    session_scope: "default",
    visible: true,
    status: "Active",
    repos: [],
    sidebar_position: 1,
    row_version: 1,
    ...overrides,
  };
}

// TestStoreReplaceByIdNoDuplicates is the core invariant: a row
// update for an id already in the cache replaces it in place — no
// duplicate rows, no merge, no event-type discriminator. This is the
// shape that retires the pre-#525 placeholder-synthesis bug class by
// construction: there's nothing to synthesize because the wire
// always carries the full row.
test("applyRowUpdate replaces by id without duplicating", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "1", row: row("8", { status: "Pending", row_version: 1 }) });
  store.applyRowUpdate({ cursor: "2", row: row("8", { status: "Active", row_version: 2 }) });

  const list = store.list();
  assert.equal(list.length, 1, "should have one row for id 8 only");
  assert.equal(list[0].status, "Active");
  assert.equal(store.getCursor(), "2");
});

// TestOptimisticDeleteTombstones is the protective-layer
// behavior the user asked for: click delete → row gone immediately,
// subsequent server-side wire payloads for the same id are dropped.
// This is what makes the SPA resilient to backend wonk like
// post-delete pod-informer events.
test("optimisticDelete tombstones the id and drops subsequent wire updates", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "5", row: row("8", { status: "Active", row_version: 5 }) });

  store.optimisticDelete("8");
  assert.equal(store.list().length, 0, "row should be removed after optimistic delete");
  assert.ok(store.isTombstoned("8"), "id should be tombstoned");

  // Server-side pod-informer event arriving after the optimistic
  // delete — must be dropped at the store boundary, not re-added.
  const applied = store.applyRowUpdate({
    cursor: "9",
    row: row("8", { status: "Failed", row_version: 9 }),
  });
  assert.equal(applied, false, "tombstoned row updates must be dropped");
  assert.equal(store.list().length, 0, "tombstoned id must not reappear");
});

// TestVisibleFalseTombstonesAndRemoves locks in the server-initiated
// delete contract: a row arriving with visible=false tombstones the
// id (so any later live event for that id is dropped too) and
// removes it from the cache.
test("applyRowUpdate with visible=false tombstones and removes", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "5", row: row("8", { row_version: 5 }) });
  assert.equal(store.list().length, 1);

  store.applyRowUpdate({ cursor: "6", row: row("8", { visible: false, row_version: 6 }) });
  assert.equal(store.list().length, 0, "visible=false must remove the row");
  assert.ok(store.isTombstoned("8"), "visible=false must tombstone the id");

  // Late post-delete pod event landing on the wire after MarkDeleted
  // — must be dropped (this is the resurrection bug class).
  store.applyRowUpdate({ cursor: "7", row: row("8", { status: "Failed", row_version: 7 }) });
  assert.equal(store.list().length, 0);
});

test("list uses sidebar_position instead of row_version", () => {
  const store = new SessionStore();
  store.applySnapshot([
    row("a", { sidebar_position: 3, row_version: 1 }),
    row("b", { sidebar_position: 2, row_version: 2 }),
    row("c", { sidebar_position: 1, row_version: 3 }),
  ], "3");

  store.applyRowUpdate({
    cursor: "99",
    row: row("c", { sidebar_position: 1, row_version: 99, test_state: { active: true } }),
  });

  assert.deepEqual(store.list().map((r) => r.id), ["a", "b", "c"]);
});

test("applyLocalOrder preserves drag order through later row updates", () => {
  const store = new SessionStore();
  store.applySnapshot([
    row("a", { sidebar_position: 3, row_version: 1 }),
    row("b", { sidebar_position: 2, row_version: 2 }),
    row("c", { sidebar_position: 1, row_version: 3 }),
  ], "3");

  assert.equal(store.applyLocalOrder(["b", "c", "a"]), true);
  assert.deepEqual(store.list().map((r) => r.id), ["b", "c", "a"]);

  store.applyRowUpdate({
    cursor: "4",
    row: row("a", { sidebar_position: 1, row_version: 4, rollout_state: { active: true } }),
  });

  assert.deepEqual(store.list().map((r) => r.id), ["b", "c", "a"]);
});

// TestApplySnapshotClearsTombstonesForVisibleIds is the recovery
// path for an optimistic delete that failed server-side: the user
// clicked X locally, the DELETE API call never reached the server
// (network blip, browser closed mid-flight), the next refresh()
// returns the row still visible — the local tombstone must clear so
// the row reappears.
test("applySnapshot clears tombstones for ids the server still considers visible", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "5", row: row("8", { row_version: 5 }) });
  store.optimisticDelete("8");
  assert.ok(store.isTombstoned("8"), "tombstoned by optimistic delete");

  // refresh() returns the row as visible — the optimistic delete
  // never made it to the server.
  store.applySnapshot([row("8", { row_version: 10 })], "10");
  assert.equal(store.list().length, 1, "row should be back");
  assert.equal(store.isTombstoned("8"), false, "tombstone must clear");
});

// TestApplySnapshotPreservesTombstoneWhenServerAgrees confirms the
// inverse: if the user deleted locally AND the server confirms
// (visible=false → row not in snapshot), the tombstone stays so the
// next post-delete pod event is still dropped.
test("applySnapshot preserves tombstones the server did not contradict", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "5", row: row("8", { row_version: 5 }) });
  store.optimisticDelete("8");

  // refresh returns no row for id 8 — server agreed the delete went through.
  store.applySnapshot([], "10");
  assert.ok(store.isTombstoned("8"), "tombstone must persist when server agrees");

  // Late wire delivery for the deleted id — still dropped.
  const applied = store.applyRowUpdate({
    cursor: "11",
    row: row("8", { status: "Failed", row_version: 11 }),
  });
  assert.equal(applied, false);
});

// TestNormalizeSessionRowUpdate pins the wire-shape parse. session_scope,
// row_version, and id are all required; anything missing → null
// (handler logs + drops). No defaulting silently.
test("normalizeSessionRowUpdate rejects malformed payloads", () => {
  assert.equal(normalizeSessionRowUpdate(null), null);
  assert.equal(normalizeSessionRowUpdate({}), null);
  assert.equal(
    normalizeSessionRowUpdate({ cursor: "1", row: { id: "8", visible: true, row_version: 1 } }),
    null,
    "missing owner + session_scope must be rejected",
  );
  assert.equal(
    normalizeSessionRowUpdate({
      cursor: "1",
      row: { id: "8", owner: "u@example.com", session_scope: "default", visible: true },
    }),
    null,
    "missing row_version + sidebar_position must be rejected",
  );
  const good = normalizeSessionRowUpdate({
    cursor: "1",
    row: {
      id: "8",
      owner: "u@example.com",
      session_scope: "default",
      visible: true,
      status: "Active",
      sidebar_position: 7,
      row_version: 1,
    },
  });
  assert.ok(good, "valid payload must parse");
  assert.equal(good!.row.id, "8");
  assert.equal(good!.row.sidebar_position, 7);
  assert.equal(good!.row.row_version, 1);
});

// TestStoreSubscribeEmitsEvents pins the subscriber contract so the
// App.tsx integration's re-render trigger fires on every state
// change.
test("subscribers receive row-added / row-replaced / row-removed events", () => {
  const store = new SessionStore();
  const events: string[] = [];
  const unsub = store.subscribe((e) => events.push(e.kind));

  store.applyRowUpdate({ cursor: "1", row: row("8") });
  store.applyRowUpdate({ cursor: "2", row: row("8", { status: "Failed", row_version: 2 }) });
  store.optimisticDelete("8");

  unsub();
  assert.deepEqual(events, ["row-added", "row-replaced", "row-removed"]);
});
