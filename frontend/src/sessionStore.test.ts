import { beforeEach, test, expect } from "vitest";
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
    name: id,
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
  expect(list.length, "should have one row for id 8 only").toBe(1);
  expect(list[0].status).toBe("Active");
  expect(store.getCursor()).toBe("2");
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
  expect(snapshot.store?.rows[0]?.id).toBe("8");
  expect(snapshot.store?.rows[0]?.agent_avatar_id).toBe("av_agent");
  expect(snapshot.events.some((event) => event.kind === "row-added" && event.session_id === "8"), "row-added event should be retained").toBeTruthy();
  expect(dump.recent_events).toEqual(snapshot.events);
});

test("applyRowUpdate ignores older row versions", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "10", row: row("8", { status: "Active", row_version: 10 }) });

  const applied = store.applyRowUpdate({
    cursor: "8",
    row: row("8", { status: "Pending", row_version: 8 }),
  });

  expect(applied, "older row update must not replace newer state").toBe(false);
  expect(store.list()[0].status).toBe("Active");
  expect(store.getCursor()).toBe("10");
});

test("applySnapshot does not regress rows updated after the snapshot was read", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "2", row: row("51", { status: "Active", row_version: 2 }) });

  store.applySnapshot([
    row("51", { status: "Pending", row_version: 1 }),
  ], "1");

  const list = store.list();
  expect(list.length).toBe(1);
  expect(list[0].status).toBe("Active");
  expect(store.getCursor()).toBe("2");
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

  expect(store.list().map((r) => r.id)).toEqual(["old", "new"]);
  expect(store.getCursor()).toBe("5");
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
  expect(store.list().length, "row should be removed after optimistic delete").toBe(0);
  expect(store.isTombstoned("8"), "id should be tombstoned").toBeTruthy();

  // Server-side pod-informer event arriving after the optimistic
  // delete — must be dropped at the store boundary, not re-added.
  const applied = store.applyRowUpdate({
    cursor: "9",
    row: row("8", { status: "Failed", row_version: 9 }),
  });
  expect(applied, "tombstoned row updates must be dropped").toBe(false);
  expect(store.list().length, "tombstoned id must not reappear").toBe(0);
});

// TestVisibleFalseTombstonesAndRemoves locks in the server-initiated
// delete contract: a row arriving with visible=false tombstones the
// id (so any later live event for that id is dropped too) and
// removes it from the cache.
test("applyRowUpdate with visible=false tombstones and removes", () => {
  const store = new SessionStore();
  store.applyRowUpdate({ cursor: "5", row: row("8", { row_version: 5 }) });
  expect(store.list().length).toBe(1);

  store.applyRowUpdate({ cursor: "6", row: row("8", { visible: false, row_version: 6 }) });
  expect(store.list().length, "visible=false must remove the row").toBe(0);
  expect(store.isTombstoned("8"), "visible=false must tombstone the id").toBeTruthy();

  // Late post-delete pod event landing on the wire after MarkDeleted
  // — must be dropped (this is the resurrection bug class).
  store.applyRowUpdate({ cursor: "7", row: row("8", { status: "Failed", row_version: 7 }) });
  expect(store.list().length).toBe(0);
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

  expect(store.list().map((r) => r.id)).toEqual(["a", "b", "c"]);
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
      bug_labels: [
        {
          id: 4,
          name: "Slow checkout",
          slug: "slow-checkout",
          display_name: "bug: Slow checkout",
        },
        {
          id: 5,
          name: "Transcript",
          slug: "transcript",
          display_name: "bug: Transcript",
        },
      ],
      agent_avatar_id: "jp1-malcolm",
      system_avatar_id: "system-logo",
      sidebar_position: 1,
      row_version: 2,
    },
  });

  expect(update, "valid rename row update must parse").toBeTruthy();
  store.applyRowUpdate(update);

  const [updated] = store.list();
  expect(updated.name).toBe("renamed session");
  expect(updated.capabilities).toEqual(["spirelens_mcp"]);
  expect(updated.bug_label?.display_name).toBe("Slow checkout");
  expect(updated.bug_labels?.map((label) => label.display_name)).toEqual([
        "Slow checkout",
        "Transcript",
      ]);
  expect(updated.agent_avatar_id).toBe("jp1-malcolm");
  expect(updated.system_avatar_id).toBe("system-logo");
});

