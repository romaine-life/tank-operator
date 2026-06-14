import { expect, test } from "vitest";

import { nextAskUserQuestionSelections } from "./askUserQuestionSelection";

test("single-select AskUserQuestion choices can be unselected by clicking again", () => {
  const selected = nextAskUserQuestionSelections({
    previousSelections: {},
    question: "Proceed?",
    label: "Yes",
    multiSelect: false,
  });

  expect(selected).toEqual({ "Proceed?": ["Yes"] });

  expect(
    nextAskUserQuestionSelections({
      previousSelections: selected,
      question: "Proceed?",
      label: "Yes",
      multiSelect: false,
    }),
  ).toEqual({ "Proceed?": [] });
});

test("single-select AskUserQuestion choices replace other selected choices", () => {
  expect(
    nextAskUserQuestionSelections({
      previousSelections: { "Proceed?": ["Yes"] },
      question: "Proceed?",
      label: "No",
      multiSelect: false,
    }),
  ).toEqual({ "Proceed?": ["No"] });
});

test("multi-select AskUserQuestion choices still toggle independently", () => {
  const selected = nextAskUserQuestionSelections({
    previousSelections: { "Pick tools": ["Read"] },
    question: "Pick tools",
    label: "Search",
    multiSelect: true,
  });

  expect(selected).toEqual({ "Pick tools": ["Read", "Search"] });

  expect(
    nextAskUserQuestionSelections({
      previousSelections: selected,
      question: "Pick tools",
      label: "Read",
      multiSelect: true,
    }),
  ).toEqual({ "Pick tools": ["Search"] });
});
