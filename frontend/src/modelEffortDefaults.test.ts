// Pins the run-options ownership contract in App.tsx without spinning up
// React or DOM. App.tsx is still the SPA monolith, so these regex guards
// catch cross-layer drift until the run-picker code is extracted into a
// separately imported module.
import { readFileSync } from "node:fs";
import { test, expect } from "vitest";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");

test("model and effort option lists come from Tank run options, not local arrays", () => {
  expect(appSource).not.toMatch(/const CLAUDE_MODELS:/);
  expect(appSource).not.toMatch(/const CODEX_MODELS:/);
  expect(appSource).not.toMatch(/const ANTIGRAVITY_MODELS:/);
  expect(appSource).not.toMatch(/const CLAUDE_EFFORTS:/);
  expect(appSource).not.toMatch(/const CODEX_EFFORTS:/);
  expect(appSource).toMatch(
    /async function fetchSessionRunOptions\(\): Promise<SessionRunOptions> \{[\s\S]{0,180}authedFetch\("\/api\/session-run-options"\)/,
  );
  expect(appSource).toMatch(
    /function modelOptionsForProvider\([\s\S]{0,180}runOptions\?\.models\[runOptionsProviderKey\(provider\)\]/,
  );
  expect(appSource).toMatch(
    /function effortOptionsForProvider\([\s\S]{0,180}runOptions\?\.efforts\[runOptionsProviderKey\(provider\)\]/,
  );
});

test("Tank's claude provider key is normalized to the frontend anthropic provider", () => {
  expect(appSource).toMatch(
    /provider === "claude" \|\| provider === "anthropic"[\s\S]{0,80}return "anthropic";/,
  );
  expect(appSource).toMatch(
    /if \(!out\.anthropic\?\.length\) out\.anthropic = stringArray\(src\.claude\);/,
  );
  expect(appSource).toMatch(
    /if \(!out\.anthropic && typeof src\.claude === "string"\) \{[\s\S]{0,80}out\.anthropic = src\.claude;/,
  );
});

test("Codex model labels do not advertise the unsupported bare GPT-5.3 model", () => {
  expect(appSource).not.toContain('"gpt-5.3-codex":');
  expect(appSource).toContain('"gpt-5.3-codex-spark"');
});

test("Antigravity model labels include Gemini 3.1 Pro", () => {
  expect(appSource).toContain('"Gemini 3.1 Pro"');
  expect(appSource).toContain("Antigravity · Gemini 3.1 Pro");
});

test("Codex run mode helper follows the session mode contract", () => {
  expect(appSource).toMatch(
    /function isCodexRunMode\(mode: SessionMode\): boolean \{[\s\S]{0,120}MODE_PROVIDERS\[mode\] === "codex" && SDK_CHAT_MODES\.has\(mode\);[\s\S]{0,20}\}/,
  );
  expect(appSource).not.toMatch(
    /function isCodexRunMode\(mode: SessionMode\): boolean \{[\s\S]{0,120}mode === "codex_gui" \|\| mode === "codex_app_server"/,
  );
});

test("RunPrefs persists provider model and effort across page reloads", () => {
  expect(appSource).toMatch(/claudeModelId:\s*string;/);
  expect(appSource).toMatch(/claudeEffort:\s*string;/);
  expect(appSource).toMatch(/codexModelId:\s*string;/);
  expect(appSource).toMatch(/codexEffort:\s*string;/);
  expect(appSource).toMatch(/antigravityModelId:\s*string;/);
  expect(appSource).toMatch(/claudeModelId:\s*DEFAULT_CLAUDE_MODEL_ID/);
  expect(appSource).toMatch(/claudeEffort:\s*DEFAULT_CLAUDE_EFFORT_ID/);
  expect(appSource).toMatch(/codexModelId:\s*DEFAULT_CODEX_MODEL_ID/);
  expect(appSource).toMatch(/codexEffort:\s*DEFAULT_CODEX_EFFORT_ID/);
  expect(appSource).toMatch(
    /antigravityModelId:\s*DEFAULT_ANTIGRAVITY_MODEL_ID/,
  );
});

test("Claude secondary uses the shared Claude run options and prefs", () => {
  expect(appSource).toMatch(
    /function providerUsesClaudeOptions\(provider: Provider\): boolean \{[\s\S]{0,100}provider === "anthropic" \|\| provider === "anthropic_secondary";/,
  );
  expect(appSource).toMatch(
    /function selectedModelIdForProvider\([\s\S]{0,260}providerUsesClaudeOptions\(provider\)[\s\S]{0,140}prefs\.claudeModelId \|\| defaultModelForProvider\(provider, runOptions\)/,
  );
  expect(appSource).toMatch(
    /function selectedEffortIdForProvider\([\s\S]{0,260}providerUsesClaudeOptions\(provider\)[\s\S]{0,140}prefs\.claudeEffort \|\| defaultEffortForProvider\(provider, runOptions\)/,
  );
  expect(appSource).toMatch(
    /const seedEffort = providerUsesEffort\(modeProvider\)[\s\S]{0,80}\? selectedHomeEffortId/,
  );
  expect(appSource).toMatch(
    /setModelPrefForProvider\([\s\S]{0,80}selectedProvider,[\s\S]{0,80}model\.id,[\s\S]{0,80}setRunPref/,
  );
  expect(appSource).toMatch(/providerUsesEffort\(selectedProvider\) && \(/);
  expect(appSource).toMatch(
    /setEffortPrefForProvider\([\s\S]{0,80}selectedProvider,[\s\S]{0,80}effort\.id,[\s\S]{0,80}setRunPref/,
  );
});

test("initialMessageMode is an ephemeral run preference that resets to direct on fresh loads", () => {
  expect(appSource).toMatch(/initialMessageMode:\s*InitialMessageMode;/);
  expect(appSource).toMatch(
    /initialMessageMode:\s*DEFAULT_INITIAL_MESSAGE_MODE/,
  );
  expect(appSource).toMatch(
    /const EPHEMERAL_RUN_PREF_KEYS = new Set<keyof RunPrefs>\(\["initialMessageMode"\]\);/,
  );
  expect(appSource).toMatch(
    /function durableRunPrefs\(prefs: RunPrefs\): Record<string, unknown> \{[\s\S]{0,260}if \(!isDurableRunPref\(key\)\) continue;/,
  );
  expect(appSource).toMatch(
    /function loadRunPrefs\(\): RunPrefs \{[\s\S]{0,260}if \(!isDurableRunPref\(key\)\) continue;[\s\S]{0,120}localStorage\.getItem/,
  );
  expect(appSource).toMatch(
    /function mergeServerRunPrefs\([\s\S]{0,220}prev: RunPrefs,[\s\S]{0,220}server: Record<string, unknown>,[\s\S]{0,420}if \(!isDurableRunPref\(key\)\) continue;[\s\S]{0,180}const raw = server\[key\];/,
  );
  expect(appSource).toMatch(
    /JSON\.stringify\(\{ run_prefs: durableRunPrefs\(prefs\) \}\)/,
  );
  expect(appSource).toMatch(
    /if \(isDurableRunPref\(key\)\) \{[\s\S]{0,120}persistRunPrefsLocally\(next\);[\s\S]{0,80}persistRunPrefs\(next\);[\s\S]{0,80}\}/,
  );
  expect(appSource).not.toMatch(/pickInitialMessageMode\(raw/);
});

test("stored legacy Codex GUI defaults migrate to the primary Codex GUI mode", () => {
  expect(appSource).toMatch(
    /stored === "codex_app_server" \|\| stored === "codex_exec_gui"[\s\S]{0,80}return "codex_gui";/,
  );
});

test("server and browser run prefs are reconciled through Tank run options", () => {
  expect(appSource).toMatch(
    /const next = reconcileRunPrefsWithRunOptions\(prev, options\);[\s\S]{0,120}persistRunPrefsLocally\(next\);[\s\S]{0,80}persistRunPrefs\(next\);/,
  );
  expect(appSource).toMatch(
    /const merged = mergeServerRunPrefs\(prev, server\);[\s\S]{0,160}reconcileRunPrefsWithRunOptions\(merged, sessionRunOptions\)/,
  );
  expect(appSource).toMatch(
    /claudeModelId: pickAllowedPrefId\([\s\S]{0,160}modelOptionsForProvider\("anthropic", runOptions\),[\s\S]{0,120}defaultModelForProvider\("anthropic", runOptions\)/,
  );
  expect(appSource).toMatch(
    /codexModelId: pickAllowedPrefId\([\s\S]{0,160}modelOptionsForProvider\("codex", runOptions\),[\s\S]{0,120}defaultModelForProvider\("codex", runOptions\)/,
  );
  expect(appSource).toMatch(
    /antigravityModelId: pickAllowedPrefId\([\s\S]{0,160}modelOptionsForProvider\("antigravity", runOptions\),[\s\S]{0,120}defaultModelForProvider\("antigravity", runOptions\)/,
  );
});

test("createSession refuses modes before Tank run options are ready", () => {
  expect(appSource).toMatch(
    /!createModeAllowedByRunOptions\(mode, sessionRunOptions\) \|\|[\s\S]{0,80}!sdkModeReadyForCreate\(mode, sessionRunOptions\)/,
  );
  expect(appSource).toMatch(
    /setError\("session run options are not ready for this mode"\);[\s\S]{0,40}return;/,
  );
  expect(appSource).toMatch(
    /const modeProvider = MODE_MENU_ICONS\[mode\];[\s\S]{0,180}providerUsesModel\(modeProvider\)[\s\S]{0,80}selectedHomeModelId/,
  );
});

test("enqueueSdkTurn forwards effort on the POST body so the runner sees the user's pick", () => {
  expect(appSource).toMatch(
    /\.\.\.\(run\.effort \? \{ effort: run\.effort \} : \{\}\)/,
  );
});

test("createSession forwards model and effort as session-owned config", () => {
  expect(appSource).toMatch(
    /const sessionModel = SDK_CHAT_MODES\.has\(mode\) \? seedModel : "";/,
  );
  expect(appSource).toMatch(
    /const sessionEffort = SDK_CHAT_MODES\.has\(mode\) \? seedEffort : "";/,
  );
  expect(appSource).toMatch(
    /\.\.\.\(sessionModel \|\| sessionEffort[\s\S]{0,120}\? \{ model: sessionModel, effort: sessionEffort \}[\s\S]{0,80}: \{\}\)/,
  );
  expect(appSource).not.toMatch(
    /initialTurnPayload[\s\S]{0,400}model: seedModel/,
  );
});

test("Glimmung launch uses Tank test-slot defaults instead of browser default mode", () => {
  expect(appSource).toMatch(/test_slot_defaults: TestSlotSessionDefaults;/);
  expect(appSource).toMatch(
    /const defaults = sessionRunOptions\?\.test_slot_defaults;[\s\S]{0,80}if \(!defaults\) return;/,
  );
  const launchRequestStart = appSource.indexOf(
    'authedFetch("/api/sessions/with-context"',
  );
  expect(launchRequestStart).toBeGreaterThan(0);
  const launchRequest = appSource.slice(launchRequestStart, launchRequestStart + 700);
  expect(launchRequest).toMatch(/mode: launchDefaults\.mode,/);
  expect(launchRequest).toMatch(
    /launchDefaults\.model \? \{ model: launchDefaults\.model \}/,
  );
  expect(launchRequest).not.toMatch(/mode: defaultSessionMode/);
});

test("forkSessionFromMessage forwards model and effort on create, not the first turn", () => {
  expect(appSource).toMatch(
    /SDK_CHAT_MODES\.has\(mode\) && \(request\.model \|\| request\.effort\)[\s\S]{0,120}\{ model: request\.model, effort: request\.effort \}/,
  );
  expect(appSource).not.toMatch(
    /client_nonce: newForkTurnId\(\)[\s\S]{0,160}model: request\.model/,
  );
});

test("mid-session run-config: the composer model chip is a Claude/Codex-gated dropdown", () => {
  // selectedModelId/effort are now mutable — the dropdown updates them
  // optimistically. The old single-binding "sealed" useState is gone.
  expect(appSource).toMatch(
    /const \[selectedModelId, setSelectedModelId\] = useState<string>/,
  );
  expect(appSource).toMatch(
    /const \[selectedEffortId, setSelectedEffortId\] =\s*useState<string>/,
  );
  // The in-session dropdown is gated to Claude/Codex; Antigravity keeps the
  // read-only chip (its model is a process-start arg, not switchable).
  expect(appSource).toMatch(
    /isClaude \|\| isCodex \? \([\s\S]{0,160}data-menu="run-model"/,
  );
});

test("mid-session run-config: a pick PUTs /run-config and only toggles the menu (option a)", () => {
  // applyRunConfig PUTs the durable run config; the turn handler forwards it
  // and the runner re-pins on the next turn (no interrupt of the running one).
  expect(appSource).toMatch(
    /applyRunConfig = useCallback\([\s\S]{0,600}run-config[\s\S]{0,80}method: "PUT"/,
  );
  // The trigger only toggles the dropdown — it never submits or interrupts.
  expect(appSource).toMatch(
    /run-model-trigger[\s\S]{0,400}onClick=\{\(\) => setRunModelMenuOpen\(\(v\) => !v\)\}/,
  );
});

test("per-turn model is captured from each turn's user message and shown in the turn summary", () => {
  // Capture: the TurnViewItem builder harvests the model/effort the backend
  // stamps on each turn's user-message entry into a per-turn map — historical,
  // distinct from the session-level next-turn selection (selectedModelId).
  expect(appSource).toMatch(/const runConfigByTurn = new Map</);
  expect(appSource).toMatch(
    /isUserMessageEntry\(entry\) && !runConfigByTurn\.has\(turnId\)[\s\S]{0,160}entry\.model/,
  );
  // TurnViewItem reads the per-turn model from the shell's activity summary
  // (the carrier the turn-summary normalizer preserves), then the shell
  // top-level, then the user-message capture.
  expect(appSource).toMatch(
    /shellSummary\?\.model[\s\S]{0,220}runConfigByTurn\.get\(turnId\)\?\.model/,
  );
  // The summary normalizer must carry model so it survives the row-merge path.
  expect(appSource).toMatch(/model: stringRecordValue\(record, "model"\)/);
  // Render: the turn summary shows the VIEWED turn's model via the shared
  // label helper. It must read selected.model (per-turn), never the composer's
  // selectedModelId (next-turn) — otherwise every historical turn would show
  // the session's current model.
  expect(appSource).toMatch(
    /run-turn-view-model[\s\S]{0,220}modelDisplayLabel\(sessionMode as SessionMode,\s*selected\.model\)/,
  );
});
