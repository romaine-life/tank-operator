import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const mainSource = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");

test("main.tsx allowlist keeps the splash repo defaults localStorage key alive", () => {
  assert.match(mainSource, /"tank\.homeSelectedRepos"/);
});