test("applyLocalOrder preserves drag order through later row updates", () => {
  const store = new SessionStore();
  store.applySnapshot([
    row("a", { sidebar_position: 3, row_version: 1 }),
    row("b", { sidebar_position: 2, row_version: 2 }),
    row("c", { sidebar_position: 1, row_version: 3 }),
  ], "3");

  expect(store.applyLocalOrder(["b", "c", "a"])).toBe(true);
  expect(store.list().map((r) => r.id)).toEqual(["b", "c", "a"]);

  store.applyRowUpdate({
    cursor: "4",
    row: row("a", { sidebar_position: 1, row_version: 4, rollout_state: { active: true } }),
  });

  expect(store.list().map((r) => r.id)).toEqual(["b", "c", "a"]);
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
  expect(store.isTombstoned("8"), "tombstoned by optimistic delete").toBeTruthy();

  // refresh() returns the row as visible — the optimistic delete
  // never made it to the server.
  store.applySnapshot([row("8", { row_version: 10 })], "10");
  expect(store.list().length, "row should be back").toBe(1);
  expect(store.isTombstoned("8"), "tombstone must clear").toBe(false);
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
  expect(store.isTombstoned("8"), "tombstone must persist when server agrees").toBeTruthy();

  // Late wire delivery for the deleted id — still dropped.
  const applied = store.applyRowUpdate({
    cursor: "11",
    row: row("8", { status: "Failed", row_version: 11 }),
  });
  expect(applied).toBe(false);
});

// TestNormalizeSessionRowUpdate pins the wire-shape parse. session_scope,
// row_version, id, and name are all required; anything missing → null
// (handler logs + drops). No defaulting silently.
test("normalizeSessionRowUpdate rejects malformed payloads", () => {
  expect(normalizeSessionRowUpdate(null)).toBe(null);
  expect(normalizeSessionRowUpdate({})).toBe(null);
  expect(normalizeSessionRowUpdate({ cursor: "1", row: { id: "8", visible: true, row_version: 1 } }), "missing owner + session_scope must be rejected").toBe(null);
  expect(normalizeSessionRowUpdate({
          cursor: "1",
          row: { id: "8", owner: "u@example.com", session_scope: "default", visible: true },
        }), "missing row_version + sidebar_position must be rejected").toBe(null);
  expect(normalizeSessionRowUpdate({
          cursor: "1",
          row: {
            id: "8",
            owner: "u@example.com",
            session_scope: "default",
            visible: true,
            sidebar_position: 1,
            row_version: 1,
          },
        }), "missing name must be rejected (server-canonical title is required)").toBe(null);
  const good = normalizeSessionRowUpdate({
    cursor: "1",
    row: {
      id: "8",
      owner: "u@example.com",
      session_scope: "default",
      name: "session-8",
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
  expect(good, "valid payload must parse").toBeTruthy();
  expect(good!.row.id).toBe("8");
  expect(good!.row.name).toBe("session-8");
  expect(good!.row.model).toBe("gpt-5.5");
  expect(good!.row.effort).toBe("xhigh");
  expect(good!.row.runtime_model).toBe("gpt-5.5");
  expect(good!.row.runtime_effort).toBe("xhigh");
  expect(good!.row.runtime_configured_at).toBe("2026-05-21T00:00:00Z");
  expect(good!.row.agent_avatar_id).toBe("jp1-malcolm");
  expect(good!.row.system_avatar_id).toBe("system-logo");
  expect(good!.row.sidebar_position).toBe(7);
  expect(good!.row.row_version).toBe(1);
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
  expect(events).toEqual(["row-added", "row-replaced", "row-removed"]);
});
