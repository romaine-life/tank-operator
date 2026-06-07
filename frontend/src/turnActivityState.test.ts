import { test, expect } from "vitest";

import {
  beginTurnActivityLoad,
  completeTurnActivityLoad,
  failTurnActivityLoad,
  turnActivityLoadIsLoaded,
  turnActivityLoadVisibleSnapshot,
  turnActivityGroupIsActive,
  turnActivityShouldStartLoad,
  turnActivityShellIsDurablyActive,
  type TurnActivityLoadSnapshot,
  type TurnActivityLoadState,
} from "./turnActivityState.ts";

type Entry = { id: string };
type PageInfo = { page: number; pageCount: number };

function snapshot(
  entries: Entry[],
  context: Entry | null,
  page = 1,
): TurnActivityLoadSnapshot<Entry, PageInfo> {
  return {
    entries,
    context,
    pageInfo: { page, pageCount: 2 },
    requestedPage: page,
    loadedAt: 1_000 + page,
  };
}

test("durable active turn activity shell remains active without client active turn id", () => {
  expect(turnActivityGroupIsActive(
          { turnId: "turn-1", status: "active", active: true },
          "turn-1",
          null,
        )).toBe(true);
});

test("client active turn id keeps locally-compacted active activity active", () => {
  expect(turnActivityGroupIsActive(undefined, "turn-1", "turn-1")).toBe(true);
});

test("completed turn activity shell is not active without a matching active turn", () => {
  expect(turnActivityShellIsDurablyActive({ turnId: "turn-1", status: "completed", active: false })).toBe(false);
  expect(turnActivityGroupIsActive(
          { turnId: "turn-1", status: "completed", active: false },
          "turn-1",
          null,
        )).toBe(false);
});

test("needs-input turn activity shell is a handoff, not active-running UI", () => {
  expect(turnActivityShellIsDurablyActive({ turnId: "turn-1", status: "needs_input", active: true })).toBe(false);
  expect(turnActivityGroupIsActive(
          { turnId: "turn-1", status: "needs_input", active: true },
          "turn-1",
          "turn-1",
        )).toBe(false);
});

test("fresh turn navigation shows no body until context and activity commit together", () => {
  const loading = beginTurnActivityLoad<Entry, PageInfo>(
    undefined,
    1,
    undefined,
    "initial",
  );
  expect(turnActivityLoadVisibleSnapshot(loading)).toBeUndefined();

  const loaded = completeTurnActivityLoad(
    loading,
    1,
    snapshot([{ id: "assistant" }], { id: "prompt" }),
  );
  expect(turnActivityLoadIsLoaded(loaded)).toBe(true);
  expect(turnActivityLoadVisibleSnapshot(loaded)?.context?.id).toBe("prompt");
  expect(turnActivityLoadVisibleSnapshot(loaded)?.entries.map((e) => e.id)).toEqual([
    "assistant",
  ]);
});

test("cached turn navigation reuses the atomic loaded snapshot", () => {
  const loaded: TurnActivityLoadState<Entry, PageInfo> = {
    status: "loaded",
    snapshot: snapshot([{ id: "assistant" }], { id: "prompt" }),
  };
  expect(turnActivityShouldStartLoad(loaded, 1, false)).toBe(false);
  expect(turnActivityLoadVisibleSnapshot(loaded)?.context?.id).toBe("prompt");
});

test("page switches hide the old page until the requested page commits", () => {
  const loaded: TurnActivityLoadState<Entry, PageInfo> = {
    status: "loaded",
    snapshot: snapshot([{ id: "old-page" }], { id: "old-prompt" }, 1),
  };
  const loading = beginTurnActivityLoad(loaded, 2, 2, "page");

  expect(turnActivityLoadVisibleSnapshot(loading)).toBeUndefined();

  const next = completeTurnActivityLoad(
    loading,
    2,
    snapshot([{ id: "new-page" }], { id: "new-prompt" }, 2),
  );
  expect(turnActivityLoadVisibleSnapshot(next)?.entries.map((e) => e.id)).toEqual([
    "new-page",
  ]);
});

test("stale activity responses cannot replace the current page", () => {
  const first = beginTurnActivityLoad<Entry, PageInfo>(undefined, 1, 1, "initial");
  const second = beginTurnActivityLoad(first, 2, 2, "page");
  const stale = completeTurnActivityLoad(
    second,
    1,
    snapshot([{ id: "stale-page" }], { id: "stale-prompt" }, 1),
  );

  expect(stale).toBe(second);
  expect(turnActivityLoadVisibleSnapshot(stale)).toBeUndefined();
});

test("load errors expose retry state without fabricating partial activity", () => {
  const loading = beginTurnActivityLoad<Entry, PageInfo>(
    undefined,
    1,
    undefined,
    "initial",
  );
  const failed = failTurnActivityLoad(loading, 1, {
    kind: "timeout",
    attempts: 1,
  });

  expect(failed?.status).toBe("error");
  expect(turnActivityLoadVisibleSnapshot(failed)).toBeUndefined();
});

test("live refresh failures keep the last coherent snapshot visible", () => {
  const loaded: TurnActivityLoadState<Entry, PageInfo> = {
    status: "loaded",
    snapshot: snapshot([{ id: "assistant" }], { id: "prompt" }),
  };
  const loading = beginTurnActivityLoad(loaded, 2, 1, "live-refresh");
  expect(turnActivityLoadVisibleSnapshot(loading)?.context?.id).toBe("prompt");

  const failed = failTurnActivityLoad(loading, 2, {
    kind: "live-refresh",
    attempts: 1,
  });
  expect(turnActivityLoadVisibleSnapshot(failed)?.entries.map((e) => e.id)).toEqual([
    "assistant",
  ]);
});
