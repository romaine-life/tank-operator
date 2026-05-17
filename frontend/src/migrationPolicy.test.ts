import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const sessionListEventsSource = readFileSync(
  new URL("./sessionListEvents.ts", import.meta.url),
  "utf8",
);

test("session activity is not refreshed by a steady interval", () => {
  assert.equal(appSource.includes("POLL_INTERVAL_MS"), false);
  assert.equal(/setInterval\(\s*refreshSessionActivity/.test(appSource), false);
});

test("chat transcript UI does not use the retired agent-ws route", () => {
  assert.equal(appSource.includes("agent-ws"), false);
});

test("stop control waits for durable turn interruption", () => {
  const cancelRunMatch = appSource.match(
    /function cancelRun\(\) \{([\s\S]*?)\n  async function requestSdkInterrupt/,
  );
  assert.ok(cancelRunMatch, "cancelRun body should be present");
  const cancelRunBody = cancelRunMatch[1]!;
  assert.equal(cancelRunBody.includes("currentRunRef.current = null"), false);
  assert.equal(
    cancelRunBody.includes('setRunStatus((prev) => (prev === "running" ? "done" : prev))'),
    false,
  );
  assert.equal(cancelRunBody.includes('setRunStatus("stopping")'), true);
  assert.equal(appSource.includes("if (!res.ok)"), true);
});

test("AskUserQuestion replies use durable input-reply turns", () => {
  assert.equal(appSource.includes("sendStdin"), false);
  assert.equal(appSource.includes("/input-reply"), true);
});

// --- chat-tab refactor completion guard (the four fallback paths #489
// retired but left live until now: closingIds local optimism,
// refresh-after-mutation wake-and-refetch, placeholder synthesis on
// pod-state events for unknown sessions, and the Reader.List pod-only
// fallback covered separately in the backend tests + the
// check-removed-chat-runtime guard). These tests are flipped
// PR-#481-style guards — what they assert about the source is exactly
// what scripts/check-removed-chat-runtime.mjs blocks on regex level. ---

test("App does not maintain a closingIds local-optimism set for delete", () => {
  // closingIds was the UI-local Set<string> that disabled the X button,
  // showed a spinner, dimmed the row, and excluded the session from
  // navigation while a DELETE was in flight. Per
  // docs/product-inspirations.md, user-visible run state derives from
  // durable events, not local optimism. Row removal now comes from the
  // SSE session.deleted event; the only in-flight state retained is a
  // ref (`pendingDeletes`) that prevents double-firing the HTTP call
  // and does NOT drive any rendered output.
  assert.equal(/\bsetClosingIds\b/.test(appSource), false, "setClosingIds must not exist");
  assert.equal(/\buseState<Set<string>>\(\(\) => new Set\(\)\)\s*;[\s\S]{0,80}closingIds/.test(appSource), false, "no useState-backed closingIds");
  assert.equal(/\bclosingIds\.has\(/.test(appSource), false, "no render-time closingIds reads");
  assert.equal(/\bisClosing\b/.test(appSource), false, "no isClosing render flag");
});

test("mutation handlers do not call refresh() to reseed sessions", () => {
  // The refresh-after-mutation pattern was the wake-and-refetch shape
  // #489's ledger refactor was supposed to retire. With it gone:
  //   deleteSession  → SSE session.deleted removes the row.
  //   createSession  → POST response optimistically adds the row; SSE
  //                     session.created arrives later and dedupes.
  //   forkSessionFromMessage / glimmung-launch → same shape as create.
  // The check looks at actual code only (line comments stripped) so the
  // explanatory `// No await refresh() — ...` comments in the handlers
  // do not trigger a false positive.
  const code = stripLineComments(appSource);
  for (const handler of [
    "deleteSession",
    "createSession",
    "forkSessionFromMessage",
  ]) {
    const pattern = new RegExp(
      `async function ${handler}\\b[\\s\\S]*?\\n  (?:async )?function `,
    );
    const match = code.match(pattern);
    assert.ok(match, `${handler} body should be present`);
    assert.equal(
      /\bawait\s+refresh\s*\(\s*\)/.test(match[0]),
      false,
      `${handler} must not call await refresh() — SSE owns live updates`,
    );
  }
  // The glimmung-launch path lives inside a useEffect, not a named
  // function, so search by the POST URL it hits.
  const glimmungBlock = code.match(
    /\/api\/sessions\/with-context[\s\S]*?activate\(session\.id\)/,
  );
  assert.ok(glimmungBlock, "glimmung launch block should be present");
  assert.equal(
    /\bawait\s+refresh\s*\(\s*\)/.test(glimmungBlock[0]),
    false,
    "glimmung launch must not call await refresh()",
  );
});

// stripLineComments drops `//`-style line comments from the source so
// migration-policy assertions can scan for retired call shapes without
// false-positives from comments that explain why the call was removed.
// Crude but correct enough for App.tsx — string literals containing `//`
// (URLs etc.) are scanned out of context, but they are not followed by
// `await refresh()` patterns so the assertions stay sound.
function stripLineComments(source: string): string {
  return source.replace(/^([ \t]*)\/\/[^\n]*$/gm, "$1");
}

test("session-list reducer does not synthesize placeholder Sessions on unknown ids", () => {
  // The pre-fix reducer fabricated a Session for any session.pod_*
  // event with an unknown session_id. That branch was the second half
  // of the stuck-deleting bug — session.pod_terminating arriving after
  // session.deleted would resurrect the row indefinitely. Per
  // docs/product-inspirations.md ("Unknown cursors force an explicit
  // resync instead of silently skipping a gap"), the reducer drops the
  // event; the SSE handler's resync_required path is the only legal
  // way to recover a missed session.created.
  assert.equal(
    sessionListEventsSource.includes("[...state.sessions, placeholder]"),
    false,
    "no placeholder-append branch in applyPodStatusEvent",
  );
  // applyPodStatusEvent must not call the factory — the factory is the
  // legitimate constructor for session.created, but invoking it inside
  // a pod-state handler is what produced the resurrection bug.
  const podStatusBody = sessionListEventsSource.match(
    /function applyPodStatusEvent[\s\S]*?\n}\n/,
  );
  assert.ok(podStatusBody, "applyPodStatusEvent body should be present");
  assert.equal(
    /\bfactory\(/.test(podStatusBody[0]),
    false,
    "applyPodStatusEvent must not invoke the session factory",
  );
});
