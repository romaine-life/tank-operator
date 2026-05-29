import assert from "node:assert/strict";
import test from "node:test";

import { shouldSubmitAskUserFreeFormKey } from "./askUserQuestionKeys";

test("AskUserQuestion free-form textarea submits on plain Enter", () => {
  assert.equal(shouldSubmitAskUserFreeFormKey({ key: "Enter" }), true);
});

test("AskUserQuestion free-form textarea keeps modified Enter for text editing", () => {
  assert.equal(shouldSubmitAskUserFreeFormKey({ key: "Enter", shiftKey: true }), false);
  assert.equal(shouldSubmitAskUserFreeFormKey({ key: "Enter", altKey: true }), false);
  assert.equal(shouldSubmitAskUserFreeFormKey({ key: "Enter", ctrlKey: true }), false);
  assert.equal(shouldSubmitAskUserFreeFormKey({ key: "Enter", metaKey: true }), false);
  assert.equal(shouldSubmitAskUserFreeFormKey({ key: "Enter", isComposing: true }), false);
});

test("AskUserQuestion free-form textarea ignores non-Enter keys", () => {
  assert.equal(shouldSubmitAskUserFreeFormKey({ key: "Tab" }), false);
});
