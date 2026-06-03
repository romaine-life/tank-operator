import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8").replace(/\r\n/g, "\n");
}

const appSource = readSource("./App.tsx");
const appRoutesSource = readSource("./appRoutes.ts");
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
const sessionBarCapabilitiesSource = readSource(
  "../../docs/features/session-bar/capabilities.md",
);
const chatScrollMetricsHandlerSource = readSource(
  "../../backend-go/cmd/tank-operator/handlers_client_metrics.go",
);
const appConfigMapSource = readSource("../../k8s/templates/app-configmap.yaml");
const appDeploymentSource = readSource("../../k8s/templates/deployment.yaml");
const tankServerGoSource = readSource("../../backend-go/cmd/tank-operator/server.go");
const initialModeDiagnoseSource = readSource("../../k8s/app-config/initial-mode-diagnose.md");
const initialModeBugReportSource = readSource("../../k8s/app-config/initial-mode-bug-report.md");
const initialModeQualityGapsSource = readSource("../../k8s/app-config/initial-mode-quality-gaps.md");
const initialModeGoLongSource = readSource("../../k8s/app-config/initial-mode-go-long.md");
const initialModeTestSource = readSource("../../k8s/app-config/initial-mode-test.md");

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

test("AskUserQuestion handoff renders as the session system identity", () => {
  const componentMatch = appSource.match(
    /function RunNeedsInputAnnouncement\([\s\S]*?\n}\n\n(?:\/\/[^\n]*\n)*function RunMetaBlock/,
  );
  assert.ok(componentMatch, "RunNeedsInputAnnouncement component should be present");
  const componentSource = componentMatch[0]!;
  assert.equal(componentSource.includes("systemAvatar: AgentAvatar | null"), true);
  assert.equal(componentSource.includes('className="run-transcript-message"'), true);
  assert.equal(componentSource.includes('data-variant="system"'), true);
  assert.equal(componentSource.includes('data-kind="needs-input-announcement"'), true);
  assert.equal(componentSource.includes('className="run-msg-system-avatar"'), true);
  assert.equal(componentSource.includes("AgentAvatarIcon avatar={systemAvatar}"), true);
  assert.equal(componentSource.includes("<BotIcon"), true);
  assert.match(
    appSource,
    /<RunNeedsInputAnnouncement[\s\S]*systemAvatar=\{systemAvatar\}[\s\S]*showTimestamps=\{showTimestamps\}/,
  );
  assert.equal(indexCssSource.includes(".run-needs-input-announcement-copy"), true);
});

test("transcript meta status lines are attributed to the session system identity", () => {
  // "Stopped" / "Turn stopped by user.", "Turn failed" + provider error,
  // and "Stop requested" are not authored by the human owner or the
  // assistant — they're the session's system identity speaking. They must
  // render inside the same system-avatar frame as session.status banners,
  // not as a headless info/error line floating in the column with no author.
  const componentMatch = appSource.match(
    /function RunMetaBlock\([\s\S]*?\n}\n\n(?:\/\/[^\n]*\n)*function isBackgroundTaskRunning/,
  );
  assert.ok(componentMatch, "RunMetaBlock component should be present");
  const componentSource = componentMatch[0]!;
  assert.equal(componentSource.includes("systemAvatar: AgentAvatar | null"), true);
  assert.equal(componentSource.includes('className="run-transcript-message"'), true);
  assert.equal(componentSource.includes('data-variant="system"'), true);
  assert.equal(componentSource.includes('data-kind="meta"'), true);
  assert.equal(componentSource.includes('className="run-msg-system-avatar"'), true);
  assert.equal(componentSource.includes("AgentAvatarIcon avatar={systemAvatar}"), true);
  assert.equal(componentSource.includes("<BotIcon"), true);
  // Every call site must pass the resolved session systemAvatar so the
  // attribution holds in the main transcript, the Turns detail view, and
  // compacted history.
  const metaCallSites = appSource.match(/<RunMetaBlock\b/g) ?? [];
  assert.equal(metaCallSites.length >= 3, true);
  assert.equal(appSource.includes("<RunMetaBlock entry={g.entry} systemAvatar={systemAvatar} />"), true);
  assert.equal(
    indexCssSource.includes('[data-slot="message"][data-kind="meta"] .run-transcript-message-content'),
    true,
  );
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
  assert.equal(appSource.includes("turnActivityRequestPathForPane(trimmedTurnId)"), true);
  assert.match(
    appSource,
    /\/api\/public\/message-links\/\$\{encodeURIComponent\(publicShareTokenValue\)\}\/turns\/\$\{encodeURIComponent\(turnId\)\}\/activity/,
  );
  assert.equal(appSource.includes('kind !== "turn_activity"'), true);
});

