import { test, expect } from "vitest";

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
  expect(refreshFlashLabel(ctx())).toBe(REFRESH_FLASH_LABEL);
});

test("an active flash shows on the turns page too", () => {
  expect(refreshFlashLabel(ctx({ activeTab: "turns" }))).toBe(REFRESH_FLASH_LABEL);
});

test("no flash shows when the window has elapsed (active=false)", () => {
  expect(refreshFlashLabel(ctx({ active: false }))).toBe(null);
});

test("a flash does not show on a hidden pane", () => {
  expect(refreshFlashLabel(ctx({ visible: false }))).toBe(null);
});

test("a flash does not bleed onto non-transcript tabs", () => {
  for (const activeTab of ["files", "background", "settings", "help"]) {
    expect(refreshFlashLabel(ctx({ activeTab })), activeTab).toBe(null);
  }
});

test("the display window is a brief, transient duration", () => {
  // A debug affordance: visible long enough to read, short enough not to
  // linger as chrome. Guard against an accidental multi-second value.
  expect(REFRESH_FLASH_DURATION_MS > 0).toBeTruthy();
  expect(REFRESH_FLASH_DURATION_MS <= 3000).toBeTruthy();
});
