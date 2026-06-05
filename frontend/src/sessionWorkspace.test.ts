import assert from "node:assert/strict";
import test from "node:test";

import {
  sessionContainerAvailable,
  sessionFilesAvailable,
  sessionFilesTabTitle,
  sessionModeSupportsWorkspaceFiles,
} from "./sessionWorkspace.ts";

test("workspace file support is limited to pod-backed GUI modes", () => {
  assert.equal(sessionModeSupportsWorkspaceFiles("claude_gui"), true);
  assert.equal(sessionModeSupportsWorkspaceFiles("codex_gui"), true);
  assert.equal(sessionModeSupportsWorkspaceFiles("codex_exec_gui"), true);
  assert.equal(sessionModeSupportsWorkspaceFiles("codex_app_server"), true);
  assert.equal(sessionModeSupportsWorkspaceFiles("gemini_gui"), true);
  assert.equal(sessionModeSupportsWorkspaceFiles("claude_cli"), false);
});

test("session container availability waits for a pod that reached ready", () => {
  assert.equal(
    sessionContainerAvailable({ mode: "codex_gui", status: "Pending", pod_name: "session-1" }),
    false,
  );
  assert.equal(
    sessionContainerAvailable({
      mode: "codex_gui",
      status: "Pending",
      pod_name: "session-1",
      ready_at: "2026-05-20T21:08:33Z",
    }),
    true,
  );
  assert.equal(
    sessionContainerAvailable({
      mode: "codex_gui",
      status: "Failed",
      pod_name: "session-1",
      ready_at: "2026-05-20T21:08:33Z",
    }),
    false,
  );
});

test("session files wait for the durable ready pod state", () => {
  assert.equal(
    sessionFilesAvailable({ mode: "codex_gui", status: "Pending", pod_name: "session-1" }),
    false,
  );
  assert.equal(
    sessionFilesAvailable({ mode: "codex_gui", status: "Active", pod_name: null }),
    false,
  );
  assert.equal(
    sessionFilesAvailable({ mode: "codex_gui", status: "Active", pod_name: "session-1" }),
    true,
  );
  assert.equal(
    sessionFilesAvailable({
      mode: "codex_gui",
      status: "Pending",
      pod_name: "session-1",
      ready_at: "2026-05-20T21:08:33Z",
    }),
    true,
  );
});

test("session files stay unavailable without a pod", () => {
  assert.equal(
    sessionFilesAvailable({ mode: "codex_gui", status: "Active", pod_name: null }),
    false,
  );
  assert.equal(
    sessionFilesTabTitle({ mode: "codex_gui", status: "Active", pod_name: null }),
    "Files are available once the session container starts",
  );
});
