import { expect, test } from "vitest";

import {
  SOMETHING_ELSE_LABEL,
  buildAskUserQuestionAnswerPayload,
  effectiveAskUserQuestionSelection,
  nextAskUserQuestionSelections,
} from "./askUserQuestionSelection";

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

test("a question with no stored selection answers as the default 'Something else'", () => {
  expect(effectiveAskUserQuestionSelection({}, "Proceed?")).toEqual([
    SOMETHING_ELSE_LABEL,
  ]);
  // A selection toggled back to empty also reads as the default — there is no
  // nothing-selected state.
  expect(
    effectiveAskUserQuestionSelection({ "Proceed?": [] }, "Proceed?"),
  ).toEqual([SOMETHING_ELSE_LABEL]);
});

test("a real stored selection is returned verbatim by the effective reader", () => {
  expect(
    effectiveAskUserQuestionSelection({ "Proceed?": ["Yes"] }, "Proceed?"),
  ).toEqual(["Yes"]);
});

test("multi-select 'Something else' is mutually exclusive with real picks", () => {
  // Selecting a real option while only the sentinel is selected drops the
  // sentinel.
  expect(
    nextAskUserQuestionSelections({
      previousSelections: { "Pick tools": [SOMETHING_ELSE_LABEL] },
      question: "Pick tools",
      label: "Read",
      multiSelect: true,
    }),
  ).toEqual({ "Pick tools": ["Read"] });

  // Selecting "Something else" while real options are picked clears them.
  expect(
    nextAskUserQuestionSelections({
      previousSelections: { "Pick tools": ["Read", "Search"] },
      question: "Pick tools",
      label: SOMETHING_ELSE_LABEL,
      multiSelect: true,
    }),
  ).toEqual({ "Pick tools": [SOMETHING_ELSE_LABEL] });
});

test("single-select can select the 'Something else' sentinel like any choice", () => {
  expect(
    nextAskUserQuestionSelections({
      previousSelections: { "Proceed?": ["Yes"] },
      question: "Proceed?",
      label: SOMETHING_ELSE_LABEL,
      multiSelect: false,
    }),
  ).toEqual({ "Proceed?": [SOMETHING_ELSE_LABEL] });
});

// --- Contract: AskUserQuestion answer input ---------------------------------

const Q = (question: string, options: Array<{ label: string; preview?: string }>) => ({
  question,
  options,
});

test("an empty pass answers as 'Something else' with no annotations", () => {
  // Nothing picked, nothing typed: the question still produces a valid, honest
  // answer instead of being unsubmittable. This is the bug fix — a human can
  // decline without being trapped.
  const payload = buildAskUserQuestionAnswerPayload(
    [Q("Which DB?", [{ label: "Postgres" }, { label: "MySQL" }])],
    {},
    {},
  );
  expect(payload.answers).toEqual({ "Which DB?": [SOMETHING_ELSE_LABEL] });
  expect(payload.annotations).toEqual({});
});

test("companion text is carried on a real-option pick (any selection)", () => {
  // The user picked a concrete option AND added context. The text must NOT be
  // dropped just because the option exists — it rides as a note. (Pre-fix, text
  // was discarded unless allowFreeForm was set.)
  const payload = buildAskUserQuestionAnswerPayload(
    [Q("Which DB?", [{ label: "Postgres", preview: "<i>pg</i>" }])],
    { "Which DB?": ["Postgres"] },
    { "Which DB?": "  but only the read replica  " },
  );
  expect(payload.answers).toEqual({ "Which DB?": ["Postgres"] });
  expect(payload.annotations).toEqual({
    "Which DB?": { preview: "<i>pg</i>", notes: "but only the read replica" },
  });
});

test("typed text with no option picked becomes the 'Something else' answer", () => {
  // Grabbing the wheel: the agent's options don't fit, so the user types their
  // own answer. It is a first-class answer, not an error.
  const payload = buildAskUserQuestionAnswerPayload(
    [Q("Which DB?", [{ label: "Postgres" }])],
    {},
    { "Which DB?": "actually, DynamoDB" },
  );
  expect(payload.answers).toEqual({ "Which DB?": [SOMETHING_ELSE_LABEL] });
  expect(payload.annotations).toEqual({
    "Which DB?": { notes: "actually, DynamoDB" },
  });
});

test("multi-question sets answer every question, passing the untouched ones", () => {
  const payload = buildAskUserQuestionAnswerPayload(
    [
      Q("Which DB?", [{ label: "Postgres" }]),
      Q("Region?", [{ label: "us-east" }, { label: "eu-west" }]),
    ],
    { "Region?": ["eu-west"] },
    {},
  );
  expect(payload.answers).toEqual({
    "Which DB?": [SOMETHING_ELSE_LABEL],
    "Region?": ["eu-west"],
  });
});
