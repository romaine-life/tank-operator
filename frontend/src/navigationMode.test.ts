import { test, expect } from "vitest";

import {
  DEFAULT_NAVIGATION_MODE,
  type NavigationModeReason,
  navigationModeTelemetryEvent,
  targetModeForReason,
  transitionNavigationMode,
} from "./navigationMode.ts";

test("default navigation mode is live-tail per the contract", () => {
  expect(DEFAULT_NAVIGATION_MODE).toBe("live-tail");
});

test("session-open reasons map to the correct target mode", () => {
  expect(targetModeForReason("session-open-tail")).toBe("live-tail");
  expect(targetModeForReason("session-open-anchored")).toBe("historical-anchor");
});

test("user gestures that leave the tail target historical-anchor", () => {
  const upGestures: NavigationModeReason[] = [
    "up-button",
    "jump-oldest",
    "keyboard-home",
    "user-scroll-up",
  ];
  for (const reason of upGestures) {
    expect(targetModeForReason(reason), `${reason} should target historical-anchor`).toBe("historical-anchor");
  }
});

test("explicit return-to-tail reasons target live-tail", () => {
  const downGestures: NavigationModeReason[] = [
    "submit",
    "down-button",
    "jump-latest",
    "keyboard-end",
    "virtuoso-at-bottom-true",
  ];
  for (const reason of downGestures) {
    expect(targetModeForReason(reason), `${reason} should target live-tail`).toBe("live-tail");
  }
});

test("transitioning into the current mode reports changed=false", () => {
  const t = transitionNavigationMode("live-tail", "submit");
  expect(t.from).toBe("live-tail");
  expect(t.to).toBe("live-tail");
  expect(t.changed).toBe(false);
  expect(t.reason).toBe("submit");
});

test("transitioning out of the current mode reports changed=true", () => {
  const t = transitionNavigationMode("live-tail", "user-scroll-up");
  expect(t.from).toBe("live-tail");
  expect(t.to).toBe("historical-anchor");
  expect(t.changed).toBe(true);
  expect(t.reason).toBe("user-scroll-up");
});

test("an SSE-arrival-class reason does not exist", () => {
  // Encodes the invariant that no row-arrival path can name a reason.
  // The closed reason set means the reducer cannot be called with
  // "sse-row-burst" or similar; the bug class that latched
  // userScrolledUp=true during smooth-scroll catch-up cannot recur
  // through this surface.
  const candidatePostBugReasons = [
    "sse-row-burst",
    "timeline-refresh",
    "smooth-scroll",
    "virtuoso-at-bottom-false",
    "dom-distance-threshold",
  ];
  for (const candidate of candidatePostBugReasons) {
    // @ts-expect-error — these names are intentionally absent from the
    // closed reason union; the cast would fail compile but the runtime
    // test confirms targetModeForReason never returns anything for
    // them.
    expect(() => targetModeForReason(candidate)).toThrow();
  }
});

test("virtuoso-at-bottom-true returns to live-tail when in historical-anchor", () => {
  const t = transitionNavigationMode("historical-anchor", "virtuoso-at-bottom-true");
  expect(t.to).toBe("live-tail");
  expect(t.changed).toBe(true);
});

test("virtuoso-at-bottom-true is a no-op while already in live-tail", () => {
  const t = transitionNavigationMode("live-tail", "virtuoso-at-bottom-true");
  expect(t.to).toBe("live-tail");
  expect(t.changed).toBe(false);
});

test("telemetry event name binds to the target mode", () => {
  expect(navigationModeTelemetryEvent("live-tail")).toBe("navigation-mode-entered-live-tail");
  expect(navigationModeTelemetryEvent("historical-anchor")).toBe("navigation-mode-entered-historical-anchor");
});
