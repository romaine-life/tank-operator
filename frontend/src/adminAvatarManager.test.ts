import { test, expect } from "vitest";
import {
  avatarSaveErrorMessage,
  fetchAvatarViews,
  imageFileFromTransfer,
  requestAvatarKindChange,
  requestAvatarUpdate,
} from "./AdminAvatarManager";

test("avatar save errors include server attempt references", () => {
  expect(avatarSaveErrorMessage(400, {
          detail: "Avatar upload request must use multipart/form-data.",
          code: "wrong_content_type",
          attempt_id: "avu_123",
        })).toBe("Avatar upload request must use multipart/form-data. Reference avu_123.");
  expect(avatarSaveErrorMessage(500, {})).toBe("save failed: 500");
});

test("avatar admin accepts image files from drag transfer data", () => {
  const file = new File(["png"], "portrait.png", { type: "image/png" });
  const transfer = {
    items: [],
    files: [file],
  } as unknown as DataTransfer;

  expect(imageFileFromTransfer(transfer)).toBe(file);
});

test("avatar admin names dropped image data that has no filename", () => {
  const file = new File(["webp"], "", { type: "image/webp" });
  const transfer = {
    items: [
      {
        kind: "file",
        type: "image/webp",
        getAsFile: () => file,
      },
    ],
    files: [],
  } as unknown as DataTransfer;

  const image = imageFileFromTransfer(transfer, "dropped-avatar");

  expect(image?.name).toBe("dropped-avatar.webp");
  expect(image?.type).toBe("image/webp");
});

test("avatar admin ignores non-image drag transfer data", () => {
  const transfer = {
    items: [
      {
        kind: "file",
        type: "text/plain",
        getAsFile: () => new File(["text"], "notes.txt", { type: "text/plain" }),
      },
    ],
    files: [new File(["text"], "notes.txt", { type: "text/plain" })],
  } as unknown as DataTransfer;

  expect(imageFileFromTransfer(transfer)).toBe(null);
});

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
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer jwt");
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

    expect(views.length).toBe(2);
    expect(views[0].id).toBe("jp1-malcolm");
    expect(views[0].avatarSrc).toBe("blob:avatar-1");
    expect(views[0].imageError).toBe(null);
    expect(views[1].id).toBe("av_missing_system");
    expect(views[1].avatarSrc).toBe(null);
    expect(views[1].imageError).toBe("avatar image failed: 404");
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as { localStorage?: Storage }).localStorage = originalLocalStorage;
    URL.createObjectURL = originalCreateObjectURL;
  }
});

test("requestAvatarKindChange PATCHes /api/admin/avatars/{id}/kind with the requested kind", async () => {
  const originalFetch = globalThis.fetch;
  const originalLocalStorage = (globalThis as { localStorage?: Storage }).localStorage;
  const storage = new Map<string, string>([["auth-romaine-jwt", "jwt"]]);

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

  let observedMethod = "";
  let observedURL = "";
  let observedBody = "";
  let observedAuth = "";
  let observedContentType = "";
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    observedURL = String(input);
    observedMethod = String(init?.method ?? "GET");
    observedBody = typeof init?.body === "string" ? init.body : "";
    const headers = new Headers(init?.headers);
    observedAuth = headers.get("Authorization") ?? "";
    observedContentType = headers.get("Content-Type") ?? "";
    return new Response(
      JSON.stringify({
        id: "av_1",
        kind: "system",
        name: "Ada",
        avatar_url: "/api/avatars/av_1/image",
        backing_url: "/api/avatars/av_1/backing",
        crop: { center_x: 0.5, center_y: 0.5, size: 1 },
        created_by: "admin@example.test",
        created_at: "2026-05-25T00:00:00Z",
        updated_at: "2026-05-25T00:01:00Z",
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }) as typeof fetch;

  try {
    const result = await requestAvatarKindChange("av_1", "system");

    expect(result).toEqual({ ok: true });
    expect(observedMethod).toBe("PATCH");
    expect(observedURL).toBe("/api/admin/avatars/av_1/kind");
    expect(observedBody).toBe(JSON.stringify({ kind: "system" }));
    expect(observedAuth).toBe("Bearer jwt");
    expect(observedContentType).toBe("application/json");
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as { localStorage?: Storage }).localStorage = originalLocalStorage;
  }
});

test("requestAvatarUpdate PATCHes /api/admin/avatars/{id} with multipart payload", async () => {
  const originalFetch = globalThis.fetch;
  const originalLocalStorage = (globalThis as { localStorage?: Storage }).localStorage;
  const storage = new Map<string, string>([["auth-romaine-jwt", "jwt"]]);

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

  let observedMethod = "";
  let observedURL = "";
  let observedAuth = "";
  let observedBody: BodyInit | null | undefined = null;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    observedURL = String(input);
    observedMethod = String(init?.method ?? "GET");
    observedBody = init?.body;
    observedAuth = new Headers(init?.headers).get("Authorization") ?? "";
    return jsonResponse({
      id: "av_1",
      kind: "agent",
      name: "Ada",
      avatar_url: "/api/avatars/av_1/image?v=123",
      backing_url: "/api/avatars/av_1/backing",
      crop: { center_x: 0.6, center_y: 0.4, size: 0.5 },
      created_by: "admin@example.test",
      created_at: "2026-05-25T00:00:00Z",
      updated_at: "2026-05-25T00:01:00Z",
    });
  }) as typeof fetch;

  try {
    const payload = new FormData();
    payload.set("name", "Ada");
    payload.set("avatar", new Blob(["png"], { type: "image/png" }), "avatar.png");

    const result = await requestAvatarUpdate("av_1", payload);

    expect(result).toEqual({ ok: true });
    expect(observedMethod).toBe("PATCH");
    expect(observedURL).toBe("/api/admin/avatars/av_1");
    expect(observedAuth).toBe("Bearer jwt");
    expect(observedBody).toBe(payload);
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as { localStorage?: Storage }).localStorage = originalLocalStorage;
  }
});

test("requestAvatarKindChange surfaces backend detail on failure", async () => {
  const originalFetch = globalThis.fetch;
  const originalLocalStorage = (globalThis as { localStorage?: Storage }).localStorage;
  const storage = new Map<string, string>([["auth-romaine-jwt", "jwt"]]);

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

  globalThis.fetch = (async () =>
    new Response(JSON.stringify({ detail: "avatar already has the requested kind" }), {
      status: 409,
      headers: { "Content-Type": "application/json" },
    })) as typeof fetch;

  try {
    const result = await requestAvatarKindChange("av_1", "agent");
    expect(result).toEqual({ ok: false, detail: "avatar already has the requested kind" });
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as { localStorage?: Storage }).localStorage = originalLocalStorage;
  }
});

test("requestAvatarKindChange falls back to status when body has no detail", async () => {
  const originalFetch = globalThis.fetch;
  const originalLocalStorage = (globalThis as { localStorage?: Storage }).localStorage;
  const storage = new Map<string, string>([["auth-romaine-jwt", "jwt"]]);

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

  globalThis.fetch = (async () =>
    new Response("not json", { status: 500 })) as typeof fetch;

  try {
    const result = await requestAvatarKindChange("av_1", "agent");
    expect(result).toEqual({ ok: false, detail: "kind change failed: 500" });
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
