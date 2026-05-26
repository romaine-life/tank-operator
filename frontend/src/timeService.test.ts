import { afterEach, beforeEach, test } from "node:test";
import assert from "node:assert/strict";

import {
  __timeServiceListenerCountForTest,
  __timeServiceResetForTest,
  __timeServiceSubscribeForTest,
  __timeServiceTickForTest,
  __timeServiceTickerRunningForTest,
} from "./timeService";

// The hooks themselves wrap useSyncExternalStore + a stable getSnapshot
// per consumer. The React-level semantics (snapshot dedup → no
// re-render unless the bucket changed) belong to React; what we own at
// this layer is the ticker lifecycle and the listener fan-out. Those
// invariants are tested here directly. The hook integration is
// exercised by the App at render time and observable via
// `tank_client_long_task_duration_seconds` once deployed.

beforeEach(() => {
  // Patch window.setInterval so the singleton can install/tear down
  // its ticker in this test environment.
  (globalThis as { window?: unknown }).window = globalThis;
  __timeServiceResetForTest();
});

afterEach(() => {
  __timeServiceResetForTest();
  delete (globalThis as { window?: unknown }).window;
});

test("ticker stays off until the first listener subscribes", () => {
  assert.equal(__timeServiceListenerCountForTest(), 0);
  assert.equal(__timeServiceTickerRunningForTest(), false);
});

test("first subscriber starts the ticker; tick fans out to every listener", () => {
  let firstCalls = 0;
  let secondCalls = 0;
  const unsubA = __timeServiceSubscribeForTest(() => { firstCalls += 1; });
  assert.equal(__timeServiceTickerRunningForTest(), true);
  const unsubB = __timeServiceSubscribeForTest(() => { secondCalls += 1; });
  assert.equal(__timeServiceListenerCountForTest(), 2);

  __timeServiceTickForTest();
  __timeServiceTickForTest();

  assert.equal(firstCalls, 2);
  assert.equal(secondCalls, 2);

  unsubA();
  unsubB();
});

test("ticker stops once the last subscriber unsubscribes", () => {
  const unsub = __timeServiceSubscribeForTest(() => {});
  assert.equal(__timeServiceTickerRunningForTest(), true);
  unsub();
  assert.equal(__timeServiceListenerCountForTest(), 0);
  assert.equal(__timeServiceTickerRunningForTest(), false);
});

test("unsubscribing one of several listeners keeps the ticker running", () => {
  const unsubA = __timeServiceSubscribeForTest(() => {});
  const unsubB = __timeServiceSubscribeForTest(() => {});
  unsubA();
  assert.equal(__timeServiceListenerCountForTest(), 1);
  assert.equal(__timeServiceTickerRunningForTest(), true);
  unsubB();
  assert.equal(__timeServiceTickerRunningForTest(), false);
});
