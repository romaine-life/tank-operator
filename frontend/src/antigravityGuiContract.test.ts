import assert from "node:assert/strict";
import test from "node:test";

import {
  CHAT_MODES,
  DEFAULT_SESSION_MODES,
  GUI_ROLLOUT_MODES,
  PROVIDERS,
  PROVIDER_INTERACTION_MODES,
  REPO_SUPPORTED_MODES,
  SDK_CHAT_MODES,
  SESSION_MODE_CONTRACT,
  WORKSPACE_FILE_MODES,
  isDefaultSessionMode,
  type SessionMode,
} from "./sessionModes.ts";

test("antigravity_gui keeps the full GUI chat surface contract", () => {
  const mode = "antigravity_gui";
  assert.equal(isDefaultSessionMode(mode), true);
  assert.ok(DEFAULT_SESSION_MODES.has(mode));
  assert.ok(CHAT_MODES.has(mode));
  assert.ok(SDK_CHAT_MODES.has(mode));
  assert.ok(GUI_ROLLOUT_MODES.has(mode));
  assert.ok(REPO_SUPPORTED_MODES.has(mode));
  assert.ok(WORKSPACE_FILE_MODES.has(mode));
});

test("provider-picker GUI modes declare the complete chat surface", () => {
  for (const provider of PROVIDERS) {
    const mode = PROVIDER_INTERACTION_MODES[provider].gui;
    if (mode == null) continue;

    const contract = SESSION_MODE_CONTRACT[mode];
    assert.equal(
      contract.interaction,
      "gui",
      `${mode} should be a GUI interaction`,
    );
    assert.equal(
      contract.defaultSelectable,
      true,
      `${mode} should round-trip default mode persistence`,
    );
    assert.equal(contract.chatSurface, true, `${mode} should keep chat chrome`);
    assert.equal(contract.sdkChat, true, `${mode} should use SDK chat flow`);
    assert.equal(
      contract.workspaceFiles,
      true,
      `${mode} should keep attachment/workspace affordances`,
    );
    assert.equal(contract.repos, true, `${mode} should support repo selection`);
    assert.equal(contract.rollout, "gui", `${mode} should expose GUI rollout`);
  }
});

test("derived mode sets match the session-mode contract", () => {
  for (const [mode, contract] of Object.entries(SESSION_MODE_CONTRACT) as Array<
    [SessionMode, (typeof SESSION_MODE_CONTRACT)[SessionMode]]
  >) {
    assert.equal(DEFAULT_SESSION_MODES.has(mode), contract.defaultSelectable);
    assert.equal(CHAT_MODES.has(mode), contract.chatSurface);
    assert.equal(SDK_CHAT_MODES.has(mode), contract.sdkChat);
    assert.equal(WORKSPACE_FILE_MODES.has(mode), contract.workspaceFiles);
    assert.equal(REPO_SUPPORTED_MODES.has(mode), contract.repos);
    assert.equal(GUI_ROLLOUT_MODES.has(mode), contract.rollout === "gui");
  }
});
