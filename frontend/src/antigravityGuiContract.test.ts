import { test, expect } from "vitest";

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
  expect(isDefaultSessionMode(mode)).toBe(true);
  expect(DEFAULT_SESSION_MODES.has(mode)).toBeTruthy();
  expect(CHAT_MODES.has(mode)).toBeTruthy();
  expect(SDK_CHAT_MODES.has(mode)).toBeTruthy();
  expect(GUI_ROLLOUT_MODES.has(mode)).toBeTruthy();
  expect(REPO_SUPPORTED_MODES.has(mode)).toBeTruthy();
  expect(WORKSPACE_FILE_MODES.has(mode)).toBeTruthy();
});

test("provider-picker GUI modes declare the complete chat surface", () => {
  for (const provider of PROVIDERS) {
    const mode = PROVIDER_INTERACTION_MODES[provider].gui;
    if (mode == null) continue;

    const contract = SESSION_MODE_CONTRACT[mode];
    expect(contract.interaction, `${mode} should be a GUI interaction`).toBe("gui");
    expect(contract.defaultSelectable, `${mode} should round-trip default mode persistence`).toBe(true);
    expect(contract.chatSurface, `${mode} should keep chat chrome`).toBe(true);
    expect(contract.sdkChat, `${mode} should use SDK chat flow`).toBe(true);
    expect(contract.workspaceFiles, `${mode} should keep attachment/workspace affordances`).toBe(true);
    expect(contract.repos, `${mode} should support repo selection`).toBe(true);
    expect(contract.rollout, `${mode} should expose GUI rollout`).toBe("gui");
  }
});

test("provider picker uses the primary Codex GUI mode instead of legacy exec", () => {
  expect(PROVIDER_INTERACTION_MODES.codex.gui).toBe("codex_gui");
});

test("retired Codex GUI modes are not create-time affordances", () => {
  for (const mode of ["codex_exec_gui", "codex_app_server"] as const) {
    expect(DEFAULT_SESSION_MODES.has(mode)).toBe(false);
    expect(REPO_SUPPORTED_MODES.has(mode)).toBe(false);
    expect(SESSION_MODE_CONTRACT[mode].chatSurface).toBe(true);
    expect(SESSION_MODE_CONTRACT[mode].sdkChat).toBe(true);
  }
});

test("derived mode sets match the session-mode contract", () => {
  for (const [mode, contract] of Object.entries(SESSION_MODE_CONTRACT) as Array<
    [SessionMode, (typeof SESSION_MODE_CONTRACT)[SessionMode]]
  >) {
    expect(DEFAULT_SESSION_MODES.has(mode)).toBe(contract.defaultSelectable);
    expect(CHAT_MODES.has(mode)).toBe(contract.chatSurface);
    expect(SDK_CHAT_MODES.has(mode)).toBe(contract.sdkChat);
    expect(WORKSPACE_FILE_MODES.has(mode)).toBe(contract.workspaceFiles);
    expect(REPO_SUPPORTED_MODES.has(mode)).toBe(contract.repos);
    expect(GUI_ROLLOUT_MODES.has(mode)).toBe(contract.rollout === "gui");
  }
});
