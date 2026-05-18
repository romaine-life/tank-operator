import { test } from "node:test";
import assert from "node:assert/strict";

import { readClaudeCliVersion } from "./cliVersion.js";

test("readClaudeCliVersion extracts the semver token from `claude --version` output", () => {
  // Format observed on session pods today: "2.1.143 (Claude Code)\n".
  // The product-name suffix is stripped because the log line only needs
  // the version for "is this in the affected range?" lookups.
  const got = readClaudeCliVersion(() => "2.1.143 (Claude Code)\n");
  assert.equal(got, "2.1.143");
});

test("readClaudeCliVersion returns null when `claude` is not on PATH", () => {
  // Defensive fallback: a malformed session image, a bash sandbox that
  // hides the binary, or an environment where `claude --version` exits
  // non-zero must not crash the runner. Logging `claude_cli_version:null`
  // is the right signal — it tells the operator "the binary was reachable
  // by SDK but not introspectable" without taking the pod down.
  const got = readClaudeCliVersion(() => {
    throw new Error("ENOENT");
  });
  assert.equal(got, null);
});

test("readClaudeCliVersion returns null when the subprocess prints nothing", () => {
  // A future CLI release that changes --version output (or a Windows path
  // where the command silently returns empty) shouldn't write the empty
  // string into the log line; null is more honest.
  const got = readClaudeCliVersion(() => "");
  assert.equal(got, null);
});

test("readClaudeCliVersion tolerates leading/trailing whitespace", () => {
  const got = readClaudeCliVersion(() => "   2.1.143 (Claude Code)   \n");
  assert.equal(got, "2.1.143");
});
