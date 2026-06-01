import assert from "node:assert/strict";
import test from "node:test";
import { 
  readSessionRouteFromPath, 
  readHomeRouteFromPath,
  sessionRouteUrl,
  homeRouteUrl
} from "./routing";

// Polyfill window for tests if not present
if (typeof (global as any).window === "undefined") {
  (global as any).window = {
    location: {
      href: "https://tank.test/",
      pathname: "/"
    }
  };
}

test("readSessionRouteFromPath parses standard routes", () => {
  assert.deepEqual(readSessionRouteFromPath("/sessions/abc"), { sessionId: "abc", tab: "chat", turnId: null });
  assert.deepEqual(readSessionRouteFromPath("/sessions/abc/turns/123"), { sessionId: "abc", tab: "turns", turnId: "123" });
});

test("readSessionRouteFromPath parses settings and admin routes", () => {
  assert.deepEqual(readSessionRouteFromPath("/sessions/abc/settings"), { sessionId: "abc", tab: "settings", turnId: null, settingsTab: "preferences", adminView: undefined });
  assert.deepEqual(readSessionRouteFromPath("/sessions/abc/settings/admin"), { sessionId: "abc", tab: "settings", turnId: null, settingsTab: "admin", adminView: "controls" });
  assert.deepEqual(readSessionRouteFromPath("/sessions/abc/settings/admin/avatars"), { sessionId: "abc", tab: "settings", turnId: null, settingsTab: "admin", adminView: "avatars" });
});

test("readSessionRouteFromPath parses help, files, background", () => {
  assert.deepEqual(readSessionRouteFromPath("/sessions/abc/help"), { sessionId: "abc", tab: "help", turnId: null });
  assert.deepEqual(readSessionRouteFromPath("/sessions/abc/files"), { sessionId: "abc", tab: "files", turnId: null });
  assert.deepEqual(readSessionRouteFromPath("/sessions/abc/background"), { sessionId: "abc", tab: "background", turnId: null });
});

test("readHomeRouteFromPath parses root, settings, help", () => {
  assert.deepEqual(readHomeRouteFromPath("/"), { tab: "chat" });
  assert.deepEqual(readHomeRouteFromPath("/settings"), { tab: "settings" });
  assert.deepEqual(readHomeRouteFromPath("/help"), { tab: "help" });
  assert.deepEqual(readHomeRouteFromPath("/sessions/abc"), null);
});

test("sessionRouteUrl generates correct URLs", () => {
  assert.equal(sessionRouteUrl("abc"), "https://tank.test/sessions/abc");
  assert.equal(sessionRouteUrl("abc", "turns", { turnId: "123" }), "https://tank.test/sessions/abc/turns/123");
  assert.equal(sessionRouteUrl("abc", "settings"), "https://tank.test/sessions/abc/settings");
  assert.equal(sessionRouteUrl("abc", "settings", { settingsTab: "admin" }), "https://tank.test/sessions/abc/settings/admin");
  assert.equal(sessionRouteUrl("abc", "settings", { settingsTab: "admin", adminView: "avatars" }), "https://tank.test/sessions/abc/settings/admin/avatars");
  assert.equal(sessionRouteUrl("abc", "help"), "https://tank.test/sessions/abc/help");
});

test("homeRouteUrl generates correct URLs", () => {
  assert.equal(homeRouteUrl("chat"), "https://tank.test/");
  assert.equal(homeRouteUrl("settings"), "https://tank.test/settings");
  assert.equal(homeRouteUrl("help"), "https://tank.test/help");
});
