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

test("session routes parse only session-scoped pages", () => {
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1"), {
    sessionId: "s-1",
    tab: "chat",
    turnNumber: null,
    turnSegmentPresent: false,
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1/turns/3"), {
    sessionId: "s-1",
    tab: "turns",
    turnNumber: 3,
    turnSegmentPresent: true,
    settingsTab: "preferences",
    adminView: "controls",
  });
  // A bare /turns with no number selects the latest turn (segment absent).
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1/turns"), {
    sessionId: "s-1",
    tab: "turns",
    turnNumber: null,
    turnSegmentPresent: false,
    settingsTab: "preferences",
    adminView: "controls",
  });
  // A non-numeric segment (e.g. a bookmarked legacy turn_<uuid>) is a
  // present-but-unresolvable target: turnNumber null, turnSegmentPresent true,
  // so the SPA shows the unavailable-target state instead of silently
  // defaulting. This is the migration guard against the retired route shape.
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1/turns/turn_abc"), {
    sessionId: "s-1",
    tab: "turns",
    turnNumber: null,
    turnSegmentPresent: true,
    settingsTab: "preferences",
    adminView: "controls",
  });
  // Leading-zero / signed / decimal segments are not valid turn numbers.
  assert.equal(readSessionRouteFromPathname("/sessions/s-1/turns/01")?.turnNumber, null);
  assert.equal(readSessionRouteFromPathname("/sessions/s-1/turns/-1")?.turnNumber, null);
  assert.deepEqual(readSessionRouteFromPathname("/sessions/s-1/session-data"), {
    sessionId: "s-1",
    tab: "session-data",
    turnNumber: null,
    turnSegmentPresent: false,
    settingsTab: "preferences",
    adminView: "controls",
  });
  assert.equal(readSessionRouteFromPathname("/sessions/s-1/session-data/extra"), null);
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
    buildSessionRouteUrl(current, "s 1", "turns", 2),
    "https://tank.example.test/sessions/s%201/turns/2",
  );
  // turns tab with no selected number stays on the bare /turns page.
  assert.equal(
    buildSessionRouteUrl(current, "s 1", "turns"),
    "https://tank.example.test/sessions/s%201/turns",
  );
  assert.equal(
    buildSessionRouteUrl(current, "s 1", "session-data"),
    "https://tank.example.test/sessions/s%201/session-data",
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
