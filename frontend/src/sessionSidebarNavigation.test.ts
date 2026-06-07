import { readFileSync } from "node:fs";
import { test, expect } from "vitest";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");

test("sidebar session clicks open the turns view", () => {
  expect(appSource, "normal sidebar clicks should request the target session turns view before activation").toMatch(/function openSession\(id: string, e: ReactMouseEvent\) \{[\s\S]*?requestSessionTurnsOpen\(id\);[\s\S]*?replaceSessionRoute\(id, "turns"\);[\s\S]*?activate\(id\);[\s\S]*?\n  \}/);
  expect(appSource.includes("sidebarTranscriptOpenRequest"), "sidebar transcript-open request wiring should stay retired").toBe(false);
});

test("sidebar turns menu opens the latest turn", () => {
  expect(appSource, "each mounted ChatPane should receive its session turns-open request signal").toMatch(/sidebarTurnsOpenRequest=\{sessionTurnsOpenRequests\[s\.id\] \?\? 0\}/);
  expect(appSource, "the visible ChatPane should clear any prior turn selection so /turns falls back to the latest turn").toMatch(/if \(!visible \|\| sidebarTurnsOpenRequest === 0\) return;[\s\S]*?setPendingRouteTurnNumber\(null\);[\s\S]*?setRouteTurnUnavailable\(false\);[\s\S]*?setPendingTurnViewRouteAnchor\("bottom"\);[\s\S]*?setSelectedTurnId\(null\);[\s\S]*?setSelectedTurnNumberAnchor\(null\);[\s\S]*?setActiveTab\("turns"\);[\s\S]*?replaceSessionRoute\(session\.id, "turns"\);/);
});
