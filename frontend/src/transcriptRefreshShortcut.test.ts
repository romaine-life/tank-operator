import { test, expect } from "vitest";

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
  expect(isTranscriptRefreshShortcut(ctx())).toBe(true);
});

test("R triggers refresh on the turns page too", () => {
  expect(isTranscriptRefreshShortcut(ctx({ activeTab: "turns" }))).toBe(true);
});

test("caps-lock R (uppercase key, no shift) still triggers refresh", () => {
  expect(isTranscriptRefreshShortcut(ctx({ key: "R" }))).toBe(true);
});

test("a non-R key never triggers refresh", () => {
  for (const key of ["a", "Enter", "Home", "End", "ArrowUp", "Escape", " "]) {
    expect(isTranscriptRefreshShortcut(ctx({ key })), key).toBe(false);
  }
});

test("modifier combos are ignored so native shortcuts (Cmd+R/Ctrl+R) win", () => {
  expect(isTranscriptRefreshShortcut(ctx({ metaKey: true }))).toBe(false);
  expect(isTranscriptRefreshShortcut(ctx({ ctrlKey: true }))).toBe(false);
  expect(isTranscriptRefreshShortcut(ctx({ altKey: true }))).toBe(false);
  // Shift+R is excluded; caps-lock "R" (handled above) is not, because it
  // arrives without shiftKey set.
  expect(isTranscriptRefreshShortcut(ctx({ shiftKey: true }))).toBe(false);
});

test("OS key auto-repeat from a held key does not storm refreshes", () => {
  expect(isTranscriptRefreshShortcut(ctx({ repeat: true }))).toBe(false);
});

test("IME composition keystrokes are ignored", () => {
  expect(isTranscriptRefreshShortcut(ctx({ isComposing: true }))).toBe(false);
});

test("typing R into an open palette does not refresh", () => {
  expect(isTranscriptRefreshShortcut(ctx({ palettesOpen: true }))).toBe(false);
});

test("R refreshes only when the transcript region holds focus", () => {
  expect(isTranscriptRefreshShortcut(ctx({ targetIsTranscript: false }))).toBe(false);
});

test("refresh does not apply to non-transcript tabs", () => {
  for (const activeTab of ["files", "background", "settings", "help"]) {
    expect(isTranscriptRefreshShortcut(ctx({ activeTab })), activeTab).toBe(false);
  }
});
