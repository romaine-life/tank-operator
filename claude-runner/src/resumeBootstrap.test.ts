import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { runResumeBootstrap, type ResumeOutcome } from "./resumeBootstrap.js";
import type { ResumeTranscript } from "../../runner-shared/transcriptDownload.js";

async function withHome(fn: (home: string) => Promise<void>): Promise<void> {
  const home = await mkdtemp(join(tmpdir(), "tank-resume-"));
  try {
    await fn(home);
  } finally {
    await rm(home, { recursive: true, force: true });
  }
}

const REL = ".claude/projects/-workspace/sess-xyz.jsonl";

function transcript(over: Partial<ResumeTranscript> = {}): ResumeTranscript {
  return {
    sdkSessionId: "sess-xyz",
    relPath: REL,
    sdkVersion: "1.0.0",
    bytes: Buffer.from('{"type":"system"}\n{"type":"user"}\n'),
    ...over,
  };
}

test("materializes the transcript at HOME/relPath and returns the sdk session id", async () => {
  await withHome(async (home) => {
    const outcomes: ResumeOutcome[] = [];
    const id = await runResumeBootstrap({
      homeDir: home,
      runningSdkVersion: "1.0.0",
      fetchTranscript: async () => transcript(),
      onOutcome: (o) => outcomes.push(o),
      log: () => {},
    });
    assert.equal(id, "sess-xyz");
    assert.deepEqual(outcomes, ["materialized"]);
    const written = await readFile(join(home, REL), "utf8");
    assert.match(written, /"type":"system"/);
  });
});

test("returns undefined when there is nothing to resume", async () => {
  await withHome(async (home) => {
    const outcomes: ResumeOutcome[] = [];
    const id = await runResumeBootstrap({
      homeDir: home,
      runningSdkVersion: "1.0.0",
      fetchTranscript: async () => null,
      onOutcome: (o) => outcomes.push(o),
      log: () => {},
    });
    assert.equal(id, undefined);
    assert.deepEqual(outcomes, ["not_found"]);
  });
});

test("refuses to resume across an SDK version mismatch and writes nothing", async () => {
  await withHome(async (home) => {
    const outcomes: ResumeOutcome[] = [];
    const id = await runResumeBootstrap({
      homeDir: home,
      runningSdkVersion: "2.0.0",
      fetchTranscript: async () => transcript({ sdkVersion: "1.0.0" }),
      onOutcome: (o) => outcomes.push(o),
      log: () => {},
    });
    assert.equal(id, undefined);
    assert.deepEqual(outcomes, ["version_mismatch"]);
    await assert.rejects(stat(join(home, REL)), "no file should be written on mismatch");
  });
});

test("still resumes when the captured version is unknown", async () => {
  await withHome(async (home) => {
    const id = await runResumeBootstrap({
      homeDir: home,
      runningSdkVersion: "2.0.0",
      fetchTranscript: async () => transcript({ sdkVersion: "" }),
      log: () => {},
    });
    assert.equal(id, "sess-xyz");
  });
});

test("rejects an unsafe rel-path that would escape HOME", async () => {
  await withHome(async (home) => {
    const outcomes: ResumeOutcome[] = [];
    const id = await runResumeBootstrap({
      homeDir: home,
      runningSdkVersion: "1.0.0",
      fetchTranscript: async () => transcript({ relPath: "../../etc/evil.jsonl" }),
      onOutcome: (o) => outcomes.push(o),
      log: () => {},
    });
    assert.equal(id, undefined);
    assert.deepEqual(outcomes, ["error"]);
  });
});

test("a fetch failure starts fresh, never throws", async () => {
  await withHome(async (home) => {
    const outcomes: ResumeOutcome[] = [];
    const id = await runResumeBootstrap({
      homeDir: home,
      runningSdkVersion: "1.0.0",
      fetchTranscript: async () => {
        throw new Error("network down");
      },
      onOutcome: (o) => outcomes.push(o),
      log: () => {},
    });
    assert.equal(id, undefined);
    assert.deepEqual(outcomes, ["error"]);
  });
});
