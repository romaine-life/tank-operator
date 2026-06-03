import { beforeEach, test } from "node:test";
import assert from "node:assert/strict";

import {
  SessionStore,
  normalizeSessionRowUpdate,
  type SessionRow,
} from "./sessionStore";
import {
  getSessionListDebugSnapshot,
  resetSessionListDebugForTest,
} from "./sessionListDebug";

function row(id: string, overrides: Partial<SessionRow> = {}): SessionRow {
  return {
    id,
    owner: "u@example.com",
    mode: "claude_gui",
    session_scope: "default",
    visible: true,
    status: "Active",
    repos: [],
    capabilities: [],
    sidebar_position: 1,
    row_version: 1,
    ...overrides,
  };
}

beforeEach(() => {
  resetSessionListDebugForTest();
});

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

test("debug dump includes recent session-list row events", () => {
  const store = new SessionStore();
  store.applyRowUpdate({
    cursor: "1",
    row: row("8", {
      status: "Pending",
      row_version: 1,
      agent_avatar_id: "av_agent",
      system_avatar_id: "av_system",
    }),
  });

  const snapshot = getSessionListDebugSnapshot();
  const dump = store.debugDump();
  assert.equal(snapshot.store?.rows[0]?.id, "8");
  assert.equal(snapshot.store?.rows[0]?.agent_avatar_id, "av_agent");
  assert.ok(
    snapshot.events.some((event) => event.kind === "row-added" && event.session_id === "8"),
    "row-added event should be retained",
  );
  assert.deepEqual(dump.recent_events, snapshot.events);
});

test("applyRowUpdate ignores older row versions", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "10", row: row("8", { status: "Active", row_version: 10 }) });

  const applied = store.applyRowUpdate({
    cursor: "8",
    row: row("8", { status: "Pending", row_version: 8 }),
  });

  assert.equal(applied, false, "older row update must not replace newer state");
  assert.equal(store.list()[0].status, "Active");
  assert.equal(store.getCursor(), "10");
});

test("applySnapshot does not regress rows updated after the snapshot was read", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "2", row: row("51", { status: "Active", row_version: 2 }) });

  store.applySnapshot([
    row("51", { status: "Pending", row_version: 1 }),
  ], "1");

  const list = store.list();
  assert.equal(list.length, 1);
  assert.equal(list[0].status, "Active");
  assert.equal(store.getCursor(), "2");
});

test("applySnapshot preserves rows newer than the snapshot cursor", () => {
  const store = new SessionStore();
  store.applySnapshot([
    row("old", { row_version: 4, sidebar_position: 2 }),
  ], "4");
  store.applyRowUpdate({
    cursor: "5",
    row: row("new", { row_version: 5, sidebar_position: 1 }),
  });

  store.applySnapshot([
    row("old", { row_version: 4, sidebar_position: 2 }),
  ], "4");

  assert.deepEqual(store.list().map((r) => r.id), ["old", "new"]);
  assert.equal(store.getCursor(), "5");
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

test("rename row updates keep assigned avatar ids", () => {
  const store = new SessionStore();
  const update = normalizeSessionRowUpdate({
    cursor: "2",
    row: {
      id: "8",
      owner: "u@example.com",
      mode: "codex_gui",
      session_scope: "default",
      name: "renamed session",
      visible: true,
      status: "Active",
      repos: [],
      capabilities: ["spirelens_mcp"],
      bug_label: {
        id: 4,
        name: "Slow checkout",
        slug: "slow-checkout",
        display_name: "bug: Slow checkout",
      },
      agent_avatar_id: "jp1-malcolm",
      system_avatar_id: "system-logo",
      sidebar_position: 1,
      row_version: 2,
    },
  });

  assert.ok(update, "valid rename row update must parse");
  store.applyRowUpdate(update);

  const [updated] = store.list();
  assert.equal(updated.name, "renamed session");
  assert.deepEqual(updated.capabilities, ["spirelens_mcp"]);
  assert.equal(updated.bug_label?.display_name, "bug: Slow checkout");
  assert.equal(updated.agent_avatar_id, "jp1-malcolm");
  assert.equal(updated.system_avatar_id, "system-logo");
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
      model: "gpt-5.5",
      effort: "xhigh",
      runtime_model: "gpt-5.5",
      runtime_effort: "xhigh",
      runtime_configured_at: "2026-05-21T00:00:00Z",
      agent_avatar_id: "jp1-malcolm",
      system_avatar_id: "system-logo",
      sidebar_position: 7,
      row_version: 1,
    },
  });
  assert.ok(good, "valid payload must parse");
  assert.equal(good!.row.id, "8");
  assert.equal(good!.row.model, "gpt-5.5");
  assert.equal(good!.row.effort, "xhigh");
  assert.equal(good!.row.runtime_model, "gpt-5.5");
  assert.equal(good!.row.runtime_effort, "xhigh");
  assert.equal(good!.row.runtime_configured_at, "2026-05-21T00:00:00Z");
  assert.equal(good!.row.agent_avatar_id, "jp1-malcolm");
  assert.equal(good!.row.system_avatar_id, "system-logo");
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
