import assert from "node:assert/strict";
import { afterEach, test } from "node:test";

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
  assert.equal(readMatch("(max-width: 768px)"), false);
});

test("readMatch reflects the live matchMedia result", () => {
  const controller: FakeController = {
    matches: true,
    listeners: new Set(),
    modern: true,
  };
  installMatchMedia(controller);
  assert.equal(readMatch("(max-width: 768px)"), true);
  controller.matches = false;
  assert.equal(readMatch("(max-width: 768px)"), false);
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
  assert.equal(controller.listeners.size, 1);
  for (const cb of controller.listeners) cb();
  assert.equal(fired, 1);
  unsubscribe();
  assert.equal(controller.listeners.size, 0);
});

test("subscribeMatch falls back to legacy addListener/removeListener", () => {
  const controller: FakeController = {
    matches: false,
    listeners: new Set(),
    modern: false,
  };
  installMatchMedia(controller);
  const unsubscribe = subscribeMatch("(max-width: 640px)", () => {});
  assert.equal(controller.listeners.size, 1);
  unsubscribe();
  assert.equal(controller.listeners.size, 0);
});

test("subscribeMatch is a safe no-op when matchMedia is unavailable", () => {
  delete slot.window;
  const unsubscribe = subscribeMatch("(max-width: 768px)", () => {});
  assert.equal(typeof unsubscribe, "function");
  unsubscribe(); // must not throw
});
