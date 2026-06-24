import { hasInternalAuthConfig, internalBearerToken } from "./internalAuth.js";

function trimTrailingSlashes(value) {
    return String(value || "").replace(/\/+$/, "");
}

export async function registerScheduledWakeup(cfg, payload) {
    const baseURL = trimTrailingSlashes(cfg.operatorInternalURL || "");
    if (!baseURL || !hasInternalAuthConfig(cfg) || !cfg.sessionId) {
        return false;
    }
    const token = await internalBearerToken(cfg);
    const url = `${baseURL}/api/internal/sessions/${encodeURIComponent(cfg.sessionId)}/scheduled-wakeups`;
    const response = await fetch(url, {
        method: "POST",
        headers: {
            Authorization: `Bearer ${token}`,
            "Content-Type": "application/json",
        },
        body: JSON.stringify({
            delay_ms: Math.max(0, Math.floor(Number(payload?.delayMs ?? 0))),
            prompt: String(payload?.prompt ?? ""),
            provider_item_id: String(payload?.providerItemID ?? ""),
            scheduled_turn_id: String(payload?.scheduledTurnID ?? ""),
        }),
    });
    if (!response.ok) {
        throw new Error(`scheduled wakeup register failed: ${response.status}`);
    }
    return true;
}
