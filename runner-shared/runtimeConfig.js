import { readFile } from "node:fs/promises";

function trimTrailingSlashes(value) {
    return String(value || "").replace(/\/+$/, "");
}

export async function reportRuntimeConfig(cfg, payload) {
    const baseURL = trimTrailingSlashes(cfg.operatorInternalURL || "");
    const tokenPath = cfg.operatorTokenPath || "";
    if (!baseURL || !tokenPath || !cfg.sessionId) {
        return false;
    }
    const token = (await readFile(tokenPath, "utf8")).trim();
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
        }),
    });
    if (!response.ok) {
        throw new Error(`runtime config report failed: ${response.status}`);
    }
    return true;
}
