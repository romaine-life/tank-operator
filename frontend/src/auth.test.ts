import assert from "node:assert/strict";
import test from "node:test";
import {
  authedFetch,
  authedEventSourceURL,
  bootstrapAuth,
  clearStoredToken,
  getStoredToken,
} from "./auth";

test("bootstrapAuth stores and presents the upstream auth.romaine.life JWT directly", async () => {
  const storage = new Map<string, string>();
  const calls: string[] = [];
  const originalFetch = globalThis.fetch;
  const originalLocalStorage = (globalThis as { localStorage?: Storage }).localStorage;

  (globalThis as { localStorage?: Storage }).localStorage = {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => {
      storage.set(key, value);
    },
    removeItem: (key: string) => {
      storage.delete(key);
    },
    clear: () => storage.clear(),
    key: (index: number) => Array.from(storage.keys())[index] ?? null,
    get length() {
      return storage.size;
    },
  } as Storage;

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(url);
    const retiredExchangePath = "/api/auth/" + "exchange";
    if (url === retiredExchangePath) {
      throw new Error("retired exchange endpoint was called");
    }
    if (url === "/api/config") {
      return jsonResponse({ auth_url: "https://auth.test" });
    }
    if (url === "https://auth.test/api/auth/token") {
      return jsonResponse({ token: "upstream.jwt" });
    }
    if (url === "/api/auth/me") {
      assert.equal(new Headers(init?.headers).get("Authorization"), "Bearer upstream.jwt");
      return jsonResponse({
        sub: "sub-1",
        email: "user@example.test",
        name: "User",
        role: "user",
        avatar_url: "https://example.test/avatar",
        github_login: null,
        installation_id: null,
        pinned_repos: [],
        run_prefs: null,
      });
    }
    return new Response("not found", { status: 404 });
  }) as typeof fetch;

  try {
    clearStoredToken();
    const user = await bootstrapAuth();
    assert.equal(user?.email, "user@example.test");
    assert.equal(getStoredToken(), "upstream.jwt");
    assert.deepEqual(calls, ["/api/config", "https://auth.test/api/auth/token", "/api/auth/me"]);
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as { localStorage?: Storage }).localStorage = originalLocalStorage;
  }
});

test("authedFetch refreshes an expired JWT and retries the original request", async () => {
  const originalLocalStorage = (globalThis as { localStorage?: Storage }).localStorage;
  const originalFetch = globalThis.fetch;
  const storage = new Map<string, string>([
    ["auth-romaine-jwt", "expired.jwt"],
  ]);
  let protectedAttempts = 0;

  (globalThis as { localStorage?: Storage }).localStorage = {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => {
      storage.set(key, value);
    },
    removeItem: (key: string) => {
      storage.delete(key);
    },
    clear: () => storage.clear(),
    key: (index: number) => Array.from(storage.keys())[index] ?? null,
    get length() {
      return storage.size;
    },
  } as Storage;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/config") {
      return jsonResponse({ auth_url: "https://auth.test" });
    }
    if (url === "https://auth.test/api/auth/token") {
      return jsonResponse({ token: "fresh.jwt" });
    }
    if (url === "/api/sessions/123") {
      protectedAttempts += 1;
      const authorization = new Headers(init?.headers).get("Authorization");
      if (protectedAttempts === 1) {
        assert.equal(authorization, "Bearer expired.jwt");
        return new Response("expired", { status: 401 });
      }
      assert.equal(authorization, "Bearer fresh.jwt");
      assert.equal(init?.method, "DELETE");
      return new Response(null, { status: 204 });
    }
    return new Response("not found", { status: 404 });
  }) as typeof fetch;

  try {
    const res = await authedFetch("/api/sessions/123", { method: "DELETE" });
    assert.equal(res.status, 204);
    assert.equal(protectedAttempts, 2);
    assert.equal(getStoredToken(), "fresh.jwt");
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as { localStorage?: Storage }).localStorage = originalLocalStorage;
  }
});

test("authedEventSourceURL mints a short-lived stream ticket", async () => {
  const originalLocalStorage = (globalThis as { localStorage?: Storage }).localStorage;
  const originalFetch = globalThis.fetch;
  const storage = new Map<string, string>([
    ["auth-romaine-jwt", "jwt.with+/chars"],
  ]);

  (globalThis as { localStorage?: Storage }).localStorage = {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => {
      storage.set(key, value);
    },
    removeItem: (key: string) => {
      storage.delete(key);
    },
    clear: () => storage.clear(),
    key: (index: number) => Array.from(storage.keys())[index] ?? null,
    get length() {
      return storage.size;
    },
  } as Storage;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    assert.equal(String(input), "/api/auth/stream-ticket");
    assert.equal(new Headers(init?.headers).get("Authorization"), "Bearer jwt.with+/chars");
    assert.equal(init?.method, "POST");
    assert.deepEqual(JSON.parse(String(init?.body)), {
      stream: "session-events",
      session_id: "152",
    });
    return jsonResponse({ ticket: "ticket.with+/chars" });
  }) as typeof fetch;

  try {
    assert.equal(
      await authedEventSourceURL("/api/sessions/152/events?last_order_key=7", {
        stream: "session-events",
        sessionId: "152",
      }),
      "/api/sessions/152/events?last_order_key=7&stream_ticket=ticket.with%2B%2Fchars",
    );
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as { localStorage?: Storage }).localStorage = originalLocalStorage;
  }
});

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
