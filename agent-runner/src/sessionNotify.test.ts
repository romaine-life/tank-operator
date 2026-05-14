import { test } from "node:test";
import assert from "node:assert/strict";

import { SessionEventNotifier } from "../../runner-shared/sessionNotify.js";

test("session event notifier posts durable order cursor to backend", async () => {
  const requests: Array<{ url: string; init: RequestInit }> = [];
  const notifier = new SessionEventNotifier(
    {
      sessionId: "63",
      operatorInternalURL: "http://tank-operator/",
      operatorTokenPath: "/token",
    },
    {
      readFile: async () => "jwt-token\n",
      fetch: async (url, init) => {
        requests.push({ url: String(url), init: init ?? {} });
        return new Response("", { status: 202 });
      },
    },
  );

  const ok = await notifier.notify({ order_key: "order-123" });

  assert.equal(ok, true);
  assert.equal(requests.length, 1);
  const request = requests[0]!;
  assert.equal(request.url, "http://tank-operator/api/internal/sessions/63/events/notify");
  assert.equal(request.init.method, "POST");
  assert.equal((request.init.headers as Record<string, string>).Authorization, "Bearer jwt-token");
  assert.equal(request.init.body, JSON.stringify({ last_order_key: "order-123" }));
});

test("session event notifier no-ops when backend config is absent", async () => {
  let fetchCalled = false;
  const notifier = new SessionEventNotifier(
    { sessionId: "63", operatorInternalURL: "", operatorTokenPath: "" },
    {
      fetch: async () => {
        fetchCalled = true;
        return new Response("", { status: 202 });
      },
    },
  );

  assert.equal(await notifier.notify({ order_key: "order-123" }), false);
  assert.equal(fetchCalled, false);
});
