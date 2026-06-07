import { test, expect } from "vitest";

import {
  buildAppRouteUrl,
  buildHomeRouteUrl,
  buildSessionRouteUrl,
  readAppRouteFromPathname,
  readHomeRouteFromPathname,
  readSessionRouteFromPathname,
} from "./appRoutes";

test("session routes parse only session-scoped pages", () => {
  expect(readSessionRouteFromPathname("/sessions/s-1")).toEqual({
        sessionId: "s-1",
        tab: "chat",
        turnNumber: null,
        turnSegmentPresent: false,
        staticPath: null,
        settingsTab: "preferences",
        adminView: "controls",
      });
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/3")).toEqual({
        sessionId: "s-1",
        tab: "turns",
        turnNumber: 3,
        turnSegmentPresent: true,
        staticPath: null,
        settingsTab: "preferences",
        adminView: "controls",
      });
  // A bare /turns with no number selects the latest turn (segment absent).
  expect(readSessionRouteFromPathname("/sessions/s-1/turns")).toEqual({
        sessionId: "s-1",
        tab: "turns",
        turnNumber: null,
        turnSegmentPresent: false,
        staticPath: null,
        settingsTab: "preferences",
        adminView: "controls",
      });
  // A non-numeric segment (e.g. a bookmarked legacy turn_<uuid>) is a
  // present-but-unresolvable target: turnNumber null, turnSegmentPresent true,
  // so the SPA shows the unavailable-target state instead of silently
  // defaulting. This is the migration guard against the retired route shape.
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/turn_abc")).toEqual({
        sessionId: "s-1",
        tab: "turns",
        turnNumber: null,
        turnSegmentPresent: true,
        staticPath: null,
        settingsTab: "preferences",
        adminView: "controls",
      });
  // Leading-zero / signed / decimal segments are not valid turn numbers.
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/01")?.turnNumber).toBe(null);
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/-1")?.turnNumber).toBe(null);
  expect(readSessionRouteFromPathname("/sessions/s-1/session-data")).toEqual({
        sessionId: "s-1",
        tab: "session-data",
        turnNumber: null,
        turnSegmentPresent: false,
        staticPath: null,
        settingsTab: "preferences",
        adminView: "controls",
      });
  expect(readSessionRouteFromPathname("/sessions/s-1/session-data/extra")).toBe(null);
  expect(readSessionRouteFromPathname("/sessions/s-1/settings")).toBe(null);
  expect(readSessionRouteFromPathname("/sessions/s-1/settings/admin/observability")).toBe(null);
  expect(readSessionRouteFromPathname("/sessions/s-1/help")).toBe(null);
});

test("session routes parse the sandboxed static-page subroute", () => {
  expect(readSessionRouteFromPathname("/sessions/s-1/static/diagram.html")).toEqual({
        sessionId: "s-1",
        tab: "static",
        turnNumber: null,
        turnSegmentPresent: false,
        staticPath: "diagram.html",
        settingsTab: "preferences",
        adminView: "controls",
      });
  // Nested workspace paths keep their slashes.
  expect(readSessionRouteFromPathname("/sessions/s-1/static/out/report.html")?.staticPath).toBe("out/report.html");
  // A `..` segment is rejected so the link can't escape the workspace.
  expect(readSessionRouteFromPathname("/sessions/s-1/static/../etc/passwd")).toBe(null);
  // Bare /static with no path is not a valid target.
  expect(readSessionRouteFromPathname("/sessions/s-1/static")).toBe(null);
});

test("static page route urls embed the workspace path per segment", () => {
  const current = "https://tank.example.test/sessions/s-1";
  expect(buildSessionRouteUrl(current, "s-1", "static", null, "out/report.html")).toBe("https://tank.example.test/sessions/s-1/static/out/report.html");
  // Spaces in a segment are percent-encoded; path separators are preserved.
  expect(buildSessionRouteUrl(current, "s-1", "static", null, "my diagram.html")).toBe("https://tank.example.test/sessions/s-1/static/my%20diagram.html");
});

test("session route urls broadcast only session-owned pages", () => {
  const current = "https://tank.example.test/sessions/old?session=s-1#stale";
  expect(buildSessionRouteUrl(current, "s 1")).toBe("https://tank.example.test/sessions/s%201");
  expect(buildSessionRouteUrl(current, "s 1", "turns", 2)).toBe("https://tank.example.test/sessions/s%201/turns/2");
  // turns tab with no selected number stays on the bare /turns page.
  expect(buildSessionRouteUrl(current, "s 1", "turns")).toBe("https://tank.example.test/sessions/s%201/turns");
  expect(buildSessionRouteUrl(current, "s 1", "session-data")).toBe("https://tank.example.test/sessions/s%201/session-data");
});

test("home splash route parses only the new-session splash", () => {
  expect(readHomeRouteFromPathname("/new")).toEqual({
        tab: "chat",
        settingsTab: "preferences",
        adminView: "controls",
      });
  expect(readHomeRouteFromPathname("/new/settings")).toBe(null);
  expect(readHomeRouteFromPathname("/new/settings/admin/report")).toBe(null);
  expect(readHomeRouteFromPathname("/new/help")).toBe(null);
});

test("home route urls broadcast only the new-session splash surface", () => {
  const current = "https://tank.example.test/?github_install_state=stale#ignored";
  expect(buildHomeRouteUrl(current)).toBe("https://tank.example.test/new");
});

test("app route urls broadcast top-level settings and help surfaces", () => {
  const current = "https://tank.example.test/sessions/s-1?session=s-1#ignored";
  expect(readAppRouteFromPathname("/settings")).toEqual({
        tab: "settings",
        settingsTab: "preferences",
        adminView: "controls",
      });
  expect(readAppRouteFromPathname("/settings/admin/report")).toEqual({
        tab: "settings",
        settingsTab: "admin",
        adminView: "report",
      });
  expect(readAppRouteFromPathname("/help")).toEqual({
        tab: "help",
        settingsTab: "preferences",
        adminView: "controls",
      });
  expect(buildAppRouteUrl(current, "settings", "admin", "observability")).toBe("https://tank.example.test/settings/admin/observability");
  expect(buildAppRouteUrl(current, "help")).toBe("https://tank.example.test/help");
});
