import assert from "node:assert/strict";
import { test } from "node:test";

import {
  DEFAULT_NAVIGATION_MODE,
  type NavigationModeReason,
  navigationModeTelemetryEvent,
  targetModeForReason,
  transitionNavigationMode,
} from "./navigationMode.ts";

test("default navigation mode is live-tail per the contract", () => {
  assert.equal(DEFAULT_NAVIGATION_MODE, "live-tail");
});

test("session-open reasons map to the correct target mode", () => {
  assert.equal(targetModeForReason("session-open-tail"), "live-tail");
  assert.equal(targetModeForReason("session-open-anchored"), "historical-anchor");
});

test("user gestures that leave the tail target historical-anchor", () => {
  const upGestures: NavigationModeReason[] = [
    "up-button",
    "jump-oldest",
    "keyboard-home",
    "user-scroll-up",
  ];
  for (const reason of upGestures) {
    assert.equal(
      targetModeForReason(reason),
      "historical-anchor",
      `${reason} should target historical-anchor`,
    );
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
    assert.equal(
      targetModeForReason(reason),
      "live-tail",
      `${reason} should target live-tail`,
    );
  }
});

test("transitioning into the current mode reports changed=false", () => {
  const t = transitionNavigationMode("live-tail", "submit");
  assert.equal(t.from, "live-tail");
  assert.equal(t.to, "live-tail");
  assert.equal(t.changed, false);
  assert.equal(t.reason, "submit");
});

test("transitioning out of the current mode reports changed=true", () => {
  const t = transitionNavigationMode("live-tail", "user-scroll-up");
  assert.equal(t.from, "live-tail");
  assert.equal(t.to, "historical-anchor");
  assert.equal(t.changed, true);
  assert.equal(t.reason, "user-scroll-up");
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
    assert.throws(() => targetModeForReason(candidate));
  }
});

test("virtuoso-at-bottom-true returns to live-tail when in historical-anchor", () => {
  const t = transitionNavigationMode("historical-anchor", "virtuoso-at-bottom-true");
  assert.equal(t.to, "live-tail");
  assert.equal(t.changed, true);
});

test("virtuoso-at-bottom-true is a no-op while already in live-tail", () => {
  const t = transitionNavigationMode("live-tail", "virtuoso-at-bottom-true");
  assert.equal(t.to, "live-tail");
  assert.equal(t.changed, false);
});

test("telemetry event name binds to the target mode", () => {
  assert.equal(
    navigationModeTelemetryEvent("live-tail"),
    "navigation-mode-entered-live-tail",
  );
  assert.equal(
    navigationModeTelemetryEvent("historical-anchor"),
    "navigation-mode-entered-historical-anchor",
  );
});
