export const WORKSPACE_FILE_MODES: ReadonlySet<string> = new Set([
  "claude_gui",
  "codex_gui",
  "codex_exec_gui",
  "codex_app_server",
  "gemini_gui",
]);

export interface SessionWorkspaceState {
  mode: string;
  status: string;
  pod_name?: string | null;
  ready_at?: string | null;
}

export function sessionModeSupportsWorkspaceFiles(mode: string): boolean {
  return WORKSPACE_FILE_MODES.has(mode);
}

export function sessionContainerAvailable(session: SessionWorkspaceState): boolean {
  return (
    session.status !== "Failed" &&
    Boolean(session.pod_name) &&
    (session.status === "Active" || Boolean(session.ready_at))
  );
}

export function sessionFilesAvailable(session: SessionWorkspaceState): boolean {
  // File APIs exec into the pod's /workspace; wait for durable lifecycle
  // state instead of enabling the UI from the optimistic create response.
  return (
    sessionModeSupportsWorkspaceFiles(session.mode) &&
    sessionContainerAvailable(session)
  );
}

export function sessionFilesTabTitle(session: SessionWorkspaceState): string {
  if (!sessionModeSupportsWorkspaceFiles(session.mode)) {
    return "This session does not have workspace files";
  }
  if (sessionFilesAvailable(session)) {
    return "Browse files in /workspace";
  }
  return "Files are available once the session container starts";
}
