#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import process from "node:process";

const root = process.cwd();
const join = (...parts) => parts.join("");
const word = (...parts) => new RegExp(`\\b${join(...parts)}\\b`);
const forbidden = [
  { re: word("apply_test_slot_", "hot_swap"), label: "deleted Glimmung apply tool" },
  { re: word("artifact", "_kind"), label: "deleted artifact selector" },
  { re: word("classify-tank-test-", "fid", "elity"), label: "deleted classifier" },
  { re: word("_GLIMMUNG_", "HOT_SWAP", "_TOOL"), label: "deleted proxy tool constant" },
  { re: word("GLIMMUNG_", "HOT_SWAP", "_[A-Z0-9_]+"), label: "deleted proxy env plumbing" },
  { re: word("test_slot_", "hot_swap"), label: "deleted project metadata contract" },
  { re: word("fid", "elity", "_classifier"), label: "deleted classifier contract" },
];

const include = [
  "backend-go",
  "claude-container",
  "docs",
  "frontend",
  "k8s",
  "README.md",
  "scripts",
];
const ignored = new Set(["scripts/check-session-pod-hot-swap-migration.mjs"]);

function* walk(target) {
  const abs = path.join(root, target);
  if (!fs.existsSync(abs)) return;
  const stat = fs.statSync(abs);
  if (stat.isFile()) {
    if (!ignored.has(target)) yield target;
    return;
  }
  for (const entry of fs.readdirSync(abs, { withFileTypes: true })) {
    if (entry.name === ".git" || entry.name === "node_modules" || entry.name === "dist") continue;
    const rel = path.join(target, entry.name);
    if (entry.isDirectory()) {
      yield* walk(rel);
    } else if (!ignored.has(rel)) {
      yield rel;
    }
  }
}

const failures = [];
for (const target of include) {
  for (const rel of walk(target)) {
    const text = fs.readFileSync(path.join(root, rel), "utf8");
    for (const check of forbidden) {
      if (check.re.test(text)) failures.push(`${rel}: ${check.label}`);
    }
  }
}

if (failures.length > 0) {
  console.error("retired test-slot hot-swap surface found:");
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log("retired test-slot hot-swap surface is absent");
