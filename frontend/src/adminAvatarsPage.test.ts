import assert from "node:assert/strict";
import test from "node:test";
import { fetchAvatarViews } from "./AdminAvatarsPage";

test("avatar admin keeps usable entries when another avatar image is missing", async () => {
  const originalFetch = globalThis.fetch;
  const originalLocalStorage = (globalThis as { localStorage?: Storage }).localStorage;
  const originalCreateObjectURL = URL.createObjectURL;
  const storage = new Map<string, string>([["auth-romaine-jwt", "jwt"]]);
  const objectURLs: string[] = [];

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
  URL.createObjectURL = (blob: Blob) => {
    objectURLs.push(blob.type);
    return `blob:avatar-${objectURLs.length}`;
  };
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    assert.equal(new Headers(init?.headers).get("Authorization"), "Bearer jwt");
    const url = String(input);
    if (url === "/api/avatars") {
      return jsonResponse({
        entries: [
          {
            id: "jp1-malcolm",
            kind: "agent",
            name: "Dr. Ian Malcolm",
            avatar_url: "/api/avatars/jp1-malcolm/image",
            backing_url: "/api/avatars/jp1-malcolm/backing",
            crop: { center_x: 0.5, center_y: 0.5, size: 1 },
            created_by: "tank-operator",
            created_at: "2026-05-25T00:00:00Z",
          },
          {
            id: "av_missing_system",
            kind: "system",
            name: "Missing system",
            avatar_url: "/api/avatars/av_missing_system/image",
            backing_url: "/api/avatars/av_missing_system/backing",
            crop: { center_x: 0.5, center_y: 0.5, size: 1 },
            created_by: "admin@example.test",
            created_at: "2026-05-25T00:00:00Z",
          },
        ],
      });
    }
    if (url === "/api/avatars/jp1-malcolm/image") {
      return new Response(new Blob(["png"], { type: "image/png" }), {
        status: 200,
        headers: { "Content-Type": "image/png" },
      });
    }
    if (url === "/api/avatars/av_missing_system/image") {
      return new Response("missing", { status: 404 });
    }
    return new Response("not found", { status: 404 });
  }) as typeof fetch;

  try {
    const views = await fetchAvatarViews();

    assert.equal(views.length, 2);
    assert.equal(views[0].id, "jp1-malcolm");
    assert.equal(views[0].avatarSrc, "blob:avatar-1");
    assert.equal(views[0].imageError, null);
    assert.equal(views[1].id, "av_missing_system");
    assert.equal(views[1].avatarSrc, null);
    assert.equal(views[1].imageError, "avatar image failed: 404");
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as { localStorage?: Storage }).localStorage = originalLocalStorage;
    URL.createObjectURL = originalCreateObjectURL;
  }
});

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
