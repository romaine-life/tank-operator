import assert from "node:assert/strict";
import { test } from "node:test";

import {
  applyProviderEvent,
  parseProviderRunHistory,
  providerFrameEffects,
} from "./providerEventAdapters.ts";

test("explicitly ignores transient Claude provider frames", () => {
  const entries = applyProviderEvent([], "claude_gui", {
    type: "stream_event",
    event: { type: "content_block_delta" },
    timestamp: "2026-05-12T00:00:00.000Z",
  });

  assert.deepEqual(entries, []);
});

test("extracts legacy Claude usage and active-tool effects behind adapter", () => {
  const usage = { input_tokens: 12, cache_creation_input_tokens: 3 };
  const effects = providerFrameEffects({
    type: "assistant",
    message: {
      usage,
      content: [{ type: "tool_use", id: "toolu_1", name: "Bash" }],
    },
  });

  assert.deepEqual(effects, {
    usage,
    activeTool: { name: "Bash", id: "toolu_1" },
  });
});

test("adapts Codex tool items and surfaces unknown Codex drift explicitly", () => {
  const toolEntries = applyProviderEvent([], "codex_gui", {
    type: "item.completed",
    tank_turn_seq: 7,
    item: {
      id: "cmd-1",
      type: "command_execution",
      command: "npm test",
      aggregated_output: "ok",
    },
    created_at: "2026-05-12T00:00:00.000Z",
  });

  assert.equal(toolEntries[0]?.kind, "tool");
  assert.equal(toolEntries[0]?.toolKind, "shell");
  assert.equal(toolEntries[0]?.toolName, "npm test");
  assert.equal(toolEntries[0]?.toolOutput, "ok");

  const unknownEntries = applyProviderEvent([], "codex_gui", {
    type: "future.codex.event",
    payload: { value: true },
    created_at: "2026-05-12T00:00:00.000Z",
  });

  assert.equal(unknownEntries[0]?.kind, "meta");
  assert.equal(unknownEntries[0]?.meta?.title, "future.codex.event");
});

test("parses legacy provider JSONL through adapter boundary", () => {
  const history = [
    JSON.stringify({
      type: "assistant",
      uuid: "msg-1",
      message: { content: [{ type: "text", text: "hello" }] },
      created_at: "2026-05-12T00:00:00.000Z",
    }),
    "not json",
    JSON.stringify({ type: "stream_event" }),
  ].join("\n");

  const entries = parseProviderRunHistory(history, "claude_gui");

  assert.deepEqual(
    entries.map((entry) => [entry.kind, entry.kind === "message" ? entry.text : ""]),
    [["message", "hello"]],
  );
});
