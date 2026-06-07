import assert from "node:assert/strict";
import test from "node:test";
import {
  addAvatarPreviewEditRequestListener,
  avatarPreviewIsEditable,
  closeAvatarPreview,
  openAvatarPreview,
  type AvatarPreviewDetail,
} from "./avatarPreview";

class TestCustomEvent<T> extends Event {
  detail: T;

  constructor(type: string, init: CustomEventInit<T>) {
    super(type);
    this.detail = init.detail as T;
  }
}

function withWindow<T>(fn: (windowTarget: EventTarget) => T): T {
  const originalWindow = (globalThis as { window?: Window }).window;
  const originalCustomEvent = globalThis.CustomEvent;
  const windowTarget = new EventTarget();
  (globalThis as { window?: Window }).window = windowTarget as Window;
  globalThis.CustomEvent = TestCustomEvent as typeof CustomEvent;
  try {
    return fn(windowTarget);
  } finally {
    if (originalWindow) {
      (globalThis as { window?: Window }).window = originalWindow;
    } else {
      delete (globalThis as { window?: Window }).window;
    }
    globalThis.CustomEvent = originalCustomEvent;
  }
}

test("avatar preview editability is limited to managed avatar kinds", () => {
  assert.equal(
    avatarPreviewIsEditable({
      name: "Agent",
      avatarSrc: "/agent.png",
      kind: "agent",
    }),
    true,
  );
  assert.equal(
    avatarPreviewIsEditable({
      name: "System",
      avatarSrc: "/system.png",
      kind: "system",
    }),
    true,
  );
  assert.equal(
    avatarPreviewIsEditable({
      name: "Profile",
      avatarSrc: "/profile.png",
      kind: "personal",
    }),
    false,
  );
});

test("openAvatarPreview dispatches the preview detail and consumes source events", () => {
  withWindow((windowTarget) => {
    const detail: AvatarPreviewDetail = {
      name: "Dr. Ellie Sattler",
      avatarSrc: "/assets/avatars/jp1-sattler.png",
      kind: "agent",
    };
    let observed: AvatarPreviewDetail | null = null;
    let stopped = false;
    let prevented = false;
    windowTarget.addEventListener("tank-avatar-preview", (event) => {
      observed = (event as CustomEvent<AvatarPreviewDetail>).detail;
    });

    openAvatarPreview(detail, {
      stopPropagation: () => {
        stopped = true;
      },
      preventDefault: () => {
        prevented = true;
      },
    });

    assert.equal(observed, detail);
    assert.equal(stopped, true);
    assert.equal(prevented, true);
  });
});

test("avatar preview edit request listeners receive details and unsubscribe cleanly", () => {
  withWindow((windowTarget) => {
    const detail: AvatarPreviewDetail = {
      name: "System Avatar",
      avatarSrc: "/system.png",
      kind: "system",
    };
    const observed: AvatarPreviewDetail[] = [];
    const remove = addAvatarPreviewEditRequestListener((next) => {
      observed.push(next);
    });

    windowTarget.dispatchEvent(
      new CustomEvent<AvatarPreviewDetail>("tank-avatar-preview-edit-request", {
        detail,
      }),
    );
    remove();
    windowTarget.dispatchEvent(
      new CustomEvent<AvatarPreviewDetail>("tank-avatar-preview-edit-request", {
        detail: { ...detail, name: "Ignored" },
      }),
    );

    assert.deepEqual(observed, [detail]);
  });
});

test("closeAvatarPreview dispatches a lightbox close event", () => {
  withWindow((windowTarget) => {
    let closeCount = 0;
    windowTarget.addEventListener("tank-avatar-preview-close", () => {
      closeCount += 1;
    });

    closeAvatarPreview();

    assert.equal(closeCount, 1);
  });
});