test("public message links render a read-only unauthenticated transcript shell", () => {
  assert.equal(appSource.includes("readInitialPublicMessageLinkRoute"), true);
  assert.equal(appSource.includes("function PublicMessageLinkApp"), true);
  assert.equal(appSource.includes("/api/public/message-links/"), true);
  assert.equal(appSource.includes("publicShareToken={route.token}"), true);
  assert.equal(appSource.includes('composerVisible={activeTab === "chat" && !publicView}'), true);
  assert.equal(appSource.includes("{!publicView && ("), true);
  assert.equal(indexCssSource.includes(".shell.public-share-shell"), true);
  assert.equal(indexCssSource.includes("grid-template-columns: minmax(0, 1fr);"), true);
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

test("active turn thinking row is placed by durable order key, not a turnId-structural rule", () => {
  assert.equal(appSource.includes("function insertActiveTurnThinkingGroups"), true);
  assert.equal(appSource.includes("function entryGroupIncludesTurn"), true);
  assert.equal(appSource.includes("pendingThinkingFallbackIndexes.set(group.turnId, groups.length)"), true);
  // The placeholder position is resolved by durable order keys via the pure
  // transcriptThinkingPlacement module — not by "latest turn-tagged group + 1",
  // which stranded the row above untagged session.status notices on a new
  // session's first turn.
  assert.equal(appSource.includes("resolveThinkingInsertIndex"), true);
  assert.equal(appSource.includes("turnActivityShellTailOrderKey"), true);
  assert.equal(appSource.includes("function entryGroupOrderKey"), true);
  assert.equal(appSource.includes("latestTurnGroupIndex + 1"), false);
  assert.equal(appSource.includes("groups.push(turnThinkingGroup(group.turnId, entry));"), false);
});

test("turn internals move out of the transcript into a turn view", () => {
  assert.equal(appSource.includes('type RunTab = "chat" | "turns"'), true);
  assert.equal(appSource.includes("buildTurnViewItems"), true);
  assert.equal(appSource.includes("const turnsAvailable = turnViewItems.length > 0"), true);
  assert.equal(appSource.includes("function readSessionRouteFromPath"), true);
  assert.equal(appRoutesSource.includes('url.pathname = `/sessions/${encodedId}${'), true);
  assert.equal(appRoutesSource.includes('export type SessionRouteTab = "chat" | "turns";'), true);
  assert.equal(appRoutesSource.includes('export type AppRouteTab = "settings" | "help";'), true);
  assert.equal(appRoutesSource.includes("readAppRouteFromPathname"), true);
  assert.equal(appRoutesSource.includes("buildAppRouteUrl"), true);
  assert.equal(appRoutesSource.includes('url.pathname = "/new";'), true);
  assert.equal(appSource.includes('replaceSessionRoute(session.id, "settings"'), false);
  assert.equal(appSource.includes('replaceSessionRoute(session.id, "help"'), false);
  assert.equal(appSource.includes('replaceAppRoute("settings", homeSettingsTab, homeAdminView)'), true);
  assert.equal(appSource.includes('replaceAppRoute("help")'), true);
  assert.equal(appSource.includes('setActiveTab("turns")'), true);
  assert.equal(appSource.includes("replaceSessionRoute(session.id, \"turns\", routedSelectedTurnNumber)"), true);
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
  assert.equal(indexCssSource.includes(".run-turn-thinking-label"), true);
  assert.equal(indexCssSource.includes("@keyframes run-thinking-shimmer"), true);
  assert.equal(indexCssSource.includes("@keyframes run-thinking-dot-bounce"), false);
  assert.equal(indexCssSource.includes(".run-msg-turn"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("TurnViewSpecimen"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("showAssistantAvatar"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("run-turn-thinking-content"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("run-turn-thinking-label"), true);
  assert.equal(styleguidePortfolioTranscriptSource.includes("run-turn-thinking-last-activity"), true);
});

test("thinking bubble renders an elapsed-time readout while a turn is live", () => {
  // The original dots-only indicator gave no signal for how long a turn
  // had been working or whether activity was still landing. The richer
  // indicator keeps "Thinking..." as the primary state, with duration and
  // last-activity metadata under it.
  //
  // The timer is a purely client-side stopwatch anchored to the first
  // moment the UI renders the bubble for a (user, turn) pair. Backend
  // timestamps are intentionally NOT consulted — earlier revisions tried
  // and kept getting ambushed by a moving `activity.startedAt`, stale
  // sessionStorage from previous tabs, or empty values during the
  // pre-projection window. The anchor is captured eagerly on first read
  // and mirrored to sessionStorage so a mid-turn refresh keeps counting
  // from the same baseline.
  assert.equal(appSource.includes("function formatThinkingElapsed"), true);
  assert.equal(appSource.includes("function RunTurnThinkingDuration"), true);
  assert.equal(appSource.includes("function formatThinkingLastActivity"), true);
  assert.equal(appSource.includes('if (totalSeconds === 0) return "0s";'), true);
  assert.equal(appSource.includes('if (totalSeconds < 5) return "now";'), false);
  assert.equal(appSource.includes("function RunTurnThinkingLastActivity"), true);
  assert.equal(appSource.includes("run-turn-thinking-duration"), true);
  assert.equal(appSource.includes("run-turn-thinking-last-activity"), true);
  assert.equal(appSource.includes("lastActivityAt"), true);
  assert.match(
    appSource,
    /<RunTurnThinkingBubble[\s\S]{0,260}userKey=\{userKey\}[\s\S]{0,80}turnId=\{g\.turnId\}/,
  );
  assert.match(
    appSource,
    /<RunTurnThinkingDuration userKey=\{userKey\} turnId=\{selected\.turnId\}/,
  );
  assert.match(
    appSource,
    /<RunTurnThinkingLastActivity lastActivityAt=\{selected\.lastActivityAt\} turnId=\{selected\.turnId\}/,
  );
  // No backend timestamp should leak into the timer's anchor — the
  // resolver takes only (userKey, turnId) and never reads a startedAt
  // prop. If a future refactor tries to add one back, this assertion
  // makes it visible.
  assert.match(
    appSource,
    /function resolveTurnThinkingStart\(userKey: string, turnId: string\): number/,
  );
  assert.equal(appSource.includes("turnThinkingStartCache"), true);
  assert.equal(appSource.includes("resolveTurnThinkingStart"), true);
  assert.equal(
    appSource.includes("TURN_THINKING_START_CACHE_KEY_PREFIX"),
    true,
  );
  // The thinking duration uses a module-level shared ticker driven
  // through useSyncExternalStore so multiple remounts can't each lose
  // their interval before it fires (Virtuoso recycles items
  // aggressively when new entries push scroll position around). One
  // setInterval, every concurrent bubble subscribes.
  assert.equal(appSource.includes("useTurnThinkingNow"), true);
  assert.equal(appSource.includes("useSyncExternalStore"), true);
  assert.equal(appSource.includes("turnThinkingTickerListeners"), true);
  // Per-user keying so a second account signed in on the same tab
  // can't inherit anchors written by the first account.
  assert.match(
    appSource,
    /userKey=\{user\?\.sub \?\? user\?\.email \?\? "anon"\}/,
  );
  assert.equal(indexCssSource.includes(".run-turn-thinking-duration"), true);
  assert.equal(indexCssSource.includes(".run-turn-thinking-last-activity"), true);
  assert.equal(
    styleguidePortfolioTranscriptSource.includes("run-turn-thinking-duration"),
    true,
  );
  assert.equal(
    styleguidePortfolioTranscriptSource.includes("run-turn-thinking-last-activity"),
    true,
  );
});

test("turn view entry points open at the turn bottom", () => {
  assert.equal(appSource.includes('type TurnViewScrollAnchor = "bottom"'), true);
  assert.equal(
    appSource.includes('onClick={() => onOpenTurn?.(turnId, { anchor: "bottom" })}'),
    true,
  );
  assert.equal(
    appSource.includes('onOpenTurn?.(targetTurnId, { anchor: "bottom" })'),
    true,
  );
  assert.equal(
    appSource.includes('onOpenTurn(turnId, { anchor: "bottom" })'),
    true,
  );
  assert.equal(
    appSource.includes('else openTurnPage(undefined, { anchor: "bottom" });'),
    true,
  );
  assert.equal(
    appSource.includes("const [pendingTurnViewRouteAnchor, setPendingTurnViewRouteAnchor]"),
    true,
  );
  assert.equal(appSource.includes('setPendingTurnViewRouteAnchor("bottom")'), true);
  assert.equal(
    appSource.includes("const [turnViewScrollRequest, setTurnViewScrollRequest]"),
    true,
  );
  assert.equal(appSource.includes("scrollRequest?: TurnViewScrollRequest | null;"), true);
  assert.equal(
    appSource.includes("onScrollRequestConsumed?: (signal: number) => void;"),
    true,
  );
  assert.equal(appSource.includes("scrollRequest={turnViewScrollRequest}"), true);
  assert.equal(
    appSource.includes("onScrollRequestConsumed={clearTurnViewScrollRequest}"),
    true,
  );
  assert.equal(appSource.includes("if (!selected.loaded) return;"), true);
  assert.equal(appSource.includes("if (loading && detailGroups.length === 0) return;"), true);
  assert.equal(
    appSource.includes('body.scrollTo({ top: body.scrollHeight, behavior: "auto" });'),
    true,
  );
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
  assert.match(appSource, /authedEventSource\([\s\S]{0,400}stream: "pinned-repos"/);
});

test("pinned repo shortcuts converge from the durable profile endpoint", () => {
  assert.match(appSource, /authedFetch\("\/api\/github\/pinned-repos"\)/);
  assert.match(appSource, /document\.addEventListener\("visibilitychange", refreshWhenVisible\)/);
  assert.match(appSource, /window\.addEventListener\("focus", refreshOnFocus\)/);
  assert.match(appSource, /addEventListener\("pinned-repos"/);
  assert.match(appSource, /pinnedReposSnapshotVersionRef/);
  assert.match(appSource, /updatedAt < currentVersion/);
});

test("pin reorder writes through the durable pinned-repos endpoint, not browser-local order", () => {
  // Drag/keyboard reordering of repo pins is a per-user preference shared
  // across sessions and devices, so it must persist to profiles.pinned_repos
  // via the same PUT the pin toggle uses — never a browser-local order key
  // (the failure mode the retired tank.sessionOrder / tank.homePinnedRepos
  // keys represented). The order a user drags into is exactly the array PUT.
  assert.match(appSource, /const reorderPinnedRepo = useCallback\(/);
  assert.match(appSource, /reorderPinnedRepoSlugs\(current, sourceSlug, targetSlug\)/);
  assert.match(
    appSource,
    /reorderPinnedRepo[\s\S]{0,400}method: "PUT"[\s\S]{0,200}body: JSON\.stringify\(\{ repos: next \}\)/,
  );
  // Reorder is wired into the picker through onReorderPin.
  assert.match(appSource, /onReorderPin=\{reorderPinnedRepo\}/);
  // No browser-local pin-order shadow is introduced.
  assert.equal(appSource.includes("tank.homePinnedReposOrder"), false);
  assert.equal(appSource.includes("writePinnedReposOrder"), false);
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
  assert.match(appSource, /homeComposerText\.trim\(\) \|\| undefined,[\s\S]*"test"/);
  assert.equal(appSource.includes("Available once your session starts"), false);
});

test("home splash test action stays disabled on the splash page", () => {
  assert.match(appSource, /test=\{\{[\s\S]*?disabled: true,[\s\S]*?title: "Available in an active chat session"/);
  assert.equal(
    appSource.includes("disabled={busy || !CHAT_MODES.has(defaultSessionMode)}"),
    false,
  );
});

test("pull request composer action persists before a PR URL exists", () => {
  assert.match(appSource, /function ComposerToolButtons\(/);
  assert.match(appSource, /const pullRequestURL = testState\?\.pull_request_url\?\.trim\(\) \|\| "";/);
  assert.match(appSource, /pullRequestURL \? \([\s\S]*?href=\{pullRequestURL\}[\s\S]*?\) : \([\s\S]*?disabled[\s\S]*?aria-label="Pull request link unavailable"/);
  assert.equal((appSource.match(/pullRequest=\{\{\}\}/g) ?? []).length, 2);
  assert.equal(appSource.includes("testState?.active && testState.pull_request_url"), false);
});

test("splash and transcript composers share the same tool button component", () => {
  assert.equal((appSource.match(/<ComposerToolButtons\b/g) ?? []).length, 3);
  assert.equal(appSource.includes("toolButtons={\n                    <>"), false);
  assert.equal(appSource.includes("toolButtons={\n                  <>"), false);
  assert.equal(appSource.includes("toolButtons={\n            <>"), false);
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
  assert.equal(indexCssSource.includes(".run-composer.run-composer-interactive::before"), true);
  assert.equal(indexCssSource.includes(".run-composer.run-composer-home"), false);
  assert.equal(indexCssSource.includes(".run-composer.run-composer-runpane"), false);
  assert.equal(indexCssSource.includes('.run-main[aria-label="Transcript"]:is(:focus, :focus-within),'), true);
  assert.equal(indexCssSource.includes('.run-main[aria-label="New session setup"]:is(:focus, :focus-within),'), true);
  assert.equal(indexCssSource.includes('.run-main[aria-label="Transcript"]:is(:focus, :focus-within) .run-transcript::before'), false);
  assert.equal(indexCssSource.includes('.run-main[aria-label="Transcript"]:focus::before'), false);
  assert.equal(indexCssSource.includes('.run-main[aria-label="New session setup"]:focus::before'), false);
  assert.equal(styleguideSessionRowSource.includes("session-activity-chip"), false);
  assert.equal(styleguideSessionRowSource.includes("mode-interaction-chip"), true);
  assert.equal(styleguideSharedSource.includes("codex_app_server"), true);
  assert.equal(styleguideSharedSource.includes("agent-needs-input"), true);
});

test("files tab is gated until the session container is available", () => {
  assert.equal(appSource.includes("sessionFilesAvailable(session)"), true);
  assert.match(appSource, /if \(tab === "files" && !filesAvailable\) return;/);
  assert.match(appSource, /disabled=\{!filesAvailable\}/);
});

test("read-only cross-scope sessions keep an explicit composer affordance", () => {
  assert.match(appSource, /composerVisible=\{activeTab === "chat" && !publicView\}/);
  assert.equal(
    appSource.includes("Production sessions are read-only in this test slot"),
    false,
  );
  assert.equal(
    appSource.includes("Read-only production view. Switch back to this slot's sessions in Settings to send messages."),
    false,
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

test("background page surfaces scheduled wakeups as first-class continuation state", () => {
  assert.match(appSource, /type BackgroundView = "shells" \| "scheduled" \| "detached"/);
  assert.match(
    appSource,
    /function isScheduledWakeupEntry\([\s\S]*?entry\.taskKind === "scheduled_wakeup"/,
  );
  assert.match(appSource, /<span>Scheduled<\/span>[\s\S]*?<span>\{scheduledEntries\.length\}<\/span>/);
  assert.match(
    appSource,
    /scheduledWakeupRowsToEntries\(body\.scheduled_wakeups \?\? \[\]\)/,
  );
  assert.match(
    appSource,
    /<BackgroundScreen\n\s+shellEntries=\{activeBackgroundEntries\}\n\s+scheduledEntries=\{scheduledWakeupEntries\}/,
  );
});

test("background page separates tracked shells from detached shell candidates", () => {
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
    /<BackgroundScreen\n\s+shellEntries=\{activeBackgroundEntries\}\n\s+scheduledEntries=\{scheduledWakeupEntries\}\n\s+detachedEntries=\{detachedShellEntries\}/,
  );
});

test("background stop controls exclude untracked detached shells", () => {
  assert.match(
    appSource,
    /function canStopBackgroundActivity\([\s\S]*?isDetachedShellCandidateEntry\(entry\)\) return false[\s\S]*?isRunningShellInvocationEntry\(entry\)[\s\S]*?isScheduledWakeupEntry\(entry\)\) return false[\s\S]*?isBackgroundTaskEntry\(entry\)[\s\S]*?codexBackgroundStopAvailable/,
  );
  assert.match(appSource, /<BackgroundScreen[\s\S]*canStopEntry=\{canStopBackgroundEntry\}[\s\S]*onStop=\{stopBackgroundActivity\}/);
  assert.match(appSource, /className="run-shell-task-stop"/);
  assert.match(appSource, /\/background-tasks\/\$\{encodeURIComponent\(taskID\)\}\/stop/);
});

test("web search transcript tools use the web glyph", () => {
  assert.match(
    appSource,
    /function isWebToolName\(name: string\): boolean \{[\s\S]*normalized === "websearch"[\s\S]*normalized === "webfetch"[\s\S]*\}/,
  );
  assert.match(
    appSource,
    /if \(isWebToolName\(name\)\) \{\s+return \{ Icon: GlobeIcon, colorClass: "tool-color-search", tooltip: "Web tool call" \};\s+\}/,
  );
});

test("home splash initial-message modes rewrite the first turn deliberately", () => {
  assert.match(
    appSource,
    /type InitialMessageMode = "direct" \| "diagnose" \| "bug_report" \| "quality_gaps" \| "go_long" \| "test"/,
  );
  assert.equal(appSource.includes("composeInitialMessageModePrompt"), true);
  assert.match(appSource, /initialMessageModeSkillName\(mode: InitialMessageMode\): SkillStateName \| undefined/);
  assert.match(appSource, /initialMode !== "direct"[\s\S]*chatModeForHomePrompt\(defaultSessionMode\)/);
});

test("initial-message directives are sourced from the app-config ConfigMap, not baked into the SPA", () => {
  // The directive text moved out of compiled SPA code into k8s/app-config/*.md
  // so it is editable against main without a frontend rebuild: markdown file ->
  // app-config ConfigMap (.Files.Get) -> /api/config (readOptionalFile) -> SPA
  // fetch with an offline const fallback. This mirrors fork_session_prompt_template.
  // The migration is complete when the directive files exist, are wired
  // through the ConfigMap + deployment env + server.go, and the SPA resolves
  // them async from config instead of returning inline literals.

  // SPA reads directives from /api/config, with const fallbacks, async.
  assert.match(
    appSource,
    /async function initialMessageModeDirective\(mode: InitialMessageMode\): Promise<string>/,
  );
  assert.match(appSource, /await fetchAppPublicConfig\(\)/);
  for (const key of [
    "initial_mode_diagnose_directive",
    "initial_mode_bug_report_directive",
    "initial_mode_quality_gaps_directive",
    "initial_mode_go_long_directive",
    "initial_mode_test_directive",
  ]) {
    assert.equal(appSource.includes(key), true, `App.tsx missing config key ${key}`);
    assert.equal(tankServerGoSource.includes(key), true, `server.go missing config key ${key}`);
  }
  assert.match(appSource, /await composeInitialMessageModePrompt\(/);

  // The old persistent-stance diagnose phrasing must not be reintroduced
  // anywhere — it is the bug this change fixes (agents read it as a
  // never-write-code stance for the whole session, not just turn one).
  assert.equal(appSource.includes("diagnose issue without writing code"), false);
  assert.equal(initialModeDiagnoseSource.includes("diagnose issue without writing code"), false);

  // Reworded diagnose directive scopes the no-code stance to the first turn.
  assert.equal(initialModeDiagnoseSource.includes("first message only"), true);
  assert.equal(initialModeDiagnoseSource.includes("this first turn only"), true);

  // Other modes relocated to markdown with their invariants intact.
  assert.equal(initialModeBugReportSource.includes("Initial message type: bug report"), true);
  assert.equal(initialModeBugReportSource.includes("docs/diagnostic-discipline*.md"), true);
  assert.equal(initialModeBugReportSource.includes("Identify the architectural miss"), true);
  assert.equal(initialModeBugReportSource.includes("Propose the code-change shape"), true);
  assert.equal(initialModeBugReportSource.includes("Stop and wait for permission"), true);
  assert.equal(initialModeQualityGapsSource.includes("/workspace/.tank/docs/quality-timeframes.md"), true);
  assert.equal(initialModeQualityGapsSource.includes("/workspace/.tank/docs/migration-policy.md"), true);
  assert.equal(initialModeGoLongSource.includes("Initial message type: go long."), true);
  assert.equal(initialModeGoLongSource.includes("/workspace/.tank/docs/product-inspirations.md"), true);
  assert.equal(initialModeGoLongSource.includes("Settled decisions stay settled"), true);
  assert.equal(initialModeTestSource.includes("run the test skill"), true);

  // ConfigMap renders every initial-mode file via .Files.Get.
  for (const file of [
    "initial-mode-diagnose.md",
    "initial-mode-bug-report.md",
    "initial-mode-quality-gaps.md",
    "initial-mode-go-long.md",
    "initial-mode-test.md",
  ]) {
    assert.equal(
      appConfigMapSource.includes(`.Files.Get "app-config/${file}"`),
      true,
      `app-configmap.yaml does not render ${file}`,
    );
  }

  // Deployment points each directive env var at the mounted ConfigMap file.
  for (const [envVar, file] of [
    ["TANK_INITIAL_MODE_DIAGNOSE_FILE", "initial-mode-diagnose.md"],
    ["TANK_INITIAL_MODE_BUG_REPORT_FILE", "initial-mode-bug-report.md"],
    ["TANK_INITIAL_MODE_QUALITY_GAPS_FILE", "initial-mode-quality-gaps.md"],
    ["TANK_INITIAL_MODE_GO_LONG_FILE", "initial-mode-go-long.md"],
    ["TANK_INITIAL_MODE_TEST_FILE", "initial-mode-test.md"],
  ]) {
    assert.equal(appDeploymentSource.includes(envVar), true, `deployment.yaml missing ${envVar}`);
    assert.equal(
      appDeploymentSource.includes(`/etc/tank-operator/app-config/${file}`),
      true,
      `deployment.yaml missing mount path for ${file}`,
    );
    assert.equal(
      tankServerGoSource.includes(`os.Getenv("${envVar}")`),
      true,
      `server.go does not read ${envVar}`,
    );
  }
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

test("SSE reconnect status stays out of transcript/composer flow", () => {
  assert.equal(appSource.includes("run-connection-banner"), false);
  assert.equal(indexCssSource.includes(".run-connection-banner"), false);
  assert.equal(appSource.includes("onConnectionLabelChange"), true);
  assert.equal(appSource.includes("activeConnectionLabel"), true);
  assert.equal(indexCssSource.includes(".run-connection-pill"), true);
  assert.match(indexCssSource, /absolute title chrome[\s\S]*\.run-connection-pill/);
  assert.match(indexCssSource, /\.workspace-title-overlay \.run-header-name-btn \{[\s\S]*flex: 0 1 auto;[\s\S]*text-align: left;/);
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

test("live-tail follow is durable-mode gated and never hardcoded smooth", () => {
  // Transcript-navigation contract — "Load, ready, reconnect, and resync do
  // not introduce scroll jumps." The transcript's followOutput used to be a
  // hardcoded "smooth", which animated the live-tail catch-up on every
  // row-length change during the open/load/resync row storm. Users saw the
  // transcript "zip around" before it settled. The follow is now:
  //   - gated on the durable NavigationMode via the followLiveTail prop, so
  //     a reader in historical-anchor is never auto-scrolled to the tail;
  //   - instant ("auto"), so a live-tail settle to the measured bottom is a
  //     single snap, not an animated chase.
  // The hardcoded smooth follow is also blocked from reintroduction by
  // scripts/check-removed-chat-runtime.mjs.
  assert.equal(appSource.includes('followOutput="smooth"'), false);
  assert.equal(appSource.includes('followOutput={followLiveTail ? "auto" : false}'), true);
  assert.equal(appSource.includes('followLiveTail={navigationMode === "live-tail"}'), true);
  // Deterministic single landing: the last group is bottom-aligned on first
  // data application instead of being made the topmost item, so a caught-up
  // session lands at the true tail in one measured step.
  assert.match(
    appSource,
    /initialTopMostItemIndex=\{\{ index: Math\.max\(groups\.length - 1, 0\), align: "end" \}\}/,
  );
});

test("chat submit explicitly lands at the latest message", () => {
  const startRunMatch = appSource.match(/function startRun\([\s\S]*?\n  \}/);
  assert.ok(startRunMatch, "startRun should be present");
  assert.equal(startRunMatch[0]!.includes('requestScrollToLatest("auto", "submit")'), true);
});

test("navigation mode owns live-tail vs historical-anchor explicitly", () => {
  // The mode is an explicit state machine (see ./navigationMode.ts);
  // it is NOT derived from continuous DOM measurement of the
  // transcript scroll container. The retired bug (session 269,
  // 2026-05-27) was a DOM-distance heuristic that latched true during
  // react-virtuoso's followOutput smooth-scroll catch-up window,
  // freezing both the scroll-to-bottom affordance and the durable
  // conversation_read_state cursor. The new shape pins these
  // invariants:
  //
  //   - The mode state is a NavigationMode literal, not a boolean
  //     mirror of layout.
  //   - The dispatcher is the only mutation surface.
  //   - Read-cursor advance and pending-tail clearing happen on
  //     mode entry into "live-tail" via the dispatcher's side
  //     effects, not from a DOM-distance branch.
  //   - The pending-tail counter only increments for kind ===
  //     "message" rows; tool / reasoning / activity / meta rows do
  //     not count toward the "N new messages below" affordance.
  //   - Virtuoso's atBottomStateChange is consumed asymmetrically:
  //     a true edge transitions back to live-tail, a false edge is
  //     ignored (gestures own the leaving-tail direction).
  assert.equal(appSource.includes("syncSdkVisualTailState"), false);
  assert.equal(appSource.includes("transcriptVisuallyAtBottom"), false);
  assert.equal(appSource.includes("setUserScrolledUp"), false);
  assert.equal(appSource.includes("sdkAtBottomRef"), false);
  assert.equal(
    appSource.includes('from "./navigationMode"'),
    true,
  );
  assert.equal(
    appSource.includes("function dispatchNavigationMode(reason: NavigationModeReason)"),
    true,
  );
  assert.match(
    appSource,
    /if \(navigationModeRef\.current === "historical-anchor"\) \{[\s\S]{0,400}if \(row\.kind !== "message"\) continue;/,
  );
  const applySdkTranscriptRowsMatch = appSource.match(
    /function applySdkTranscriptRows\(rows: TranscriptEntry\[], orderKey: string\): void \{[\s\S]*?\n  \}/,
  );
  assert.ok(applySdkTranscriptRowsMatch, "applySdkTranscriptRows should be present");
  assert.equal(
    applySdkTranscriptRowsMatch[0]!.includes("mergeProjectedTranscriptRowUpdates"),
    true,
    "post-cursor SSE rows must merge into the rendered projection",
  );
  assert.equal(
    applySdkTranscriptRowsMatch[0]!.includes("if (sdkFoundNewestRef.current)"),
    false,
    "found_newest must not gate rendering of post-cursor live rows",
  );
  assert.match(
    appSource,
    /function handleSdkAtBottomChange\(atBottom: boolean\): void \{[\s\S]{0,300}if \(atBottom\) \{[\s\S]{0,80}dispatchNavigationMode\("virtuoso-at-bottom-true"\)/,
  );
  assert.equal(appSource.includes("sdkPendingTailRowIdsRef"), true);
});

test("user-scroll-up gestures are the only DOM-input mode transition", () => {
  // The leaving-live-tail direction is owned by explicit user input
  // events on the transcript scroll container (wheel / keydown /
  // touchstart+touchmove). The contract test pins all three event
  // attachments AND the live-tail mode gate that ensures gestures
  // are no-ops while the user is already in historical-anchor — so
  // the emitted telemetry stream represents real transitions.
  assert.match(
    appSource,
    /target\.addEventListener\("wheel", onWheel, \{ passive: true \}\)/,
  );
  assert.match(appSource, /target\.addEventListener\("keydown", onKey\)/);
  assert.match(
    appSource,
    /target\.addEventListener\("touchstart", onTouchStart, \{ passive: true \}\)/,
  );
  assert.match(
    appSource,
    /target\.addEventListener\("touchmove", onTouchMove, \{ passive: true \}\)/,
  );
  assert.match(
    appSource,
    /if \(navigationModeRef\.current !== "live-tail"\) return;[\s\S]{0,80}dispatchNavigationMode\("user-scroll-up"\);/,
  );
});

test("read-cursor advance gates on live-tail mode, not DOM distance", () => {
  // conversation_read_state.last_read_order_key only moves when the
  // user is reading the live tail. The gate must be the mode, not a
  // DOM-distance check — pinning the durable contract that the
  // session 269 case violated.
  assert.match(
    appSource,
    /function scheduleSdkReadStateUpdate\(\): void \{[\s\S]{0,800}if \(navigationModeRef\.current !== "live-tail"\) return;/,
  );
  assert.match(
    appSource,
    /async function flushSdkReadStateUpdate\(\): Promise<void> \{[\s\S]{0,400}if \(navigationModeRef\.current !== "live-tail"\) return;/,
  );
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

test("focused Turns page Home and End keys scroll the turn detail to its edges", () => {
  // Turns is its own scroll surface (.run-turn-view-body), so Home/End reuse the
  // turn-view scroll-request channel (anchor "top"/"bottom") instead of the chat
  // SDK jump. Mirrors the chat Home/End focus gate: the shared <main>
  // (transcriptScrollEl) must be the key event target. transcript-navigation
  // contract — keyboard edge navigation extends to the Turns surface.
  assert.match(appSource, /type TurnViewScrollAnchor = "bottom" \| "top"/);
  assert.match(appSource, /if \(!visible \|\| activeTab !== "turns" \|\| !transcriptScrollEl\) return;/);
  assert.match(appSource, /if \(e\.key !== "Home" && e\.key !== "End"\) return;/);
  assert.match(appSource, /const anchor: TurnViewScrollAnchor = e\.key === "Home" \? "top" : "bottom";/);
  assert.match(appSource, /setTurnViewScrollRequest\(\{\s*turnId: effectiveSelectedTurnId,\s*anchor,/);
  assert.match(appSource, /if \(scrollRequest\.anchor === "top"\) \{\s*body\.scrollTo\(\{ top: 0, behavior: "auto" \}\);/);
});

test("focused transcript T opens Turns and Escape returns from Turns", () => {
  assert.equal(appSource.includes("isTranscriptToTurnsShortcut"), true);
  assert.equal(appSource.includes("isTurnsToTranscriptShortcut"), true);
  assert.match(appSource, /targetIsTranscript: e\.target === transcriptScrollEl/);
  assert.match(appSource, /openTurnPage\(undefined, \{ anchor: "bottom" \}\)/);
  assert.match(appSource, /if \(activeTab === "turns"\) return;/);
  assert.match(appSource, /setActiveTab\("chat"\);[\s\S]{0,120}focusTranscriptSection\(\);/);
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
  // The candidate-B (zombie SSE) + projected-row receipt stethoscope
  // on the browser side. If a future refactor silently
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
  assert.equal(appSource.includes('logSessionEventStreamEvent("transcript_rows_received"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("transcript_rows_applied"'), true);
  assert.equal(appSource.includes("terminal_matched_by_turn_id"), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("queued_followup_blocked_after_terminal"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("stale_running_blocked_submit"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("resync_required"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("stream_error"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("closed_error"'), true);
  assert.equal(appSource.includes('logSessionEventStreamEvent("closed_unmount"'), true);
  assert.equal(appSource.includes("silenceWatchdogRef"), true);
  assert.equal(appSource.includes('addEventListener("transcript-rows"'), true);
  assert.equal(appSource.includes('addEventListener("tank-event"'), false);
  assert.equal(appSource.includes("applySdkDurableEvent"), false);
  assert.equal(appSource.includes("eventCountsAsTailOutput"), false);
  // The receipt-count telemetry MUST observe before the projected rows
  // mutate UI state; otherwise server-emit vs client-receive deltas would
  // be measured at the wrong layer.
  assert.match(
    appSource,
    /logSessionEventStreamEvent\("transcript_rows_received"[\s\S]{0,250}applySdkTranscriptRows/,
  );
  assert.match(
    appSource,
    /mergeProjectedTranscriptRowUpdates[\s\S]{0,250}syncSdkRenderedEntries\(\);[\s\S]{0,250}logSessionEventStreamEvent\("transcript_rows_applied"/,
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

test("repo selection stays queryable without sidebar filter UI", () => {
  assert.equal(appSource.includes("sessionFilter"), false);
  assert.equal(appSource.includes("sessionMatchesFilter"), false);
  assert.equal(appSource.includes("repoShortName"), false);
  assert.equal(appSource.includes("filter by repo"), false);
  assert.equal(indexCssSource.includes(".sidebar-filter"), false);
  assert.equal(sessionBarCapabilitiesSource.includes("sessions.repos text[]"), true);
  assert.equal(sessionBarCapabilitiesSource.includes("workspace scans"), true);
  assert.equal(sessionBarCapabilitiesSource.includes("filter input"), false);
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
