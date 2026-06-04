import assert from "node:assert/strict";
import { test } from "node:test";

import {
  CONNECTION_CONNECTING_VISIBLE_AFTER_MS,
  CONNECTION_LOST_LABEL,
  CONNECTION_RECONNECTING_LABEL,
  CONNECTION_RESYNCING_LABEL,
  sessionConnectionIndicatorLabel,
  type SessionConnectionIndicatorContext,
} from "./sessionConnectionIndicator.ts";

function ctx(
  overrides: Partial<SessionConnectionIndicatorContext> = {},
): SessionConnectionIndicatorContext {
  return {
    state: "connected",
    visible: true,
    activeTab: "chat",
    delayedConnectingVisible: false,
    ...overrides,
  };
}

test("routine connecting is telemetry-only before the display threshold", () => {
  assert.equal(
    sessionConnectionIndicatorLabel(ctx({ state: "connecting" })),
    null,
  );
});

test("connecting that outlasts the threshold is shown as reconnecting", () => {
  assert.equal(
    sessionConnectionIndicatorLabel(ctx({
      state: "connecting",
      delayedConnectingVisible: true,
    })),
    CONNECTION_RECONNECTING_LABEL,
  );
});

test("connection failures and explicit resyncs show immediately", () => {
  assert.equal(
    sessionConnectionIndicatorLabel(ctx({ state: "connection_lost" })),
    CONNECTION_LOST_LABEL,
  );
  assert.equal(
    sessionConnectionIndicatorLabel(ctx({ state: "resyncing" })),
    CONNECTION_RESYNCING_LABEL,
  );
});

test("healthy or idle streams do not show connection chrome", () => {
  assert.equal(sessionConnectionIndicatorLabel(ctx({ state: "idle" })), null);
  assert.equal(sessionConnectionIndicatorLabel(ctx({ state: "connected" })), null);
});

test("connection state is scoped to the visible chat pane", () => {
  assert.equal(
    sessionConnectionIndicatorLabel(ctx({
      state: "connection_lost",
      visible: false,
    })),
    null,
  );
  for (const activeTab of ["turns", "files", "background", "settings", "help"]) {
    assert.equal(
      sessionConnectionIndicatorLabel(ctx({
        state: "connection_lost",
        activeTab,
      })),
      null,
      activeTab,
    );
  }
});

test("the reconnect threshold is long enough to suppress flicker and still prompt", () => {
  assert.ok(CONNECTION_CONNECTING_VISIBLE_AFTER_MS >= 700);
  assert.ok(CONNECTION_CONNECTING_VISIBLE_AFTER_MS <= 1500);
});
