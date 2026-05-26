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
const sessionListDebugSource = readSource("./sessionListDebug.ts");
const sessionListDebugRecorderSource = readSource("./sessionListDebugRecorder.ts");
const sessionListDebugPageSource = readSource("./SessionListDebugPage.tsx");
const sessionListDebugCaptureControlsSource = readSource("./SessionListDebugCaptureControls.tsx");
const turnActivityStateSource = readSource("./turnActivityState.ts");
const sessionAvatarsSource = readSource("./sessionAvatars.tsx");
const adminAvatarManagerSource = readSource("./AdminAvatarManager.tsx");
const mainSource = readSource("./main.tsx");
const indexCssSource = readSource("./index.css");
const styleguideIndexSource = readSource("./styleguide/index.tsx");
const styleguideSessionLauncherSource = readSource("./styleguide/new-session-row.tsx");
const styleguideRuntimeControlsSource = readSource("./styleguide/mode-dropdown.tsx");
const styleguideSessionRowSource = readSource("./styleguide/session-row.tsx");
const styleguidePortfolioWorkspaceSource = readSource("./styleguide/portfolio-workspace.tsx");
const styleguidePortfolioTranscriptSource = readSource("./styleguide/portfolio-transcript.tsx");
const styleguideSharedSource = readSource("./styleguide/shared.tsx");
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
const chatScrollMetricsHandlerSource = readSource(
  "../../backend-go/cmd/tank-operator/handlers_client_metrics.go",
);

