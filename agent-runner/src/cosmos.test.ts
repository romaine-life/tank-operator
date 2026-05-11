import { test } from "node:test";
import assert from "node:assert/strict";

import { isCanonical } from "./cosmos.js";

// Pins which SDK event types make it into the durable Cosmos log vs which
// stay live-only (the typewriter deltas, status pings, etc). Drift here
// affects the SPA's history-replay correctness.

test("canonical: user, assistant, result messages", () => {
  assert.equal(isCanonical({ type: "user" } as any), true);
  assert.equal(isCanonical({ type: "assistant" } as any), true);
  assert.equal(isCanonical({ type: "result", subtype: "success" } as any), true);
});

test("canonical: system init + compact_boundary + tool_use_summary", () => {
  assert.equal(isCanonical({ type: "system", subtype: "init" } as any), true);
  assert.equal(
    isCanonical({ type: "system", subtype: "compact_boundary" } as any),
    true,
  );
  assert.equal(
    isCanonical({ type: "system", subtype: "tool_use_summary" } as any),
    true,
  );
});

test("canonical: permission_denied + plugin_install + rate_limit", () => {
  assert.equal(
    isCanonical({ type: "system", subtype: "permission_denied" } as any),
    true,
  );
  assert.equal(
    isCanonical({ type: "system", subtype: "plugin_install" } as any),
    true,
  );
  assert.equal(isCanonical({ type: "rate_limit" } as any), true);
});

test("NOT canonical: stream_event (typewriter deltas — live-only)", () => {
  assert.equal(isCanonical({ type: "stream_event" } as any), false);
});

test("NOT canonical: tool_progress, status, hook_*, task_*", () => {
  // Per the SDK docs these are ephemeral system events emitted during
  // tool execution — they exist to drive live UX but have no replay value.
  assert.equal(
    isCanonical({ type: "system", subtype: "tool_progress" } as any),
    false,
  );
  assert.equal(isCanonical({ type: "system", subtype: "status" } as any), false);
  assert.equal(
    isCanonical({ type: "system", subtype: "hook_started" } as any),
    false,
  );
  assert.equal(
    isCanonical({ type: "system", subtype: "task_started" } as any),
    false,
  );
});

test("NOT canonical: unknown types", () => {
  assert.equal(isCanonical({ type: "weird_new_thing" } as any), false);
  assert.equal(isCanonical({} as any), false);
});
