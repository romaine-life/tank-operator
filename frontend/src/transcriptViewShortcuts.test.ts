import { test, expect } from "vitest";

import {
  isTranscriptToTurnsShortcut,
  isTurnsToTranscriptShortcut,
  type TranscriptViewShortcutContext,
} from "./transcriptViewShortcuts.ts";

function ctx(
  overrides: Partial<TranscriptViewShortcutContext> = {},
): TranscriptViewShortcutContext {
  return {
    key: "t",
    repeat: false,
    altKey: false,
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    isComposing: false,
    targetIsTranscript: true,
    targetIsTextEntry: false,
    activeTab: "chat",
    turnsAvailable: true,
    palettesOpen: false,
    ...overrides,
  };
}

test("plain T on the focused chat transcript opens turns", () => {
  expect(isTranscriptToTurnsShortcut(ctx())).toBe(true);
});

test("caps-lock T still opens turns", () => {
  expect(isTranscriptToTurnsShortcut(ctx({ key: "T" }))).toBe(true);
});

test("T opens turns only from a focused chat transcript with turns available", () => {
  expect(isTranscriptToTurnsShortcut(ctx({ targetIsTranscript: false }))).toBe(false);
  expect(isTranscriptToTurnsShortcut(ctx({ activeTab: "turns" }))).toBe(false);
  expect(isTranscriptToTurnsShortcut(ctx({ turnsAvailable: false }))).toBe(false);
});

test("T shortcut ignores modifiers, repeat, IME, and palettes", () => {
  expect(isTranscriptToTurnsShortcut(ctx({ altKey: true }))).toBe(false);
  expect(isTranscriptToTurnsShortcut(ctx({ ctrlKey: true }))).toBe(false);
  expect(isTranscriptToTurnsShortcut(ctx({ metaKey: true }))).toBe(false);
  expect(isTranscriptToTurnsShortcut(ctx({ shiftKey: true }))).toBe(false);
  expect(isTranscriptToTurnsShortcut(ctx({ repeat: true }))).toBe(false);
  expect(isTranscriptToTurnsShortcut(ctx({ isComposing: true }))).toBe(false);
  expect(isTranscriptToTurnsShortcut(ctx({ palettesOpen: true }))).toBe(false);
});

test("plain Escape in the turns view returns to transcript", () => {
  expect(isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape" }))).toBe(true);
});

test("Escape does not return from turns while text entry owns the event", () => {
  expect(isTurnsToTranscriptShortcut(
          ctx({
            activeTab: "turns",
            key: "Escape",
            targetIsTranscript: false,
            targetIsTextEntry: true,
          }),
        )).toBe(false);
});

test("Escape return ignores modifiers, repeat, IME, palettes, and other tabs", () => {
  expect(isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", altKey: true }))).toBe(false);
  expect(isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", ctrlKey: true }))).toBe(false);
  expect(isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", metaKey: true }))).toBe(false);
  expect(isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", shiftKey: true }))).toBe(false);
  expect(isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", repeat: true }))).toBe(false);
  expect(isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", isComposing: true }))).toBe(false);
  expect(isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", palettesOpen: true }))).toBe(false);
  expect(isTurnsToTranscriptShortcut(ctx({ key: "Escape" }))).toBe(false);
});
