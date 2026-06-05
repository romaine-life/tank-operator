const DEFAULT_RETRY_DELAYS_MS = [1000, 2000, 5000, 10000, 20000];

function configuredRetryDelaysMs(cfg) {
    if (!Array.isArray(cfg?.registerRetryDelaysMs)) {
        return DEFAULT_RETRY_DELAYS_MS;
    }
    return cfg.registerRetryDelaysMs
        .map((value) => Math.max(0, Math.floor(Number(value))))
        .filter((value) => Number.isFinite(value));
}

function retryableStatus(status) {
    return status === 408 || status === 409 || status === 425 || status === 429 || status >= 500;
}

function sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
}

export async function postOperatorInternalJSONWithRetry(cfg, url, token, body, failureLabel) {
    const delays = configuredRetryDelaysMs(cfg);
    let lastError;
    for (let attempt = 0; attempt <= delays.length; attempt += 1) {
        try {
            const response = await fetch(url, {
                method: "POST",
                headers: {
                    Authorization: `Bearer ${token}`,
                    "Content-Type": "application/json",
                },
                body: JSON.stringify(body),
            });
            if (response.ok) {
                return;
            }
            lastError = new Error(`${failureLabel}: ${response.status}`);
            if (!retryableStatus(response.status)) {
                throw Object.assign(lastError, { permanent: true });
            }
        } catch (err) {
            if (err?.permanent) {
                throw err;
            }
            lastError = err;
        }
        if (attempt < delays.length) {
            await sleep(delays[attempt]);
        }
    }
    throw lastError ?? new Error(failureLabel);
}
