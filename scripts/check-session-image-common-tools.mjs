#!/usr/bin/env node
import { readFileSync } from "node:fs";

const files = {
  versions: "session-images/versions.env",
  alpine: "session-images/install-common-alpine.sh",
  debian: "session-images/install-common-debian.sh",
  claudeDockerfile: "claude-container/Dockerfile",
  antigravityDockerfile: "antigravity-container/Dockerfile",
  sessionImagesWorkflow: ".github/workflows/session-images-build.yml",
};

const read = (path) => readFileSync(path, "utf8");
const fail = (message) => {
  console.error(message);
  process.exitCode = 1;
};

const versions = read(files.versions);
const alpine = read(files.alpine);
const debian = read(files.debian);
const claudeDockerfile = read(files.claudeDockerfile);
const antigravityDockerfile = read(files.antigravityDockerfile);
const sessionImagesWorkflow = read(files.sessionImagesWorkflow);

const versionVars = [
  "KUBECTL_VERSION",
  "TOFU_VERSION",
  "HELM_VERSION",
  "YQ_VERSION",
  "UV_VERSION",
  "TAILSCALE_VERSION",
  "RUFF_VERSION",
  "PYTEST_VERSION",
  "GO_VERSION",
  "VITE_VERSION",
  "TYPESCRIPT_VERSION",
  "SANDBOX_AGENT_VERSION",
];

for (const name of versionVars) {
  if (!new RegExp(`^${name}=\\S+`, "m").test(versions)) {
    fail(`${files.versions} is missing ${name}`);
  }
  for (const [label, body] of [
    [files.alpine, alpine],
    [files.debian, debian],
  ]) {
    if (!body.includes(name)) {
      fail(`${label} does not consume ${name}`);
    }
  }
}

const commonToolTokens = [
  "bash",
  "ca-certificates",
  "curl",
  "git",
  "jq",
  "less",
  "make",
  "openssh-client",
  "python3",
  "ripgrep",
  "unzip",
  "vim",
  "go.dev/dl/go",
  "/kubectl",
  "/tofu",
  "/helm",
  "yq_linux",
  "/uv",
  "/uvx",
  "/tailscale",
  "/tailscaled",
  "@sandbox-agent/cli",
  "vite@",
  "typescript@",
  "pytest==",
  "ruff==",
];

for (const token of commonToolTokens) {
  if (!alpine.includes(token)) {
    fail(`${files.alpine} is missing common tool token ${token}`);
  }
  if (!debian.includes(token)) {
    fail(`${files.debian} is missing common tool token ${token}`);
  }
}

if (!alpine.includes("github-cli")) {
  fail(`${files.alpine} must install the GitHub CLI`);
}
if (!debian.includes("apt-get install -y --no-install-recommends gh")) {
  fail(`${files.debian} must install the GitHub CLI`);
}

const dockerfileChecks = [
  [files.claudeDockerfile, claudeDockerfile, "install-common-alpine.sh"],
  [files.antigravityDockerfile, antigravityDockerfile, "install-common-debian.sh"],
];
for (const [path, body, installer] of dockerfileChecks) {
  if (!body.includes("COPY session-images /opt/tank/session-images")) {
    fail(`${path} does not copy the shared session image baseline`);
  }
  if (!body.includes(installer)) {
    fail(`${path} does not run ${installer}`);
  }
}

for (const staleArg of versionVars.filter((name) => name !== "SANDBOX_AGENT_VERSION")) {
  if (new RegExp(`ARG ${staleArg}\\b`).test(claudeDockerfile)) {
    fail(`${files.claudeDockerfile} should not define ${staleArg}; use ${files.versions}`);
  }
  if (new RegExp(`ARG ${staleArg}\\b`).test(antigravityDockerfile)) {
    fail(`${files.antigravityDockerfile} should not define ${staleArg}; use ${files.versions}`);
  }
}

const fingerprintRows = sessionImagesWorkflow
  .split("\n")
  .filter((line) => line.includes("fingerprint_paths:"));
if (fingerprintRows.length !== 3) {
  fail(`${files.sessionImagesWorkflow} should define three session image fingerprint rows`);
}
for (const line of fingerprintRows) {
  if (!line.includes("session-images")) {
    fail(`${files.sessionImagesWorkflow} fingerprint row is missing session-images: ${line.trim()}`);
  }
}

if (process.exitCode) {
  process.exit(process.exitCode);
}
console.log("session image common tool baseline is wired consistently");
