import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");

test("sidebar session clicks open the turns view", () => {
  assert.match(
    appSource,
    /function openSession\(id: string, e: ReactMouseEvent\) \{[\s\S]*?requestSessionTurnsOpen\(id\);[\s\S]*?replaceSessionRoute\(id, "turns"\);[\s\S]*?activate\(id\);[\s\S]*?\n  \}/,
    "normal sidebar clicks should request the target session turns view before activation",
  );
  assert.equal(
    appSource.includes("sidebarTranscriptOpenRequest"),
    false,
    "sidebar transcript-open request wiring should stay retired",
  );
});

test("sidebar turns menu opens the latest turn", () => {
  assert.match(
    appSource,
    /sidebarTurnsOpenRequest=\{sessionTurnsOpenRequests\[s\.id\] \?\? 0\}/,
    "each mounted ChatPane should receive its session turns-open request signal",
  );
  assert.match(
    appSource,
    /if \(!visible \|\| sidebarTurnsOpenRequest === 0\) return;[\s\S]*?setPendingRouteTurnNumber\(null\);[\s\S]*?setRouteTurnUnavailable\(false\);[\s\S]*?setPendingTurnViewRouteAnchor\("bottom"\);[\s\S]*?setSelectedTurnId\(null\);[\s\S]*?setSelectedTurnNumberAnchor\(null\);[\s\S]*?setActiveTab\("turns"\);[\s\S]*?replaceSessionRoute\(session\.id, "turns"\);/,
    "the visible ChatPane should clear any prior turn selection so /turns falls back to the latest turn",
  );
});
