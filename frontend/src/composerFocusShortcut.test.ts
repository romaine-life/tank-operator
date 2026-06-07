import { test, expect } from "vitest";

import {
  resolveComposerFocusShortcut,
  type ComposerFocusShortcutContext,
} from "./composerFocusShortcut.ts";

function ctx(
  overrides: Partial<ComposerFocusShortcutContext> = {},
): ComposerFocusShortcutContext {
  return {
    key: "/",
    altKey: false,
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    isComposing: false,
    targetIsComposer: false,
    activeTab: "chat",
    ...overrides,
  };
}

test("plain / on the chat tab focuses the composer in place", () => {
  expect(resolveComposerFocusShortcut(ctx())).toBe("focus-in-place");
});

test("plain / on the turns tab focuses the composer in place", () => {
  // Regression: the turns view renders the same composer, so `/` must focus it
  // where it is instead of navigating back to the main transcript.
  expect(resolveComposerFocusShortcut(ctx({ activeTab: "turns" }))).toBe("focus-in-place");
});

test("/ on a tab without an in-page composer switches to turns first", () => {
  for (const activeTab of ["files", "background", "session-data", "terminal"]) {
    expect(resolveComposerFocusShortcut(ctx({ activeTab })), `expected switch-to-turns for ${activeTab}`).toBe("switch-to-turns");
  }
});

test("/ is ignored once the composer textarea owns the keystroke", () => {
  // Typing `/` inside the composer must keep its normal slash-palette behavior.
  expect(resolveComposerFocusShortcut(ctx({ targetIsComposer: true }))).toBe("ignore");
  expect(resolveComposerFocusShortcut(
          ctx({ targetIsComposer: true, activeTab: "turns" }),
        )).toBe("ignore");
});

test("/ shortcut ignores modifiers and IME composition", () => {
  expect(resolveComposerFocusShortcut(ctx({ altKey: true }))).toBe("ignore");
  expect(resolveComposerFocusShortcut(ctx({ ctrlKey: true }))).toBe("ignore");
  expect(resolveComposerFocusShortcut(ctx({ metaKey: true }))).toBe("ignore");
  expect(resolveComposerFocusShortcut(ctx({ shiftKey: true }))).toBe("ignore");
  expect(resolveComposerFocusShortcut(ctx({ isComposing: true }))).toBe("ignore");
});

test("only the / key triggers the shortcut", () => {
  for (const key of ["t", "Escape", "Enter", "?", "/ ", "Slash"]) {
    expect(resolveComposerFocusShortcut(ctx({ key })), `expected ignore for key ${JSON.stringify(key)}`).toBe("ignore");
  }
});
