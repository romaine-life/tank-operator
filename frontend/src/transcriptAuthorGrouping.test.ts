import assert from "node:assert/strict";
import test from "node:test";

import {
  shouldGroupTranscriptMessageWithPrevious,
  transcriptAuthorGroupKey,
} from "./transcriptAuthorGrouping.ts";

function message(fields: Record<string, unknown>) {
  return {
    id: "msg",
    kind: "message",
    role: "system",
    time: "2026-05-20T10:00:00.000Z",
    text: "Session is loading.",
    ...fields,
  };
}

test("adjacent system status messages share an author group", () => {
  assert.equal(
    shouldGroupTranscriptMessageWithPrevious(
      message({ id: "loading", text: "Session is loading." }),
      message({
        id: "ready",
        time: "2026-05-20T10:00:08.000Z",
        text: "Session is ready.",
      }),
    ),
    true,
  );
});

test("message grouping stops when the author role changes", () => {
  assert.equal(
    shouldGroupTranscriptMessageWithPrevious(
      message({ role: "user", text: "hello" }),
      message({ role: "assistant", text: "hi" }),
    ),
    false,
  );
});

test("system error banners start a new avatar group", () => {
  assert.equal(
    shouldGroupTranscriptMessageWithPrevious(
      message({ severity: "info", text: "Session is ready." }),
      message({
        severity: "error",
        text: "Codex sign-in expired.",
      }),
    ),
    false,
  );
});

test("author groups use a Discord-like short time window", () => {
  assert.equal(
    shouldGroupTranscriptMessageWithPrevious(
      message({ time: "2026-05-20T10:00:00.000Z" }),
      message({ time: "2026-05-20T10:08:01.000Z" }),
    ),
    false,
  );
});

test("origin session keeps handoff user messages separate from human messages", () => {
  assert.notEqual(
    transcriptAuthorGroupKey(message({ role: "user" })),
    transcriptAuthorGroupKey(message({ role: "user", originSessionId: "42" })),
  );
});

test("bot-authored system turns stay out of the human avatar group", () => {
  assert.notEqual(
    transcriptAuthorGroupKey(message({ role: "user" })),
    transcriptAuthorGroupKey(message({ role: "user", authorKind: "system" })),
  );
});

test("origin handoff outranks author_kind for the user group key", () => {
  assert.equal(
    transcriptAuthorGroupKey(
      message({ role: "user", originSessionId: "42", authorKind: "system" }),
    ),
    transcriptAuthorGroupKey(message({ role: "user", originSessionId: "42" })),
  );
});

test("bot-authored user turns do not group with adjacent human turns", () => {
  assert.equal(
    shouldGroupTranscriptMessageWithPrevious(
      message({ role: "user", text: "human turn" }),
      message({
        role: "user",
        authorKind: "system",
        time: "2026-05-20T10:00:05.000Z",
        text: "bot turn",
      }),
    ),
    false,
  );
});

test("consecutive bot-authored system turns share an avatar group", () => {
  assert.equal(
    shouldGroupTranscriptMessageWithPrevious(
      message({ role: "user", authorKind: "system", text: "first bot turn" }),
      message({
        role: "user",
        authorKind: "system",
        time: "2026-05-20T10:00:05.000Z",
        text: "second bot turn",
      }),
    ),
    true,
  );
});
