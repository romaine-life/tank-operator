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
  // The bare session route is the turns view (turns is the default surface).
  expect(readSessionRouteFromPathname("/sessions/s-1")).toEqual({
    sessionId: "s-1",
    tab: "turns",
    turnNumber: null,
    turnSegmentPresent: false,
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  // The main transcript is its own /transcript route.
  expect(readSessionRouteFromPathname("/sessions/s-1/transcript")).toEqual({
    sessionId: "s-1",
    tab: "chat",
    turnNumber: null,
    turnSegmentPresent: false,
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/3")).toEqual({
    sessionId: "s-1",
    tab: "turns",
    turnNumber: 3,
    turnSegmentPresent: true,
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  // A bare /turns with no number selects the latest turn (segment absent).
  expect(readSessionRouteFromPathname("/sessions/s-1/turns")).toEqual({
    sessionId: "s-1",
    tab: "turns",
    turnNumber: null,
    turnSegmentPresent: false,
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
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
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
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
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  expect(readSessionRouteFromPathname("/sessions/s-1/session-data/extra")).toBe(null);
  expect(readSessionRouteFromPathname("/sessions/s-1/pull-requests")).toEqual({
    sessionId: "s-1",
    tab: "pull-requests",
    turnNumber: null,
    turnSegmentPresent: false,
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  expect(readSessionRouteFromPathname("/sessions/s-1/break-glass/request%201")).toBe(null);
  expect(readSessionRouteFromPathname("/sessions/s-1/test-slot-model/request%201")).toEqual({
    sessionId: "s-1",
    tab: "test-slot-model",
    turnNumber: null,
    turnSegmentPresent: false,
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: "request 1",
    settingsTab: "preferences",
    adminView: "controls",
  });
  // The file-browser and background panes are surface-routed so they are
  // addressable, reload-stable, and appear in the breadcrumb.
  expect(readSessionRouteFromPathname("/sessions/s-1/files")).toEqual({
    sessionId: "s-1",
    tab: "files",
    turnNumber: null,
    turnSegmentPresent: false,
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  expect(readSessionRouteFromPathname("/sessions/s-1/background")).toEqual({
    sessionId: "s-1",
    tab: "background",
    turnNumber: null,
    turnSegmentPresent: false,
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  expect(readSessionRouteFromPathname("/sessions/s-1/files/src/App.tsx:42")).toEqual({
    sessionId: "s-1",
    tab: "files",
    turnNumber: null,
    turnSegmentPresent: false,
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: null,
    filePath: "src/App.tsx",
    fileLine: 42,
    testSlotModelRequestId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  expect(readSessionRouteFromPathname("/sessions/s-1/files/../secret")).toBe(null);
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
    pageNumber: null,
    pageSegmentPresent: false,
    staticPath: "diagram.html",
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
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
  expect(
    buildSessionRouteUrl(
      current,
      "s-1",
      "files",
      null,
      null,
      null,
      "screenshots/main menu.png",
      3,
    ),
  ).toBe("https://tank.example.test/sessions/s-1/files/screenshots/main%20menu.png:3");
});

test("session route urls broadcast only session-owned pages", () => {
  const current = "https://tank.example.test/sessions/old?session=s-1#stale";
  // The bare session route is the turns default.
  expect(buildSessionRouteUrl(current, "s 1")).toBe("https://tank.example.test/sessions/s%201");
  // The main transcript broadcasts its own /transcript route.
  expect(buildSessionRouteUrl(current, "s 1", "chat")).toBe("https://tank.example.test/sessions/s%201/transcript");
  expect(buildSessionRouteUrl(current, "s 1", "turns", 2)).toBe("https://tank.example.test/sessions/s%201/turns/2");
  // turns tab with no selected number stays on the root session page.
  expect(buildSessionRouteUrl(current, "s 1", "turns")).toBe("https://tank.example.test/sessions/s%201");
  expect(buildSessionRouteUrl(current, "s 1", "session-data")).toBe("https://tank.example.test/sessions/s%201/session-data");
  expect(buildSessionRouteUrl(current, "s 1", "pull-requests")).toBe("https://tank.example.test/sessions/s%201/pull-requests");
  expect(buildSessionRouteUrl(current, "s 1", "test-slot-model", null, null, null, null, null, "request 1")).toBe("https://tank.example.test/sessions/s%201/test-slot-model/request%201");
  expect(buildSessionRouteUrl(current, "s 1", "files")).toBe("https://tank.example.test/sessions/s%201/files");
  expect(buildSessionRouteUrl(current, "s 1", "background")).toBe("https://tank.example.test/sessions/s%201/background");
});

test("turn routes carry an optional page ordinal", () => {
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/3/pages/2")).toEqual({
    sessionId: "s-1",
    tab: "turns",
    turnNumber: 3,
    turnSegmentPresent: true,
    pageNumber: 2,
    pageSegmentPresent: true,
    staticPath: null,
    filePath: null,
    fileLine: null,
    testSlotModelRequestId: null,
    settingsTab: "preferences",
    adminView: "controls",
  });
  // A non-numeric or leading-zero page segment is a present-but-unresolvable
  // target (pageNumber null, pageSegmentPresent true) — the same discipline as
  // turn numbers, so a bad page link shows an explicit miss, not page 1.
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/3/pages/abc")?.pageNumber).toBe(null);
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/3/pages/abc")?.pageSegmentPresent).toBe(true);
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/3/pages/01")?.pageNumber).toBe(null);
  // An unknown subsegment after the turn is rejected, not silently ignored.
  expect(readSessionRouteFromPathname("/sessions/s-1/turns/3/foo")).toBe(null);
  // Round-trip: a turn + page builds the fully-qualified, shareable page URL.
  const current = "https://tank.example.test/sessions/s-1";
  expect(buildSessionRouteUrl(current, "s-1", "turns", 3, null, 2)).toBe("https://tank.example.test/sessions/s-1/turns/3/pages/2");
  // A page ordinal without a turn never appears — a bare turns route can't pin one.
  expect(buildSessionRouteUrl(current, "s-1", "turns", null, null, 2)).toBe("https://tank.example.test/sessions/s-1");
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

test("app route urls broadcast top-level settings help and cluster surfaces", () => {
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
  expect(readAppRouteFromPathname("/settings/admin/version")).toEqual({
        tab: "settings",
        settingsTab: "admin",
        adminView: "version",
      });
  expect(readAppRouteFromPathname("/settings/admin/break-glass")).toEqual({
        tab: "settings",
        settingsTab: "admin",
        adminView: "controls",
      });
  expect(readAppRouteFromPathname("/settings/admin/hidden-transcripts")).toEqual({
        tab: "settings",
        settingsTab: "admin",
        adminView: "hidden-transcripts",
      });
  expect(readAppRouteFromPathname("/help")).toEqual({
        tab: "help",
        settingsTab: "preferences",
        adminView: "controls",
      });
  expect(readAppRouteFromPathname("/cluster")).toEqual({
        tab: "cluster",
        settingsTab: "preferences",
        adminView: "controls",
      });
  expect(buildAppRouteUrl(current, "settings", "admin", "observability")).toBe("https://tank.example.test/settings/admin/observability");
  expect(buildAppRouteUrl(current, "settings", "admin", "version")).toBe("https://tank.example.test/settings/admin/version");
  expect(buildAppRouteUrl(current, "settings", "admin", "hidden-transcripts")).toBe("https://tank.example.test/settings/admin/hidden-transcripts");
  expect(buildAppRouteUrl(current, "help")).toBe("https://tank.example.test/help");
  expect(buildAppRouteUrl(current, "cluster")).toBe("https://tank.example.test/cluster");
});
