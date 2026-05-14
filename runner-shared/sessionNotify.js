import { readFile } from "node:fs/promises";

export class SessionEventNotifier {
    constructor(cfg, deps = {}) {
        this.cfg = cfg;
        this.fetch = deps.fetch ?? globalThis.fetch;
        this.readFile = deps.readFile ?? readFile;
    }
    async notify(event) {
        const baseURL = trimTrailingSlashes(this.cfg.operatorInternalURL ?? "");
        const tokenPath = this.cfg.operatorTokenPath ?? "";
        const sessionID = this.cfg.sessionId ?? "";
        if (!baseURL || !tokenPath || !sessionID || typeof this.fetch !== "function") {
            return false;
        }
        const token = (await this.readFile(tokenPath, "utf8")).trim();
        if (!token) {
            return false;
        }
        const orderKey = typeof event?.order_key === "string" ? event.order_key : "";
        const response = await this.fetch(`${baseURL}/api/internal/sessions/${encodeURIComponent(sessionID)}/events/notify`, {
            method: "POST",
            headers: {
                "Authorization": `Bearer ${token}`,
                "Content-Type": "application/json",
            },
            body: JSON.stringify({ last_order_key: orderKey }),
        });
        if (!response.ok) {
            throw new Error(`session event notify failed: ${response.status}`);
        }
        return true;
    }
}

function trimTrailingSlashes(value) {
    return String(value).trim().replace(/\/+$/, "");
}
