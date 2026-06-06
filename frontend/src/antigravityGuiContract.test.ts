import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const reposSource = readFileSync(new URL("./repos.ts", import.meta.url), "utf8");
const workspaceSource = readFileSync(
  new URL("./sessionWorkspace.ts", import.meta.url),
  "utf8",
);

function setLiteralBody(name: string, source: string): string {
  const match = source.match(
    new RegExp(`const ${name}[^=]*= new Set(?:<[^>]+>)?\\(\\[([\\s\\S]*?)\\]\\);`),
  );
  assert.ok(match, `${name} set literal should exist`);
  return match[1]!;
}

test("antigravity_gui keeps the full GUI chat surface contract", () => {
  assert.match(
    appSource,
    /function isDefaultSessionMode[\s\S]*value === "antigravity_gui"/,
  );
  for (const name of ["CHAT_MODES", "SDK_CHAT_MODES", "GUI_ROLLOUT_MODES"]) {
    assert.match(setLiteralBody(name, appSource), /"antigravity_gui"/);
  }
  assert.match(setLiteralBody("REPO_SUPPORTED_MODES", reposSource), /"antigravity_gui"/);
  assert.match(setLiteralBody("WORKSPACE_FILE_MODES", workspaceSource), /"antigravity_gui"/);
});
