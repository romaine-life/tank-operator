import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import http from "node:http";
import assert from "node:assert/strict";
import test from "node:test";

import { registerBackgroundTaskWake } from "./backgroundTaskWake.js";

async function withWakeServer(handler, fn) {
    const requests = [];
    const server = http.createServer(async (req, res) => {
        const chunks = [];
        for await (const chunk of req) {
            chunks.push(chunk);
        }
        const body = Buffer.concat(chunks).toString("utf8");
        requests.push({ req, body });
        await handler(req, res, requests);
    });
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    const { port } = server.address();
    try {
        await fn(`http://127.0.0.1:${port}`, requests);
    } finally {
        await new Promise((resolve, reject) => server.close((err) => (err ? reject(err) : resolve())));
    }
}

async function withTokenFile(fn) {
    const dir = await mkdtemp(join(tmpdir(), "tank-wake-test-"));
    const tokenPath = join(dir, "token");
    await writeFile(tokenPath, "session-token\n", "utf8");
    try {
        await fn(tokenPath);
    } finally {
        await rm(dir, { recursive: true, force: true });
    }
}

test("registerBackgroundTaskWake retries transient operator failures", async () => {
    await withTokenFile(async (tokenPath) => {
        await withWakeServer((req, res, requests) => {
            if (requests.length === 1) {
                res.writeHead(500).end("temporarily unavailable");
                return;
            }
            res.writeHead(202, { "Content-Type": "application/json" }).end("{}");
        }, async (baseURL, requests) => {
            const registered = await registerBackgroundTaskWake({
                operatorInternalURL: baseURL,
                operatorTokenPath: tokenPath,
                sessionId: "44",
                registerRetryDelaysMs: [1],
            }, {
                taskID: "bg5y9vim7",
                status: "completed",
                description: "Sleep 3 seconds then print done",
            });

            assert.equal(registered, true);
            assert.equal(requests.length, 2);
            assert.equal(requests[1].req.headers.authorization, "Bearer session-token");
            assert.equal(requests[1].req.url, "/api/internal/sessions/44/background-task-wakes");
            assert.deepEqual(JSON.parse(requests[1].body), {
                task_id: "bg5y9vim7",
                status: "completed",
                description: "Sleep 3 seconds then print done",
                summary: "",
                last_tool_name: "",
                error: "",
            });
        });
    });
});

test("registerBackgroundTaskWake does not retry permanent request failures", async () => {
    await withTokenFile(async (tokenPath) => {
        await withWakeServer((_req, res) => {
            res.writeHead(400).end("bad request");
        }, async (baseURL, requests) => {
            await assert.rejects(
                () => registerBackgroundTaskWake({
                    operatorInternalURL: baseURL,
                    operatorTokenPath: tokenPath,
                    sessionId: "44",
                    registerRetryDelaysMs: [1, 1],
                }, { taskID: "" }),
                /background task wake register failed: 400/,
            );
            assert.equal(requests.length, 1);
        });
    });
});
