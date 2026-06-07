import assert from "node:assert/strict";
import test from "node:test";

import { classifyAgyTerminal, modelForAgyTurn } from "./runner.js";

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

test("agy success requires observed transcript steps", () => {
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
    ),
    { kind: "completed" },
  );
});
