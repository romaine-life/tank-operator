import { readFileSync } from "node:fs";
import { test, expect } from "vitest";

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8").replace(
    /\r\n/g,
    "\n",
  );
}

function cssRule(source: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = new RegExp(`${escaped}\\s*\\{([^}]*)\\}`).exec(source);
  return match?.[1] ?? "";
}

const appSource = readSource("./App.tsx");
const appRoutesSource = readSource("./appRoutes.ts");
const authSource = readSource("./auth.ts");
const conversationReducerSource = readSource("./conversationReducer.ts");
const conversationProjectionSource = readSource("./conversationProjection.ts");
const chatScrollTelemetrySource = readSource("./chatScrollTelemetry.ts");
const sessionEventStreamTelemetrySource = readSource(
  "./sessionEventStreamTelemetry.ts",
);
const sessionConnectionIndicatorSource = readSource(
  "./sessionConnectionIndicator.ts",
);
const longChatDebugSource = readSource("./LongChatDebugPage.tsx");
const sessionListDebugSource = readSource("./sessionListDebug.ts");
const sessionListDebugRecorderSource = readSource(
  "./sessionListDebugRecorder.ts",
);
const sessionListDebugPageSource = readSource("./SessionListDebugPage.tsx");
const sessionListDebugCaptureControlsSource = readSource(
  "./SessionListDebugCaptureControls.tsx",
);
const turnActivityCacheSource = readSource("./turnActivityCache.ts");
const turnActivityStateSource = readSource("./turnActivityState.ts");
const sessionAvatarsSource = readSource("./sessionAvatars.tsx");
const adminAvatarManagerSource = readSource("./AdminAvatarManager.tsx");
const mainSource = readSource("./main.tsx");
const indexCssSource = readSource("./index.css");
const styleguideIndexSource = readSource("./styleguide/index.tsx");
const styleguideSessionLauncherSource = readSource(
  "./styleguide/new-session-row.tsx",
);
const styleguideRuntimeControlsSource = readSource(
  "./styleguide/mode-dropdown.tsx",
);
const styleguideSessionRowSource = readSource("./styleguide/session-row.tsx");
const styleguidePortfolioWorkspaceSource = readSource(
  "./styleguide/portfolio-workspace.tsx",
);
const styleguidePortfolioTranscriptSource = readSource(
  "./styleguide/portfolio-transcript.tsx",
);
const styleguideSharedSource = readSource("./styleguide/shared.tsx");
const sessionConfigMapSource = readSource(
  "../../k8s/templates/session-configmap.yaml",
);
const installTankDocsSource = readSource(
  "../../k8s/session-config/install-tank-docs.sh",
);
const agentRunnerLaunchSource = readSource(
  "../../k8s/session-config/claude-runner-launch.sh",
);
const codexRunnerLaunchSource = readSource(
  "../../k8s/session-config/codex-runner-launch.sh",
);
const defaultClaudeSource = readSource(
  "../../k8s/session-config/default-claude.md",
);
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
const observabilitySource = readSource("../../k8s/templates/observability.yaml");
const tankServerGoSource = readSource(
  "../../backend-go/cmd/tank-operator/server.go",
);
const initialModeDiagnoseSource = readSource(
  "../../k8s/app-config/initial-mode-diagnose.md",
);
const initialModeBugReportSource = readSource(
  "../../k8s/app-config/initial-mode-bug-report.md",
);
const initialModeQualityGapsSource = readSource(
  "../../k8s/app-config/initial-mode-quality-gaps.md",
);
const initialModeGoLongSource = readSource(
  "../../k8s/app-config/initial-mode-go-long.md",
);
const initialModeTestSource = readSource(
  "../../k8s/app-config/initial-mode-test.md",
);
const dockerBuildCheckWorkflowSource = readSource(
  "../../.github/workflows/docker-build-check.yaml",
);
const k8sValuesSource = readSource("../../k8s/values.yaml");
const testingDocsSource = readSource("../../docs/testing.md");
const tankOperatorTestSkillSource = readSource(
  "../../k8s/session-config/skills/common/test/references/repos/tank-operator.md",
);

test("session activity is not refreshed by a steady interval", () => {
  expect(appSource.includes("POLL_INTERVAL_MS")).toBe(false);
  expect(/setInterval\(\s*refreshSessionActivity/.test(appSource)).toBe(false);
});

test("Glimmung app image deploys stay fingerprint-first", () => {
  expect(dockerBuildCheckWorkflowSource.includes("Compute image fingerprint")).toBe(true);
  expect(dockerBuildCheckWorkflowSource.includes("Build and push proof image")).toBe(true);
  expect(dockerBuildCheckWorkflowSource.includes("Tag app image by CI run lookup")).toBe(true);
  expect(dockerBuildCheckWorkflowSource).toMatch(/if: matrix\.name == 'app' &&/);
  expect(dockerBuildCheckWorkflowSource.includes("ci-pr-${PR_NUMBER}-run-${RUN_ID}-attempt-${RUN_ATTEMPT}")).toBe(true);
  expect(dockerBuildCheckWorkflowSource.includes("ci-ref-${short_ref_hash}-run-${RUN_ID}-attempt-${RUN_ATTEMPT}")).toBe(true);
  expect(dockerBuildCheckWorkflowSource.includes("--source \"romainecr.azurecr.io/${{ matrix.image-repo }}:${src}\"")).toBe(true);
  expect(dockerBuildCheckWorkflowSource.includes("--image \"${{ matrix.image-repo }}:${lookup_tag}\"")).toBe(true);
  expect(dockerBuildCheckWorkflowSource.includes("Tag image by commit SHA")).toBe(false);
  expect(dockerBuildCheckWorkflowSource.includes("commit-SHA tag")).toBe(false);
  expect(dockerBuildCheckWorkflowSource.includes("sha='${{ github.event.pull_request.head.sha || github.sha }}'")).toBe(false);
  expect(dockerBuildCheckWorkflowSource).not.toMatch(/image-repo\s*\}\}:\$\{\{\s*github\.(?:sha|event\.pull_request\.head\.sha)/);
  expect(dockerBuildCheckWorkflowSource).not.toMatch(/\$\{\{\s*matrix\.image-repo\s*\}\}:\$\{sha\}/);

  expect(k8sValuesSource.includes("Fingerprint-pinned. The build workflow")).toBe(true);
  expect(k8sValuesSource.includes("SHA-pinned")).toBe(false);

  expect(testingDocsSource.includes("CI-run lookup tag")).toBe(true);
  expect(testingDocsSource).toMatch(/raw commit-SHA image\s+alias/);
  expect(tankOperatorTestSkillSource.includes("commit ref")).toBe(true);
  expect(tankOperatorTestSkillSource.includes("branch or SHA")).toBe(false);
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
  //      ClusterHealthScreen now owns its own state and polling.
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
    expect(new RegExp(`\\b${forbidden}\\b`).test(
              appSource.replace(/\/\/.*$/gm, "").replace(/\/\*[\s\S]*?\*\//g, ""),
            ), `${forbidden} must not appear in App.tsx code (only in comments). The cascading-rerender pattern is retired; use timeService or co-locate the state with its consumer.`).toBe(false);
  }
  // Sidebar boot/runtime labels must use the timeService hooks, not
  // wall-clock reads at render time. A bare Date.now() at render would
  // give a stale label until the parent re-rendered for some other
  // reason — the failure mode that the old nowMs ticker covered up.
  expect(appSource, "App.tsx must import from ./timeService for relative-time labels").toMatch(/from\s+"\.\/timeService"/);
  expect(appSource, "App.tsx must call a timeService hook (useRelativeSeconds / useRelativeMinutes) so per-row labels stay live without an App-root ticker").toMatch(/\buseRelative(Seconds|Minutes)\b/);
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
    expect(indexCssSource.includes(`.status-dot.status-${state}`), `missing status-dot style for ${state}`).toBe(true);
  }
  expect(indexCssSource.includes(".session-activity-chip")).toBe(false);
  expect(appSource.includes("sessionActivityChips")).toBe(false);
});

test("chat transcript UI does not use the retired agent-ws route", () => {
  expect(appSource.includes("agent-ws")).toBe(false);
});

