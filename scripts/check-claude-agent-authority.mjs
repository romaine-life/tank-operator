#!/usr/bin/env node

// Guard the Claude session-pod authority model.
//
// Tank sessions are the trust boundary. Claude subagents must receive generated
// server-level MCP authority from the mounted .mcp.json, not a hand-maintained
// per-tool list that drifts from the configured servers.

import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
const writer = path.join(
  repoRoot,
  "k8s/session-config/write-claude-settings.sh",
);
const bootstrap = path.join(
  repoRoot,
  "k8s/session-config/session-pod-bootstrap.sh",
);
const configMap = path.join(
  repoRoot,
  "k8s/templates/session-configmap.yaml",
);
const launch = path.join(
  repoRoot,
  "k8s/session-config/claude-runner-launch.sh",
);

const bootstrapSource = await fs.readFile(bootstrap, "utf8");
const claudeModeArm = bootstrapSource.match(
  /(?<modes>claude_cli[^\n]+)\n\s*write_claude_settings/,
);
const seededClaudeModes = new Set(
  claudeModeArm?.groups?.modes
    .replace(/\)/g, "")
    .split("|")
    .map((mode) => mode.trim()) ?? [],
);
for (const mode of [
  "claude_cli",
  "claude_gui",
  "claude_secondary_cli",
  "claude_secondary_gui",
]) {
  if (!seededClaudeModes.has(mode)) {
    throw new Error(`session-pod-bootstrap.sh must explicitly seed ${mode}`);
  }
}
if (!bootstrapSource.includes("write-claude-settings.sh")) {
  throw new Error("session-pod-bootstrap.sh must call write-claude-settings.sh");
}
const configMapSource = await fs.readFile(configMap, "utf8");
if (!configMapSource.includes("write-claude-settings.sh")) {
  throw new Error("session ConfigMap must mount write-claude-settings.sh");
}
const launchSource = await fs.readFile(launch, "utf8");
if (!launchSource.includes("write-claude-settings.sh")) {
  throw new Error("claude-runner-launch.sh must call write-claude-settings.sh");
}
if (/Bash\(.+\)/.test(launchSource)) {
  throw new Error("claude-runner-launch.sh must not carry per-command Bash rules");
}

const tmp = await fs.mkdtemp(path.join(os.tmpdir(), "tank-claude-authority-"));
try {
  const mcpConfig = path.join(tmp, ".mcp.json");
  const settingsPath = path.join(tmp, "settings.json");
  await fs.writeFile(
    mcpConfig,
    JSON.stringify({
      mcpServers: {
        github: { type: "http", url: "http://github.invalid/mcp" },
        glimmung: { type: "http", url: "http://glimmung.invalid/mcp" },
        "tank-operator": {
          type: "http",
          url: "http://tank-operator.invalid/mcp",
        },
      },
    }),
  );

  const result = spawnSync("sh", [writer, settingsPath], {
    env: { ...process.env, MCP_CONFIG: mcpConfig },
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(
      `settings writer failed (${result.status}): ${result.stderr || result.stdout}`,
    );
  }

  const settings = JSON.parse(await fs.readFile(settingsPath, "utf8"));
  const allow = settings?.permissions?.allow;
  if (!Array.isArray(allow)) {
    throw new Error("settings.permissions.allow is not an array");
  }

  for (const rule of ["mcp__github", "mcp__glimmung", "mcp__tank-operator"]) {
    if (!allow.includes(rule)) {
      throw new Error(`missing generated server-level MCP rule ${rule}`);
    }
  }
  for (const rule of allow) {
    if (/^mcp__[^_]+__/.test(rule)) {
      throw new Error(`per-tool MCP allow rule is forbidden: ${rule}`);
    }
    if (/^Bash\(.+\)$/.test(rule)) {
      throw new Error(`per-command Bash allow rule is forbidden: ${rule}`);
    }
  }
  if (!allow.includes("Bash")) {
    throw new Error("expected blanket Bash authority inside the session pod");
  }
  if (settings.permissions.defaultMode !== "bypassPermissions") {
    throw new Error("Claude defaultMode must stay bypassPermissions");
  }
} finally {
  await fs.rm(tmp, { recursive: true, force: true });
}

console.log("Claude agent authority guard passed.");
