import assert from "node:assert/strict";
import test from "node:test";

import { normalizeTranscriptCopyText } from "./transcriptCopy";

test("removes trailing blank lines from transcript selections", () => {
  assert.equal(
    normalizeTranscriptCopyText("can you web fetch on the most commonly consumed pastries\n\n\n\n"),
    "can you web fetch on the most commonly consumed pastries",
  );
});

test("preserves internal blank lines while trimming the copied tail", () => {
  assert.equal(
    normalizeTranscriptCopyText("first paragraph\n\nsecond paragraph\n\n"),
    "first paragraph\n\nsecond paragraph",
  );
});

test("handles CRLF selection padding", () => {
  assert.equal(
    normalizeTranscriptCopyText("line one\r\n\r\n\t \r\n"),
    "line one",
  );
});

test("leaves text without trailing line breaks unchanged", () => {
  assert.equal(normalizeTranscriptCopyText("line with trailing spaces  "), "line with trailing spaces  ");
});