test("stop control waits for durable turn interruption", () => {
  const cancelRunMatch = appSource.match(
    /function cancelRun\(\) \{([\s\S]*?)\n  async function requestSdkInterrupt/,
  );
  expect(cancelRunMatch, "cancelRun body should be present").toBeTruthy();
  const cancelRunBody = cancelRunMatch[1]!;
  expect(cancelRunBody.includes("currentRunRef.current = null")).toBe(false);
  expect(cancelRunBody.includes(
          'setRunStatus((prev) => (prev === "running" ? "done" : prev))',
        )).toBe(false);
  // Post-migration: cancelRun does NOT set run status imperatively. The
  // POST returns 202 only after turn.interrupt_requested is durable, and
  // applySdkProjectionToUi reads projection.runStatus === "stopping" to
  // drive the local stopping state. The three negative assertions below
  // pin the UI-local stop-optimism shape out of cancelRun.
  expect(cancelRunBody.includes('setRunStatus("stopping")')).toBe(false);
  expect(cancelRunBody.includes("stopRequested")).toBe(false);
  expect(cancelRunBody.includes("stoppingTargetRef")).toBe(false);
  expect(appSource.includes("if (!res.ok)")).toBe(true);
});

test("AskUserQuestion answers submit a continuation turn via POST /answer", () => {
  // The answer is durably recorded by POST /turns/{askingTurnId}/answer,
  // surfaced as a normal user submission, and delivered to the paused provider
  // callback as input_reply. The open Turns question page refreshes its cached
  // server projection after accept, since turn.input_answered is not itself a
  // visible transcript row. There is still no terminal socket path or retired
  // /input-reply browser route.
  expect(appSource.includes("sendStdin")).toBe(false);
  expect(appSource.includes("/input-reply")).toBe(false);
  expect(appSource.includes("/answer")).toBe(true);
  expect(appSource.includes("silentlyRefreshCachedTurnActivity(turnID)")).toBe(true);
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
  expect(appSource.includes("q.question")).toBe(true);
  expect(appSource.includes("q.header")).toBe(true);
  expect(appSource.includes("q.multiSelect")).toBe(true);
  expect(appSource.includes("opt.preview") || appSource.includes("option.preview")).toBe(true);
});

test("the awaiting-input card reads answered state from the durable event payload", () => {
  // Local selection state alone is not allowed to drive the answered pill —
  // a fresh tab opened after another tab answered must still render the
  // resolved card. The answered flag comes from the durable awaiting-input
  // card payload (entry.awaitingInput), derived server-side from a later
  // turn.input_answered event, not the retired `askUserAnswers` decoration.
  expect(appSource.includes("entry.awaitingInput")).toBe(true);
  expect(appSource.includes("askUserAnswers")).toBe(false);
});

test("pending AskUserQuestion opens collapsed tool groups", () => {
  expect(appSource.includes("isPendingAskUserQuestionTool")).toBe(true);
  expect(appSource.includes("pendingAskUserCount")).toBe(true);
  expect(appSource).toMatch(/entries\.some\(isPendingAskUserQuestionTool\)/);
  expect(appSource).toMatch(/toolGroupDefaultOpen\(\s*g\.entries,\s*autoExpandTools,\s*toolExpansionOverrides,\s*\)/);
  expect(appSource.includes(
          "return autoExpand || isPendingAskUserQuestionTool(entry);",
        )).toBe(true);
});

test("AskUserQuestion questions are the assistant message and the answer form is owned by Turns", () => {
  const messagesMatch = appSource.match(
    /export function RunMessages\([\s\S]*?\n}\n\nfunction AdminObservabilityPanel/,
  );
  expect(messagesMatch, "RunMessages source should be present").toBeTruthy();
  expect(messagesMatch![0].includes("RunAwaitingInputCard")).toBe(false);
  expect(messagesMatch![0].includes("RunNeedsInputAnnouncement")).toBe(false);

  const transcriptActivityMatch = appSource.match(
    /function RunTurnActivityGroup\([\s\S]*?\n}\n\nfunction RunTurnActivityScreen/,
  );
  expect(transcriptActivityMatch, "RunTurnActivityGroup source should be present").toBeTruthy();
  expect(transcriptActivityMatch![0].includes("RunAwaitingInputCard")).toBe(true);
  expect(transcriptActivityMatch![0].includes("RunNeedsInputAnnouncement")).toBe(false);

  const turnScreenMatch = appSource.match(
    /function RunTurnActivityScreen\([\s\S]*?\n}\n\nfunction rangeIntersectsNode/,
  );
  expect(turnScreenMatch, "RunTurnActivityScreen source should be present").toBeTruthy();
  expect(turnScreenMatch![0].includes("RunAwaitingInputCard")).toBe(true);
  expect(appSource.includes('kind === "question"')).toBe(true);
  expect(appSource.includes("resetPage?: boolean")).toBe(true);
  expect(appSource.includes("function RunAwaitingInputNotice")).toBe(false);
  expect(appSource.includes("function RunNeedsInputAnnouncement")).toBe(false);
  expect(appSource.includes('data-kind="needs-input-announcement"')).toBe(false);
  expect(appSource.includes("Answer in Turns")).toBe(true);
  expect(appSource.includes("run-msg-question-action")).toBe(true);
  expect(appSource.includes('data-variant="assistant"')).toBe(true);
  expect(appSource.includes("<SessionAvatarIcon avatar={avatar}")).toBe(true);
  expect(appSource.includes(
          'onOpenTurn?.(questionTurnId, { anchor: "top", resetPage: true })',
        )).toBe(true);
  expect(indexCssSource.includes(".run-needs-input-announcement-copy")).toBe(false);
  expect(indexCssSource.includes(".run-msg-question-action")).toBe(true);
  expect(appSource.includes('data-page-kind={selectedPageInfo?.kind ?? "activity"}')).toBe(true);
  expect(indexCssSource.includes(".run-turn-view {\n  display: flex;")).toBe(true);
  expect(indexCssSource.includes('.run-turn-view-body[data-page-kind="question"]')).toBe(true);
  expect(appSource.includes("askUserQuestionDrafts")).toBe(true);
  expect(appSource).toMatch(/questionIndex:\s*typeof body\.question_index === "number"/);
  expect(appSource).toMatch(/questionSet:\s*typeof body\.question_set === "number"/);
  expect(appSource.includes("turnActivityPageOptionParts")).toBe(true);
  expect(indexCssSource.includes(".run-turn-view-page-option-index")).toBe(true);
  expect(appSource.includes("Next question")).toBe(true);
  expect(appSource.includes("Question set ${selectedPageInfo.questionSet}")).toBe(false);
  expect(appSource.includes("Answer every question before submit.")).toBe(true);
  // The question page heading is a system-user message (system avatar + label),
  // not an orphaned full-width banner. It speaks through the same system-avatar
  // message frame as RunMetaBlock status lines and the background-wake prompt,
  // so the old pinned page-head template is retired.
  expect(appSource.includes(
          "Question ${selectedPageInfo.questionIndex} of ${selectedPageInfo.questionCount}",
        )).toBe(false);
  expect(appSource.includes('className="run-turn-question-page-head"')).toBe(false);
  expect(appSource.includes("function RunQuestionHeadingMessage")).toBe(true);
  expect(appSource.includes("<RunQuestionHeadingMessage")).toBe(true);
  expect(appSource.includes('data-kind="question-heading"')).toBe(true);
  expect(appSource).toMatch(
    /data-variant="system"\s+data-role="system"\s+data-kind="question-heading"/,
  );
  expect(appSource.includes("${questionIndex} of ${questionCount}")).toBe(true);
});

test("background wake prompts stay hidden from chat but visible in Turns activity", () => {
  expect(appSource.includes("turnOnly?: boolean")).toBe(true);
  expect(appSource.includes("wakePrompt?: boolean")).toBe(true);
  expect(appSource.includes("function isTurnActivityUserMessageEntry")).toBe(true);
  expect(appSource.includes("entry.turnOnly === true || entry.wakePrompt === true")).toBe(true);
  expect(appSource.includes(
          "if (isUserMessageEntry(entry) && !isTurnActivityUserMessageEntry(entry))",
        )).toBe(true);
  expect(appSource.includes(
          "(!isUserMessageEntry(entry) || isTurnActivityUserMessageEntry(entry))",
        )).toBe(true);
  expect(turnActivityCacheSource.includes("isTurnActivityUserMessageEntry")).toBe(true);
});

test("Turns view renders server-projected turn context outside paged activity", () => {
  expect(appSource.includes("turn_context?: unknown")).toBe(true);
  expect(appSource.includes("turnActivityLoadsByTurn")).toBe(true);
  expect(appSource.includes("setTurnActivityContextByTurn")).toBe(false);
  expect(appSource.includes("applyTurnActivityLoad")).toBe(false);
  expect(appSource.includes("turnActivityLoadVisibleSnapshot")).toBe(true);
  expect(appSource.includes("completeTurnActivityLoad")).toBe(true);
  expect(appSource.includes("failTurnActivityLoad")).toBe(true);
  expect(appSource).toMatch(/type TurnActivityLoadResult = \{[\s\S]{0,260}finalAnswerEntries: TranscriptEntry\[\];[\s\S]{0,180}pageInfo\?: TurnActivityPageInfo;/);
  expect(appSource).toMatch(/const showActivityLoading =[\s\S]{0,220}!\s*selectedSnapshot/);
  const fetchTurnActivityEntriesMatch = appSource.match(
    /const fetchTurnActivityEntries = useCallback\([\s\S]*?\n  \);\n  const startTurnActivityLoad/,
  );
  expect(fetchTurnActivityEntriesMatch, "fetchTurnActivityEntries source should be present").toBeTruthy();
  expect(fetchTurnActivityEntriesMatch![0].includes("setTurnActivityLoadsByTurn")).toBe(false);
  expect(appSource.includes("selectedTurnContext")).toBe(true);
  expect(appSource.includes("showPromptContextShell")).toBe(true);
  expect(appSource.includes('aria-label="Turn prompt"')).toBe(true);
  expect(appSource.includes('data-context-loaded={selectedTurnContext ? "true" : "false"}')).toBe(true);
  expect(appSource.includes("Prompt context unavailable")).toBe(true);
  expect(appSource.includes("{selectedTurnContext && selected && (")).toBe(false);
  expect(appSource).toMatch(/selectedTurnContext[\s\S]{0,1200}canonicalMessage=\{false\}/);
  expect(appSource.includes("showContextToggleInActivityDivider")).toBe(false);
  expect(appSource.includes("showTurnSectionDivider")).toBe(true);
  expect(appSource.includes("canToggleDetailActivity")).toBe(true);
  expect(appSource.includes("const canToggleDetailActivity = Boolean(selected);")).toBe(true);
  expect(appSource.includes("const canToggleDetailActivity = showDetailActivityDivider;")).toBe(false);
  expect(appSource.includes("const showDetailActivityDivider")).toBe(false);
  expect(appSource.includes("selected.active || selectedCollapse?.defaultCollapsed === true")).toBe(true);
  expect(appSource.includes("selectedActivityCollapseOverride ?? selectedActivityDefaultCollapsed")).toBe(true);
  expect(appSource.includes("Object.prototype.hasOwnProperty.call(prev, turnId)")).toBe(true);
  expect(appSource.includes("{selected && showDetailActivityDivider && (")).toBe(false);
  expect(appSource.includes('className="run-turn-view-prompt-section"')).toBe(true);
  expect(appSource.includes("{showTurnSectionDivider && (")).toBe(true);
  expect(appSource.includes('data-section-divider={showTurnSectionDivider ? "true" : undefined}')).toBe(true);
  expect(indexCssSource.includes(".run-turn-view-prompt-section")).toBe(true);
  expect(indexCssSource.includes('.run-turn-view-body[data-section-divider="true"]')).toBe(true);
  expect(indexCssSource.includes(".run-turn-view-prompt-section + .run-turn-view-body")).toBe(true);
  expect(appSource.includes("run-turn-view-context-label")).toBe(false);
  expect(indexCssSource.includes(".run-turn-view-context-label")).toBe(false);
  expect(styleguidePortfolioTranscriptSource.includes("run-turn-view-context-label")).toBe(false);
  expect(appSource).not.toMatch(/run-turn-view-context-head[\s\S]{0,500}run-turn-view-context-toggle/);
  expect(appSource.includes("originSessionAvatarByID")).toBe(true);
  expect(appSource.includes("originSessionAvatarId")).toBe(true);
  expect(conversationProjectionSource.includes("originSessionAvatarId")).toBe(true);
  expect(appSource.includes("getSessionAvatarByID(null)")).toBe(false);
  expect(appSource.includes("function RunTurnViewControls")).toBe(true);
  expect(appSource.includes('tabsClassName={activeTab === "turns" ? "run-tabs-turn-view" : undefined}')).toBe(true);
  expect(appSource.includes('tabsClassName={\n              homeActiveTab === "chat" ? "run-tabs-turn-view" : undefined\n            }')).toBe(true);
  expect(appSource.includes('homeActiveTab === "chat" && (\n                  <RunTurnViewControls')).toBe(true);
  expect(appSource.includes('className="run-turn-titlebar-controls"')).toBe(true);
  expect(appSource.includes("const [turnStatsExpanded, setTurnStatsExpanded] = useState(false);")).toBe(true);
  expect(appSource.includes("onStatsExpandedChange={setTurnStatsExpanded}")).toBe(true);
  expect(appSource.includes("const [statsExpanded, setStatsExpanded] = useState(false);")).toBe(false);
  expect(appSource.includes('className="run-turn-view-head"')).toBe(false);
  expect(appSource.includes('className="run-turn-view-stats-toggle"')).toBe(true);
  expect(appSource.includes("{statsExpanded && (")).toBe(true);
  expect(appSource.includes('className="run-turn-view-event-progress"')).toBe(false);
  expect(appSource.includes("canTogglePromptContext")).toBe(true);
  expect(appSource.includes("canToggleTurnSections")).toBe(true);
  expect(appSource.includes("turnSectionCycleLabel")).toBe(true);
  expect(appSource.includes('data-direction="both"')).toBe(true);
  expect(appSource.includes("Show agent activity and collapse user message")).toBe(true);
  expect(appSource.includes("Show user message and collapse agent activity")).toBe(true);
  expect(appSource.includes("Cannot cycle turn sections")).toBe(true);
  expect(appSource.includes('disabled={!canTogglePromptContext}')).toBe(true);
  expect(appSource.includes('disabled={!canToggleTurnSections}')).toBe(true);
  expect(appSource.includes('disabled={!canToggleDetailActivity}')).toBe(true);
  expect(appSource.includes('if (group.kind === "thinking") return true;')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes("prompt-and-activity-controls-present")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes('aria-label="Collapse agent activity"')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes('data-direction="both"')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes("Show agent activity and collapse user message")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes("Collapse assistance turn")).toBe(false);
  expect(appSource.includes("No final answer to isolate")).toBe(true);
  expect(appSource.includes("No assistance turn to collapse")).toBe(false);
  expect(indexCssSource.includes(".run-turn-view-context-unavailable")).toBe(true);
  expect(indexCssSource.includes(".run-turn-titlebar-controls")).toBe(true);
  expect(indexCssSource.includes(".run-tabs.run-tabs-turn-view")).toBe(true);
  expect(indexCssSource.includes(".run-turn-view-controls")).toBe(false);
  expect(styleguidePortfolioTranscriptSource.includes("run-turn-titlebar-controls")).toBe(true);
  expect(appSource.includes("(selected?.entries ?? [])")).toBe(false);
});

test("Turns collapse uses server-projected final answers instead of page-local assistant inference", () => {
  expect(appSource.includes("final_answer?: { entries?: unknown };")).toBe(true);
  expect(appSource.includes("selectedFinalAnswerEntries")).toBe(true);
  expect(appSource.includes("finalAnswerEntries: loaded.finalAnswerEntries")).toBe(true);
  expect(appSource.includes("collapse: loaded.collapse")).toBe(true);
  expect(appSource.includes('entry.turnDetailRole !== "final_answer"')).toBe(true);
  expect(appSource.includes("selected?.shell?.activityIds ??")).toBe(false);
  expect(appSource.includes("selected?.shell?.activity?.compactedEntryIds ??")).toBe(false);
  expect(appSource.includes("turn_activity_collapse_applied")).toBe(true);
  expect(appSource.includes("turn_activity_collapse_projection_mismatch")).toBe(true);
});

test("collapsed Turns prompt context stays a minimal one-line entry, not hidden", () => {
  // Collapsing the prompt must NOT remove the body outright. The
  // RunMessageBubble renders in compact mode: the avatar stays at full size and
  // the text collapses to a single ellipsis-truncated line. Collapse is a pure
  // CSS restyle of the SAME DOM the expanded prompt renders — the prompt text
  // and its inline footer are never remounted — so toggling collapsed/expanded
  // never flickers. This pins the wiring (compact follows the collapsed flag)
  // and the stable-DOM renderer so a future refactor can't revert to hiding the
  // prompt OR to the old dual-DOM that swapped a .run-msg-compact-text preview
  // (and the footer's parent) in and out on every toggle.
  expect(appSource.includes("compact?: boolean;")).toBe(true);
  expect(appSource.includes("compact={selectedTurnContextCollapsed}")).toBe(true);
  // The retired dual-DOM preview element is deleted end to end (App + CSS).
  expect(appSource.includes("run-msg-compact-text")).toBe(false);
  expect(indexCssSource.includes("run-msg-compact-text")).toBe(false);
  // Collapse is CSS-only over a stable tree: the full prompt rides the title
  // attribute instead of a separate preview element.
  expect(appSource.includes("const collapsedTitle =")).toBe(true);
  expect(appSource).toMatch(/title=\{compact \? collapsedTitle : undefined\}/);
  expect(appSource.includes("{!compact && variant === \"user\" && visibleAttachments.length > 0 && (")).toBe(false);
  expect(appSource.includes("{!compact && (\n          <div\n            className=\"run-msg-footer\"")).toBe(false);
  expect(appSource.includes("{variant === \"user\" && visibleAttachments.length > 0 && (")).toBe(true);
  expect(appSource.includes("const messageFooter = (")).toBe(true);
  expect(appSource.includes("className=\"run-msg-footer\"")).toBe(true);
  // Footer renders in exactly one position for a no-attachment user prompt
  // (inline, inside the message text) in both collapsed and expanded states.
  expect(appSource.includes("{inlineFooter && messageFooter}")).toBe(true);
  expect(appSource.includes("{!inlineFooter && showFooter && messageFooter}")).toBe(true);
  expect(appSource.includes("data-has-attachments=")).toBe(false);
  expect(indexCssSource).toMatch(
    /\.run-transcript-message-content\s*\{[^}]*width:\s*100%/,
  );
  expect(indexCssSource).toMatch(
    /\.run-transcript-message:is\(\[data-variant="assistant"\], \[data-variant="user"\]\):not\(\[data-compact="true"\]\)\s+\.run-transcript-message-content\s*\{[^}]*display:\s*flex;[\s\S]*flex-wrap:\s*wrap/,
  );
  expect(indexCssSource).toMatch(
    /\.run-transcript-message:is\(\[data-variant="assistant"\], \[data-variant="user"\]\):not\(\[data-compact="true"\]\)\s+\.run-msg-footer\s*\{[^}]*margin-left:\s*auto;[\s\S]*white-space:\s*nowrap/,
  );
  expect(indexCssSource).not.toMatch(/--run-msg-footer-reserve/);
  expect(indexCssSource).not.toMatch(/\.run-msg-footer\s*\{[^}]*position:\s*absolute/);
  // Compact is a CSS restyle of the stable tree: the content stays a block while
  // the message text becomes the single flex row that holds the prompt + footer.
  expect(indexCssSource).toMatch(
    /\.run-transcript-message\[data-compact="true"\]\s+\.run-transcript-message-content\s*\{[^}]*display:\s*block/,
  );
  expect(indexCssSource).toMatch(
    /\.run-transcript-message\[data-compact="true"\]\s+\.run-transcript-message-text\s*\{[^}]*display:\s*flex;[^}]*align-items:\s*flex-end/,
  );
  expect(indexCssSource).toMatch(
    /\.run-transcript-message\[data-compact="true"\]\s+\.run-msg-footer\s*\{[^}]*flex:\s*0\s+0\s+auto;[\s\S]*white-space:\s*nowrap/,
  );
  expect(indexCssSource).toMatch(
    /\.run-turn-activity-divider-controls\s*\{[^}]*width:\s*calc\(96px \+ 0\.4rem\)/,
  );
  expect(styleguidePortfolioTranscriptSource.includes("collapsed-text-preview-controls-inline")).toBe(true);
  // The old "hide the whole bubble when collapsed" gate must be gone.
  expect(appSource.includes("{!selectedTurnContextCollapsed && (")).toBe(false);
  // CSS: the prompt text element itself (not a separate preview) truncates with
  // an ellipsis under data-compact, and the avatar's top anchor is unchanged.
  expect(indexCssSource).toMatch(
    /\.run-transcript-message\[data-compact="true"\]\s+\.run-plain-message-text\s*\{[^}]*text-overflow:\s*ellipsis/,
  );
  expect(indexCssSource).toMatch(
    /\.run-transcript-message\[data-compact="true"\]\s*\{[^}]*align-items:\s*start/,
  );
});

test("Turns prompt context styleguide includes system-authored wake prompt state", () => {
  expect(styleguidePortfolioTranscriptSource.includes("system-background-wake-context")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes("Background task finished - agent re-invoked")).toBe(true);
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
  expect(componentMatch, "RunMetaBlock component should be present").toBeTruthy();
  const componentSource = componentMatch[0]!;
  expect(componentSource.includes("systemAvatar: AgentAvatar | null")).toBe(true);
  expect(componentSource.includes('className="run-transcript-message"')).toBe(true);
  expect(componentSource.includes('data-variant="system"')).toBe(true);
  expect(componentSource.includes('data-kind="meta"')).toBe(true);
  expect(componentSource.includes('className="run-msg-system-avatar"')).toBe(true);
  expect(componentSource.includes("AgentAvatarIcon avatar={systemAvatar}")).toBe(true);
  expect(componentSource.includes("<BotIcon")).toBe(true);
  // Every call site must pass the resolved session systemAvatar so the
  // attribution holds in the main transcript, the Turns detail view, and
  // compacted history.
  const metaCallSites = appSource.match(/<RunMetaBlock\b/g) ?? [];
  expect(metaCallSites.length >= 3).toBe(true);
  expect(appSource.includes(
          "<RunMetaBlock entry={g.entry} systemAvatar={systemAvatar} />",
        )).toBe(true);
  expect(indexCssSource.includes(
          '[data-slot="message"][data-kind="meta"] .run-transcript-message-content',
        )).toBe(true);
});

test("AskUserQuestion placeholder 'Answer questions?' never leaks into App source", () => {
  // The Claude Agent SDK ships this string as the AskUserQuestion
  // checkPermissions message. If it shows up in our renderer, the
  // cutover regressed (either the runner stopped gating, or someone
  // hard-coded the SDK fallback into a fixture).
  expect(appSource.includes("Answer questions?")).toBe(false);
});

test("chat history bootstrap is tail-first and not browser-position based", () => {
  expect(appSource.includes('params.set("anchor", "newest")')).toBe(true);
  expect(appSource.includes('params.set("timeline_id", targetTimelineId)')).toBe(true);
  expect(appSource.includes("first_unread")).toBe(false);
  expect(appSource.includes("tank.transcript.position")).toBe(false);
  expect(appSource.includes("readSdkTranscriptPosition")).toBe(false);
  expect(appSource.includes("writeSdkTranscriptPosition")).toBe(false);
});

test("historical transcript bootstrap requires server-projected turn activity", () => {
  expect(appSource.includes("transcriptRowsFromTimelineBody")).toBe(true);
  expect(appSource.includes("timeline response missing server transcript rows")).toBe(true);
  expect(appSource.includes("replaceSdkServerRows(projectedEntries")).toBe(true);
  expect(appSource.includes("body.events")).toBe(false);
  expect(appSource.includes("before_order_key")).toBe(false);
  expect(appSource.includes("min_transcript_entries")).toBe(false);
  expect(appSource.includes("SDK_TIMELINE_TAIL_EVENT_LIMIT")).toBe(false);
  expect(appSource.includes(
          "turnActivityRequestPathForPane(trimmedTurnId, selectedPage)",
        )).toBe(true);
  expect(appSource).toMatch(/\/api\/public\/message-links\/\$\{encodeURIComponent\(publicShareTokenValue\)\}\/turns\/\$\{encodeURIComponent\(turnId\)\}\/activity/);
  expect(appSource.includes('kind !== "turn_activity"')).toBe(true);
});

test("selected turn activity spinner render emits bounded diagnostics", () => {
  expect(appSource.includes("turnActivityLoadStatusMetricCode")).toBe(true);
  expect(appSource.includes('"turn-activity-selected-loading-stranded"')).toBe(true);
  expect(appSource.includes('"turn-activity-selected-loading-slow"')).toBe(true);
  expect(appSource.includes('"turn-activity-selected-route-session-mismatch"')).toBe(true);
  expect(appSource.includes("selectedActivityRouteSessionMismatch")).toBe(true);
  expect(appSource.includes("window.setTimeout")).toBe(true);
  expect(appSource.includes("TURN_ACTIVITY_STUCK_THRESHOLD_MS")).toBe(true);
  expect(appSource.includes("activityLoadingSessionSwitchTelemetry")).toBe(true);
  expect(appSource.includes("activityLoadingTelemetrySource")).toBe(true);
  expect(appSource.includes("activityLoadingPreviousSessionId")).toBe(true);
  expect(appSource.includes('"session-switch"')).toBe(true);
  expect(appSource.includes('"turns-selected"')).toBe(true);
  expect(appSource.includes("previousSessionId: activityLoadingPreviousSessionId")).toBe(true);
  expect(chatScrollTelemetrySource.includes("previousSessionId?: string")).toBe(true);
  expect(chatScrollTelemetrySource.includes("routeSessionId?: string")).toBe(true);
  expect(chatScrollTelemetrySource.includes("selectedTurnId?: string")).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes("PreviousSessionID")).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes("RouteSessionID")).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes("SelectedTurnID")).toBe(true);
  expect(appSource.includes("reason: selectedLoadingReason")).toBe(true);
  expect(appSource.includes("key: selectedTurnIdForTelemetry")).toBe(true);
  expect(appSource.includes(
          "status: turnActivityLoadStatusMetricCode(selectedLoadStatus)",
        )).toBe(true);
  expect(appSource.includes("activityEntries: selectedTurnActivityChildCount")).toBe(true);
  expect(appSource.includes("durableActiveTurnActivityShells")).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes(
          '"turn-activity-selected-loading-stranded"',
        )).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes(
          '"turn-activity-selected-loading-slow"',
        )).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes(
          '"turn-activity-selected-route-session-mismatch"',
        )).toBe(true);
  expect(observabilitySource.includes("TankTurnActivitySelectedLoadingStranded")).toBe(true);
  expect(observabilitySource.includes("TankTurnActivitySelectedRouteSessionMismatch")).toBe(true);
  expect(observabilitySource.includes("TankTurnActivitySelectedLoadingSlow")).toBe(true);
  expect(observabilitySource.includes(
          'tank_chat_scroll_client_events_total{event="turn-activity-selected-loading-stranded",surface="session"}',
        )).toBe(true);
});

