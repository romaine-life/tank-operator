import { readFile } from "node:fs/promises";
import { postOperatorInternalJSONWithRetry } from "./operatorInternalRequest.js";

function trimTrailingSlashes(value) {
    return String(value || "").replace(/\/+$/, "");
}

export async function registerScheduledWakeup(cfg, payload) {
    const baseURL = trimTrailingSlashes(cfg.operatorInternalURL || "");
    const tokenPath = cfg.operatorTokenPath || "";
    if (!baseURL || !tokenPath || !cfg.sessionId) {
        return false;
    }
    const token = (await readFile(tokenPath, "utf8")).trim();
    const url = `${baseURL}/api/internal/sessions/${encodeURIComponent(cfg.sessionId)}/scheduled-wakeups`;
    await postOperatorInternalJSONWithRetry(cfg, url, token, {
        delay_ms: Math.max(0, Math.floor(Number(payload?.delayMs ?? 0))),
        prompt: String(payload?.prompt ?? ""),
        provider_item_id: String(payload?.providerItemID ?? ""),
        scheduled_turn_id: String(payload?.scheduledTurnID ?? ""),
    }, "scheduled wakeup register failed");
    return true;
}
