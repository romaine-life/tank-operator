import assert from "node:assert/strict";
import test from "node:test";

import {
  buildHomeRouteUrl,
  buildSessionRouteUrl,
  readHomeRouteFromPathname,
  readSessionRouteFromPathname,
} from "./appRoutes";

test("session routes parse chat, turns, settings admin, and help pages", () => {
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1"), {
    sessionId: "s-1",
    tab: "chat",
    turnId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1/turns/turn%201"), {
    sessionId: "s-1",
    tab: "turns",
    turnId: "turn 1",
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1/settings"), {
    sessionId: "s-1",
    tab: "settings",
    turnId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1/settings/admin/observability"), {
    sessionId: "s-1",
    tab: "settings",
    turnId: null,
    settingsTab: "admin",
    adminView: "observability",
  });
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1/help"), {
    sessionId: "s-1",
    tab: "help",
    turnId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
});

test("session route urls broadcast dedicated settings admin and help pages", () => {
  const current = "https://tank.example.test/sessions/old?session=s-1#stale";
  assert.equal(
    buildSessionRouteUrl(current, "s 1", "settings", null, "admin", "avatars"),
    "https://tank.example.test/sessions/s%201/settings/admin/avatars",
  );
  assert.equal(
    buildSessionRouteUrl(current, "s 1", "settings", null, "admin", "report"),
    "https://tank.example.test/sessions/s%201/settings/admin/report",
  );
  assert.equal(
    buildSessionRouteUrl(current, "s 1", "help"),
    "https://tank.example.test/sessions/s%201/help",
  );
});

test("home splash routes parse chat, settings admin, and help pages", () => {
  assert.deepEqual(readHomeRouteFromPathname("/new"), {
    tab: "chat",
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.deepEqual(readHomeRouteFromPathname("/new/settings"), {
    tab: "settings",
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.deepEqual(readHomeRouteFromPathname("/new/settings/admin/report"), {
    tab: "settings",
    settingsTab: "admin",
    adminView: "report",
  });
  assert.deepEqual(readHomeRouteFromPathname("/new/help"), {
    tab: "help",
    settingsTab: "preferences",
    adminView: "controls",
  });
});

test("home route urls broadcast the new session splash surface", () => {
  const current = "https://tank.example.test/?github_install_state=stale#ignored";
  assert.equal(buildHomeRouteUrl(current), "https://tank.example.test/new");
  assert.equal(
    buildHomeRouteUrl(current, "settings", "admin", "observability"),
    "https://tank.example.test/new/settings/admin/observability",
  );
  assert.equal(buildHomeRouteUrl(current, "help"), "https://tank.example.test/new/help");
});
