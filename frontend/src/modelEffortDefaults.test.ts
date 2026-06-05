// Pins the load-bearing model + effort defaults end-to-end in App.tsx
// without spinning up React or DOM. Mirrors the regex-assert style of
// migrationPolicy.test.ts because App.tsx is the SPA's monolith and
// extracting these constants into a separate module would require
// touching ~hundreds of unrelated lines.
//
// Each assertion catches a specific user-trust failure if it regresses:
//   - "Opus 4.8 is the default" — a re-order of CLAUDE_MODELS that
//     puts Opus 4.7 / Sonnet / Haiku first would silently change every
//     new session's model. Pin the first id.
//   - "high is the default effort" — same shape, but for the effort
//     enum.
//   - "RunPrefs persists model + effort" — without these keys the
//     dropdown picks would reset on every page reload.
//   - "wire body forwards effort" — without the field in the POST
//     body the dropdown is a no-op end to end (the agent-runner's
//     pinning relies on it).
//   - "validation lives in pickAllowedPrefId" — a localStorage value
//     pinned in a prior SPA version that no longer matches the
//     allowlist must fall back to the default, not be forwarded as
//     opaque.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");

test("CLAUDE_MODELS lists claude-opus-4-8 first so it is the default selection", () => {
  const match = appSource.match(/const CLAUDE_MODELS:\s*ModelOption\[\][^[]*\[([\s\S]*?)\];/);
  assert.ok(match, "CLAUDE_MODELS literal should be present");
  const firstId = match[1]!.match(/id:\s*"([^"]+)"/);
  assert.ok(firstId, "CLAUDE_MODELS first entry should have an id");
  assert.equal(firstId[1], "claude-opus-4-8");
});

test("DEFAULT_CLAUDE_MODEL_ID and DEFAULT_CLAUDE_EFFORT_ID match the agent-runner constants", () => {
  // Pin the literal values in lockstep with
  // agent-runner/src/runner.ts DEFAULT_MODEL / DEFAULT_EFFORT and
  // backend-go/cmd/tank-operator/middleware.go allowedClaudeEfforts.
  // If product changes the defaults, ALL three layers must move
  // together — that's the cross-layer contract this test enforces.
  assert.match(appSource, /const DEFAULT_CLAUDE_MODEL_ID = "claude-opus-4-8";/);
  assert.match(appSource, /const DEFAULT_CLAUDE_EFFORT_ID = "high";/);
});

test("DEFAULT_CODEX_MODEL_ID and DEFAULT_CODEX_EFFORT_ID pin the strongest Codex defaults", () => {
  assert.match(appSource, /const DEFAULT_CODEX_MODEL_ID = "gpt-5\.5";/);
  assert.match(appSource, /const DEFAULT_CODEX_EFFORT_ID = "xhigh";/);
});

test("Codex model options require a concrete model instead of account default", () => {
  assert.doesNotMatch(appSource, /codex-account-default/);
  assert.doesNotMatch(appSource, /Codex (?:· )?Account default/i);
  assert.doesNotMatch(appSource, /Codex account default/i);
});

test("RunPrefs persists provider model and effort across page reloads", () => {
  assert.match(appSource, /claudeModelId:\s*string;/);
  assert.match(appSource, /claudeEffort:\s*string;/);
  assert.match(appSource, /codexModelId:\s*string;/);
  assert.match(appSource, /codexEffort:\s*string;/);
  assert.match(appSource, /claudeModelId:\s*DEFAULT_CLAUDE_MODEL_ID/);
  assert.match(appSource, /claudeEffort:\s*DEFAULT_CLAUDE_EFFORT_ID/);
  assert.match(appSource, /codexModelId:\s*DEFAULT_CODEX_MODEL_ID/);
  assert.match(appSource, /codexEffort:\s*DEFAULT_CODEX_EFFORT_ID/);
});

