import assert from "node:assert/strict";
import { test } from "node:test";

import {
  initialTimelineBootstrapState,
  reduceTimelineBootstrap,
} from "./chatTimelineBootstrap";

test("timeline bootstrap ignores stale async completions", () => {
  let state = initialTimelineBootstrapState("121", 1);
  state = reduceTimelineBootstrap(state, {
    type: "loading",
    sessionId: "121",
    epoch: 1,
  });
  state = reduceTimelineBootstrap(state, {
    type: "reset",
    sessionId: "121",
    epoch: 2,
  });
  state = reduceTimelineBootstrap(state, {
    type: "ready",
    sessionId: "121",
    epoch: 1,
  });

  assert.deepEqual(state, {
    sessionId: "121",
    epoch: 2,
    status: "idle",
    error: null,
  });
});

test("timeline bootstrap exposes current-generation failures for retry", () => {
  let state = initialTimelineBootstrapState("121", 3);
  state = reduceTimelineBootstrap(state, {
    type: "loading",
    sessionId: "121",
    epoch: 3,
  });
  state = reduceTimelineBootstrap(state, {
    type: "error",
    sessionId: "121",
    epoch: 3,
    error: "timeline request failed: 500",
  });

  assert.equal(state.status, "error");
  assert.equal(state.error, "timeline request failed: 500");

  state = reduceTimelineBootstrap(state, {
    type: "reset",
    sessionId: "121",
    epoch: 4,
  });
  assert.equal(state.status, "idle");
  assert.equal(state.error, null);
});
