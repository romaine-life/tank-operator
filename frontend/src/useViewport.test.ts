import { afterEach, test, expect } from "vitest";

import { readMatch, subscribeMatch } from "./useViewport.ts";

interface FakeController {
  matches: boolean;
  listeners: Set<() => void>;
  modern: boolean;
}

const slot = globalThis as { window?: unknown };
const originalWindow = slot.window;

function installMatchMedia(controller: FakeController): void {
  slot.window = {
    matchMedia(_query: string) {
      const mql: Record<string, unknown> = {
        get matches() {
          return controller.matches;
        },
      };
      if (controller.modern) {
        mql.addEventListener = (_type: string, cb: () => void) =>
          controller.listeners.add(cb);
        mql.removeEventListener = (_type: string, cb: () => void) =>
          controller.listeners.delete(cb);
      } else {
        mql.addListener = (cb: () => void) => controller.listeners.add(cb);
        mql.removeListener = (cb: () => void) => controller.listeners.delete(cb);
      }
      return mql;
    },
  };
}

afterEach(() => {
  if (originalWindow === undefined) {
    delete slot.window;
  } else {
    slot.window = originalWindow;
  }
});

test("readMatch returns false without matchMedia (SSR/node desktop default)", () => {
  delete slot.window;
  expect(readMatch("(max-width: 768px)")).toBe(false);
});

test("readMatch reflects the live matchMedia result", () => {
  const controller: FakeController = {
    matches: true,
    listeners: new Set(),
    modern: true,
  };
  installMatchMedia(controller);
  expect(readMatch("(max-width: 768px)")).toBe(true);
  controller.matches = false;
  expect(readMatch("(max-width: 768px)")).toBe(false);
});

test("subscribeMatch wires the modern change listener and unsubscribes cleanly", () => {
  const controller: FakeController = {
    matches: false,
    listeners: new Set(),
    modern: true,
  };
  installMatchMedia(controller);
  let fired = 0;
  const unsubscribe = subscribeMatch("(max-width: 768px)", () => {
    fired += 1;
  });
  expect(controller.listeners.size).toBe(1);
  for (const cb of controller.listeners) cb();
  expect(fired).toBe(1);
  unsubscribe();
  expect(controller.listeners.size).toBe(0);
});

test("subscribeMatch falls back to legacy addListener/removeListener", () => {
  const controller: FakeController = {
    matches: false,
    listeners: new Set(),
    modern: false,
  };
  installMatchMedia(controller);
  const unsubscribe = subscribeMatch("(max-width: 640px)", () => {});
  expect(controller.listeners.size).toBe(1);
  unsubscribe();
  expect(controller.listeners.size).toBe(0);
});

test("subscribeMatch is a safe no-op when matchMedia is unavailable", () => {
  delete slot.window;
  const unsubscribe = subscribeMatch("(max-width: 768px)", () => {});
  expect(typeof unsubscribe).toBe("function");
  unsubscribe(); // must not throw
});
