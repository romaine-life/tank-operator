import { test, expect } from "vitest";

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
  expect(sessionConnectionIndicatorLabel(ctx({ state: "connecting" }))).toBe(null);
});

test("connecting that outlasts the threshold is shown as reconnecting", () => {
  expect(sessionConnectionIndicatorLabel(ctx({
          state: "connecting",
          delayedConnectingVisible: true,
        }))).toBe(CONNECTION_RECONNECTING_LABEL);
});

test("connection failures and explicit resyncs show immediately", () => {
  expect(sessionConnectionIndicatorLabel(ctx({ state: "connection_lost" }))).toBe(CONNECTION_LOST_LABEL);
  expect(sessionConnectionIndicatorLabel(ctx({ state: "resyncing" }))).toBe(CONNECTION_RESYNCING_LABEL);
});

test("healthy or idle streams do not show connection chrome", () => {
  expect(sessionConnectionIndicatorLabel(ctx({ state: "idle" }))).toBe(null);
  expect(sessionConnectionIndicatorLabel(ctx({ state: "connected" }))).toBe(null);
});

test("connection state is scoped to the visible chat pane", () => {
  expect(sessionConnectionIndicatorLabel(ctx({
          state: "connection_lost",
          visible: false,
        }))).toBe(null);
  for (const activeTab of ["turns", "files", "background", "settings", "help"]) {
    expect(sessionConnectionIndicatorLabel(ctx({
              state: "connection_lost",
              activeTab,
            })), activeTab).toBe(null);
  }
});

test("the reconnect threshold is long enough to suppress flicker and still prompt", () => {
  expect(CONNECTION_CONNECTING_VISIBLE_AFTER_MS >= 700).toBeTruthy();
  expect(CONNECTION_CONNECTING_VISIBLE_AFTER_MS <= 1500).toBeTruthy();
});
