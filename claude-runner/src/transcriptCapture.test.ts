import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { TranscriptCapture, type CaptureResult } from "./transcriptCapture.js";
import type { TranscriptSnapshot } from "../../runner-shared/transcriptUpload.js";

interface Harness {
  home: string;
  projectsRoot: string;
  cleanup(): Promise<void>;
}

async function makeHarness(): Promise<Harness> {
  const home = await mkdtemp(join(tmpdir(), "tank-transcript-"));
  const projectsRoot = join(home, ".claude", "projects");
  await mkdir(projectsRoot, { recursive: true });
  return { home, projectsRoot, cleanup: () => rm(home, { recursive: true, force: true }) };
}

function recorder() {
  const snaps: TranscriptSnapshot[] = [];
  const results: CaptureResult[] = [];
  let lag = -1;
  return {
    snaps,
    results,
    lagMs: () => lag,
    onResult: (r: CaptureResult) => results.push(r),
    setLagMs: (ms: number) => {
      lag = ms;
    },
  };
}

test("uploads a new transcript with correct snapshot fields and records ok", async () => {
  const h = await makeHarness();
  try {
    const dir = join(h.projectsRoot, "-workspace");
    await mkdir(dir, { recursive: true });
    await writeFile(join(dir, "sess-abc.jsonl"), '{"type":"system"}\n');

    const rec = recorder();
    const cap = new TranscriptCapture({
      projectsRoot: h.projectsRoot,
      homeDir: h.home,
      sdkVersion: "9.9.9",
      upload: async (snap) => {
        rec.snaps.push(snap);
        return true;
      },
      onResult: rec.onResult,
      setLagMs: rec.setLagMs,
      now: () => 1_000_000,
      log: () => {},
    });
    await cap.scanOnce();

    assert.equal(rec.snaps.length, 1);
    const snap = rec.snaps[0]!;
    assert.equal(snap.sdkSessionId, "sess-abc");
    assert.equal(snap.relPath, ".claude/projects/-workspace/sess-abc.jsonl");
    assert.equal(snap.sdkVersion, "9.9.9");
    assert.equal(Buffer.from(snap.bytes).toString(), '{"type":"system"}\n');
    assert.deepEqual(rec.results, ["ok"]);
    assert.ok(rec.lagMs() >= 0);
  } finally {
    await h.cleanup();
  }
});

test("does not re-upload an unchanged file across scans", async () => {
  const h = await makeHarness();
  try {
    const dir = join(h.projectsRoot, "-workspace");
    await mkdir(dir, { recursive: true });
    await writeFile(join(dir, "s.jsonl"), "a\n");

    const rec = recorder();
    const cap = new TranscriptCapture({
      projectsRoot: h.projectsRoot,
      homeDir: h.home,
      sdkVersion: "",
      upload: async (snap) => {
        rec.snaps.push(snap);
        return true;
      },
      onResult: rec.onResult,
      log: () => {},
    });
    await cap.scanOnce();
    await cap.scanOnce();
    await cap.scanOnce();

    assert.equal(rec.snaps.length, 1, "unchanged file uploaded exactly once");
  } finally {
    await h.cleanup();
  }
});

test("re-uploads after the file content changes", async () => {
  const h = await makeHarness();
  try {
    const dir = join(h.projectsRoot, "-workspace");
    await mkdir(dir, { recursive: true });
    const file = join(dir, "s.jsonl");
    await writeFile(file, "one\n");

    const rec = recorder();
    const cap = new TranscriptCapture({
      projectsRoot: h.projectsRoot,
      homeDir: h.home,
      sdkVersion: "",
      upload: async (snap) => {
        rec.snaps.push(snap);
        return true;
      },
      onResult: rec.onResult,
      log: () => {},
    });
    await cap.scanOnce();
    // Grow the file; mtime+size both change, so the signature differs.
    await writeFile(file, "one\ntwo\n");
    await cap.scanOnce();

    assert.equal(rec.snaps.length, 2);
    assert.equal(Buffer.from(rec.snaps[1]!.bytes).toString(), "one\ntwo\n");
  } finally {
    await h.cleanup();
  }
});

test("skipped upload (storage unconfigured) does not advance the cursor and retries", async () => {
  const h = await makeHarness();
  try {
    const dir = join(h.projectsRoot, "-workspace");
    await mkdir(dir, { recursive: true });
    await writeFile(join(dir, "s.jsonl"), "x\n");

    const rec = recorder();
    let configured = false;
    const cap = new TranscriptCapture({
      projectsRoot: h.projectsRoot,
      homeDir: h.home,
      sdkVersion: "",
      upload: async (snap) => {
        if (!configured) return false; // simulate 503 / not configured
        rec.snaps.push(snap);
        return true;
      },
      onResult: rec.onResult,
      log: () => {},
    });
    await cap.scanOnce(); // skipped
    assert.equal(rec.snaps.length, 0);
    assert.deepEqual(rec.results, ["skipped"]);

    configured = true;
    await cap.scanOnce(); // now stored, because cursor was not advanced
    assert.equal(rec.snaps.length, 1);
    assert.deepEqual(rec.results, ["skipped", "ok"]);
  } finally {
    await h.cleanup();
  }
});

test("upload error is counted and retried, never thrown", async () => {
  const h = await makeHarness();
  try {
    const dir = join(h.projectsRoot, "-workspace");
    await mkdir(dir, { recursive: true });
    await writeFile(join(dir, "s.jsonl"), "x\n");

    const rec = recorder();
    let fail = true;
    const cap = new TranscriptCapture({
      projectsRoot: h.projectsRoot,
      homeDir: h.home,
      sdkVersion: "",
      upload: async (snap) => {
        if (fail) throw new Error("boom");
        rec.snaps.push(snap);
        return true;
      },
      onResult: rec.onResult,
      log: () => {},
    });
    await cap.scanOnce(); // error, swallowed
    assert.deepEqual(rec.results, ["error"]);

    fail = false;
    await cap.scanOnce(); // retried, stored
    assert.equal(rec.snaps.length, 1);
    assert.deepEqual(rec.results, ["error", "ok"]);
  } finally {
    await h.cleanup();
  }
});

test("captures multiple jsonl files across subdirs", async () => {
  const h = await makeHarness();
  try {
    const a = join(h.projectsRoot, "-workspace");
    const b = join(h.projectsRoot, "-other");
    await mkdir(a, { recursive: true });
    await mkdir(b, { recursive: true });
    await writeFile(join(a, "s1.jsonl"), "1\n");
    await writeFile(join(a, "s2.jsonl"), "2\n");
    await writeFile(join(b, "s3.jsonl"), "3\n");
    await writeFile(join(a, "ignore.txt"), "nope\n");

    const ids: string[] = [];
    const cap = new TranscriptCapture({
      projectsRoot: h.projectsRoot,
      homeDir: h.home,
      sdkVersion: "",
      upload: async (snap) => {
        ids.push(snap.sdkSessionId);
        return true;
      },
      log: () => {},
    });
    await cap.scanOnce();

    assert.deepEqual([...ids].sort(), ["s1", "s2", "s3"]);
  } finally {
    await h.cleanup();
  }
});
