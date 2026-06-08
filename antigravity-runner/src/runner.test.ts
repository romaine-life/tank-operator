import assert from "node:assert/strict";
import test from "node:test";

import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import { agyDiagnostics, classifyAgyTerminal, modelForAgyTurn } from "./runner.js";
import { expandSkillPrompt, stripSkillTrigger } from "./skills.js";

test("agy turns require a concrete model", () => {
  assert.equal(
    modelForAgyTurn(" Gemini 3.5 Flash (Medium) "),
    "Gemini 3.5 Flash (Medium)",
  );
  assert.equal(modelForAgyTurn(""), null);
  assert.equal(modelForAgyTurn(undefined), null);
});

test("zero-step agy timeout is a durable failure, not completion", () => {
  const terminal = classifyAgyTerminal(
    {
      exitCode: 0,
      killed: false,
      stdout: "",
      stderr: [
        "failed to construct executor: neither PlanModel nor RequestedModel specified.",
        "Print mode: timed out waiting for cascade to start running",
      ].join("\n"),
    },
    0,
    false,
  );

  assert.deepEqual(terminal.kind, "failed");
  assert.equal(
    terminal.kind === "failed" ? terminal.metricReason : "",
    "provider_start_timeout",
  );
  assert.match(
    terminal.kind === "failed" ? terminal.reason : "",
    /provider_start_timeout/,
  );
});

test("zero-step agy model setup failure is a durable failure", () => {
  const terminal = classifyAgyTerminal(
    {
      exitCode: 0,
      killed: false,
      stdout: "",
      stderr:
        "failed to construct executor: neither PlanModel nor RequestedModel specified.",
    },
    0,
    false,
  );

  assert.deepEqual(terminal.kind, "failed");
  assert.equal(
    terminal.kind === "failed" ? terminal.metricReason : "",
    "provider_model_unavailable",
  );
});

test("zero-step agy auth failure is a durable failure", () => {
  const terminal = classifyAgyTerminal(
    {
      exitCode: 0,
      killed: false,
      stdout: "",
      stderr:
        "Request had invalid authentication credentials. status: UNAUTHENTICATED",
    },
    0,
    false,
  );

  assert.deepEqual(terminal.kind, "failed");
  assert.equal(
    terminal.kind === "failed" ? terminal.metricReason : "",
    "provider_auth_failed",
  );
});

test("nonzero agy exit remains a provider failure", () => {
  const terminal = classifyAgyTerminal(
    {
      exitCode: 2,
      killed: false,
      stdout: "",
      stderr: "fatal provider error",
    },
    0,
    false,
  );

  assert.deepEqual(terminal.kind, "failed");
  assert.equal(
    terminal.kind === "failed" ? terminal.metricReason : "",
    "nonzero_exit",
  );
  assert.match(terminal.kind === "failed" ? terminal.reason : "", /agy exit 2/);
});

test("agy diagnostics classify auxiliary 401s separately from provider failures", () => {
  assert.deepEqual(
    agyDiagnostics({
      stdout: "",
      stderr: [
        "Cache(peopleInfo): Singleflight refresh failed: failed to fetch user info: 401 Unauthorized",
        "Clearcut responded with HTTP code: 401",
      ].join("\n"),
    }),
    ["auxiliary_userinfo_401", "telemetry_clearcut_401"],
  );
});

test("agy executor 500 after tool output is a durable failure", () => {
  const terminal = classifyAgyTerminal(
    {
      exitCode: 0,
      killed: false,
      stdout: "",
      stderr: [
        "agent executor error: UNKNOWN (code 500): Unknown Error.",
        "PlannerResponse without ModifiedResponse encountered",
      ].join("\n"),
    },
    124,
    false,
  );

  assert.deepEqual(terminal.kind, "failed");
  assert.equal(
    terminal.kind === "failed" ? terminal.metricReason : "",
    "provider_executor_error",
  );
  assert.match(
    terminal.kind === "failed" ? terminal.reason : "",
    /provider_executor_error/,
  );
});

test("agy success with tool steps still requires a final answer", () => {
  const terminal = classifyAgyTerminal(
    {
      exitCode: 0,
      killed: false,
      stdout: "",
      stderr: "",
    },
    124,
    false,
    { hasFinalAnswer: false },
  );

  assert.deepEqual(terminal.kind, "failed");
  assert.equal(
    terminal.kind === "failed" ? terminal.metricReason : "",
    "provider_no_final_answer",
  );
});

test("native schedule parking may complete without final answer", () => {
  assert.deepEqual(
    classifyAgyTerminal(
      {
        exitCode: 0,
        killed: false,
        stdout: "",
        stderr: "",
      },
      12,
      false,
      { hasFinalAnswer: false, allowNoFinalAnswer: true },
    ),
    { kind: "completed" },
  );
});

test("Antigravity skill prompt expansion embeds hydrated SKILL.md", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agy-skills-"));
  try {
    const skillDir = path.join(root, "north-star");
    await mkdir(skillDir, { recursive: true });
    await writeFile(path.join(skillDir, "SKILL.md"), "Read the policy docs.\n");
    const expanded = await expandSkillPrompt(
      "$north-star\n\nwhat now?",
      "north-star",
      root,
    );
    assert.equal(expanded.loaded, true);
    assert.match(expanded.prompt, /Use the Tank skill "north-star"/);
    assert.match(expanded.prompt, /Read the policy docs/);
    assert.match(expanded.prompt, /User request:\nwhat now\?/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("Antigravity skill prompt expansion reports missing skills", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agy-skills-"));
  try {
    const expanded = await expandSkillPrompt("$missing\n\nrun", "missing", root);
    assert.equal(expanded.loaded, false);
    assert.equal(expanded.reason, "missing");
    assert.equal(expanded.prompt, "$missing\n\nrun");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("skill trigger stripping handles slash and dollar prefixes", () => {
  assert.equal(stripSkillTrigger("north-star", "$north-star\n\ngo"), "go");
  assert.equal(stripSkillTrigger("north-star", "/north-star go"), "go");
});

test("interrupted agy turn remains interrupted", () => {
  assert.deepEqual(
    classifyAgyTerminal(
      {
        exitCode: 0,
        killed: true,
        stdout: "",
        stderr: "",
      },
      0,
      false,
    ),
    { kind: "interrupted" },
  );
});

test("agy success requires observed transcript steps and a final answer", () => {
  assert.deepEqual(
    classifyAgyTerminal(
      {
        exitCode: 0,
        killed: false,
        stdout: "",
        stderr: "",
      },
      1,
      false,
      { hasFinalAnswer: true },
    ),
    { kind: "completed" },
  );
});
