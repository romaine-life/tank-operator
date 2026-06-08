import assert from "node:assert/strict";
import { access, chmod, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { AgyDriver } from "./driver.js";

async function exists(file: string): Promise<boolean> {
  try {
    await access(file);
    return true;
  } catch {
    return false;
  }
}

test("driver drains transcript writes from filesystem events before process exit", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agy-driver-"));
  try {
    const agyHome = path.join(root, "agy-home");
    const marker = path.join(root, "process-exiting");
    const fakeAgy = path.join(root, "fake-agy.js");
    await writeFile(
      fakeAgy,
      [
        "#!/usr/bin/env node",
        'const fs = require("node:fs/promises");',
        'const path = require("node:path");',
        `const agyHome = ${JSON.stringify(agyHome)};`,
        `const marker = ${JSON.stringify(marker)};`,
        "const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));",
        "(async () => {",
        '  const dir = path.join(agyHome, "brain", "conv-event", ".system_generated", "logs");',
        "  await fs.mkdir(dir, { recursive: true });",
        "  await sleep(100);",
        '  await fs.appendFile(path.join(dir, "transcript_full.jsonl"), JSON.stringify({',
        "    step_index: 1,",
        '    source: "MODEL",',
        '    type: "PLANNER_RESPONSE",',
        '    status: "DONE",',
        '    content: "event-driven",',
        "  }) + \"\\n\");",
        "  await sleep(700);",
        '  await fs.writeFile(marker, "exiting\\n");',
        "})().catch((err) => {",
        "  console.error(err);",
        "  process.exitCode = 1;",
        "});",
        "",
      ].join("\n"),
    );
    await chmod(fakeAgy, 0o755);

    let sawStepBeforeExitMarker = false;
    const eventSourceResults: string[] = [];
    const driver = new AgyDriver(agyHome, fakeAgy, {
      recordTranscriptEventSource: (result) => {
        eventSourceResults.push(result);
      },
    });
    const result = await driver.runTurn(
      {
        prompt: "go",
        resume: false,
        workspace: root,
      },
      async (step) => {
        assert.equal(step.content, "event-driven");
        sawStepBeforeExitMarker = !(await exists(marker));
      },
      new AbortController().signal,
    );

    assert.equal(result.exitCode, 0);
    assert.equal(sawStepBeforeExitMarker, true);
    assert.deepEqual(eventSourceResults, ["started"]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
