import assert from "node:assert/strict";
import { test } from "node:test";

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
  assert.equal(isTranscriptToTurnsShortcut(ctx()), true);
});

test("caps-lock T still opens turns", () => {
  assert.equal(isTranscriptToTurnsShortcut(ctx({ key: "T" })), true);
});

test("T opens turns only from a focused chat transcript with turns available", () => {
  assert.equal(
    isTranscriptToTurnsShortcut(ctx({ targetIsTranscript: false })),
    false,
  );
  assert.equal(isTranscriptToTurnsShortcut(ctx({ activeTab: "turns" })), false);
  assert.equal(isTranscriptToTurnsShortcut(ctx({ turnsAvailable: false })), false);
});

test("T shortcut ignores modifiers, repeat, IME, and palettes", () => {
  assert.equal(isTranscriptToTurnsShortcut(ctx({ altKey: true })), false);
  assert.equal(isTranscriptToTurnsShortcut(ctx({ ctrlKey: true })), false);
  assert.equal(isTranscriptToTurnsShortcut(ctx({ metaKey: true })), false);
  assert.equal(isTranscriptToTurnsShortcut(ctx({ shiftKey: true })), false);
  assert.equal(isTranscriptToTurnsShortcut(ctx({ repeat: true })), false);
  assert.equal(isTranscriptToTurnsShortcut(ctx({ isComposing: true })), false);
  assert.equal(isTranscriptToTurnsShortcut(ctx({ palettesOpen: true })), false);
});

test("plain Escape in the turns view returns to transcript", () => {
  assert.equal(
    isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape" })),
    true,
  );
});

test("Escape does not return from turns while text entry owns the event", () => {
  assert.equal(
    isTurnsToTranscriptShortcut(
      ctx({
        activeTab: "turns",
        key: "Escape",
        targetIsTranscript: false,
        targetIsTextEntry: true,
      }),
    ),
    false,
  );
});

test("Escape return ignores modifiers, repeat, IME, palettes, and other tabs", () => {
  assert.equal(
    isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", altKey: true })),
    false,
  );
  assert.equal(
    isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", ctrlKey: true })),
    false,
  );
  assert.equal(
    isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", metaKey: true })),
    false,
  );
  assert.equal(
    isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", shiftKey: true })),
    false,
  );
  assert.equal(
    isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", repeat: true })),
    false,
  );
  assert.equal(
    isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", isComposing: true })),
    false,
  );
  assert.equal(
    isTurnsToTranscriptShortcut(ctx({ activeTab: "turns", key: "Escape", palettesOpen: true })),
    false,
  );
  assert.equal(isTurnsToTranscriptShortcut(ctx({ key: "Escape" })), false);
});
