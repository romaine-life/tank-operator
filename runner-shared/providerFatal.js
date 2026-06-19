import { readFile } from "node:fs/promises";

function trimTrailingSlashes(value) {
    return String(value || "").replace(/\/+$/, "");
}

// reportProviderFatal tells the orchestrator that the pod-side runner has hit
// an UNRECOVERABLE, session-level condition and cannot continue — the agent
// process cannot make progress on this session no matter how many times its
// container restarts. The orchestrator marks the session durably Failed
// (session.provider_fatal → status=Failed → the loud session.status:failed
// banner) and reaps the pod so the kubelet stops crash-looping it.
//
// This is NOT a per-turn turn.failed. Use it only when the failure is terminal
// for the whole session (e.g. a provider-session resume that can never
// succeed because the on-disk transcript is gone after a container restart).
// It mirrors runtimeConfig.js's auth/transport so the same SA-token internal
// path carries it.
export async function reportProviderFatal(cfg, payload) {
    const baseURL = trimTrailingSlashes(cfg.operatorInternalURL || "");
    const tokenPath = cfg.operatorTokenPath || "";
    if (!baseURL || !tokenPath || !cfg.sessionId) {
        return false;
    }
    const provider = String(payload?.provider ?? "").trim();
    const reason = String(payload?.reason ?? "").trim();
    if (!provider || !reason) {
        throw new Error("reportProviderFatal requires provider and reason");
    }
    const token = (await readFile(tokenPath, "utf8")).trim();
    const url = `${baseURL}/api/internal/sessions/${encodeURIComponent(cfg.sessionId)}/provider-fatal`;
    const response = await fetch(url, {
        method: "POST",
        headers: {
            Authorization: `Bearer ${token}`,
            "Content-Type": "application/json",
        },
        body: JSON.stringify({
            provider,
            reason,
            exit_code: Number.isFinite(payload?.exitCode)
                ? Math.trunc(payload.exitCode)
                : undefined,
            message: String(payload?.message ?? "").trim() || undefined,
        }),
    });
    if (!response.ok) {
        throw new Error(`provider-fatal report failed: ${response.status}`);
    }
    return true;
}
