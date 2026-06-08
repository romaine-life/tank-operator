import assert from "node:assert/strict";
import { appendFile, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import type { AgyStep } from "./adapters/antigravity.js";
import { TranscriptTailer } from "./transcriptTailer.js";

function transcriptPath(root: string, conversationID: string): string {
  return path.join(
    root,
    "brain",
    conversationID,
    ".system_generated",
    "logs",
    "transcript_full.jsonl",
  );
}

test("transcript tailer starts after the existing file size", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agy-tail-"));
  try {
    const transcript = transcriptPath(root, "conv-main");
    await mkdir(path.dirname(transcript), { recursive: true });
    await writeFile(
      transcript,
      `${JSON.stringify({
        step_index: 1,
        source: "MODEL",
        type: "PLANNER_RESPONSE",
        status: "DONE",
        content: "old",
      })}\n`,
    );

    const tailer = new TranscriptTailer(root);
    await tailer.snapshotExisting();
    const seen: AgyStep[] = [];
    await tailer.drain((step) => {
      seen.push(step);
    });
    assert.equal(seen.length, 0);

    await appendFile(
      transcript,
      `${JSON.stringify({
        step_index: 2,
        source: "MODEL",
        type: "PLANNER_RESPONSE",
        status: "DONE",
        content: "new",
      })}\n`,
    );
    await tailer.drain((step) => {
      seen.push(step);
    });

    assert.equal(seen.length, 1);
    const step = seen[0]!;
    assert.equal(step.content, "new");
    assert.equal(step.conversation_id, "conv-main");
    assert.equal(step.transcript_path, transcript);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("transcript tailer buffers partial appended lines", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agy-tail-"));
  try {
    const transcript = transcriptPath(root, "conv-partial");
    await mkdir(path.dirname(transcript), { recursive: true });
    const tailer = new TranscriptTailer(root);
    await tailer.snapshotExisting();

    const seen: AgyStep[] = [];
    await appendFile(
      transcript,
      '{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE"',
    );
    await tailer.drain((step) => {
      seen.push(step);
    });
    assert.equal(seen.length, 0);

    await appendFile(
      transcript,
      ',"status":"DONE","content":"finished"}\n',
    );
    await tailer.drain((step) => {
      seen.push(step);
    });

    assert.equal(seen.length, 1);
    const step = seen[0]!;
    assert.equal(step.step_index, 3);
    assert.equal(step.content, "finished");
    assert.equal(step.conversation_id, "conv-partial");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
