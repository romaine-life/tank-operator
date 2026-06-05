import { readFile } from "node:fs/promises";
import { postOperatorInternalJSONWithRetry } from "./operatorInternalRequest.js";

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
    await postOperatorInternalJSONWithRetry(cfg, url, token, {
        task_id: String(payload?.taskID ?? ""),
        status: String(payload?.status ?? ""),
        description: String(payload?.description ?? ""),
        summary: String(payload?.summary ?? ""),
        last_tool_name: String(payload?.lastToolName ?? ""),
        error: String(payload?.error ?? ""),
    }, "background task wake register failed");
    return true;
}
