import { test } from "node:test";
import assert from "node:assert/strict";

import { isCanonical, stampEventID } from "./cosmos.js";

// Pins which codex SDK event types make it into the durable Cosmos log
// vs which stay live-only. Drift here affects the SPA's history-replay
// correctness.

test("canonical: thread.started", () => {
  assert.equal(isCanonical({ type: "thread.started", thread_id: "t1" }), true);
});

test("canonical: turn.completed + turn.failed", () => {
  assert.equal(isCanonical({ type: "turn.completed", usage: {} } as never), true);
  assert.equal(isCanonical({ type: "turn.failed", error: { message: "x" } } as never), true);
});

test("canonical: item.completed (the main durable signal)", () => {
  assert.equal(
    isCanonical({
      type: "item.completed",
      item: { id: "i1", type: "agent_message" },
    } as never),
    true,
  );
});

test("canonical: error (thread-level)", () => {
  assert.equal(isCanonical({ type: "error", message: "boom" } as never), true);
});

test("NOT canonical: turn.started (structural marker, not user-visible)", () => {
  assert.equal(isCanonical({ type: "turn.started" } as never), false);
});

test("NOT canonical: item.started / item.updated (partial / streaming)", () => {
  assert.equal(isCanonical({ type: "item.started", item: {} } as never), false);
  assert.equal(isCanonical({ type: "item.updated", item: {} } as never), false);
});

test("NOT canonical: unknown event types", () => {
  assert.equal(isCanonical({ type: "weird_thing" }), false);
  assert.equal(isCanonical({ type: "" }), false);
});

test("stampEventID attaches a v4 uuid without mutating the input", () => {
  const before = { type: "thread.started", thread_id: "t1" };
  const after = stampEventID(before);
  assert.equal(typeof after.uuid, "string");
  assert.match(
    after.uuid,
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
  );
  // Input untouched: pins purity at this boundary so retries don't
  // double-stamp.
  assert.equal((before as { uuid?: string }).uuid, undefined);
});

test("stampEventID produces a fresh uuid each call (no accidental aliasing)", () => {
  const a = stampEventID({ type: "thread.started" });
  const b = stampEventID({ type: "thread.started" });
  assert.notEqual(a.uuid, b.uuid);
});