test("initialMessageMode is an ephemeral run preference that resets to direct on fresh loads", () => {
  assert.match(appSource, /initialMessageMode:\s*InitialMessageMode;/);
  assert.match(appSource, /initialMessageMode:\s*DEFAULT_INITIAL_MESSAGE_MODE/);
  assert.match(
    appSource,
    /const EPHEMERAL_RUN_PREF_KEYS = new Set<keyof RunPrefs>\(\["initialMessageMode"\]\);/,
  );
  assert.match(
    appSource,
    /function durableRunPrefs\(prefs: RunPrefs\): Record<string, unknown> \{[\s\S]{0,260}if \(!isDurableRunPref\(key\)\) continue;/,
  );
  assert.match(
    appSource,
    /function loadRunPrefs\(\): RunPrefs \{[\s\S]{0,260}if \(!isDurableRunPref\(key\)\) continue;[\s\S]{0,120}localStorage\.getItem/,
  );
  assert.match(
    appSource,
    /function mergeServerRunPrefs\(prev: RunPrefs, server: Record<string, unknown>\): RunPrefs \{[\s\S]{0,260}if \(!isDurableRunPref\(key\)\) continue;[\s\S]{0,120}const raw = server\[key\];/,
  );
  assert.match(appSource, /JSON\.stringify\(\{ run_prefs: durableRunPrefs\(prefs\) \}\)/);
  assert.match(
    appSource,
    /if \(isDurableRunPref\(key\)\) \{[\s\S]{0,80}persistRunPrefs\(next\);[\s\S]{0,80}\}[\s\S]{0,120}if \(!isDurableRunPref\(key\)\) return;[\s\S]{0,120}localStorage\.setItem/,
  );
  assert.doesNotMatch(appSource, /pickInitialMessageMode\(raw/);
});

test("loadRunPrefs filters localStorage-loaded model/effort through the allowlist", () => {
  // Without this filter a stale or hand-edited LS key would forward
  // an unknown string to the backend. The backend would reject the
  // effort path with a 400, but the model path is opaque-validated;
  // a typo would silently become a runner default at pod boot,
  // looking to the user like "my pick was ignored." The
  // pickAllowedPrefId call is the load-bearing fix.
  assert.match(
    appSource,
    /key === "claudeModelId"[\s\S]{0,300}pickAllowedPrefId\(raw, CLAUDE_MODELS, DEFAULT_CLAUDE_MODEL_ID\)/,
  );
  assert.match(
    appSource,
    /key === "claudeEffort"[\s\S]{0,300}pickAllowedPrefId\(raw, CLAUDE_EFFORTS, DEFAULT_CLAUDE_EFFORT_ID\)/,
  );
  assert.match(
    appSource,
    /key === "codexModelId"[\s\S]{0,300}pickAllowedPrefId\(raw, CODEX_MODELS, DEFAULT_CODEX_MODEL_ID\)/,
  );
  assert.match(
    appSource,
    /key === "codexEffort"[\s\S]{0,300}pickAllowedPrefId\(raw, CODEX_EFFORTS, DEFAULT_CODEX_EFFORT_ID\)/,
  );
  assert.doesNotMatch(
    appSource,
    /key === "initialMessageMode"[\s\S]{0,300}localStorage\.getItem/,
  );
});

test("enqueueSdkTurn forwards effort on the POST body so the runner sees the user's pick", () => {
  // The whole point of plumbing effort through three layers is that
  // it arrives in the wire body. The spread form keeps Codex sessions
  // (which set effort to "") from sending a noisy empty field, but
  // the property MUST be present when run.effort is set.
  assert.match(appSource, /\.\.\.\(run\.effort \? \{ effort: run\.effort \} : \{\}\)/);
});

test("createSession forwards model and effort as session-owned config", () => {
  assert.match(appSource, /const sessionModel = SDK_CHAT_MODES\.has\(mode\) \? seedModel : "";/);
  assert.match(appSource, /const sessionEffort = SDK_CHAT_MODES\.has\(mode\) \? seedEffort : "";/);
  assert.match(
    appSource,
    /\.\.\.\(sessionModel \|\| sessionEffort \? \{ model: sessionModel, effort: sessionEffort \} : \{\}\)/,
  );
  assert.doesNotMatch(appSource, /initialTurnPayload[\s\S]{0,400}model: seedModel/);
});

test("forkSessionFromMessage forwards model and effort on create, not the first turn", () => {
  assert.match(
    appSource,
    /SDK_CHAT_MODES\.has\(mode\) && \(request\.model \|\| request\.effort\)[\s\S]{0,120}\{ model: request\.model, effort: request\.effort \}/,
  );
  assert.doesNotMatch(appSource, /client_nonce: newForkTurnId\(\)[\s\S]{0,160}model: request\.model/);
});
