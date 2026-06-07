import { readFileSync } from "node:fs";
import { test, expect } from "vitest";

const mainSource = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");

test("main.tsx allowlist keeps the splash repo defaults localStorage key alive", () => {
  expect(mainSource).toMatch(/"tank\.homeSelectedRepos"/);
});

test("main.tsx does not allowlist retired local repo pins", () => {
  expect(mainSource).not.toMatch(/"tank\.homePinnedRepos"/);
});

test("main.tsx no longer allowlists retired local tank auth token", () => {
  expect(mainSource).not.toMatch(new RegExp('"tank-operator' + '-jwt"'));
});

test("main.tsx exposes the session-list debug route", () => {
  expect(mainSource).toMatch(/SessionListDebugPage/);
  expect(mainSource).toMatch(/"\/_debug\/session-list"/);
});
