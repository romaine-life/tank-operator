import assert from "node:assert/strict";
import test from "node:test";
import { bootstrapAuth, clearStoredToken, getStoredToken } from "./auth";

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

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
