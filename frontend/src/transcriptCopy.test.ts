import { test, expect } from "vitest";

import { normalizeTranscriptCopyText } from "./transcriptCopy";

test("removes trailing blank lines from transcript selections", () => {
  expect(normalizeTranscriptCopyText("can you web fetch on the most commonly consumed pastries\n\n\n\n")).toBe("can you web fetch on the most commonly consumed pastries");
});

test("preserves internal blank lines while trimming the copied tail", () => {
  expect(normalizeTranscriptCopyText("first paragraph\n\nsecond paragraph\n\n")).toBe("first paragraph\n\nsecond paragraph");
});

test("handles CRLF selection padding", () => {
  expect(normalizeTranscriptCopyText("line one\r\n\r\n\t \r\n")).toBe("line one");
});

test("leaves text without trailing line breaks unchanged", () => {
  expect(normalizeTranscriptCopyText("line with trailing spaces  ")).toBe("line with trailing spaces  ");
});
