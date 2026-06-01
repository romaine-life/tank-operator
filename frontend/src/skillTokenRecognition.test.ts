import assert from "node:assert/strict";
import { test } from "node:test";

import { recognizeLeadingSkillToken } from "./skillTokenRecognition.ts";

const skills = [
  { name: "test", tokenText: "$test" },
  { name: "test", tokenText: "/test" },
  { name: "rollout", tokenText: "/rollout" },
  { name: "clear", tokenText: "/clear" },
];

test("recognizes a leading skill token by exact invocation text", () => {
  assert.deepEqual(recognizeLeadingSkillToken("$test validate this", skills), {
    leadingText: "",
    tokenText: "$test",
    skillName: "test",
    trailingText: " validate this",
  });
});

test("preserves leading whitespace while recognizing the token", () => {
  assert.deepEqual(recognizeLeadingSkillToken("  /rollout\nnow", skills), {
    leadingText: "  ",
    tokenText: "/rollout",
    skillName: "rollout",
    trailingText: "\nnow",
  });
});

test("recognizes built-in slash command candidates", () => {
  assert.deepEqual(recognizeLeadingSkillToken("/clear", skills), {
    leadingText: "",
    tokenText: "/clear",
    skillName: "clear",
    trailingText: "",
  });
});

test("does not recognize unknown or unavailable invocation tokens", () => {
  assert.equal(recognizeLeadingSkillToken("$unknown", skills), null);
  assert.equal(
    recognizeLeadingSkillToken("/missing", [{ name: "test", tokenText: "$test" }]),
    null,
  );
});

test("requires the skill token to end before whitespace or prompt end", () => {
  assert.equal(recognizeLeadingSkillToken("$testing", skills), null);
  assert.equal(recognizeLeadingSkillToken("please $test", skills), null);
});