test("public message links render a read-only unauthenticated transcript shell", () => {
  expect(appSource.includes("readInitialPublicMessageLinkRoute")).toBe(true);
  expect(appSource.includes("function PublicMessageLinkApp")).toBe(true);
  expect(appSource.includes("/api/public/message-links/")).toBe(true);
  expect(appSource.includes("publicShareToken={route.token}")).toBe(true);
  expect(appSource).toMatch(/composerVisible=\{\s*\(activeTab === "chat" \|\| activeTab === "turns"\) && !publicView\s*\}/);
  expect(appSource.includes("{!publicView && (")).toBe(true);
  expect(indexCssSource.includes(".shell.public-share-shell")).toBe(true);
  expect(indexCssSource.includes("grid-template-columns: minmax(0, 1fr);")).toBe(true);
});

test("server-projected active turn activity shells own thinking row active state", () => {
  expect(turnActivityStateSource.includes(
          'summary?.active === true || summary?.status === "active"',
        )).toBe(true);
  expect(appSource.includes(
          "turnActivityGroupIsActive(entry.activity, turnId, activeTurnId)",
        )).toBe(true);
  expect(appSource.includes("function turnActivityGroupNeedsInput")).toBe(true);
  expect(appSource.includes("function turnActivityGroupIsNeedsInputTarget")).toBe(true);
  expect(appSource.includes("function insertActiveTurnTailGroups")).toBe(false);
  expect(appSource.includes("const pendingNeedsInputGroups")).toBe(false);
  expect(appSource.includes(
          "pendingNeedsInputFallbackIndexes.set(group.turnId, groups.length)",
        )).toBe(false);
  expect(appSource).toMatch(/group\.active &&\s+!needsInput &&\s+!insertedThinkingTurnIds\.has\(group\.turnId\)/);
  expect(appSource.includes('turnThinkingGroup(group.turnId, entry, "needs_input")')).toBe(true);
  expect(appSource.includes("data-status={status}")).toBe(true);
  expect(appSource.includes("Answer requested")).toBe(true);
  expect(indexCssSource.includes(
          '[data-kind="turn-thinking"][data-status="needs_input"]',
        )).toBe(true);
  expect(appSource.includes("groups.push(group);")).toBe(false);
  expect(appSource.includes(
          "turnActivityShellIsDurablyActive(group.shell.activity)",
        )).toBe(true);
  expect(appSource.includes("turnActivityShellIsDurablyActive(entry.activity)")).toBe(true);
  expect(appSource.includes("durableActiveTurnActivityShells")).toBe(true);
  expect(appSource.includes('logChatScrollEvent("thinking-row-missing"')).toBe(true);
  expect(appSource.includes('active: turnId === (activeTurnId?.trim() ?? "")')).toBe(false);
  expect(appSource.includes('shellSummary?.status !== "needs_input"')).toBe(true);
  expect(appSource.includes('activity?.status === "needs_input"')).toBe(true);
  expect(turnActivityStateSource.includes('summary?.status === "needs_input"')).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes('"thinking-row-missing"')).toBe(true);
});

test("active turn thinking row is placed by durable order key, not a turnId-structural rule", () => {
  expect(appSource.includes("function insertActiveTurnThinkingGroups")).toBe(true);
  expect(appSource.includes("function entryGroupIncludesTurn")).toBe(true);
  expect(appSource.includes(
          "pendingThinkingFallbackIndexes.set(group.turnId, groups.length)",
        )).toBe(true);
  // The placeholder position is resolved by durable order keys via the pure
  // transcriptThinkingPlacement module — not by "latest turn-tagged group + 1",
  // which stranded the row above untagged session.status notices on a new
  // session's first turn.
  expect(appSource.includes("resolveThinkingInsertIndex")).toBe(true);
  expect(appSource.includes("turnActivityShellTailOrderKey")).toBe(true);
  expect(appSource.includes("function entryGroupOrderKey")).toBe(true);
  expect(appSource.includes("latestTurnGroupIndex + 1")).toBe(false);
  expect(appSource.includes("groups.push(turnThinkingGroup(group.turnId, entry));")).toBe(false);
});

