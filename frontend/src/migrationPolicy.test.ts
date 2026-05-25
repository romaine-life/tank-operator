import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8").replace(/\r\n/g, "\n");
}

const appSource = readSource("./App.tsx");
const authSource = readSource("./auth.ts");
const conversationReducerSource = readSource("./conversationReducer.ts");
const chatScrollTelemetrySource = readSource("./chatScrollTelemetry.ts");
const sessionEventStreamTelemetrySource = readSource("./sessionEventStreamTelemetry.ts");
const longChatDebugSource = readSource("./LongChatDebugPage.tsx");
const adminAvatarManagerSource = readSource("./AdminAvatarManager.tsx");
const mainSource = readSource("./main.tsx");
const indexCssSource = readSource("./index.css");
const sessionConfigMapSource = readSource("../../k8s/templates/session-configmap.yaml");
const installTankDocsSource = readSource("../../k8s/session-config/install-tank-docs.sh");
const agentRunnerLaunchSource = readSource("../../k8s/session-config/agent-runner-launch.sh");
const codexRunnerLaunchSource = readSource("../../k8s/session-config/codex-runner-launch.sh");
const defaultClaudeSource = readSource("../../k8s/session-config/default-claude.md");
const bundledQualityTimeframesSource = readSource(
  "../../k8s/session-config/docs/quality-timeframes.md",
);
const bundledMigrationPolicySource = readSource(
  "../../k8s/session-config/docs/migration-policy.md",
);
const appChromeCapabilitiesSource = readSource(
  "../../docs/features/app-chrome/capabilities.md",
);

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