test("session activity is not refreshed by a steady interval", () => {
  assert.equal(appSource.includes("POLL_INTERVAL_MS"), false);
  assert.equal(/setInterval\(\s*refreshSessionActivity/.test(appSource), false);
});

test("App-root holds no periodic React state setters (cascade-prone pattern)", () => {
  // Two App-root tickers used to live here and were the dominant source
  // of `correlation=idle` long-task blocks (5+ s p95) observed via
  // `tank_client_long_task_duration_seconds`. Both are retired:
  //
  //   1. `nowMs` / `setNowMs` ticker (1s while any session was Pending,
  //      else 30s) → SessionStats now subscribes to the singleton
  //      timeService at minute/second granularity, re-rendering only
  //      the row whose bucket changed.
  //   2. `clusterHealth*` + `loadClusterHealth` + 30s setInterval →
  //      ClusterHealthWidget now owns its own state and polling.
  //
  // Re-introducing either pattern restores the cascading-rerender
  // failure mode; this guard catches it at PR time. If a future
  // surface legitimately needs a periodic refresh, scope it to its
  // own component (or use the timeService for time-relative labels).
  for (const forbidden of [
    "setNowMs",
    "hasPendingSession",
    "SESSION_BOOT_TICK_MS",
    "SESSION_RUNTIME_TICK_MS",
    "setClusterHealth",
    "loadClusterHealth",
    "sessionBootLabel",
    "sessionRuntimeLabel",
    "sessionBootTitle",
    "sessionRuntimeTitle",
  ]) {
    assert.equal(
      new RegExp(`\\b${forbidden}\\b`).test(
        appSource.replace(/\/\/.*$/gm, "").replace(/\/\*[\s\S]*?\*\//g, ""),
      ),
      false,
      `${forbidden} must not appear in App.tsx code (only in comments). The cascading-rerender pattern is retired; use timeService or co-locate the state with its consumer.`,
    );
  }
  // Sidebar boot/runtime labels must use the timeService hooks, not
  // wall-clock reads at render time. A bare Date.now() at render would
  // give a stale label until the parent re-rendered for some other
  // reason — the failure mode that the old nowMs ticker covered up.
  assert.match(
    appSource,
    /from\s+"\.\/timeService"/,
    "App.tsx must import from ./timeService for relative-time labels",
  );
  assert.match(
    appSource,
    /\buseRelative(Seconds|Minutes)\b/,
    "App.tsx must call a timeService hook (useRelativeSeconds / useRelativeMinutes) so per-row labels stay live without an App-root ticker",
  );
});

test("session activity status states have explicit sidebar styles", () => {
  for (const state of [
    "active",
    "failed",
    "pending",
    "agent-working",
    "agent-waiting",
    "agent-needs-input",
    "agent-stopping",
    "agent-stopped",
    "agent-error",
  ]) {
    assert.equal(
      indexCssSource.includes(`.status-dot.status-${state}`),
      true,
      `missing status-dot style for ${state}`,
    );
  }
  assert.equal(indexCssSource.includes(".session-activity-chip"), false);
  assert.equal(appSource.includes("sessionActivityChips"), false);
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
  assert.equal(appSource.includes("transcriptRowsFromTimelineBody"), true);
  assert.equal(
    appSource.includes("timeline response missing server transcript rows"),
    true,
  );
  assert.equal(appSource.includes("replaceSdkServerRows(projectedEntries"), true);
  assert.equal(appSource.includes("body.events"), false);
  assert.equal(appSource.includes("before_order_key"), false);
  assert.equal(appSource.includes("min_transcript_entries"), false);
  assert.equal(appSource.includes("SDK_TIMELINE_TAIL_EVENT_LIMIT"), false);
  assert.match(
    appSource,
    /\/turns\/\$\{encodeURIComponent\(trimmedTurnId\)\}\/activity/,
  );
  assert.match(
    appSource,
    /authedFetch\(\s*scopedSessionPathForPane\([\s\S]{0,220}\/turns\/\$\{encodeURIComponent\(trimmedTurnId\)\}\/activity/,
  );
  assert.equal(appSource.includes('kind !== "turn_activity"'), true);
});

test("server-projected active turn activity shells own thinking row active state", () => {
  assert.equal(
    turnActivityStateSource.includes('summary?.active === true || summary?.status === "active"'),
    true,
  );
  assert.equal(appSource.includes("turnActivityGroupIsActive(entry.activity, turnId, activeTurnId)"), true);
  assert.equal(appSource.includes("turnActivityShellIsDurablyActive(group.shell.activity)"), true);
  assert.equal(appSource.includes("turnActivityShellIsDurablyActive(entry.activity)"), true);
  assert.equal(appSource.includes("durableActiveTurnActivityShells"), true);
  assert.equal(appSource.includes('logChatScrollEvent("thinking-row-missing"'), true);
  assert.equal(appSource.includes('active: turnId === (activeTurnId?.trim() ?? "")'), false);
  assert.equal(appSource.includes("active: turnId === active,"), true);
  assert.equal(chatScrollMetricsHandlerSource.includes('"thinking-row-missing"'), true);
});

test("turn internals move out of the transcript into a turn view", () => {
  assert.equal(appSource.includes('type RunTab = "chat" | "turns"'), true);
  assert.equal(appSource.includes("buildTurnViewItems"), true);
  assert.equal(appSource.includes("const turnsAvailable = turnViewItems.length > 0"), true);
  assert.equal(appSource.includes("function readSessionRouteFromPath"), true);
  assert.equal(appSource.includes('url.pathname = `/sessions/${encodeURIComponent(id)}${'), true);
  assert.equal(appSource.includes('setActiveTab("turns")'), true);
  assert.equal(appSource.includes("replaceSessionRoute(session.id, \"turns\", routedSelectedTurnId)"), true);
  assert.equal(appSource.includes("window.addEventListener(\"popstate\", applyCurrentSessionRoute)"), true);
  assert.equal(appSource.includes("RunTurnActivityScreen"), true);
  assert.equal(appSource.includes("RunTurnThinkingBubble"), true);
  assert.equal(appSource.includes("turnThinkingGroup"), true);
  assert.equal(appSource.includes("showAssistantAvatar = !ownedByTurnActivity"), true);
  assert.match(appSource, /ownedByTurnActivity\s+showAssistantAvatar/);
  assert.equal(appSource.includes("createTurnActivityEntryGroup(entry, activityEntriesByTurn, activeTurnId)"), true);
  assert.equal(appSource.includes("pushTurnActivityEntryGroup(groups, entry, activityEntriesByTurn)"), false);
  assert.equal(appSource.includes('data-kind="turn-thinking"'), true);
  assert.equal(appSource.includes("function TurnsTab"), true);
  assert.equal(appSource.includes("openTurnPage"), true);
  assert.match(appSource, /<TurnsTab\n\s+active=\{activeTab === "turns"\}[\s\S]{0,260}disabled=\{!turnsAvailable\}/);
  assert.match(
    appSource,
    /<TurnsTab\n\s+active=\{false\}\n\s+disabled\n\s+onOpen=\{\(\) => undefined\}/,
  );
  assert.match(appSource, /if \(activeTab !== "turns" \|\| turnsAvailable\) return;/);
  assert.equal(indexCssSource.includes(".run-turn-view"), true);
  assert.equal(indexCssSource.includes('.run-turn-view-body [data-slot="message"][data-owner="activity"][data-variant="assistant"]'), true);
  assert.equal(indexCssSource.includes(".run-turn-thinking-content"), true);
  assert.equal(indexCssSource.includes(".run-turn-thinking-dots"), true);
  assert.equal(indexCssSource.includes("@keyframes run-thinking-dot-bounce"), true);
  assert.equal(indexCssSource.includes(".run-msg-turn"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("TurnViewSpecimen"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("showAssistantAvatar"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("run-turn-thinking-content"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("run-turn-thinking-dots"), true);
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

test("session-list debug route keeps client row history visible without devtools", () => {
  assert.equal(mainSource.includes('"/_debug/session-list"'), true);
  assert.equal(mainSource.includes("SessionListDebugPage"), true);
  assert.equal(sessionListDebugSource.includes("MAX_EVENTS"), true);
  assert.equal(sessionListDebugSource.includes("sessionStorage"), true);
  assert.equal(sessionListDebugSource.includes("__tankSessionListDebug"), true);
  assert.equal(
    sessionListDebugSource.includes("/api/client-metrics/session-list-debug-capture"),
    true,
  );
  assert.equal(sessionListDebugSource.includes("captureSessionListDebugSnapshot"), true);
  assert.equal(sessionListDebugSource.includes(["created", "session", "name"].join("-") + "-mutated"), false);
  assert.equal(sessionListDebugPageSource.includes("/api/debug/session-list-state"), true);
  assert.equal(sessionListDebugPageSource.includes("subscribeSessionListDebug"), true);
  assert.equal(sessionListDebugCaptureControlsSource.includes("Record 2m"), true);
  assert.equal(sessionListDebugCaptureControlsSource.includes("startSessionListDebugRecording"), true);
  assert.equal(sessionListDebugRecorderSource.includes("subscribeSessionListDebug"), true);
  assert.equal(sessionListDebugRecorderSource.includes("event-sample"), true);
  assert.equal(appSource.includes("Session-list diagnostics"), true);
  assert.equal(appSource.includes('<SessionListDebugCaptureControls source="SettingsAdmin"'), true);
});

test("session rows do not fall back to client-hashed avatar identity", () => {
  assert.equal(sessionAvatarsSource.includes("hashString"), false);
  assert.equal(sessionAvatarsSource.includes("chooseAvatar"), false);
  assert.equal(sessionAvatarsSource.includes("Math.imul"), false);
  assert.equal(sessionAvatarsSource.includes("getSessionAvatar("), false);
  assert.match(sessionAvatarsSource, /getSessionAvatarByID\(assignedAvatarId\?: string \| null\)[\s\S]*?return findAvatarByID\(getAgentAvatarPool\(\), assignedAvatarId\);/);
  assert.match(sessionAvatarsSource, /getSystemAvatarByID\(assignedAvatarId\?: string \| null\)[\s\S]*?return findAvatarByID\(runtimeSystemAvatars, assignedAvatarId\);/);
  assert.equal(appSource.includes("session-avatar-missing"), true);
  assert.equal(appSource.includes("display_name_source"), true);
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

test("settings admin exposes the design portfolio catalog", () => {
  assert.match(
    appSource,
    /href="\/_styleguide"[\s\S]*target="_blank"[\s\S]*Design portfolio/,
  );
  assert.equal(mainSource.includes('"/_styleguide": () => <StyleguideIndex />'), true);
});

test("styleguide catalog tracks current home and sidebar surfaces", () => {
  assert.equal(styleguideIndexSource.includes("new session row"), false);
  assert.equal(styleguideIndexSource.includes("mode dropdown"), false);
  assert.equal(styleguideIndexSource.includes("active / pending / error"), false);
  assert.equal(styleguideIndexSource.includes("claude / api / config / codex"), false);
  assert.equal(styleguideIndexSource.includes("session launcher"), true);
  assert.equal(styleguideIndexSource.includes("runtime controls"), true);
  assert.equal(styleguideSessionLauncherSource.includes("home-initial-grid"), true);
  assert.equal(styleguideSessionLauncherSource.includes("home-repos"), true);
  assert.equal(styleguideSessionLauncherSource.includes("new-row"), false);
  assert.equal(styleguideRuntimeControlsSource.includes("home-choice-grid"), true);
  assert.equal(styleguideRuntimeControlsSource.includes("dropdown-provider"), false);
  assert.equal(styleguideRuntimeControlsSource.includes("new-row"), false);
  assert.equal(styleguidePortfolioWorkspaceSource.includes("new-row"), false);
  assert.equal(styleguidePortfolioTranscriptSource.includes("input selected"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("styleguide-transcript-surface-active"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("styleguide-composer-surface-active"), true);
  assert.equal(indexCssSource.includes(".styleguide-transcript-focus-shell"), true);
  assert.equal(indexCssSource.includes(".run-composer.run-composer-interactive:focus-within"), true);
  assert.equal(indexCssSource.includes('.run-main[aria-label="Transcript"]:is(:focus, :focus-within) .run-transcript::before'), true);
  assert.equal(indexCssSource.includes('.run-main[aria-label="Transcript"]:focus::before'), false);
  assert.equal(indexCssSource.includes(".run-composer.run-composer-runpane:focus-within"), true);
  assert.equal(styleguideSessionRowSource.includes("session-activity-chip"), false);
  assert.equal(styleguideSessionRowSource.includes("mode-interaction-chip"), true);
  assert.equal(styleguideSharedSource.includes("hermes_gui"), true);
  assert.equal(styleguideSharedSource.includes("agent-needs-input"), true);
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

test("workspace title editor survives session creation", () => {
  assert.equal(appSource.includes("autoRenameSessionId"), false);
  assert.equal(appSource.includes("pendingCreateTitleSessionId"), true);
  assert.equal(appSource.includes("WorkspaceTitleSpacer"), true);
  assert.equal(indexCssSource.includes("workspace-title-overlay"), true);
  assert.equal(appSource.includes("requested_name_applied"), false);
  assert.match(appSource, /\.\.\.\(requestedName \? \{ name: requestedName \} : \{\}\)/);
  assert.equal(appSource.includes("beginSessionTitleEdit(session)"), true);
  assert.equal(appSource.includes("setAutoFocusComposerSessionId(created.id)"), true);
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

test("live transcript tail state checks the actual scroll container", () => {
  assert.equal(appSource.includes("syncSdkVisualTailState"), true);
  assert.equal(appSource.includes("transcriptVisuallyAtBottom"), true);
  assert.match(appSource, /const atLiveTail = syncSdkVisualTailState\(\)/);
  assert.match(appSource, /if \(!syncSdkVisualTailState\(\)\) return;/);
});

test("chat back-pagination keeps an explicit access path", () => {
  assert.equal(appSource.includes("before_cursor"), true);
  assert.equal(appSource.includes("beforeCursor"), true);
  assert.equal(appSource.includes("Load earlier messages"), true);
  assert.equal(appSource.includes("older-missing-cursor"), true);
});

test("focused transcript Home and End keys resolve durable conversation edges", () => {
  assert.equal(appSource.includes("scrollTranscriptToConversationStart"), true);
  assert.equal(appSource.includes("scrollTranscriptToConversationEnd"), true);
  assert.match(appSource, /async function scrollTranscriptToConversationStart[\s\S]*?jumpSdkToOldest\("keyboard"\)/);
  assert.match(appSource, /async function scrollTranscriptToConversationEnd[\s\S]*?jumpSdkToLatest\("keyboard"\)/);
  assert.match(appSource, /if \(e\.key === "Home"\)[\s\S]*?scrollTranscriptToConversationStart\(\)/);
  assert.match(appSource, /if \(e\.key === "End"\)[\s\S]*?scrollTranscriptToConversationEnd\(\)/);
  assert.match(appSource, /requestScrollToLatest\("smooth", "keyboard"\)/);
  assert.equal(appSource.includes("transcriptScrollEl.scrollTop = 0"), false);
  assert.equal(appSource.includes("consumedScrollToOldestSignalRef"), true);
  assert.match(appSource, /consumedScrollToOldestSignalRef\.current === scrollToOldestSignal/);
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
  assert.equal(sessionEventStreamTelemetrySource.includes("terminal_matched_by_turn_id"), true);
  assert.equal(sessionEventStreamTelemetrySource.includes("queued_followup_blocked_after_terminal"), true);
  assert.equal(sessionEventStreamTelemetrySource.includes("stale_running_blocked_submit"), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("opened"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("tank_event_received"'), true);
  assert.equal(appSource.includes("terminal_matched_by_turn_id"), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("queued_followup_blocked_after_terminal"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("stale_running_blocked_submit"'), true);
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
  assert.equal(appSource.includes('"keyboard-edge-navigation"'), true);
  assert.equal(appSource.includes('jumpSdkToOldest("button")'), true);
  assert.equal(appSource.includes('jumpSdkToLatest("button")'), true);
  assert.equal(chatScrollTelemetrySource.includes("sessionId: metricString(detail.sessionId)"), true);
  assert.equal(chatScrollTelemetrySource.includes("pagePath: currentPagePath()"), true);
  assert.equal(chatScrollTelemetrySource.includes("pageSearch: currentPageSearch()"), true);
  assert.equal(chatScrollMetricsHandlerSource.includes("logChatScrollClientEvent"), true);
  assert.equal(chatScrollMetricsHandlerSource.includes('"browser chat scroll event"'), true);
  assert.equal(chatScrollMetricsHandlerSource.includes('"session_id"'), true);
  assert.equal(chatScrollMetricsHandlerSource.includes('"page_search"'), true);
});

test("long-chat scroll lab route is admin gated and uses prometheus metrics", () => {
  assert.equal(mainSource.includes('"/_debug/long-chat"'), true);
  assert.equal(mainSource.includes("LongChatDebugPage"), true);
  assert.equal(mainSource.includes("tank.chatScrollEvents"), false);
  assert.equal(longChatDebugSource.includes("bootstrapAuth"), true);
  assert.equal(longChatDebugSource.includes("user.is_admin"), true);
  assert.equal(longChatDebugSource.includes("readChatScrollEvents"), false);
});

test("admin browser gates use Tank is_admin contract, not effective_role", () => {
  assert.equal(appSource.includes("effective_role"), false);
  assert.equal(authSource.includes("effective_role"), false);
  assert.equal(longChatDebugSource.includes("effective_role"), false);
  assert.equal(appSource.includes("is_admin"), true);
  assert.equal(authSource.includes("is_admin"), true);
});
