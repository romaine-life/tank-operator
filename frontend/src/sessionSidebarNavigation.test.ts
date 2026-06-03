import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");

test("sidebar session clicks reopen the main transcript", () => {
  assert.match(
    appSource,
    /function openSession\(id: string, e: ReactMouseEvent\) \{[\s\S]*?requestSessionTranscriptOpen\(id\);[\s\S]*?replaceSessionTranscriptRoute\(id\);[\s\S]*?activate\(id\);[\s\S]*?\n  \}/,
    "normal sidebar clicks should request the target session transcript before activation",
  );
  assert.match(
    appSource,
    /sidebarTranscriptOpenRequest=\{sessionTranscriptOpenRequests\[s\.id\] \?\? 0\}/,
    "each mounted ChatPane should receive its session's transcript-open request signal",
  );
  assert.match(
    appSource,
    /if \(!visible \|\| sidebarTranscriptOpenRequest === 0\) return;[\s\S]*?setActiveTab\("chat"\);[\s\S]*?setPendingRouteTurnNumber\(null\);[\s\S]*?setPendingTurnViewRouteAnchor\(null\);[\s\S]*?setSlashOpen\(false\);[\s\S]*?setMentionOpen\(false\);[\s\S]*?setMcpOpen\(false\);[\s\S]*?replaceSessionTranscriptRoute\(session\.id\);/,
    "the visible ChatPane should leave side tabs and palettes for the main transcript route",
  );
});
