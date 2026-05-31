import assert from "node:assert/strict";
import { test } from "node:test";

import { recognizeLeadingSkillToken } from "./skillTokenRecognition.ts";

const skills = [{ name: "test" }, { name: "rollout" }];

test("recognizes a leading provider-specific skill token", () => {
  assert.deepEqual(recognizeLeadingSkillToken("$test validate this", skills, "$"), {
    leadingText: "",
    tokenText: "$test",
    skillName: "test",
    trailingText: " validate this",
  });
});

test("preserves leading whitespace while recognizing the token", () => {
  assert.deepEqual(recognizeLeadingSkillToken("  /rollout\nnow", skills, "/"), {
    leadingText: "  ",
    tokenText: "/rollout",
    skillName: "rollout",
    trailingText: "\nnow",
  });
});

test("does not recognize unknown skills or the wrong provider prefix", () => {
  assert.equal(recognizeLeadingSkillToken("$unknown", skills, "$"), null);
  assert.equal(recognizeLeadingSkillToken("/test", skills, "$"), null);
});

test("requires the skill token to end before whitespace or prompt end", () => {
  assert.equal(recognizeLeadingSkillToken("$testing", skills, "$"), null);
  assert.equal(recognizeLeadingSkillToken("please $test", skills, "$"), null);
});