test("historical transcript bootstrap requires server-projected turn activity", () => {
  assert.equal(appSource.includes("projectedTranscriptEntriesFromTimelineBody"), true);
  assert.equal(
    appSource.includes("timeline response missing server transcript projection"),
    true,
  );
  assert.match(
    appSource,
    /replaceSdkServerEvents\(\s*canonicalEvents,[\s\S]*?projectedEntries,/,
  );
  assert.match(
    appSource,
    /\/turns\/\$\{encodeURIComponent\(trimmedTurnId\)\}\/activity/,
  );
  assert.equal(appSource.includes('kind !== "turn_activity"'), true);
});

test("chat live stream waits for timeline bootstrap", () => {
  assert.equal(appSource.includes("historyBootstrapped"), true);
  assert.match(
    appSource,
    /if \(!visible \|\| !CHAT_MODES\.has\(session\.mode\) \|\| !historyBootstrapped\) return;/,
  );
});

test("browser EventSource streams use opaque stream tickets, not bearer query strings", () => {
  assert.equal(authSource.includes("/api/auth/stream-ticket"), true);
  assert.equal(authSource.includes("stream_ticket"), true);
  assert.equal(authSource.includes("access_token"), false);
  assert.match(appSource, /authedEventSource\([\s\S]{0,400}stream: "session-events"/);
  assert.match(appSource, /authedEventSource\([\s\S]{0,400}stream: "session-list"/);
});

test("browser-native protected resources are not loaded with raw API URLs", () => {
  assert.equal(appSource.includes('src={`/api/sessions/${session.id}/files/raw'), false);
  assert.equal(appSource.includes("URL.createObjectURL(blob)"), true);
  assert.match(appSource, /authedFetch\([\s\S]{0,120}\/files\/raw\?path=/);
});

test("startup transcript rows come from durable conversation events", () => {
  assert.equal(appSource.includes("startupTranscript"), false);
  assert.equal(appSource.includes("sessionStartupDrafts"), false);
  assert.equal(appSource.includes("startupDraft"), false);
  assert.equal(appSource.includes("Continuing previous conversation"), false);
  assert.equal(appSource.includes("run-continue-hint"), false);
  assert.equal(appSource.includes("run-transcript-beginning"), false);
  assert.equal(appSource.includes("Beginning of conversation"), false);
  assert.equal(appSource.includes("session-status-transition"), false);
  assert.equal(appSource.includes("initial_turn"), true);
  assert.equal(appSource.includes("CREATE_TIME_INITIAL_TURN_MODES"), true);
  assert.equal(appSource.includes("composeLaunchUserPrompt"), true);
  assert.equal(appSource.includes("seedTurnDeferredAtCreate"), true);
  assert.match(appSource, /existing_user_message: true/);
  assert.match(appSource, /if \(seedTurnRequested && !seedTurnSubmittedAtCreate\) \{/);
  assert.equal(conversationReducerSource.includes('"session.status"'), true);
  assert.match(
    appSource,
    /if \(!visible \|\| !CHAT_MODES\.has\(session\.mode\)\) return;\r?\n    if \(timelineBootstrap\.status !== "idle"\) return;/,
  );
});

test("sidebar order is not browser-local", () => {
  assert.equal(appSource.includes("tank.sessionOrder"), false);
  assert.equal(appSource.includes("readSessionOrder"), false);
  assert.equal(appSource.includes("writeSessionOrder"), false);
  assert.equal(mainSource.includes("tank.sessionOrder"), false);
  assert.equal(appSource.includes("/api/sessions/order"), true);
});

test("sidebar skill-state conflicts are not repaired in the frontend", () => {
  const skillStateMatch = appSource.match(
    /function currentSessionSkillState\([\s\S]*?\n\}/,
  );
  assert.ok(skillStateMatch, "currentSessionSkillState should be present");
  const skillStateBody = skillStateMatch[0]!;
  assert.equal(appSource.includes("mergeMutualSessionSkillState"), false);
  assert.equal(skillStateBody.includes('if (rolloutActive) return "rollout"'), false);
  assert.equal(skillStateBody.includes('if (testActive) return "test"'), false);
});

test("home splash test action seeds the first turn as a skill invocation", () => {
  assert.equal(appSource.includes("composeSkillPrompt"), true);
  assert.match(appSource, /initialSkillName\?: SkillStateName/);
  assert.match(appSource, /\.\.\.\(requestedInitialSkillName \? \{ skill_name: requestedInitialSkillName \} : \{\}\)/);
  assert.match(appSource, /homeComposerText\.trim\(\) \|\| undefined,[\s\S]*homeComposerMode,[\s\S]*"test"/);
  assert.equal(appSource.includes("Available once your session starts"), false);
});

test("home splash test action stays disabled on the splash page", () => {
  assert.match(appSource, /disabled\s+aria-label="Start test skill"\s+title="Available in an active chat session"/);
  assert.equal(
    appSource.includes("disabled={busy || !CHAT_MODES.has(defaultSessionMode)}"),
    false,
  );
});

test("avatar editor is embedded in Settings admin, not a standalone app route", () => {
  assert.equal(mainSource.includes("/admin/avatars"), false);
  assert.equal(mainSource.includes("AdminAvatarsPage"), false);
  assert.equal(appSource.includes("avatarEditorHref"), false);
  assert.equal(appSource.includes("<AdminAvatarManager"), true);
  assert.equal(indexCssSource.includes("admin-avatar-page"), false);
  assert.equal(indexCssSource.includes("admin-avatar-home"), false);
  assert.equal(adminAvatarManagerSource.includes("bootstrapAuth"), false);
  assert.equal(adminAvatarManagerSource.includes("Back to app"), false);
  assert.equal(appChromeCapabilitiesSource.includes("admin route"), false);
  assert.equal(appChromeCapabilitiesSource.includes("Settings -> Admin avatar pane"), true);
});

test("files tab is gated until the session container is available", () => {
  assert.equal(appSource.includes("sessionFilesAvailable(session)"), true);
  assert.match(appSource, /if \(tab === "files" && !filesAvailable\) return;/);
  assert.match(appSource, /disabled=\{!filesAvailable\}/);
});

test("read-only cross-scope sessions keep an explicit composer affordance", () => {
  assert.match(appSource, /composerVisible=\{activeTab === "chat"\}/);
  assert.equal(
    appSource.includes("Production sessions are read-only in this test slot"),
    true,
  );
  assert.equal(
    appSource.includes("Read-only production view. Switch back to this slot's sessions in Settings to send messages."),
    true,
  );
  assert.equal(indexCssSource.includes(".run-composer.run-composer-readonly"), true);
});

test("background page uses stacked full-width sections instead of a side pane", () => {
  assert.match(indexCssSource, /\.run-shell-tasks-page \{[\s\S]*grid-template-rows: auto minmax\(0, 1fr\);/);
  assert.equal(
    indexCssSource.includes("grid-template-columns: minmax(16rem, 24rem) minmax(0, 1fr)"),
    false,
  );
  assert.equal(indexCssSource.includes("border-right: 1px solid var(--border-subtle);"), false);
});

test("background tab stays discoverable before background entries exist", () => {
  const backgroundLedgerMatch = appSource.match(
    /function BackgroundLedger\([\s\S]*?\n}\n\nfunction BackgroundMeta/,
  );
  assert.ok(backgroundLedgerMatch, "BackgroundLedger body should be present");
  assert.equal(backgroundLedgerMatch[0]!.includes("entries.length === 0"), false);
  assert.match(appSource, /<span>Background<\/span>/);
  assert.match(appSource, /disabled\?: boolean;/);
  assert.match(
    appSource,
    /<BackgroundLedger\n\s+entries=\{\[\]\}\n\s+active=\{false\}\n\s+onOpen=\{\(\) => undefined\}\n\s+disabled\n\s+title="Background activity is available once the session starts"/,
  );
});

test("background page includes active shell invocations alongside managed tasks", () => {
  assert.match(
    appSource,
    /function isShellToolEntry\([\s\S]*?entry\.toolKind === "shell"[\s\S]*?function isRunningShellInvocationEntry\([\s\S]*?isShellToolEntry\(entry\)[\s\S]*?normalizeToolState\(entry\.toolStatus\) === "running"/,
  );
  assert.match(
    appSource,
    /const activeBackgroundEntries = useMemo\([\s\S]*?backgroundTaskEntries\.filter\(isBackgroundTaskRunning\)[\s\S]*?runningShellInvocationEntries/,
  );
  assert.match(appSource, /<BackgroundScreen\n\s+shellEntries=\{activeBackgroundEntries\}/);
});

test("background page separates tracked shells from detached shell candidates", () => {
  assert.match(appSource, /type BackgroundView = "shells" \| "detached"/);
  assert.match(
    appSource,
    /function isDetachedShellCandidateEntry\([\s\S]*?isShellToolEntry\(entry\)[\s\S]*?detachedShellLaunchReason\(entry\)/,
  );
  assert.match(appSource, /<span>Shells<\/span>[\s\S]*?<span>Detached<\/span>/);
  assert.match(
    appSource,
    /const detachedShellEntries = useMemo\([\s\S]*?renderedEntries\.filter\(isDetachedShellCandidateEntry\)/,
  );
  assert.match(
    appSource,
    /<BackgroundScreen\n\s+shellEntries=\{activeBackgroundEntries\}\n\s+detachedEntries=\{detachedShellEntries\}/,
  );
});

test("background stop controls exclude untracked detached shells", () => {
  assert.match(
    appSource,
    /function canStopBackgroundActivity\([\s\S]*?isDetachedShellCandidateEntry\(entry\)\) return false[\s\S]*?isRunningShellInvocationEntry\(entry\)[\s\S]*?isBackgroundTaskEntry\(entry\)[\s\S]*?codexBackgroundStopAvailable/,
  );
  assert.match(appSource, /<BackgroundScreen[\s\S]*canStopEntry=\{canStopBackgroundEntry\}[\s\S]*onStop=\{stopBackgroundActivity\}/);
  assert.match(appSource, /className="run-shell-task-stop"/);
  assert.match(appSource, /\/background-tasks\/\$\{encodeURIComponent\(taskID\)\}\/stop/);
});

test("home splash initial-message modes rewrite the first turn deliberately", () => {
  assert.match(appSource, /type InitialMessageMode = "direct" \| "diagnose" \| "quality_gaps" \| "test"/);
  assert.equal(appSource.includes("composeInitialMessageModePrompt"), true);
  assert.equal(appSource.includes("Initial message type: diagnose issue without writing code."), true);
  assert.equal(appSource.includes("/workspace/.tank/docs/quality-timeframes.md"), true);
  assert.equal(appSource.includes("/workspace/.tank/docs/migration-policy.md"), true);
  assert.match(appSource, /initialMessageModeSkillName\(mode: InitialMessageMode\): SkillStateName \| undefined/);
  assert.match(appSource, /initialMode !== "direct"[\s\S]*chatModeForHomePrompt\(defaultSessionMode\)/);
});

test("quality gap policy docs are bundled into session config", () => {
  assert.equal(sessionConfigMapSource.includes("install-tank-docs.sh"), true);
  assert.equal(sessionConfigMapSource.includes('$.Files.Glob "session-config/docs/**"'), true);
  assert.equal(sessionConfigMapSource.includes("docs__"), true);
  assert.equal(installTankDocsSource.includes("/workspace/.tank/docs"), true);
  assert.equal(agentRunnerLaunchSource.includes("install-tank-docs.sh"), true);
  assert.equal(codexRunnerLaunchSource.includes("install-tank-docs.sh"), true);
  assert.equal(defaultClaudeSource.includes("/workspace/.tank/docs/"), true);
  assert.equal(bundledQualityTimeframesSource.includes("# Quality Timeframes"), true);
  assert.equal(bundledMigrationPolicySource.includes("# Migration Policy"), true);
});

test("fresh chat sessions focus the composer instead of the rename field", () => {
  assert.equal(appSource.includes("setAutoRenameSessionId(created.id)"), false);
  assert.equal(appSource.includes("setAutoFocusComposerSessionId(created.id)"), true);
  assert.equal(appSource.includes("setAutoRenameSessionId(session.id)"), true);
});

test("mounted chat reactivation resets local timeline state before bootstrap", () => {
  assert.equal(appSource.includes("visible-reactivation"), true);
  assert.equal(appSource.includes("resetSdkTimelineBootstrapState"), true);
  assert.equal(appSource.includes("reduceTimelineBootstrap"), true);
  assert.equal(appSource.includes("scrollToLatestOnReady: !hasExplicitTarget"), true);
  assert.equal(appSource.includes('requestScrollToLatest("auto", source)'), true);
  assert.match(appSource, /useLayoutEffect\(\(\) => \{\s+sessionIdRef\.current = session\.id;/);
  assert.match(appSource, /if \(timelineBootstrap\.status !== "idle"\) return;/);
});

test("chat submit explicitly lands at the latest message", () => {
  const startRunMatch = appSource.match(/function startRun\([\s\S]*?\n  \}/);
  assert.ok(startRunMatch, "startRun should be present");
  assert.equal(startRunMatch[0]!.includes('requestScrollToLatest("auto", "submit")'), true);
});

test("chat back-pagination keeps an explicit access path", () => {
  assert.equal(appSource.includes("before_order_key"), true);
  assert.equal(appSource.includes("Load earlier messages"), true);
  assert.equal(appSource.includes("older-missing-cursor"), true);
});

test("chat back-pagination keeps the focused load button mounted while loading", () => {
  assert.equal(appSource.includes("aria-disabled={sdkLoadingOlder || undefined}"), true);
  assert.equal(appSource.includes("aria-busy={sdkLoadingOlder || undefined}"), true);
  assert.equal(appSource.includes("run-transcript-load-older-passive"), false);
});

test("workspace scroll container keeps the right scrollbar affordance stable", () => {
  const runMainMatch = indexCssSource.match(/\.run-main \{[\s\S]*?\n\}/);
  assert.ok(runMainMatch, "run-main styles should be present");
  const runMainCss = runMainMatch[0]!;
  assert.equal(runMainCss.includes("overflow-y: scroll;"), true);
  assert.equal(runMainCss.includes("scrollbar-gutter: stable;"), true);
  assert.equal(indexCssSource.includes(".run-main::-webkit-scrollbar-track"), true);
});

test("session-event SSE stream emits browser-side observability", () => {
  // The candidate-B (zombie SSE) + candidate-C (reducer-drop)
  // stethoscope on the browser side. If a future refactor silently
  // removes the telemetry hooks, this guard breaks before the
  // diagnostic-only observability metric quietly stops shipping.
  assert.equal(
    sessionEventStreamTelemetrySource.includes("/api/client-metrics/session-events-stream"),
    true,
  );
  assert.equal(sessionEventStreamTelemetrySource.includes("createSilenceWatchdog"), true);
  assert.equal(sessionEventStreamTelemetrySource.includes("stream_silent_while_running"), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("opened"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("tank_event_received"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("resync_required"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("stream_error"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("closed_error"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("closed_unmount"'), true);
  assert.equal(appSource.includes("silenceWatchdogRef"), true);
  // The receipt-count telemetry MUST observe before the reducer
  // filter; if a future change swaps the order, the candidate-C
  // signature would be invisible (server-emit vs client-receive
  // delta would be measured at the wrong layer).
  assert.match(
    appSource,
    /logSessionEventStreamEvent\("tank_event_received"[\s\S]{0,200}applySdkDurableEvent/,
  );
});

test("chat scroll diagnostics are prometheus backed", () => {
  assert.equal(chatScrollTelemetrySource.includes('DEBUG_TOKEN = "chat-scroll"'), true);
  assert.equal(chatScrollTelemetrySource.includes("isChatScrollDebugEnabled"), true);
  assert.equal(chatScrollTelemetrySource.includes("/api/client-metrics/chat-scroll"), true);
  assert.equal(chatScrollTelemetrySource.includes("tank.chatScrollEvents"), false);
  assert.equal(appSource.includes("logChatScrollGroups"), true);
  assert.equal(appSource.includes("logChatScrollEntries"), true);
});

test("long-chat scroll lab route is admin gated and uses prometheus metrics", () => {
  assert.equal(mainSource.includes('"/_debug/long-chat"'), true);
  assert.equal(mainSource.includes("LongChatDebugPage"), true);
  assert.equal(mainSource.includes("tank.chatScrollEvents"), false);
  assert.equal(longChatDebugSource.includes("bootstrapAuth"), true);
  assert.equal(longChatDebugSource.includes('user.role === "admin"'), true);
  assert.equal(longChatDebugSource.includes("readChatScrollEvents"), false);
});
