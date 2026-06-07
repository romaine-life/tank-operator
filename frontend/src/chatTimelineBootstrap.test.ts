import { test, expect } from "vitest";

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

  expect(state).toEqual({
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

  expect(state.status).toBe("error");
  expect(state.error).toBe("timeline request failed: 500");

  state = reduceTimelineBootstrap(state, {
    type: "reset",
    sessionId: "121",
    epoch: 4,
  });
  expect(state.status).toBe("idle");
  expect(state.error).toBe(null);
});
