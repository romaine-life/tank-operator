import { readFile } from "node:fs/promises";

function trimTrailingSlashes(value) {
    return String(value || "").replace(/\/+$/, "");
}

// registerBackgroundTaskWake reports that a Claude background (run_in_background)
// task reached a natural terminal while the session had no active turn. The
// orchestrator owns the durable wake row and later submits the wake through the
// same backend turn boundary as a user turn (source=background-task). Mirrors
// registerScheduledWakeup: returns false (not an error) when the runner is not
// configured with the operator URL/token, so callers can count "disabled"
// distinctly from "failed".
export async function registerBackgroundTaskWake(cfg, payload) {
    const baseURL = trimTrailingSlashes(cfg.operatorInternalURL || "");
    const tokenPath = cfg.operatorTokenPath || "";
    if (!baseURL || !tokenPath || !cfg.sessionId) {
        return false;
    }
    const token = (await readFile(tokenPath, "utf8")).trim();
    const url = `${baseURL}/api/internal/sessions/${encodeURIComponent(cfg.sessionId)}/background-task-wakes`;
    const response = await fetch(url, {
        method: "POST",
        headers: {
            Authorization: `Bearer ${token}`,
            "Content-Type": "application/json",
        },
        body: JSON.stringify({
            task_id: String(payload?.taskID ?? ""),
            status: String(payload?.status ?? ""),
            description: String(payload?.description ?? ""),
            summary: String(payload?.summary ?? ""),
            last_tool_name: String(payload?.lastToolName ?? ""),
            error: String(payload?.error ?? ""),
        }),
    });
    if (!response.ok) {
        throw new Error(`background task wake register failed: ${response.status}`);
    }
    return true;
}
