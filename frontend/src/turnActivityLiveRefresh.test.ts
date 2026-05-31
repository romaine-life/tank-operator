import assert from "node:assert/strict";
import { test } from "node:test";

import { turnActivityRefreshTargetsForTranscriptRows } from "./turnActivityLiveRefresh.ts";
import type { TranscriptEntry } from "./App.tsx";

function row(entry: Partial<TranscriptEntry> & { id: string; kind: TranscriptEntry["kind"] }): TranscriptEntry {
  return entry as TranscriptEntry;
}

test("loaded turn activity refreshes when its compacted live shell changes", () => {
  assert.deepEqual(
    turnActivityRefreshTargetsForTranscriptRows(
      [
        row({
          id: "turn-activity-turn-1",
          kind: "turn_activity",
          turnId: "turn-1",
          activity: { turnId: "turn-1", endOrderKey: "20" },
        }),
      ],
      { "turn-1": [] },
    ),
    ["turn-1"],
  );
});

test("unopened turns do not trigger activity body refreshes", () => {
  assert.deepEqual(
    turnActivityRefreshTargetsForTranscriptRows(
      [
        row({
          id: "turn-activity-turn-2",
          kind: "turn_activity",
          turnId: "turn-2",
          activity: { turnId: "turn-2", endOrderKey: "20" },
        }),
      ],
      { "turn-1": [] },
    ),
    [],
  );
});

test("user messages and needs-input announcements do not refresh activity bodies", () => {
  assert.deepEqual(
    turnActivityRefreshTargetsForTranscriptRows(
      [
        row({ id: "turn-1:user", kind: "message", role: "user", turnId: "turn-1" }),
        row({
          id: "turn-1:needs-input",
          kind: "meta",
          metaKind: "needs_input_announcement",
          turnId: "turn-1",
        }),
      ],
      { "turn-1": [] },
    ),
    [],
  );
});

test("deduplicates multiple rows for the same loaded turn", () => {
  assert.deepEqual(
    turnActivityRefreshTargetsForTranscriptRows(
      [
        row({ id: "tool-1", kind: "tool", turnId: "turn-1" }),
        row({ id: "message-1", kind: "message", role: "assistant", turnId: "turn-1" }),
      ],
      { "turn-1": [{ id: "old-tool", kind: "tool", turnId: "turn-1" } as TranscriptEntry] },
    ),
    ["turn-1"],
  );
});
