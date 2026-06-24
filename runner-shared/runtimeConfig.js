import { hasInternalAuthConfig, internalBearerToken } from "./internalAuth.js";

function trimTrailingSlashes(value) {
    return String(value || "").replace(/\/+$/, "");
}

export async function reportRuntimeConfig(cfg, payload) {
    const baseURL = trimTrailingSlashes(cfg.operatorInternalURL || "");
    if (!baseURL || !hasInternalAuthConfig(cfg) || !cfg.sessionId) {
        return false;
    }
    const token = await internalBearerToken(cfg);
    const url = `${baseURL}/api/internal/sessions/${encodeURIComponent(cfg.sessionId)}/runtime-config`;
    const response = await fetch(url, {
        method: "PUT",
        headers: {
            Authorization: `Bearer ${token}`,
            "Content-Type": "application/json",
        },
        body: JSON.stringify({
            model: String(payload?.model ?? "").trim(),
            effort: String(payload?.effort ?? "").trim(),
            context_window_tokens: Number.isFinite(payload?.contextWindowTokens)
                ? Math.max(0, Math.floor(payload.contextWindowTokens))
                : 0,
            context_window_source: String(payload?.contextWindowSource ?? "").trim(),
            provider_session_id: String(payload?.providerSessionId ?? "").trim(),
            provider_rate_limit_info:
                payload?.providerRateLimitInfo && typeof payload.providerRateLimitInfo === "object"
                    ? payload.providerRateLimitInfo
                    : undefined,
            provider_usage_snapshot:
                payload?.providerUsageSnapshot && typeof payload.providerUsageSnapshot === "object"
                    ? payload.providerUsageSnapshot
                    : undefined,
        }),
    });
    if (!response.ok) {
        throw new Error(`runtime config report failed: ${response.status}`);
    }
    return true;
}
