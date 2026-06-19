import { test, expect } from "vitest";

import { normalizeSpawnedSessions } from "./spawnedSessions";

test("normalizes a well-formed wire array", () => {
  const out = normalizeSpawnedSessions([
    {
      id: "101",
      name: "Child Work",
      mode: "claude_gui",
      model: "opus",
      repos: ["romaine-life/tank-operator"],
      url: "https://tank.romaine.life/?session=101",
      created_at: "2026-06-19T00:00:00Z",
    },
  ]);
  expect(out).toEqual([
    {
      id: "101",
      name: "Child Work",
      mode: "claude_gui",
      model: "opus",
      repos: ["romaine-life/tank-operator"],
      url: "https://tank.romaine.life/?session=101",
      created_at: "2026-06-19T00:00:00Z",
    },
  ]);
});

test("returns [] for non-array / nullish input", () => {
  expect(normalizeSpawnedSessions(undefined)).toEqual([]);
  expect(normalizeSpawnedSessions(null)).toEqual([]);
  expect(normalizeSpawnedSessions({ id: "1" })).toEqual([]);
  expect(normalizeSpawnedSessions("nope")).toEqual([]);
});

test("drops entries missing the load-bearing id or url so no dead links render", () => {
  const out = normalizeSpawnedSessions([
    { name: "no id", url: "https://tank.romaine.life/?session=1" },
    { id: "2", name: "no url" },
    { id: "3", name: "good", url: "https://tank.romaine.life/?session=3" },
  ]);
  expect(out.map((s) => s.id)).toEqual(["3"]);
});

test("falls back to id for a missing name and omits empty repos", () => {
  const out = normalizeSpawnedSessions([
    { id: "7", url: "https://tank.romaine.life/?session=7", repos: [] },
  ]);
  expect(out[0].name).toBe("7");
  expect(out[0].repos).toBeUndefined();
});
