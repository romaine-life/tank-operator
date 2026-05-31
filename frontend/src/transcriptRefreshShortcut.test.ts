import assert from "node:assert/strict";
import { test } from "node:test";

import {
  isTranscriptRefreshShortcut,
  type TranscriptRefreshKeyContext,
} from "./transcriptRefreshShortcut.ts";

function ctx(
  overrides: Partial<TranscriptRefreshKeyContext> = {},
): TranscriptRefreshKeyContext {
  return {
    key: "r",
    repeat: false,
    altKey: false,
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    isComposing: false,
    targetIsTranscript: true,
    activeTab: "chat",
    palettesOpen: false,
    ...overrides,
  };
}

test("plain R on a focused chat transcript triggers refresh", () => {
  assert.equal(isTranscriptRefreshShortcut(ctx()), true);
});

test("R triggers refresh on the turns page too", () => {
  assert.equal(isTranscriptRefreshShortcut(ctx({ activeTab: "turns" })), true);
});

test("caps-lock R (uppercase key, no shift) still triggers refresh", () => {
  assert.equal(isTranscriptRefreshShortcut(ctx({ key: "R" })), true);
});

test("a non-R key never triggers refresh", () => {
  for (const key of ["a", "Enter", "Home", "End", "ArrowUp", "Escape", " "]) {
    assert.equal(isTranscriptRefreshShortcut(ctx({ key })), false, key);
  }
});

test("modifier combos are ignored so native shortcuts (Cmd+R/Ctrl+R) win", () => {
  assert.equal(isTranscriptRefreshShortcut(ctx({ metaKey: true })), false);
  assert.equal(isTranscriptRefreshShortcut(ctx({ ctrlKey: true })), false);
  assert.equal(isTranscriptRefreshShortcut(ctx({ altKey: true })), false);
  // Shift+R is excluded; caps-lock "R" (handled above) is not, because it
  // arrives without shiftKey set.
  assert.equal(isTranscriptRefreshShortcut(ctx({ shiftKey: true })), false);
});

test("OS key auto-repeat from a held key does not storm refreshes", () => {
  assert.equal(isTranscriptRefreshShortcut(ctx({ repeat: true })), false);
});

test("IME composition keystrokes are ignored", () => {
  assert.equal(isTranscriptRefreshShortcut(ctx({ isComposing: true })), false);
});

test("typing R into an open palette does not refresh", () => {
  assert.equal(isTranscriptRefreshShortcut(ctx({ palettesOpen: true })), false);
});

test("R refreshes only when the transcript region holds focus", () => {
  assert.equal(
    isTranscriptRefreshShortcut(ctx({ targetIsTranscript: false })),
    false,
  );
});

test("refresh does not apply to non-transcript tabs", () => {
  for (const activeTab of ["files", "background", "settings", "help"]) {
    assert.equal(isTranscriptRefreshShortcut(ctx({ activeTab })), false, activeTab);
  }
});
