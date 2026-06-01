import { readFileSync } from "node:fs";
import { join } from "node:path";
import assert from "node:assert/strict";
import { test } from "node:test";

const chatComposerSource = readFileSync(
  join(import.meta.dirname, "ChatComposer.tsx"),
  "utf8",
);

test("chat composer textarea changes drive the mirrored skill-token state", () => {
  assert.match(
    chatComposerSource,
    /const syncComposerTextFromTextarea = useCallback\(/,
  );
  assert.match(
    chatComposerSource,
    /onInput=\{\(e\) => \{[\s\S]*?syncComposerTextFromTextarea\(target as HTMLTextAreaElement\);[\s\S]*?\}\}/,
  );
  assert.match(
    chatComposerSource,
    /<PromptInputTextarea[\s\S]*?onChange=\{\(event\) => \{[\s\S]*?syncComposerTextFromTextarea\(event\.currentTarget\);[\s\S]*?\}\}/,
  );
});
