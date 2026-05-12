#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const schemaPath = path.join(repoRoot, "schemas", "tank-conversation-event.schema.json");
const requireFromFrontend = createRequire(path.join(repoRoot, "frontend", "package.json"));
let ts;
try {
  ts = requireFromFrontend("typescript");
} catch (err) {
  console.error("Unable to load TypeScript from frontend/node_modules.");
  console.error("Run `npm ci --prefix frontend` before this contract check.");
  throw err;
}

const schema = JSON.parse(await fs.readFile(schemaPath, "utf8"));
const expectedEnums = {
  TANK_ACTORS: schemaEnum("actor"),
  TANK_EVENT_SOURCES: schemaEnum("source"),
  TANK_VISIBILITIES: schemaEnum("visibility"),
  TANK_EVENT_TYPES: schemaEnum("type"),
};

const tsContractFiles = [
  "frontend/src/tankConversation.ts",
  "agent-runner/src/conversation.ts",
  "codex-runner/src/conversation.ts",
];

const failures = [];

for (const relativePath of tsContractFiles) {
  const absolutePath = path.join(repoRoot, relativePath);
  const source = await fs.readFile(absolutePath, "utf8");
  const sourceFile = ts.createSourceFile(relativePath, source, ts.ScriptTarget.Latest, true);
  for (const [constName, expected] of Object.entries(expectedEnums)) {
    const actual = stringArrayConst(sourceFile, constName);
    if (!actual) {
      failures.push(`${relativePath}: missing exported ${constName}`);
      continue;
    }
    compareArrays(`${relativePath}:${constName}`, actual, expected);
  }
}

if (failures.length > 0) {
  console.error("Tank conversation contract drift detected:");
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log("Tank conversation TypeScript enums match schemas/tank-conversation-event.schema.json");

function schemaEnum(propertyName) {
  const values = schema?.properties?.[propertyName]?.enum;
  if (!Array.isArray(values) || values.some((value) => typeof value !== "string")) {
    throw new Error(`Schema property ${propertyName} does not define a string enum`);
  }
  return values;
}

function compareArrays(label, actual, expected) {
  if (actual.length !== expected.length) {
    failures.push(`${label}: expected ${expected.length} values, found ${actual.length}`);
  }
  const actualSet = new Set(actual);
  const expectedSet = new Set(expected);
  for (const value of expected) {
    if (!actualSet.has(value)) failures.push(`${label}: missing ${JSON.stringify(value)}`);
  }
  for (const value of actual) {
    if (!expectedSet.has(value)) failures.push(`${label}: extra ${JSON.stringify(value)}`);
  }
  if (actual.length === expected.length && actual.some((value, index) => value !== expected[index])) {
    failures.push(`${label}: values match but order differs from the schema`);
  }
}

function stringArrayConst(sourceFile, constName) {
  let values = null;
  visit(sourceFile);
  return values;

  function visit(node) {
    if (values) return;
    if (!ts.isVariableStatement(node)) {
      ts.forEachChild(node, visit);
      return;
    }
    const isExported = node.modifiers?.some((modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword);
    if (!isExported) return;
    for (const declaration of node.declarationList.declarations) {
      if (!ts.isIdentifier(declaration.name) || declaration.name.text !== constName) continue;
      const initializer = unwrapExpression(declaration.initializer);
      if (!initializer || !ts.isArrayLiteralExpression(initializer)) return;
      const elements = [];
      for (const element of initializer.elements) {
        if (!ts.isStringLiteral(element)) return;
        elements.push(element.text);
      }
      values = elements;
      return;
    }
  }
}

function unwrapExpression(expression) {
  let current = expression;
  while (
    current &&
    (ts.isAsExpression(current) ||
      ts.isSatisfiesExpression?.(current) ||
      ts.isParenthesizedExpression(current))
  ) {
    current = current.expression;
  }
  return current;
}
