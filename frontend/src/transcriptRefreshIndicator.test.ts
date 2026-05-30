import assert from "node:assert/strict";
import { test } from "node:test";

import {
  REFRESH_FLASH_DURATION_MS,
  REFRESH_FLASH_LABEL,
  refreshFlashLabel,
  type RefreshFlashContext,
} from "./transcriptRefreshIndicator.ts";

function ctx(
  overrides: Partial<RefreshFlashContext> = {},
): RefreshFlashContext {
  return {
    visible: true,
    activeTab: "chat",
    active: true,
    ...overrides,
  };
}

test("an active flash shows the label on the chat transcript", () => {
  assert.equal(refreshFlashLabel(ctx()), REFRESH_FLASH_LABEL);
});

test("an active flash shows on the turns page too", () => {
  assert.equal(refreshFlashLabel(ctx({ activeTab: "turns" })), REFRESH_FLASH_LABEL);
});

test("no flash shows when the window has elapsed (active=false)", () => {
  assert.equal(refreshFlashLabel(ctx({ active: false })), null);
});

test("a flash does not show on a hidden pane", () => {
  assert.equal(refreshFlashLabel(ctx({ visible: false })), null);
});

test("a flash does not bleed onto non-transcript tabs", () => {
  for (const activeTab of ["files", "background", "settings", "help"]) {
    assert.equal(refreshFlashLabel(ctx({ activeTab })), null, activeTab);
  }
});

test("the display window is a brief, transient duration", () => {
  // A debug affordance: visible long enough to read, short enough not to
  // linger as chrome. Guard against an accidental multi-second value.
  assert.ok(REFRESH_FLASH_DURATION_MS > 0);
  assert.ok(REFRESH_FLASH_DURATION_MS <= 3000);
});
