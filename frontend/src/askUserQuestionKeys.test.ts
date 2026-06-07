import { test, expect } from "vitest";

import { shouldSubmitAskUserFreeFormKey } from "./askUserQuestionKeys";

test("AskUserQuestion free-form textarea submits on plain Enter", () => {
  expect(shouldSubmitAskUserFreeFormKey({ key: "Enter" })).toBe(true);
});

test("AskUserQuestion free-form textarea keeps modified Enter for text editing", () => {
  expect(shouldSubmitAskUserFreeFormKey({ key: "Enter", shiftKey: true })).toBe(false);
  expect(shouldSubmitAskUserFreeFormKey({ key: "Enter", altKey: true })).toBe(false);
  expect(shouldSubmitAskUserFreeFormKey({ key: "Enter", ctrlKey: true })).toBe(false);
  expect(shouldSubmitAskUserFreeFormKey({ key: "Enter", metaKey: true })).toBe(false);
  expect(shouldSubmitAskUserFreeFormKey({ key: "Enter", isComposing: true })).toBe(false);
});

test("AskUserQuestion free-form textarea ignores non-Enter keys", () => {
  expect(shouldSubmitAskUserFreeFormKey({ key: "Tab" })).toBe(false);
});