test("turn internals move out of the transcript into a turn view", () => {
  expect(appSource).toMatch(/type RunTab =[\s\S]{0,120}"chat"[\s\S]{0,80}"turns"/);
  expect(appSource.includes("buildTurnViewItems")).toBe(true);
  expect(appSource).toMatch(/const turnsAvailable\s*=\s*turnViewItems\.length > 0/);
  expect(appSource.includes("function readSessionRouteFromPath")).toBe(true);
  expect(appRoutesSource.includes("url.pathname = `/sessions/${encodedId}${")).toBe(true);
  // SessionRouteTab covers every routed session surface — turns is the default,
  // and every primary surface is URL-addressable, including the file-browser and
  // background panes; the definition is multi-line, turns-first.
  expect(appRoutesSource).toMatch(
    /export type SessionRouteTab =[\s\S]*?"turns"[\s\S]*?"chat"[\s\S]*?"static"[\s\S]*?"session-data"[\s\S]*?"pull-requests"[\s\S]*?"files"[\s\S]*?"background"/,
  );
  expect(appRoutesSource.includes('export type AppRouteTab = "settings" | "help" | "cluster";')).toBe(true);
  expect(appRoutesSource.includes("readAppRouteFromPathname")).toBe(true);
  expect(appRoutesSource.includes("buildAppRouteUrl")).toBe(true);
  expect(appRoutesSource.includes('url.pathname = "/new";')).toBe(true);
  expect(appSource.includes('replaceSessionRoute(session.id, "settings"')).toBe(false);
  expect(appSource.includes('replaceSessionRoute(session.id, "help"')).toBe(false);
  expect(appSource.includes(
          'replaceAppRoute("settings", homeSettingsTab, homeAdminView)',
        )).toBe(true);
  expect(appSource.includes('replaceAppRoute("help")')).toBe(true);
  expect(appSource.includes('replaceAppRoute("cluster")')).toBe(true);
  expect(appSource.includes('setActiveTab("turns")')).toBe(true);
  // The turns route write threads both the durable turn number AND the page
  // ordinal so /turns/{n}/pages/{p} is deep-linkable; the call is multi-line.
  expect(appSource).toMatch(
    /replaceSessionRoute\(\s*session\.id,\s*"turns",\s*routedSelectedTurnNumber,\s*routedSelectedPageNumber,?\s*\)/,
  );
  // The /transcript route reads back to the main-transcript (chat) view.
  expect(appSource).toMatch(/if \(route\.tab === "chat"\) \{[\s\S]{0,160}setActiveTab\("chat"\)/);
  expect(appSource.includes(
          'window.addEventListener("popstate", applyCurrentSessionRoute)',
        )).toBe(true);
  expect(appSource.includes("RunTurnActivityScreen")).toBe(true);
  expect(appSource.includes("RunTurnThinkingBubble")).toBe(true);
  expect(appSource.includes("turnThinkingGroup")).toBe(true);
  expect(appSource.includes("showAssistantAvatar = !ownedByTurnActivity")).toBe(true);
  expect(appSource).toMatch(/ownedByTurnActivity\s+showAssistantAvatar/);
  expect(appSource).toMatch(/createTurnActivityEntryGroup\(\s*entry,\s*activityEntriesByTurn,\s*activeTurnId,\s*\)/);
  expect(appSource.includes(
          "pushTurnActivityEntryGroup(groups, entry, activityEntriesByTurn)",
        )).toBe(false);
  expect(appSource.includes('data-kind="turn-thinking"')).toBe(true);
  expect(appSource.includes("function TurnsTab")).toBe(true);
  expect(appSource.includes("openTurnPage")).toBe(true);
  // Turns stays a standalone tab only in the read-only public view, where the
  // overflow menu is not rendered. Normal sessions use Turns as the primary
  // surface, so the top-right overflow no longer offers it as a menu row.
  expect(appSource).toMatch(/<TurnsTab\n\s+active=\{activeTab === "turns"\}[\s\S]{0,260}disabled=\{false\}/);
  expect(appSource.includes("turns={{")).toBe(false);
  expect(appSource.includes('setActiveTab("chat");')).toBe(true);
  expect(appSource.includes('if (activeTab !== "turns" || turnsAvailable) return;')).toBe(false);
  expect(indexCssSource.includes(".run-turn-view")).toBe(true);
  expect(indexCssSource.includes(
          '.run-turn-view-body [data-slot="message"][data-owner="activity"]',
        )).toBe(true);
  expect(indexCssSource.includes(".run-turn-thinking-content")).toBe(true);
  expect(indexCssSource.includes(".run-turn-thinking-label")).toBe(true);
  expect(indexCssSource.includes(".run-turn-view-context-toggle")).toBe(false);
  expect(indexCssSource.includes(".run-turn-view-stats-toggle")).toBe(true);
  expect(indexCssSource.includes('.run-turn-view-context[data-collapsed="true"]')).toBe(true);
  expect(indexCssSource.includes("@keyframes run-thinking-shimmer")).toBe(true);
  expect(indexCssSource.includes("@keyframes run-thinking-dot-bounce")).toBe(false);
  expect(indexCssSource.includes(".run-msg-turn")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes("TurnViewSpecimen")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes('useState<HighlightTarget>("activity")')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes('data-design-component="TurnStatsPanel"')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes('className="run-turn-view-stats-toggle"')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes('data-design-component="TurnPromptContext"')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes('className="run-turn-view-context-toggle"')).toBe(false);
  expect(styleguidePortfolioTranscriptSource.includes('data-design-state="expanded-with-divider"')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes('data-design-state="context-unavailable-control-disabled"')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes('data-context-loaded="false"')).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes("showAssistantAvatar")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes("run-turn-thinking-content")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes("run-turn-thinking-label")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes(
          "run-turn-thinking-last-activity",
        )).toBe(true);
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
  expect(appSource.includes("function formatThinkingElapsed")).toBe(true);
  expect(appSource.includes("function RunTurnThinkingDuration")).toBe(true);
  expect(appSource.includes("function formatThinkingLastActivity")).toBe(true);
  expect(appSource.includes('if (totalSeconds === 0) return "0s";')).toBe(true);
  expect(appSource.includes('if (totalSeconds < 5) return "now";')).toBe(false);
  expect(appSource.includes("function RunTurnThinkingLastActivity")).toBe(true);
  expect(appSource.includes("run-turn-thinking-duration")).toBe(true);
  expect(appSource.includes("run-turn-thinking-last-activity")).toBe(true);
  expect(appSource.includes("lastActivityAt")).toBe(true);
  expect(appSource).toMatch(/<RunTurnThinkingBubble[\s\S]{0,260}userKey=\{userKey\}[\s\S]{0,80}turnId=\{g\.turnId\}/);
  expect(appSource.includes("insertTurnDetailThinkingGroup")).toBe(true);
  expect(appSource.includes("turnActivityPageContainsLiveTail")).toBe(true);
  expect(appSource).toMatch(/if \(group\.kind === "thinking"\)[\s\S]{0,220}<RunTurnThinkingBubble[\s\S]{0,180}turnId=\{group\.turnId\}/);
  expect(appSource).toMatch(/if \(group\.kind === "thinking"\)[\s\S]{0,260}lastActivityAt=\{group\.lastActivityAt\}[\s\S]{0,80}avatar=\{avatar\}/);
  expect(appSource.includes("run-turn-view-thinking")).toBe(false);
  expect(appSource.includes("selectedThinkingBubble")).toBe(false);
  // No backend timestamp should leak into the timer's anchor — the
  // resolver takes only (userKey, turnId) and never reads a startedAt
  // prop. If a future refactor tries to add one back, this assertion
  // makes it visible.
  expect(appSource).toMatch(/function resolveTurnThinkingStart\(userKey: string, turnId: string\): number/);
  expect(appSource.includes("turnThinkingStartCache")).toBe(true);
  expect(appSource.includes("resolveTurnThinkingStart")).toBe(true);
  expect(appSource.includes("TURN_THINKING_START_CACHE_KEY_PREFIX")).toBe(true);
  // The thinking duration uses a module-level shared ticker driven
  // through useSyncExternalStore so multiple remounts can't each lose
  // their interval before it fires (Virtuoso recycles items
  // aggressively when new entries push scroll position around). One
  // setInterval, every concurrent bubble subscribes.
  expect(appSource.includes("useTurnThinkingNow")).toBe(true);
  expect(appSource.includes("useSyncExternalStore")).toBe(true);
  expect(appSource.includes("turnThinkingTickerListeners")).toBe(true);
  // Per-user keying so a second account signed in on the same tab
  // can't inherit anchors written by the first account.
  expect(appSource).toMatch(/userKey=\{user\?\.sub \?\? user\?\.email \?\? "anon"\}/);
  expect(indexCssSource.includes(".run-turn-thinking-duration")).toBe(true);
  expect(indexCssSource.includes(".run-turn-thinking-last-activity")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes("run-turn-thinking-duration")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes(
          "run-turn-thinking-last-activity",
        )).toBe(true);
});

