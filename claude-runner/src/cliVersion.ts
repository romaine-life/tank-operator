// readClaudeCliVersion captures the Claude Code CLI version at runner boot
// for inclusion in the claude-runner startup log line. The session image
// installs @anthropic-ai/claude-code from npm without a version pin (see
// claude-container/Dockerfile's `npm install -g @anthropic-ai/claude-code`),
// so the binary version floats with whatever is latest at image-build time.
// Recording it per-session means future debugging — "which CC version was
// running when Monitor hung?", "did the streaming-receive regression
// (anthropics/claude-code#53328) ship before or after this stuck session?" —
// doesn't require cross-referencing the session pod's image fingerprint
// back through a chart-bump commit to a build-log artifact.
//
// `run` is injected so the unit test can substitute a fake without
// spawning a real `claude` subprocess. In production the call is one
// blocking exec at runner boot; the CLI's --version path is fast and
// the cost is paid once per pod.

import { execSync } from "node:child_process";

export function readClaudeCliVersion(
  run: (cmd: string) => string = (cmd) => execSync(cmd, { encoding: "utf8" }),
): string | null {
  try {
    const raw = run("claude --version").trim();
    // `claude --version` emits e.g. "2.1.143 (Claude Code)". The first
    // whitespace-separated token is the semver we want; everything after
    // is the human-readable product name and adds nothing to the log.
    const token = raw.split(/\s+/)[0] ?? "";
    return token || null;
  } catch {
    return null;
  }
}
