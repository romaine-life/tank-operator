import assert from "node:assert/strict";
import test from "node:test";

import {
  buildAppRouteUrl,
  buildHomeRouteUrl,
  buildSessionRouteUrl,
  readAppRouteFromPathname,
  readHomeRouteFromPathname,
  readSessionRouteFromPathname,
} from "./appRoutes";

test("session routes parse only session-scoped chat and turns pages", () => {
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
  assert.equal(readSessionRouteFromPathname("/sessions/s-1/settings"), null);
  assert.equal(readSessionRouteFromPathname("/sessions/s-1/settings/admin/observability"), null);
  assert.equal(readSessionRouteFromPathname("/sessions/s-1/help"), null);
});

test("session route urls broadcast only session-owned pages", () => {
  const current = "https://tank.example.test/sessions/old?session=s-1#stale";
  assert.equal(
    buildSessionRouteUrl(current, "s 1"),
    "https://tank.example.test/sessions/s%201",
  );
  assert.equal(
    buildSessionRouteUrl(current, "s 1", "turns", "turn 2"),
    "https://tank.example.test/sessions/s%201/turns/turn%202",
  );
});

test("home splash route parses only the new-session splash", () => {
  assert.deepEqual(readHomeRouteFromPathname("/new"), {
    tab: "chat",
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.equal(readHomeRouteFromPathname("/new/settings"), null);
  assert.equal(readHomeRouteFromPathname("/new/settings/admin/report"), null);
  assert.equal(readHomeRouteFromPathname("/new/help"), null);
});

test("home route urls broadcast only the new-session splash surface", () => {
  const current = "https://tank.example.test/?github_install_state=stale#ignored";
  assert.equal(buildHomeRouteUrl(current), "https://tank.example.test/new");
});

test("app route urls broadcast top-level settings and help surfaces", () => {
  const current = "https://tank.example.test/sessions/s-1?session=s-1#ignored";
  assert.deepEqual(readAppRouteFromPathname("/settings"), {
    tab: "settings",
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.deepEqual(readAppRouteFromPathname("/settings/admin/report"), {
    tab: "settings",
    settingsTab: "admin",
    adminView: "report",
  });
  assert.deepEqual(readAppRouteFromPathname("/help"), {
    tab: "help",
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.equal(
    buildAppRouteUrl(current, "settings", "admin", "observability"),
    "https://tank.example.test/settings/admin/observability",
  );
  assert.equal(buildAppRouteUrl(current, "help"), "https://tank.example.test/help");
});
