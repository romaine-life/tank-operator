import { afterEach, beforeEach, test, expect } from "vitest";
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
  expect(__timeServiceListenerCountForTest()).toBe(0);
  expect(__timeServiceTickerRunningForTest()).toBe(false);
});

test("first subscriber starts the ticker; tick fans out to every listener", () => {
  let firstCalls = 0;
  let secondCalls = 0;
  const unsubA = __timeServiceSubscribeForTest(() => { firstCalls += 1; });
  expect(__timeServiceTickerRunningForTest()).toBe(true);
  const unsubB = __timeServiceSubscribeForTest(() => { secondCalls += 1; });
  expect(__timeServiceListenerCountForTest()).toBe(2);

  __timeServiceTickForTest();
  __timeServiceTickForTest();

  expect(firstCalls).toBe(2);
  expect(secondCalls).toBe(2);

  unsubA();
  unsubB();
});

test("ticker stops once the last subscriber unsubscribes", () => {
  const unsub = __timeServiceSubscribeForTest(() => {});
  expect(__timeServiceTickerRunningForTest()).toBe(true);
  unsub();
  expect(__timeServiceListenerCountForTest()).toBe(0);
  expect(__timeServiceTickerRunningForTest()).toBe(false);
});

test("unsubscribing one of several listeners keeps the ticker running", () => {
  const unsubA = __timeServiceSubscribeForTest(() => {});
  const unsubB = __timeServiceSubscribeForTest(() => {});
  unsubA();
  expect(__timeServiceListenerCountForTest()).toBe(1);
  expect(__timeServiceTickerRunningForTest()).toBe(true);
  unsubB();
  expect(__timeServiceTickerRunningForTest()).toBe(false);
});
