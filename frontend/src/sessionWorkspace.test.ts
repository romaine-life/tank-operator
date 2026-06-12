import { test, expect } from "vitest";

import {
  sessionContainerAvailable,
  sessionFilesAvailable,
  sessionFilesTabTitle,
  sessionModeSupportsWorkspaceFiles,
} from "./sessionWorkspace.ts";

test("workspace file support is limited to pod-backed GUI modes", () => {
  expect(sessionModeSupportsWorkspaceFiles("claude_gui")).toBe(true);
  expect(sessionModeSupportsWorkspaceFiles("claude_secondary_gui")).toBe(true);
  expect(sessionModeSupportsWorkspaceFiles("codex_gui")).toBe(true);
  expect(sessionModeSupportsWorkspaceFiles("codex_exec_gui")).toBe(true);
  expect(sessionModeSupportsWorkspaceFiles("codex_app_server")).toBe(true);
  expect(sessionModeSupportsWorkspaceFiles("antigravity_gui")).toBe(true);

  expect(sessionModeSupportsWorkspaceFiles("claude_cli")).toBe(false);
  expect(sessionModeSupportsWorkspaceFiles("claude_secondary_cli")).toBe(false);
});

test("session container availability waits for a pod that reached ready", () => {
  expect(sessionContainerAvailable({ mode: "codex_gui", status: "Pending", pod_name: "session-1" })).toBe(false);
  expect(sessionContainerAvailable({
          mode: "codex_gui",
          status: "Pending",
          pod_name: "session-1",
          ready_at: "2026-05-20T21:08:33Z",
        })).toBe(true);
  expect(sessionContainerAvailable({
          mode: "codex_gui",
          status: "Failed",
          pod_name: "session-1",
          ready_at: "2026-05-20T21:08:33Z",
        })).toBe(false);
});

test("session files wait for the durable ready pod state", () => {
  expect(sessionFilesAvailable({ mode: "codex_gui", status: "Pending", pod_name: "session-1" })).toBe(false);
  expect(sessionFilesAvailable({ mode: "codex_gui", status: "Active", pod_name: null })).toBe(false);
  expect(sessionFilesAvailable({ mode: "codex_gui", status: "Active", pod_name: "session-1" })).toBe(true);
  expect(sessionFilesAvailable({
          mode: "codex_gui",
          status: "Pending",
          pod_name: "session-1",
          ready_at: "2026-05-20T21:08:33Z",
        })).toBe(true);
});

test("session files stay unavailable without a pod", () => {
  expect(sessionFilesAvailable({ mode: "codex_gui", status: "Active", pod_name: null })).toBe(false);
  expect(sessionFilesTabTitle({ mode: "codex_gui", status: "Active", pod_name: null })).toBe("Files are available once the session container starts");
});
