import { readFileSync } from "node:fs";
import { test, expect } from "vitest";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");

test("sidebar session clicks reopen the main transcript", () => {
  expect(appSource, "normal sidebar clicks should request the target session transcript before activation").toMatch(/function openSession\(id: string, e: ReactMouseEvent\) \{[\s\S]*?requestSessionTranscriptOpen\(id\);[\s\S]*?replaceSessionTranscriptRoute\(id\);[\s\S]*?activate\(id\);[\s\S]*?\n  \}/);
  expect(appSource, "each mounted ChatPane should receive its session's transcript-open request signal").toMatch(/sidebarTranscriptOpenRequest=\{sessionTranscriptOpenRequests\[s\.id\] \?\? 0\}/);
  expect(appSource, "the visible ChatPane should leave side tabs and palettes for the main transcript route").toMatch(/if \(!visible \|\| sidebarTranscriptOpenRequest === 0\) return;[\s\S]*?setActiveTab\("chat"\);[\s\S]*?setPendingRouteTurnNumber\(null\);[\s\S]*?setPendingTurnViewRouteAnchor\(null\);[\s\S]*?setSlashOpen\(false\);[\s\S]*?setMentionOpen\(false\);[\s\S]*?setMcpOpen\(false\);[\s\S]*?replaceSessionTranscriptRoute\(session\.id\);/);
});

test("sidebar turns menu opens the latest turn", () => {
  expect(appSource, "each mounted ChatPane should receive its session turns-open request signal").toMatch(/sidebarTurnsOpenRequest=\{sessionTurnsOpenRequests\[s\.id\] \?\? 0\}/);
  expect(appSource, "the visible ChatPane should clear any prior turn selection so /turns falls back to the latest turn").toMatch(/if \(!visible \|\| sidebarTurnsOpenRequest === 0\) return;[\s\S]*?setPendingRouteTurnNumber\(null\);[\s\S]*?setRouteTurnUnavailable\(false\);[\s\S]*?setPendingTurnViewRouteAnchor\("bottom"\);[\s\S]*?setSelectedTurnId\(null\);[\s\S]*?setSelectedTurnNumberAnchor\(null\);[\s\S]*?setActiveTab\("turns"\);[\s\S]*?replaceSessionRoute\(session\.id, "turns"\);/);
});