test("thinking bubble click expands the matching agent activity details", () => {
  expect(appSource.includes("onActivate?: (turnId: string) => void")).toBe(
    true,
  );
  expect(appSource.includes('const actionLabel = needsInput ? "Answer in Turns" : "Show agent activity";')).toBe(true);
  expect(appSource).toMatch(
    /if \(onActivate\) \{[\s\S]{0,90}onActivate\(turnId\);[\s\S]{0,40}return;[\s\S]{0,60}\}/,
  );
  expect(appSource).toMatch(
    /group\.status === "needs_input"[\s\S]{0,180}setCollapsedActivityTurnIds\(\(prev\) =>[\s\S]{0,220}\[selected\.turnId\]: false/,
  );
  expect(appSource).toMatch(
    /candidate\.kind === "activity" && candidate\.turnId === g\.turnId/,
  );
  expect(appSource).toMatch(
    /g\.status !== "needs_input" && activityGroup\?\.kind === "activity"[\s\S]{0,180}onActivityOpen\?\.\(g\.turnId\)[\s\S]{0,140}setActivityOpen\(entryGroupKey\(activityGroup\), true\)/,
  );
});

test("turn view entry points open at the turn bottom", () => {
  expect(appSource.includes('type TurnViewScrollAnchor = "bottom" | "top"')).toBe(true);
  expect(appSource.includes(
          'onOpenTurn?.(turnId, { anchor: needsInput ? "top" : "bottom" })',
        )).toBe(true);
  // AskUserQuestion uses the same turn navigation path, but with resetPage so
  // the server default opens the pending question page.
  expect(appSource.includes('{ anchor: "top", resetPage: true }')).toBe(true);
  expect(appSource.includes('onOpenTurn(turnId, { anchor: "bottom" })')).toBe(true);
  expect(appSource.includes('(variant === "assistant" || variant === "user")')).toBe(true);
  expect(appSource.includes("function TurnViewButton")).toBe(true);
  expect(appSource.includes("function TranscriptViewButton")).toBe(true);
  expect(appSource.includes("href={href}")).toBe(true);
  expect(appSource.includes('sessionRouteUrl(sessionId, "turns", turnNumber)')).toBe(true);
  expect(appSource.includes("turnLinksEnabled={!publicView}")).toBe(true);
  expect(appSource.includes("pendingTranscriptMessageId")).toBe(true);
  expect(appSource.includes("transcriptHrefForEntry")).toBe(true);
  expect(appSource.includes("onOpenTranscriptMessage")).toBe(true);
  expect(appSource.includes("Open message in transcript")).toBe(true);
  expect(appSource.includes('openTurnPage(undefined, { anchor: "bottom" });')).toBe(true);
  expect(appSource.includes(
          "const [pendingTurnViewRouteAnchor, setPendingTurnViewRouteAnchor]",
        )).toBe(true);
  expect(appSource.includes('setPendingTurnViewRouteAnchor("bottom")')).toBe(true);
  expect(appSource.includes(
          "const [turnViewScrollRequest, setTurnViewScrollRequest]",
        )).toBe(true);
  expect(appSource.includes("scrollRequest?: TurnViewScrollRequest | null;")).toBe(true);
  expect(appSource.includes("onScrollRequestConsumed?: (signal: number) => void;")).toBe(true);
  expect(appSource.includes("scrollRequest={turnViewScrollRequest}")).toBe(true);
  expect(appSource.includes("onScrollRequestConsumed={clearTurnViewScrollRequest}")).toBe(true);
  expect(appSource.includes("if (!selectedSnapshot) return;")).toBe(true);
  expect(appSource.includes("if (loading && detailGroups.length === 0) return;")).toBe(true);
  expect(appSource.includes(
          'body.scrollTo({ top: body.scrollHeight, behavior: "auto" });',
        )).toBe(true);
});

test("chat live stream waits for timeline bootstrap", () => {
  expect(appSource.includes("historyBootstrapped")).toBe(true);
  expect(appSource).toMatch(/if \([\s\S]{0,120}!visible[\s\S]{0,120}!CHAT_MODES\.has\(session\.mode\)[\s\S]{0,120}!historyBootstrapped[\s\S]{0,120}\)\s*return;/);
});

test("browser EventSource streams use opaque stream tickets, not bearer query strings", () => {
  expect(authSource.includes("/api/auth/stream-ticket")).toBe(true);
  expect(authSource.includes("stream_ticket")).toBe(true);
  expect(authSource.includes("access_token")).toBe(false);
  expect(appSource).toMatch(/authedEventSource\([\s\S]{0,400}stream: "session-events"/);
  expect(appSource).toMatch(/authedEventSource\([\s\S]{0,400}stream: "session-list"/);
  expect(appSource).toMatch(/authedEventSource\([\s\S]{0,400}stream: "pinned-repos"/);
});

test("pinned repo shortcuts converge from the durable profile endpoint", () => {
  expect(appSource).toMatch(/authedFetch\("\/api\/github\/pinned-repos"\)/);
  expect(appSource).toMatch(/document\.addEventListener\("visibilitychange", refreshWhenVisible\)/);
  expect(appSource).toMatch(/window\.addEventListener\("focus", refreshOnFocus\)/);
  expect(appSource).toMatch(/addEventListener\("pinned-repos"/);
  expect(appSource).toMatch(/pinnedReposSnapshotVersionRef/);
  expect(appSource).toMatch(/updatedAt < currentVersion/);
});

test("pin reorder writes through the durable pinned-repos endpoint, not browser-local order", () => {
  // Drag/keyboard reordering of repo pins is a per-user preference shared
  // across sessions and devices, so it must persist to profiles.pinned_repos
  // via the same PUT the pin toggle uses — never a browser-local order key
  // (the failure mode the retired tank.sessionOrder / tank.homePinnedRepos
  // keys represented). The order a user drags into is exactly the array PUT.
  expect(appSource).toMatch(/const reorderPinnedRepo = useCallback\(/);
  expect(appSource).toMatch(/reorderPinnedRepoSlugs\(current, sourceSlug, targetSlug\)/);
  expect(appSource).toMatch(/reorderPinnedRepo[\s\S]{0,400}method: "PUT"[\s\S]{0,200}body: JSON\.stringify\(\{ repos: next \}\)/);
  // Reorder is wired into the picker through onReorderPin.
  expect(appSource).toMatch(/onReorderPin=\{reorderPinnedRepo\}/);
  // No browser-local pin-order shadow is introduced.
  expect(appSource.includes("tank.homePinnedReposOrder")).toBe(false);
  expect(appSource.includes("writePinnedReposOrder")).toBe(false);
});

test("browser-native protected resources are not loaded with raw API URLs", () => {
  expect(appSource.includes("src={`/api/sessions/${session.id}/files/raw")).toBe(false);
  expect(appSource.includes("withBrowserImageMime")).toBe(false);
  expect(appSource).toMatch(/authedFileRawURL\(session\.id, selectedFile\.path\)/);
  expect(appSource).toMatch(/authedFileRawURL\(sessionId, target\.path\)/);
  expect(authSource).toMatch(/stream: "file-raw"/);
  expect(authSource).toMatch(/\/files\/raw\?path=/);
});

test("startup transcript rows come from durable conversation events", () => {
  expect(appSource.includes("startupTranscript")).toBe(false);
  expect(appSource.includes("sessionStartupDrafts")).toBe(false);
  expect(appSource.includes("startupDraft")).toBe(false);
  expect(appSource.includes("Continuing previous conversation")).toBe(false);
  expect(appSource.includes("run-continue-hint")).toBe(false);
  expect(appSource.includes("run-transcript-beginning")).toBe(false);
  expect(appSource.includes("Beginning of conversation")).toBe(false);
  expect(appSource.includes("session-status-transition")).toBe(false);
  expect(appSource.includes("initial_turn")).toBe(true);
  expect(appSource.includes("CREATE_TIME_INITIAL_TURN_MODES")).toBe(true);
  expect(appSource.includes("composeLaunchUserPrompt")).toBe(true);
  expect(appSource.includes("seedTurnDeferredAtCreate")).toBe(true);
  // Attachment launches are durable (#865): the browser stages bytes to the
  // launch-attachments endpoint and the backend reconciler dispatches the turn.
  // The retired browser-owned phase two — wait-for-ready then a
  // `existing_user_message` submit from the tab — must not come back.
  expect(appSource).toMatch(/launch-attachments\//);
  expect(appSource.includes("existing_user_message")).toBe(false);
  expect(appSource).toMatch(/if \(seedTurnRequested && !seedTurnSubmittedAtCreate\) \{/);
  expect(conversationReducerSource.includes('"session.status"')).toBe(true);
  expect(appSource).toMatch(/if \(!visible \|\| !CHAT_MODES\.has\(session\.mode\)\) return;\r?\n    if \(timelineBootstrap\.status !== "idle"\) return;/);
});

test("browser transcript normalizer rejects startup session status rows", () => {
  expect(appSource.includes("isFoldableStartupSessionStatusTranscriptRow")).toBe(true);
  expect(appSource).toMatch(/if \(isFoldableStartupSessionStatusTranscriptRow\(record\)\) return null;/);
  expect(appSource.includes('text === "Session is loading."')).toBe(true);
  expect(appSource.includes('text === "Session is ready."')).toBe(true);
  expect(appSource.includes('!id.includes(":provider:")')).toBe(true);
});

test("sidebar order is not browser-local", () => {
  expect(appSource.includes("tank.sessionOrder")).toBe(false);
  expect(appSource.includes("readSessionOrder")).toBe(false);
  expect(appSource.includes("writeSessionOrder")).toBe(false);
  expect(mainSource.includes("tank.sessionOrder")).toBe(false);
  expect(appSource.includes("/api/sessions/order")).toBe(true);
});

test("sidebar skill-state conflicts are not repaired in the frontend", () => {
  const skillStateMatch = appSource.match(
    /function currentSessionSkillState\([\s\S]*?\n\}/,
  );
  expect(skillStateMatch, "currentSessionSkillState should be present").toBeTruthy();
  const skillStateBody = skillStateMatch[0]!;
  expect(appSource.includes("mergeMutualSessionSkillState")).toBe(false);
  expect(skillStateBody.includes('if (rolloutActive) return "rollout"')).toBe(false);
  expect(skillStateBody.includes('if (testActive) return "test"')).toBe(false);
});

test("session-list debug route keeps client row history visible without devtools", () => {
  expect(mainSource.includes('"/_debug/session-list"')).toBe(true);
  expect(mainSource.includes("SessionListDebugPage")).toBe(true);
  expect(sessionListDebugSource.includes("MAX_EVENTS")).toBe(true);
  expect(sessionListDebugSource.includes("sessionStorage")).toBe(true);
  expect(sessionListDebugSource.includes("__tankSessionListDebug")).toBe(true);
  expect(sessionListDebugSource.includes(
          "/api/client-metrics/session-list-debug-capture",
        )).toBe(true);
  expect(sessionListDebugSource.includes("captureSessionListDebugSnapshot")).toBe(true);
  expect(sessionListDebugSource.includes(
          ["created", "session", "name"].join("-") + "-mutated",
        )).toBe(false);
  expect(sessionListDebugPageSource.includes("/api/debug/session-list-state")).toBe(true);
  expect(sessionListDebugPageSource.includes("subscribeSessionListDebug")).toBe(true);
  expect(sessionListDebugCaptureControlsSource.includes("Record 2m")).toBe(true);
  expect(sessionListDebugCaptureControlsSource.includes(
          "startSessionListDebugRecording",
        )).toBe(true);
  expect(sessionListDebugRecorderSource.includes("subscribeSessionListDebug")).toBe(true);
  expect(sessionListDebugRecorderSource.includes("event-sample")).toBe(true);
  expect(appSource.includes("Session-list diagnostics")).toBe(true);
  expect(appSource.includes(
          '<SessionListDebugCaptureControls source="SettingsAdmin"',
        )).toBe(true);
});

test("session rows do not fall back to client-hashed avatar identity", () => {
  expect(sessionAvatarsSource.includes("hashString")).toBe(false);
  expect(sessionAvatarsSource.includes("chooseAvatar")).toBe(false);
  expect(sessionAvatarsSource.includes("Math.imul")).toBe(false);
  expect(sessionAvatarsSource.includes("getSessionAvatar(")).toBe(false);
  expect(sessionAvatarsSource).toMatch(/getSessionAvatarByID\(assignedAvatarId\?: string \| null\)[\s\S]*?return findAvatarByID\(getAgentAvatarPool\(\), assignedAvatarId\);/);
  expect(sessionAvatarsSource).toMatch(/getSystemAvatarByID\(assignedAvatarId\?: string \| null\)[\s\S]*?return findAvatarByID\(runtimeSystemAvatars, assignedAvatarId\);/);
  expect(appSource.includes("session-avatar-missing")).toBe(true);
  // `name` is always non-null now, so the SPA no longer models title
  // provenance: sessionListDebugRow does not emit a display_name_source.
  expect(appSource.includes("display_name_source")).toBe(false);
});

test("home splash test action seeds the first turn as a skill invocation", () => {
  expect(appSource.includes("composeSkillPrompt")).toBe(true);
  expect(appSource).toMatch(/initialSkillName\?: SkillStateName/);
  expect(appSource).toMatch(/\.\.\.\([\s\S]{0,120}requestedInitialSkillName[\s\S]{0,120}\?[\s\S]{0,120}skill_name: requestedInitialSkillName[\s\S]{0,120}: \{\}[\s\S]{0,80}\)/);
  expect(appSource).toMatch(/homeComposerText\.trim\(\) \|\| undefined,[\s\S]*"test"/);
  expect(appSource.includes("Available once your session starts")).toBe(false);
});

test("home splash test action stays disabled on the splash page", () => {
  expect(appSource).toMatch(/test=\{\{[\s\S]*?disabled: true,[\s\S]*?title: "Available in an active chat session"/);
  expect(appSource.includes(
          "disabled={busy || !CHAT_MODES.has(defaultSessionMode)}",
        )).toBe(false);
});

test("break-glass composer action owns approval links and quick approval", () => {
  expect(appSource).toMatch(/function ComposerToolButtons\(/);
  // The PR control is a self-contained popup menu, not a single hard-coded link.
  expect(appSource).toMatch(/function PullRequestMenuButton\(/);
  expect(appSource).toMatch(/<PullRequestMenuButton \{\.\.\.pullRequest\} \/>/);
  expect(appSource).toMatch(/function BreakGlassApprovalMenuButton\(/);
  expect(appSource).toMatch(/<BreakGlassApprovalMenuButton \{\.\.\.breakGlass\} \/>/);
  // Latest PR and the linked PR are computed as distinct menu entries.
  expect(appSource).toMatch(
    /const latestPullRequestURL = agentGitActivity\.pullRequests\[0\]\?\.href \?\? "";/,
  );
  expect(appSource).toMatch(
    /const linkedPullRequestURL = testState\?\.pull_request_url\?\.trim\(\) \?\? "";/,
  );
  // The retired single-URL link shape must not come back.
  expect(appSource.includes("aria-label=\"Pull request link unavailable\"")).toBe(false);
  expect(appSource.includes("aria-label=\"Open pull request in new tab\"")).toBe(false);
  // Break-glass approval is a Tank-owned deep link and Tank-owned decision
  // endpoint. Auth authenticates the admin; it must not render or post grants
  // for Tank's app-specific request.
  expect(appSource).toMatch(/function breakGlassRequestUrl\(/);
  expect(appSource).toMatch(/"break-glass"/);
  expect(appSource).toMatch(/<BreakGlassRequestPage/);
  expect(appSource).toMatch(/Quick approve/);
  expect(appSource).toMatch(/appRouteUrl\("settings", "admin", "break-glass"\)/);
  expect(appSource).toMatch(/quickApproveBreakGlassMenuItem/);
  expect(appSource.includes("function BreakGlassApprovalIndicator")).toBe(false);
  expect(appSource.includes("<BreakGlassApprovalIndicator")).toBe(false);
  expect(appSource.includes("function PRLaneApprovalIndicator")).toBe(false);
  expect(appSource.includes("<PRLaneApprovalIndicator")).toBe(false);
  expect(appSource).toMatch(/type BreakGlassApprovalMenuKind = "github" \| "azure" \| "model" \| "pr-lane";/);
  expect(appSource).toMatch(/pendingPRLaneRequests\(controlActionRows\)/);
  expect(appSource).toMatch(/prLaneApprovalMenuItems\(sessionId, prLaneRequests\)/);
  expect(appSource).toMatch(/onApprovePRLane/);
  expect(indexCssSource.includes(".pr-lane-approval")).toBe(false);
  expect(appSource).toMatch(/\/break-glass-requests\/\$\{encodeURIComponent\(request\.eventId\)\}\/\$\{decision\}/);
  expect(appSource).toMatch(/\/test-slot-model-requests\/\$\{encodeURIComponent\(request\.eventId\)\}\/approve/);
  expect(appSource).toMatch(/pendingBreakGlassRequests\(breakGlassActionRows\)/);
  expect(appSource.includes("request.approvalUrl")).toBe(false);
  expect(appSource.includes("auth.romaine.life/admin")).toBe(false);
  // The retired pre-Tank endpoint shape must not return.
  expect(appSource.includes("git-break-glass/approve")).toBe(false);
  expect(appSource.includes("/admin/git-break-glass")).toBe(false);
  // Disabled placeholder composers still omit a live PR menu.
  expect((appSource.match(/pullRequest=\{\{\}\}/g) ?? []).length).toBe(2);
  expect(appSource.includes("testState?.active && testState.pull_request_url")).toBe(false);
});

test("splash and transcript composers share the same tool button component", () => {
  expect((appSource.match(/<ComposerToolButtons\b/g) ?? []).length).toBe(3);
  expect(appSource.includes("toolButtons={\n                    <>")).toBe(false);
  expect(appSource.includes("toolButtons={\n                  <>")).toBe(false);
  expect(appSource.includes("toolButtons={\n            <>")).toBe(false);
});

test("avatar editor is embedded in Settings admin, not a standalone app route", () => {
  expect(mainSource.includes("/admin/avatars")).toBe(false);
  expect(mainSource.includes("AdminAvatarsPage")).toBe(false);
  expect(appSource.includes("avatarEditorHref")).toBe(false);
  expect(appSource.includes("<AdminAvatarManager")).toBe(true);
  expect(indexCssSource.includes("admin-avatar-page")).toBe(false);
  expect(indexCssSource.includes("admin-avatar-home")).toBe(false);
  expect(adminAvatarManagerSource.includes("bootstrapAuth")).toBe(false);
  expect(adminAvatarManagerSource.includes("Back to app")).toBe(false);
  expect(appChromeCapabilitiesSource.includes("admin route")).toBe(false);
  expect(appChromeCapabilitiesSource.includes("Settings -> Admin avatar pane")).toBe(true);
});

test("settings admin exposes the design portfolio catalog", () => {
  expect(appSource).toMatch(/href="\/_styleguide"[\s\S]*target="_blank"[\s\S]*Design portfolio/);
  expect(mainSource.includes('"/_styleguide": () => <StyleguideIndex />')).toBe(true);
});

test("styleguide catalog tracks current home and sidebar surfaces", () => {
  expect(styleguideIndexSource.includes("new session row")).toBe(false);
  expect(styleguideIndexSource.includes("mode dropdown")).toBe(false);
  expect(styleguideIndexSource.includes("active / pending / error")).toBe(false);
  expect(styleguideIndexSource.includes("claude / api / config / codex")).toBe(false);
  expect(styleguideIndexSource.includes("session launcher")).toBe(true);
  expect(styleguideIndexSource.includes("runtime controls")).toBe(true);
  expect(styleguideSessionLauncherSource.includes("home-initial-grid")).toBe(true);
  expect(styleguideSessionLauncherSource.includes("home-repos")).toBe(true);
  expect(styleguideSessionLauncherSource.includes("new-row")).toBe(false);
  expect(styleguideRuntimeControlsSource.includes("home-choice-grid")).toBe(true);
  expect(styleguideRuntimeControlsSource.includes("dropdown-provider")).toBe(false);
  expect(styleguideRuntimeControlsSource.includes("new-row")).toBe(false);
  expect(styleguidePortfolioWorkspaceSource.includes("new-row")).toBe(false);
  expect(styleguidePortfolioTranscriptSource.includes("input selected")).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes(
          "styleguide-transcript-surface-active",
        )).toBe(true);
  expect(styleguidePortfolioTranscriptSource.includes(
          "styleguide-composer-surface-active",
        )).toBe(true);
  expect(indexCssSource.includes(".styleguide-transcript-focus-shell")).toBe(true);
  expect(indexCssSource.includes(
          ".run-composer.run-composer-interactive:focus-within",
        )).toBe(true);
  expect(indexCssSource.includes(".run-composer.run-composer-interactive::before")).toBe(true);
  expect(indexCssSource.includes(".run-composer.run-composer-home")).toBe(false);
  expect(indexCssSource.includes(".run-composer.run-composer-runpane")).toBe(false);
  expect(indexCssSource.includes(
          '.run-main[aria-label="Transcript"]:is(:focus, :focus-within),',
        )).toBe(true);
  expect(indexCssSource.includes(
          '.run-main[aria-label="New session setup"]:is(:focus, :focus-within),',
        )).toBe(true);
  expect(indexCssSource.includes(
          '.run-main[aria-label="Transcript"]:is(:focus, :focus-within) .run-transcript::before',
        )).toBe(false);
  expect(indexCssSource.includes('.run-main[aria-label="Transcript"]:focus::before')).toBe(false);
  expect(indexCssSource.includes(
          '.run-main[aria-label="New session setup"]:focus::before',
        )).toBe(false);
  expect(styleguideSessionRowSource.includes("session-activity-chip")).toBe(false);
  expect(styleguideSessionRowSource.includes("mode-interaction-chip")).toBe(true);
  expect(styleguideSharedSource.includes("codex_app_server")).toBe(true);
  expect(styleguideSharedSource.includes("agent-needs-input")).toBe(true);
});

test("files tab is gated until the session container is available", () => {
  expect(appSource.includes("sessionFilesAvailable(session)")).toBe(true);
  expect(appSource).toMatch(/if \(tab === "files" && !filesAvailable\) return;/);
  // Files is now a row in the run-header overflow menu; its availability gate
  // rides the menu-tab descriptor rather than a standalone tab button.
  expect(appSource).toMatch(/files=\{\{[\s\S]{0,120}disabled: !filesAvailable,/);
});

test("session bug labels are available at create time", () => {
  expect(appSource.includes("const [homeBugLabels, setHomeBugLabels]")).toBe(true);
  expect(appSource.includes("bug_label: bugLabel")).toBe(true);
  expect(appSource.includes("bug_labels: homeBugLabels")).toBe(true);
  expect(appSource.includes("setHomeBugLabels([]);")).toBe(true);
  expect(appSource.includes("<SessionBugLabelPicker")).toBe(false);
  expect(appSource.includes("bugLabelControl={")).toBe(false);
  expect(sessionBarCapabilitiesSource.includes(
          "splash setup panel stages bug labels",
        )).toBe(true);
});

test("read-only cross-scope sessions keep an explicit composer affordance", () => {
  expect(appSource).toMatch(/composerVisible=\{\s*\(activeTab === "chat" \|\| activeTab === "turns"\) && !publicView\s*\}/);
  expect(appSource.includes("Production sessions are read-only in this test slot")).toBe(false);
  expect(appSource.includes(
          "Read-only production view. Switch back to this slot's sessions in Settings to send messages.",
        )).toBe(false);
  expect(indexCssSource.includes(".run-composer.run-composer-readonly")).toBe(true);
});

test("Turns composer submit selects the newly accepted durable turn", () => {
  expect(appSource.includes('type RunSubmitSurface = "chat" | "turns"')).toBe(true);
  expect(appSource.includes("submitSurface?: RunSubmitSurface")).toBe(true);
  expect(appSource.includes('run.submitSurface === "turns"')).toBe(true);
  expect(appSource.includes("turn_number?: unknown")).toBe(true);
  expect(appSource.includes("setSelectedTurnNumberAnchor")).toBe(true);
  expect(appSource.includes('replaceSessionRoute(session.id, "turns", turnNumber)')).toBe(true);
  expect(appSource.includes("selectedTurnHasPendingAnchor")).toBe(true);
});

test("background page uses stacked full-width sections instead of a side pane", () => {
  expect(indexCssSource).toMatch(/\.run-shell-tasks-page \{[\s\S]*grid-template-rows: auto minmax\(0, 1fr\);/);
  expect(indexCssSource.includes(
          "grid-template-columns: minmax(16rem, 24rem) minmax(0, 1fr)",
        )).toBe(false);
  expect(indexCssSource.includes("border-right: 1px solid var(--border-subtle);")).toBe(false);
});

test("background tab stays discoverable before background entries exist", () => {
  // Background is a permanent row in the run-header overflow menu, so the entry
  // point renders unconditionally — there is no entries.length === 0 gate that
  // could hide it before any background work exists.
  expect(appSource).toMatch(/<span>Background<\/span>/);
  expect(appSource.includes("function BackgroundLedger")).toBe(false);
  // The session view feeds the live count straight through; an empty ledger
  // shows "0" rather than dropping the row.
  expect(appSource).toMatch(/background=\{\{[\s\S]{0,160}active: activeTab === "background"[\s\S]{0,160}count: backgroundLedgerEntries\.length,/);
  // The pre-session home view exposes Background as a disabled, no-op entry.
  expect(appSource).toMatch(/background=\{\{[\s\S]{0,400}active: false[\s\S]{0,400}count: 0[\s\S]{0,400}disabled: true[\s\S]{0,400}title:[\s\S]{0,120}"Background activity is available once the session starts"/);
});

test("background page includes active shell invocations alongside managed tasks", () => {
  expect(appSource).toMatch(/function isShellToolEntry\([\s\S]*?entry\.toolKind === "shell"[\s\S]*?function isRunningShellInvocationEntry\([\s\S]*?isShellToolEntry\(entry\)[\s\S]*?normalizeToolState\(entry\.toolStatus\) === "running"/);
  expect(appSource).toMatch(/const activeBackgroundEntries = useMemo\([\s\S]*?backgroundTaskEntries\.filter\(isBackgroundTaskRunning\)[\s\S]*?runningShellInvocationEntries/);
  expect(appSource).toMatch(/<BackgroundScreen\n\s+shellEntries=\{backgroundShellEntries\}/);
});

test("background page surfaces scheduled wakeups as first-class continuation state", () => {
  expect(appSource).toMatch(/type BackgroundView = "shells" \| "scheduled" \| "control" \| "detached"/);
  expect(appSource).toMatch(/function isScheduledWakeupEntry\([\s\S]*?entry\.taskKind === "scheduled_wakeup"/);
  expect(appSource).toMatch(/<span>Scheduled<\/span>[\s\S]*?<span>\{scheduledEntries\.length\}<\/span>/);
  expect(appSource).toMatch(/scheduled_background_tasks\?: unknown\[\]/);
  expect(appSource).toMatch(/replaceScheduledWakeupEntries\(body\.scheduled_background_tasks \?\? \[\]\)/);
  expect(appSource).toMatch(/mergeScheduledWakeupEntries\(projectedRows\)/);
  expect(appSource).toMatch(/const currentViewHasEntries =[\s\S]*?backgroundView === "scheduled"[\s\S]*?scheduledWakeupEntries\.length > 0/);
  expect(appSource).toMatch(/const nextView =[\s\S]*?backgroundShellEntries\.length > 0[\s\S]*?"shells"[\s\S]*?scheduledWakeupEntries\.length > 0[\s\S]*?"scheduled"/);
  expect(appSource).not.toContain("scheduledWakeupRowsToEntries");
  expect(appSource).not.toContain("/scheduled-wakeups");
  expect(appSource).toMatch(/<BackgroundScreen[\s\S]{0,400}shellEntries=\{backgroundShellEntries\}[\s\S]{0,400}scheduledEntries=\{scheduledWakeupEntries\}/);
});

test("background page surfaces control actions as first-class audit state", () => {
  expect(appSource).toMatch(/function isControlActionEntry\([\s\S]*?entry\.taskKind === "control_action"/);
  expect(appSource).toMatch(/<span>Control<\/span>[\s\S]*?<span>\{controlEntries\.length\}<\/span>/);
  expect(appSource).toMatch(/controlActionRowsToEntries\(body\)/);
  expect(appSource).toMatch(/<BackgroundScreen[\s\S]{0,160}shellEntries=\{backgroundShellEntries\}[\s\S]{0,160}scheduledEntries=\{scheduledWakeupEntries\}[\s\S]{0,160}controlEntries=\{controlActionEntries\}/);
});

test("background page separates tracked shells from detached shell candidates", () => {
  expect(appSource).toMatch(/function isDetachedShellCandidateEntry\([\s\S]*?isShellToolEntry\(entry\)[\s\S]*?detachedShellLaunchReason\(entry\)/);
  expect(appSource).toMatch(/<span>Shells<\/span>[\s\S]*?<span>Detached<\/span>/);
  expect(appSource).toMatch(/const detachedShellEntries = useMemo\([\s\S]*?renderedEntries\.filter\(isDetachedShellCandidateEntry\)/);
  expect(appSource).toMatch(/<BackgroundScreen\n\s+shellEntries=\{backgroundShellEntries\}\n\s+scheduledEntries=\{scheduledWakeupEntries\}\n\s+controlEntries=\{controlActionEntries\}\n\s+detachedEntries=\{detachedShellEntries\}/);
});

test("background stop controls exclude untracked detached shells", () => {
  expect(appSource).toMatch(/function canStopBackgroundActivity\([\s\S]*?isDetachedShellCandidateEntry\(entry\)\) return false[\s\S]*?isRunningShellInvocationEntry\(entry\)[\s\S]*?isScheduledWakeupEntry\(entry\)\) return false[\s\S]*?isBackgroundTaskEntry\(entry\)[\s\S]*?codexBackgroundStopAvailable/);
  expect(appSource).toMatch(/<BackgroundScreen[\s\S]*canStopEntry=\{canStopBackgroundEntry\}[\s\S]*onStop=\{stopBackgroundActivity\}/);
  expect(appSource).toMatch(/className="run-shell-task-stop"/);
  expect(appSource).toMatch(/\/background-tasks\/\$\{encodeURIComponent\(taskID\)\}\/stop/);
});

test("background tasks come from the durable session-level feed, not transcript rows", () => {
  // background_task rows live only inside per-turn activity bodies, never as
  // top-level transcript rows, so the Background feed reads the durable
  // /background-tasks projection rather than filtering renderedEntries (which
  // was empty by construction). Guards the cutover against the old filter
  // returning.
  expect(appSource).toMatch(
    /const backgroundTaskEntries = useMemo\(\s*\(\) =>\s*backgroundTaskLedgerEntries\.filter\(isBackgroundTaskEntry\)/,
  );
  expect(appSource.includes("renderedEntries.filter(isBackgroundTaskEntry)")).toBe(
    false,
  );
  expect(appSource.includes("/background-tasks`")).toBe(true);
  expect(appSource).toMatch(
    /normalizeProjectedTranscriptEntries\(\s*body\.background_tasks \?\? \[\]/,
  );
  // The shells view shows running AND recently completed tasks, so a task that
  // finished while idle (a timer) stays visible; the active subset still drives
  // the badge count.
  expect(appSource).toMatch(
    /const backgroundShellEntries = useMemo\([\s\S]*?\.\.\.backgroundTaskEntries[\s\S]*?\.\.\.runningShellInvocationEntries/,
  );
});

test("web search transcript tools use the web glyph", () => {
  expect(appSource).toMatch(/function isWebToolName\(name: string\): boolean \{[\s\S]*normalized === "websearch"[\s\S]*normalized === "webfetch"[\s\S]*\}/);
  expect(appSource).toMatch(/if \(isWebToolName\(name\)\) \{[\s\S]{0,160}return \{[\s\S]{0,80}Icon: GlobeIcon,[\s\S]{0,80}colorClass: "tool-color-search",[\s\S]{0,80}tooltip: "Web tool call"[\s\S]{0,80}\};[\s\S]{0,80}\}/);
});

test("expanded tool dumps preserve indentation instead of soft wrapping", () => {
  for (const selector of [
    ".run-tool-default-pre",
    ".run-tool-bash-cmd",
    ".run-tool-bash-out",
    ".run-tool-output pre",
  ]) {
    const rule = cssRule(indexCssSource, selector);
    expect(rule, selector).toContain("white-space: pre;");
    expect(rule, selector).toContain("overflow-wrap: normal;");
    expect(rule, selector).toContain("word-break: normal;");
    expect(rule, selector).not.toContain("white-space: pre-wrap;");
    expect(rule, selector).not.toContain("overflow-wrap: anywhere;");
  }
});

test("turn-view tool rows align with the activity message content column", () => {
  const rule = cssRule(
    indexCssSource,
    ".run-turn-view-body > .run-transcript-tool-single .run-transcript-tool",
  );

  expect(rule).toContain("grid-template-columns: 2.625rem minmax(0, 1fr);");
  expect(rule).toContain("column-gap: 0.55rem;");
  expect(rule).toContain("padding-right: 0;");
});

test("home splash initial-message modes rewrite the first turn deliberately", () => {
  expect(appSource).toMatch(/type InitialMessageMode =[\s\S]{0,260}\| "direct"[\s\S]{0,80}\| "diagnose"[\s\S]{0,80}\| "bug_report"[\s\S]{0,80}\| "quality_gaps"[\s\S]{0,80}\| "go_long"[\s\S]{0,80}\| "test"/);
  expect(appSource.includes("composeInitialMessageModePrompt")).toBe(true);
  expect(appSource).toMatch(/initialMessageModeSkillName\([\s\S]{0,120}mode: InitialMessageMode,[\s\S]{0,120}\): SkillStateName \| undefined/);
  expect(appSource).toMatch(/initialMode !== "direct"[\s\S]*chatModeForHomePrompt\(defaultSessionMode\)/);
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
  expect(appSource).toMatch(/async function initialMessageModeDirective\([\s\S]{0,120}mode: InitialMessageMode,[\s\S]{0,120}\): Promise<string>/);
  expect(appSource).toMatch(/await fetchAppPublicConfig\(\)/);
  for (const key of [
    "initial_mode_diagnose_directive",
    "initial_mode_bug_report_directive",
    "initial_mode_quality_gaps_directive",
    "initial_mode_go_long_directive",
    "initial_mode_test_directive",
  ]) {
    expect(appSource.includes(key), `App.tsx missing config key ${key}`).toBe(true);
    expect(tankServerGoSource.includes(key), `server.go missing config key ${key}`).toBe(true);
  }
  expect(appSource).toMatch(/await composeInitialMessageModePrompt\(/);

  // The old persistent-stance diagnose phrasing must not be reintroduced
  // anywhere — it is the bug this change fixes (agents read it as a
  // never-write-code stance for the whole session, not just turn one).
  expect(appSource.includes("diagnose issue without writing code")).toBe(false);
  expect(initialModeDiagnoseSource.includes("diagnose issue without writing code")).toBe(false);

  // Reworded diagnose directive scopes the no-code stance to the first turn.
  expect(initialModeDiagnoseSource.includes("first message only")).toBe(true);
  expect(initialModeDiagnoseSource.includes("this first turn only")).toBe(true);

  // Other modes relocated to markdown with their invariants intact.
  expect(initialModeBugReportSource.includes("Initial message type: bug report")).toBe(true);
  expect(initialModeBugReportSource.includes("docs/diagnostic-discipline*.md")).toBe(true);
  expect(initialModeBugReportSource.includes("Identify the architectural miss")).toBe(true);
  expect(initialModeBugReportSource.includes("Propose the code-change shape")).toBe(true);
  expect(initialModeBugReportSource.includes("Stop and wait for permission")).toBe(true);
  expect(initialModeQualityGapsSource.includes(
          "/workspace/.tank/docs/quality-timeframes.md",
        )).toBe(true);
  expect(initialModeQualityGapsSource.includes(
          "/workspace/.tank/docs/migration-policy.md",
        )).toBe(true);
  expect(initialModeGoLongSource.includes("Initial message type: go long.")).toBe(true);
  expect(initialModeGoLongSource.includes(
          "/workspace/.tank/docs/product-inspirations.md",
        )).toBe(true);
  expect(initialModeGoLongSource.includes("Settled decisions stay settled")).toBe(true);
  expect(initialModeTestSource.includes("run the test skill")).toBe(true);

  // ConfigMap renders every initial-mode file via .Files.Get.
  for (const file of [
    "initial-mode-diagnose.md",
    "initial-mode-bug-report.md",
    "initial-mode-quality-gaps.md",
    "initial-mode-go-long.md",
    "initial-mode-test.md",
  ]) {
    expect(appConfigMapSource.includes(`.Files.Get "app-config/${file}"`), `app-configmap.yaml does not render ${file}`).toBe(true);
  }

  // Deployment points each directive env var at the mounted ConfigMap file.
  for (const [envVar, file] of [
    ["TANK_INITIAL_MODE_DIAGNOSE_FILE", "initial-mode-diagnose.md"],
    ["TANK_INITIAL_MODE_BUG_REPORT_FILE", "initial-mode-bug-report.md"],
    ["TANK_INITIAL_MODE_QUALITY_GAPS_FILE", "initial-mode-quality-gaps.md"],
    ["TANK_INITIAL_MODE_GO_LONG_FILE", "initial-mode-go-long.md"],
    ["TANK_INITIAL_MODE_TEST_FILE", "initial-mode-test.md"],
  ]) {
    expect(appDeploymentSource.includes(envVar), `deployment.yaml missing ${envVar}`).toBe(true);
    expect(appDeploymentSource.includes(`/etc/tank-operator/app-config/${file}`), `deployment.yaml missing mount path for ${file}`).toBe(true);
    expect(tankServerGoSource.includes(`os.Getenv("${envVar}")`), `server.go does not read ${envVar}`).toBe(true);
  }
});

test("quality gap policy docs are bundled into session config", () => {
  expect(sessionConfigMapSource.includes("install-tank-docs.sh")).toBe(true);
  expect(sessionConfigMapSource.includes('$.Files.Glob "session-config/docs/**"')).toBe(true);
  expect(sessionConfigMapSource.includes("docs__")).toBe(true);
  expect(installTankDocsSource.includes("/workspace/.tank/docs")).toBe(true);
  expect(agentRunnerLaunchSource.includes("install-tank-docs.sh")).toBe(true);
  expect(codexRunnerLaunchSource.includes("install-tank-docs.sh")).toBe(true);
  expect(defaultClaudeSource.includes("/workspace/.tank/docs/")).toBe(true);
  expect(bundledQualityTimeframesSource.includes("# Quality Timeframes")).toBe(true);
  expect(bundledMigrationPolicySource.includes("# Migration Policy")).toBe(true);
});

test("workspace title editor survives session creation", () => {
  expect(appSource.includes("autoRenameSessionId")).toBe(false);
  expect(appSource.includes("pendingCreateTitleSessionId")).toBe(true);
  expect(appSource.includes("WorkspaceTitleSpacer")).toBe(true);
  expect(indexCssSource.includes("workspace-title-overlay")).toBe(true);
  expect(appSource.includes("requested_name_applied")).toBe(false);
  expect(appSource).toMatch(/\.\.\.\(requestedName \? \{ name: requestedName \} : \{\}\)/);
  expect(appSource.includes("beginSessionTitleEdit(session)")).toBe(true);
  expect(appSource.includes("setAutoFocusComposerSessionId(created.id)")).toBe(true);
});

test("SSE reconnect status stays out of transcript/composer flow", () => {
  expect(appSource.includes("run-connection-banner")).toBe(false);
  expect(indexCssSource.includes(".run-connection-banner")).toBe(false);
  expect(appSource.includes("onConnectionLabelChange")).toBe(true);
  expect(appSource.includes("activeConnectionLabel")).toBe(true);
  expect(appSource.includes("sessionConnectionIndicatorLabel")).toBe(true);
  expect(sessionConnectionIndicatorSource.includes(
          "CONNECTION_CONNECTING_VISIBLE_AFTER_MS",
        )).toBe(true);
  expect(sessionConnectionIndicatorSource).toMatch(/case "connecting":[\s\S]*delayedConnectingVisible/);
  expect(indexCssSource.includes(".run-connection-pill")).toBe(true);
  expect(indexCssSource).toMatch(/absolute title chrome[\s\S]*\.run-connection-pill/);
  expect(indexCssSource).toMatch(/\.workspace-title-overlay \.run-header-name-btn \{[\s\S]*flex: 0 1 auto;[\s\S]*text-align: left;/);
});

test("mounted chat reactivation resets local timeline state before bootstrap", () => {
  expect(appSource.includes("visible-reactivation")).toBe(true);
  expect(appSource.includes("resetSdkTimelineBootstrapState")).toBe(true);
  expect(appSource.includes("reduceTimelineBootstrap")).toBe(true);
  expect(appSource.includes("scrollToLatestOnReady: !hasExplicitTarget")).toBe(true);
  expect(appSource.includes('requestScrollToLatest("auto", source)')).toBe(true);
  expect(appSource).toMatch(
    /useLayoutEffect\(\(\) => \{[\s\S]{0,500}const previousSessionId = sessionIdRef\.current;[\s\S]{0,500}setActivityLoadingSessionSwitchTelemetry[\s\S]{0,500}sessionIdRef\.current = session\.id;[\s\S]{0,500}resetSdkTimelineBootstrapState\("session-change"/,
  );
  expect(appSource).toMatch(/if \(timelineBootstrap\.status !== "idle"\) return;/);
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
  expect(appSource.includes('followOutput="smooth"')).toBe(false);
  expect(appSource).toMatch(/followOutput=\{followLiveTail \? "auto" : false\}/);
  expect(appSource).toMatch(/followLiveTail=\{navigationMode === "live-tail"\}/);
  // Deterministic single landing: the last group is bottom-aligned on first
  // data application instead of being made the topmost item, so a caught-up
  // session lands at the true tail in one measured step.
  expect(appSource).toMatch(/initialTopMostItemIndex=\{\{[\s\S]{0,120}index: Math\.max\(groups\.length - 1, 0\),[\s\S]{0,80}align: "end"[\s\S]{0,80}\}\}/);
});

test("chat submit explicitly lands at the latest message", () => {
  const startRunMatch = appSource.match(/function startRun\([\s\S]*?\n  \}/);
  expect(startRunMatch, "startRun should be present").toBeTruthy();
  expect(startRunMatch[0]!.includes('requestScrollToLatest("auto", "submit")')).toBe(true);
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
  expect(appSource.includes("syncSdkVisualTailState")).toBe(false);
  expect(appSource.includes("transcriptVisuallyAtBottom")).toBe(false);
  expect(appSource.includes("setUserScrolledUp")).toBe(false);
  expect(appSource.includes("sdkAtBottomRef")).toBe(false);
  expect(appSource.includes('from "./navigationMode"')).toBe(true);
  expect(appSource.includes(
          "function dispatchNavigationMode(reason: NavigationModeReason)",
        )).toBe(true);
  expect(appSource).toMatch(/if \(navigationModeRef\.current === "historical-anchor"\) \{[\s\S]{0,400}if \(row\.kind !== "message"\) continue;/);
  const applySdkTranscriptRowsMatch = appSource.match(
    /function applySdkTranscriptRows\([\s\S]{0,5000}mergeProjectedTranscriptRowUpdates[\s\S]{0,1400}syncSdkRenderedEntries/,
  );
  expect(applySdkTranscriptRowsMatch, "applySdkTranscriptRows should be present").toBeTruthy();
  expect(applySdkTranscriptRowsMatch[0]!.includes(
          "mergeProjectedTranscriptRowUpdates",
        ), "post-cursor SSE rows must merge into the rendered projection").toBe(true);
  expect(applySdkTranscriptRowsMatch[0]!.includes("if (sdkFoundNewestRef.current)"), "found_newest must not gate rendering of post-cursor live rows").toBe(false);
  expect(appSource).toMatch(/function handleSdkAtBottomChange\(atBottom: boolean\): void \{[\s\S]{0,300}if \(atBottom\) \{[\s\S]{0,80}dispatchNavigationMode\("virtuoso-at-bottom-true"\)/);
  expect(appSource.includes("sdkPendingTailRowIdsRef")).toBe(true);
});

test("user-scroll-up gestures are the only DOM-input mode transition", () => {
  // The leaving-live-tail direction is owned by explicit user input
  // events on the transcript scroll container (wheel / keydown /
  // touchstart+touchmove). The contract test pins all three event
  // attachments AND the live-tail mode gate that ensures gestures
  // are no-ops while the user is already in historical-anchor — so
  // the emitted telemetry stream represents real transitions.
  expect(appSource).toMatch(/target\.addEventListener\("wheel", onWheel, \{ passive: true \}\)/);
  expect(appSource).toMatch(/target\.addEventListener\("keydown", onKey\)/);
  expect(appSource).toMatch(/target\.addEventListener\("touchstart", onTouchStart, \{ passive: true \}\)/);
  expect(appSource).toMatch(/target\.addEventListener\("touchmove", onTouchMove, \{ passive: true \}\)/);
  expect(appSource).toMatch(/if \(navigationModeRef\.current !== "live-tail"\) return;[\s\S]{0,80}dispatchNavigationMode\("user-scroll-up"\);/);
});

test("read-cursor advance gates on live-tail mode, not DOM distance", () => {
  // conversation_read_state.last_read_order_key only moves when the
  // user is reading the live tail. The gate must be the mode, not a
  // DOM-distance check — pinning the durable contract that the
  // session 269 case violated.
  expect(appSource).toMatch(/function scheduleSdkReadStateUpdate\(\): void \{[\s\S]{0,800}if \(navigationModeRef\.current !== "live-tail"\) return;/);
  expect(appSource).toMatch(/async function flushSdkReadStateUpdate\(\): Promise<void> \{[\s\S]{0,400}if \(navigationModeRef\.current !== "live-tail"\) return;/);
});

test("chat back-pagination keeps an explicit access path", () => {
  expect(appSource.includes("before_cursor")).toBe(true);
  expect(appSource.includes("beforeCursor")).toBe(true);
  expect(appSource.includes("Load earlier messages")).toBe(true);
  expect(appSource.includes("older-missing-cursor")).toBe(true);
});

test("focused transcript Home and End keys resolve durable conversation edges", () => {
  expect(appSource.includes("scrollTranscriptToConversationStart")).toBe(true);
  expect(appSource.includes("scrollTranscriptToConversationEnd")).toBe(true);
  expect(appSource).toMatch(/async function scrollTranscriptToConversationStart[\s\S]*?jumpSdkToOldest\("keyboard"\)/);
  expect(appSource).toMatch(/async function scrollTranscriptToConversationEnd[\s\S]*?jumpSdkToLatest\("keyboard"\)/);
  expect(appSource).toMatch(/if \(e\.key === "Home"\)[\s\S]*?scrollTranscriptToConversationStart\(\)/);
  expect(appSource).toMatch(/if \(e\.key === "End"\)[\s\S]*?scrollTranscriptToConversationEnd\(\)/);
  expect(appSource).toMatch(/requestScrollToLatest\("smooth", "keyboard"\)/);
  expect(appSource.includes("transcriptScrollEl.scrollTop = 0")).toBe(false);
  expect(appSource.includes("consumedScrollToOldestSignalRef")).toBe(true);
  expect(appSource).toMatch(/consumedScrollToOldestSignalRef\.current === scrollToOldestSignal/);
});

test("focused Turns page Home and End keys scroll the turn detail to its edges", () => {
  // Turns is its own scroll surface (.run-turn-view-body), so Home/End reuse the
  // turn-view scroll-request channel (anchor "top"/"bottom") instead of the chat
  // SDK jump. Mirrors the chat Home/End focus gate: the shared <main>
  // (transcriptScrollEl) must be the key event target. transcript-navigation
  // contract — keyboard edge navigation extends to the Turns surface.
  expect(appSource).toMatch(/type TurnViewScrollAnchor = "bottom" \| "top"/);
  expect(appSource).toMatch(/if \(!visible \|\| activeTab !== "turns" \|\| !transcriptScrollEl\) return;/);
  expect(appSource).toMatch(/if \(e\.key !== "Home" && e\.key !== "End"\) return;/);
  expect(appSource).toMatch(/const anchor: TurnViewScrollAnchor = e\.key === "Home" \? "top" : "bottom";/);
  expect(appSource).toMatch(/setTurnViewScrollRequest\(\{\s*turnId: effectiveSelectedTurnId,\s*anchor,/);
  expect(appSource).toMatch(/if \(scrollRequest\.anchor === "top"\) \{\s*body\.scrollTo\(\{ top: 0, behavior: "auto" \}\);/);
});

test("focused transcript T opens Turns and Escape returns from Turns", () => {
  expect(appSource.includes("isTranscriptToTurnsShortcut")).toBe(true);
  expect(appSource.includes("isTurnsToTranscriptShortcut")).toBe(true);
  expect(appSource).toMatch(/targetIsTranscript: e\.target === transcriptScrollEl/);
  expect(appSource).toMatch(/openTurnPage\(undefined, \{ anchor: "bottom" \}\)/);
  expect(appSource).toMatch(/if \(activeTab === "turns"\) return;/);
  expect(appSource).toMatch(/setActiveTab\("chat"\);[\s\S]{0,120}focusTranscriptSection\(\);/);
});

test("chat back-pagination keeps the focused load button mounted while loading", () => {
  expect(appSource.includes("aria-disabled={sdkLoadingOlder || undefined}")).toBe(true);
  expect(appSource.includes("aria-busy={sdkLoadingOlder || undefined}")).toBe(true);
  expect(appSource.includes("run-transcript-load-older-passive")).toBe(false);
});

test("workspace scroll container keeps the right scrollbar affordance stable", () => {
  const runMainMatch = indexCssSource.match(/\.run-main \{[\s\S]*?\n\}/);
  expect(runMainMatch, "run-main styles should be present").toBeTruthy();
  const runMainCss = runMainMatch[0]!;
  expect(runMainCss.includes("overflow-y: scroll;")).toBe(true);
  expect(runMainCss.includes("scrollbar-gutter: stable;")).toBe(true);
  expect(indexCssSource.includes(".run-main::-webkit-scrollbar-track")).toBe(true);
});

test("session-event SSE stream emits browser-side observability", () => {
  // The candidate-B (zombie SSE) + projected-row receipt stethoscope
  // on the browser side. If a future refactor silently
  // removes the telemetry hooks, this guard breaks before the
  // diagnostic-only observability metric quietly stops shipping.
  expect(sessionEventStreamTelemetrySource.includes(
          "/api/client-metrics/session-events-stream",
        )).toBe(true);
  expect(sessionEventStreamTelemetrySource.includes("createSilenceWatchdog")).toBe(true);
  expect(sessionEventStreamTelemetrySource.includes("stream_silent_while_running")).toBe(true);
  expect(sessionEventStreamTelemetrySource.includes("terminal_matched_by_turn_id")).toBe(true);
  expect(sessionEventStreamTelemetrySource.includes(
          "queued_followup_blocked_after_terminal",
        )).toBe(true);
  expect(sessionEventStreamTelemetrySource.includes("stale_running_blocked_submit")).toBe(true);
  expect(appSource.includes('logSessionEventStreamEvent("opened"')).toBe(true);
  expect(appSource.includes('logSessionEventStreamEvent("transcript_rows_received"')).toBe(true);
  expect(appSource.includes('logSessionEventStreamEvent("transcript_rows_applied"')).toBe(true);
  expect(appSource.includes("terminal_matched_by_turn_id")).toBe(true);
  expect(appSource.includes(
          'logSessionEventStreamEvent("queued_followup_blocked_after_terminal"',
        )).toBe(true);
  expect(appSource.includes(
          'logSessionEventStreamEvent("stale_running_blocked_submit"',
        )).toBe(true);
  expect(appSource.includes('logSessionEventStreamEvent("resync_required"')).toBe(true);
  expect(appSource.includes('logSessionEventStreamEvent("stream_error"')).toBe(true);
  expect(appSource.includes('logSessionEventStreamEvent("closed_error"')).toBe(true);
  expect(appSource.includes('logSessionEventStreamEvent("closed_unmount"')).toBe(true);
  expect(appSource.includes("silenceWatchdogRef")).toBe(true);
  expect(appSource.includes('addEventListener("transcript-rows"')).toBe(true);
  expect(appSource.includes('addEventListener("tank-event"')).toBe(false);
  expect(appSource.includes("applySdkDurableEvent")).toBe(false);
  expect(appSource.includes("eventCountsAsTailOutput")).toBe(false);
  // The receipt-count telemetry MUST observe before the projected rows
  // mutate UI state; otherwise server-emit vs client-receive deltas would
  // be measured at the wrong layer.
  expect(appSource).toMatch(/logSessionEventStreamEvent\("transcript_rows_received"[\s\S]{0,250}applySdkTranscriptRows/);
  expect(appSource).toMatch(/mergeProjectedTranscriptRowUpdates[\s\S]{0,250}syncSdkRenderedEntries\(\);[\s\S]{0,250}logSessionEventStreamEvent\("transcript_rows_applied"/);
});

test("chat scroll diagnostics are prometheus backed", () => {
  expect(chatScrollTelemetrySource.includes('DEBUG_TOKEN = "chat-scroll"')).toBe(true);
  expect(chatScrollTelemetrySource.includes("isChatScrollDebugEnabled")).toBe(true);
  expect(chatScrollTelemetrySource.includes("/api/client-metrics/chat-scroll")).toBe(true);
  expect(chatScrollTelemetrySource.includes("tank.chatScrollEvents")).toBe(false);
  expect(appSource.includes("logChatScrollGroups")).toBe(true);
  expect(appSource.includes("logChatScrollEntries")).toBe(true);
  expect(appSource.includes('"keyboard-edge-navigation"')).toBe(true);
  expect(appSource.includes('jumpSdkToOldest("button")')).toBe(true);
  expect(appSource.includes('jumpSdkToLatest("button")')).toBe(true);
  expect(chatScrollTelemetrySource.includes(
          "sessionId: metricString(detail.sessionId)",
        )).toBe(true);
  expect(chatScrollTelemetrySource.includes("pagePath: currentPagePath()")).toBe(true);
  expect(chatScrollTelemetrySource.includes("pageSearch: currentPageSearch()")).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes("logChatScrollClientEvent")).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes('"browser chat scroll event"')).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes('"session_id"')).toBe(true);
  expect(chatScrollMetricsHandlerSource.includes('"page_search"')).toBe(true);
});

test("repo selection stays queryable without sidebar filter UI", () => {
  expect(appSource.includes("sessionFilter")).toBe(false);
  expect(appSource.includes("sessionMatchesFilter")).toBe(false);
  expect(appSource.includes("repoShortName")).toBe(false);
  expect(appSource.includes("filter by repo")).toBe(false);
  expect(indexCssSource.includes(".sidebar-filter")).toBe(false);
  expect(sessionBarCapabilitiesSource.includes("sessions.repos text[]")).toBe(true);
  expect(sessionBarCapabilitiesSource.includes("workspace scans")).toBe(true);
  expect(sessionBarCapabilitiesSource.includes("filter input")).toBe(false);
});

test("long-chat scroll lab route is admin gated and uses prometheus metrics", () => {
  expect(mainSource.includes('"/_debug/long-chat"')).toBe(true);
  expect(mainSource.includes("LongChatDebugPage")).toBe(true);
  expect(mainSource.includes("tank.chatScrollEvents")).toBe(false);
  expect(longChatDebugSource.includes("bootstrapAuth")).toBe(true);
  expect(longChatDebugSource.includes("user.is_admin")).toBe(true);
  expect(longChatDebugSource.includes("readChatScrollEvents")).toBe(false);
});

test("admin browser gates use Tank is_admin contract, not effective_role", () => {
  expect(appSource.includes("effective_role")).toBe(false);
  expect(authSource.includes("effective_role")).toBe(false);
  expect(longChatDebugSource.includes("effective_role")).toBe(false);
  expect(appSource.includes("is_admin")).toBe(true);
  expect(authSource.includes("is_admin")).toBe(true);
});
