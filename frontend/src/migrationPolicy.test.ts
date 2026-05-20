import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const chatScrollTelemetrySource = readFileSync(
  new URL("./chatScrollTelemetry.ts", import.meta.url),
  "utf8",
);
const mainSource = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");

test("session activity is not refreshed by a steady interval", () => {
  assert.equal(appSource.includes("POLL_INTERVAL_MS"), false);
  assert.equal(/setInterval\(\s*refreshSessionActivity/.test(appSource), false);
});

test("chat transcript UI does not use the retired agent-ws route", () => {
  assert.equal(appSource.includes("agent-ws"), false);
});

test("stop control waits for durable turn interruption", () => {
  const cancelRunMatch = appSource.match(
    /function cancelRun\(\) \{([\s\S]*?)\n  async function requestSdkInterrupt/,
  );
  assert.ok(cancelRunMatch, "cancelRun body should be present");
  const cancelRunBody = cancelRunMatch[1]!;
  assert.equal(cancelRunBody.includes("currentRunRef.current = null"), false);
  assert.equal(
    cancelRunBody.includes('setRunStatus((prev) => (prev === "running" ? "done" : prev))'),
    false,
  );
  // Post-migration: cancelRun does NOT set run status imperatively. The
  // POST returns 202 only after turn.interrupt_requested is durable, and
  // applySdkProjectionToUi reads projection.runStatus === "stopping" to
  // drive the local stopping state. The three negative assertions below
  // pin the UI-local stop-optimism shape out of cancelRun.
  assert.equal(cancelRunBody.includes('setRunStatus("stopping")'), false);
  assert.equal(cancelRunBody.includes("stopRequested"), false);
  assert.equal(cancelRunBody.includes("stoppingTargetRef"), false);
  assert.equal(appSource.includes("if (!res.ok)"), true);
});

test("AskUserQuestion replies use durable input-reply turns", () => {
  assert.equal(appSource.includes("sendStdin"), false);
  assert.equal(appSource.includes("/input-reply"), true);
});

// The AskUserQuestion cutover (durable canUseTool resolution) deletes
// the previous synthetic-tool_result path and adds full question-shape
// rendering. The companion migration guard
// scripts/check-askuserquestion-migration.mjs runs the same checks
// across the whole repo; these tests pin the App.tsx surface as a
// fast-failing unit test that boots in CI before the full guard.
test("ToolAskUserBody renders the SDK AskUserQuestion question shape", () => {
  // Per-question fields the SDK schema defines (sdk-tools.d.ts
  // AskUserQuestionInput). All four must be reachable through the
  // renderer; the guard's REQUIRED entries pin the same anchors.
  assert.equal(appSource.includes("q.question"), true);
  assert.equal(appSource.includes("q.header"), true);
  assert.equal(appSource.includes("q.multiSelect"), true);
  assert.equal(
    appSource.includes("opt.preview") || appSource.includes("option.preview"),
    true,
  );
});

test("ToolAskUserBody reads the answered state from the durable event payload", () => {
  // Local selection state alone is not allowed to drive the answered
  // pill — a fresh tab opened after another tab answered must still
  // render the user's selections. The durable answers come from
  // `entry.askUserAnswers`, which `conversationProjection.ts` lifts off
  // the merged `tool.approval_resolved` payload.
  assert.equal(appSource.includes("entry.askUserAnswers"), true);
});

test("pending AskUserQuestion opens collapsed tool groups", () => {
  assert.equal(appSource.includes("isPendingAskUserQuestionTool"), true);
  assert.equal(appSource.includes("pendingAskUserCount"), true);
  assert.match(appSource, /entries\.some\(isPendingAskUserQuestionTool\)/);
  assert.match(appSource, /toolGroupDefaultOpen\(g\.entries, autoExpandTools, toolExpansionOverrides\)/);
});

test("AskUserQuestion placeholder 'Answer questions?' never leaks into App source", () => {
  // The Claude Agent SDK ships this string as the AskUserQuestion
  // checkPermissions message. If it shows up in our renderer, the
  // cutover regressed (either the runner stopped gating, or someone
  // hard-coded the SDK fallback into a fixture).
  assert.equal(appSource.includes("Answer questions?"), false);
});

test("chat history bootstrap is tail-first and not browser-position based", () => {
  assert.equal(appSource.includes('params.set("anchor", "newest")'), true);
  assert.equal(appSource.includes('params.set("timeline_id", targetTimelineId)'), true);
  assert.equal(appSource.includes("first_unread"), false);
  assert.equal(appSource.includes("tank.transcript.position"), false);
  assert.equal(appSource.includes("readSdkTranscriptPosition"), false);
  assert.equal(appSource.includes("writeSdkTranscriptPosition"), false);
});

test("chat live stream waits for timeline bootstrap", () => {
  assert.equal(appSource.includes("historyBootstrapped"), true);
  assert.match(
    appSource,
    /if \(!visible \|\| session\.status !== "Active" \|\| !historyBootstrapped\) return;/,
  );
});

test("sidebar order is not browser-local", () => {
  assert.equal(appSource.includes("tank.sessionOrder"), false);
  assert.equal(appSource.includes("readSessionOrder"), false);
  assert.equal(appSource.includes("writeSessionOrder"), false);
  assert.equal(mainSource.includes("tank.sessionOrder"), false);
  assert.equal(appSource.includes("/api/sessions/order"), true);
});

test("home splash test action seeds the first turn as a skill invocation", () => {
  assert.equal(appSource.includes("composeSkillPrompt"), true);
  assert.match(appSource, /initialSkillName\?: SkillStateName/);
  assert.match(appSource, /\.\.\.\(initialSkillName \? \{ skill_name: initialSkillName \} : \{\}\)/);
  assert.match(appSource, /homeComposerText\.trim\(\) \|\| undefined,[\s\S]*homeComposerMode,[\s\S]*"test"/);
  assert.equal(appSource.includes("Available once your session starts"), false);
});

test("fresh chat sessions focus the composer instead of the rename field", () => {
  assert.equal(appSource.includes("setAutoRenameSessionId(created.id)"), false);
  assert.equal(appSource.includes("setAutoFocusComposerSessionId(created.id)"), true);
  assert.equal(appSource.includes("setAutoRenameSessionId(session.id)"), true);
});

test("mounted chat reactivation resets local timeline state before bootstrap", () => {
  assert.equal(appSource.includes("visible-reactivation"), true);
  assert.equal(appSource.includes("resetSdkTimelineBootstrapState"), true);
  assert.match(appSource, /if \(!visible\) return;\s+if \(historyAttempted\) return;/);
  assert.equal(appSource.includes("pendingVisibleTailBootstrapRef"), true);
});

test("chat scroll diagnostics are debug gated", () => {
  assert.equal(chatScrollTelemetrySource.includes('DEBUG_TOKEN = "chat-scroll"'), true);
  assert.equal(chatScrollTelemetrySource.includes("isChatScrollDebugEnabled"), true);
  assert.equal(appSource.includes("logChatScrollGroups"), true);
  assert.equal(appSource.includes("logChatScrollEntries"), true);
});
