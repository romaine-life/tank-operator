#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const schemaPath = path.join(repoRoot, "schemas", "tank-conversation-event.schema.json");
const fixturePath = path.join(repoRoot, "schemas", "tank-conversation-event.fixtures.json");
const requireFromFrontend = createRequire(path.join(repoRoot, "frontend", "package.json"));
let ts;
let Ajv2020;
let addFormats;
try {
  ts = requireFromFrontend("typescript");
  Ajv2020 = requireDefault(requireFromFrontend("ajv/dist/2020"));
  addFormats = requireDefault(requireFromFrontend("ajv-formats"));
} catch (err) {
  console.error("Unable to load contract check dependencies from frontend/node_modules.");
  console.error("Run `npm ci --prefix frontend` before this contract check.");
  throw err;
}

const schema = JSON.parse(await fs.readFile(schemaPath, "utf8"));
const fixtures = JSON.parse(await fs.readFile(fixturePath, "utf8"));
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

const validatedFixtureCount = validateFixtures();

if (failures.length > 0) {
  console.error("Tank conversation contract drift detected:");
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log(
  `Tank conversation contract matches schemas/tank-conversation-event.schema.json; validated ${validatedFixtureCount} canonical fixtures.`,
);

function requireDefault(module) {
  return module.default ?? module;
}

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

function validateFixtures() {
  const fixtureEvents = fixtures?.events;
  if (!Array.isArray(fixtureEvents)) {
    failures.push("schemas/tank-conversation-event.fixtures.json: missing events array");
    return 0;
  }

  const ajv = new Ajv2020({ allErrors: true, strict: true, strictRequired: false });
  addFormats(ajv);
  const validate = ajv.compile(schema);
  const fixtureTypes = new Set();
  let sawRunnerStampedEvent = false;
  let sawPersistedEvent = false;

  for (const [index, fixture] of fixtureEvents.entries()) {
    const label = fixtureLabel(fixture, index);
    const event = fixture?.event;
    if (!event || typeof event !== "object" || Array.isArray(event)) {
      failures.push(`${label}: missing event object`);
      continue;
    }
    if (typeof event.type === "string") fixtureTypes.add(event.type);
    if (hasRunnerStamps(event)) sawRunnerStampedEvent = true;
    if (hasStorageStamps(event)) sawPersistedEvent = true;
    if (!validate(event)) {
      failures.push(`${label}: ${formatAjvErrors(validate.errors)}`);
    }
  }

  for (const eventType of expectedEnums.TANK_EVENT_TYPES) {
    if (!fixtureTypes.has(eventType)) {
      failures.push(
        `schemas/tank-conversation-event.fixtures.json: missing fixture for ${JSON.stringify(eventType)}`,
      );
    }
  }
  for (const eventType of fixtureTypes) {
    if (!expectedEnums.TANK_EVENT_TYPES.includes(eventType)) {
      failures.push(
        `schemas/tank-conversation-event.fixtures.json: fixture uses unknown type ${JSON.stringify(eventType)}`,
      );
    }
  }
  if (!sawRunnerStampedEvent) {
    failures.push("schemas/tank-conversation-event.fixtures.json: expected at least one runner-stamped fixture");
  }
  if (!sawPersistedEvent) {
    failures.push("schemas/tank-conversation-event.fixtures.json: expected at least one persisted timeline fixture");
  }

  return fixtureEvents.length;
}

function fixtureLabel(fixture, index) {
  const name = typeof fixture?.name === "string" && fixture.name ? fixture.name : `fixture ${index + 1}`;
  return `schemas/tank-conversation-event.fixtures.json:${name}`;
}

function hasRunnerStamps(event) {
  return (
    typeof event.uuid === "string" &&
    Number.isInteger(event.tank_event_seq) &&
    typeof event.tank_order_key === "string" &&
    typeof event.written_at === "string"
  );
}

function hasStorageStamps(event) {
  return (
    typeof event.id === "string" &&
    typeof event.tank_session_id === "string" &&
    typeof event.email === "string" &&
    typeof event.runtime === "string"
  );
}

function formatAjvErrors(errors) {
  if (!errors || errors.length === 0) return "schema validation failed";
  return errors
    .map((error) => {
      const location = error.instancePath || "/";
      const allowed = Array.isArray(error.params?.allowedValues)
        ? `: ${error.params.allowedValues.map((value) => JSON.stringify(value)).join(", ")}`
        : "";
      return `${location} ${error.message}${allowed}`;
    })
    .join("; ");
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
