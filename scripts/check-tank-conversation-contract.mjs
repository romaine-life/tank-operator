#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { createRequire } from "node:module";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const schemaPath = path.join(repoRoot, "schemas", "tank-conversation-event.schema.json");
const fixturePath = path.join(repoRoot, "schemas", "tank-conversation-event.fixtures.json");
const sharedContractPath = path.join(repoRoot, "runner-shared", "conversation.js");
const requireFromFrontend = createRequire(path.join(repoRoot, "frontend", "package.json"));
let Ajv2020;
let addFormats;
try {
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

const failures = [];

// runner-shared/conversation.js is the single source of truth for the TS
// side of the contract. Both runners and the frontend import these arrays
// from here, so verifying just this module covers every TS consumer.
const sharedModule = await import(pathToFileURL(sharedContractPath).href);
for (const [constName, expected] of Object.entries(expectedEnums)) {
  const actual = sharedModule[constName];
  if (!Array.isArray(actual) || actual.some((value) => typeof value !== "string")) {
    failures.push(`runner-shared/conversation.js: missing or invalid ${constName}`);
    continue;
  }
  compareArrays(`runner-shared/conversation.js:${constName}`, actual, expected);
}

// Defence-in-depth: confirm no stray local copies of the contract reintroduce
// drift between languages. If any runner or the frontend grows its own
// TANK_EVENT_TYPES const, the cutover to the shared module has regressed.
const forbiddenLocalConstFiles = [
  "frontend/src/tankConversation.ts",
  "claude-runner/src/conversation.ts",
  "codex-runner/src/conversation.ts",
];
for (const relativePath of forbiddenLocalConstFiles) {
  try {
    await fs.access(path.join(repoRoot, relativePath));
    failures.push(`${relativePath}: must not exist — runner-shared/conversation.js is the single source`);
  } catch {
    // missing — that's the desired state
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
    typeof event.order_key === "string" &&
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

