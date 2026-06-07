import { test } from "node:test";
import assert from "node:assert/strict";

import {
  commandSubject,
  controlSubject,
  eventSubject,
  SessionBus,
} from "./sessionBus.js";

// The antigravity runner rides the same shared JetStream subject + durable
// consumer scheme as the claude/codex runners (runner-shared/sessionBus.js),
// instantiated with the provider token "antigravity" (see sessionEvents.ts).
// These names are a server-immutable wire contract: a drift in the subject or
// the durable consumer name strands an existing session pod's chat (the failure
// class called out in CLAUDE.md → "Migration audit checklist"). Pin them.

const STORAGE_KEY = "tank-operator-slot-3:17";
// base64url tokens the shared scheme derives, matched by the sibling runners'
// suites: "tank-operator-slot-3" and the "17" session id.
const SCOPE_TOKEN = "dGFuay1vcGVyYXRvci1zbG90LTM";
const SESSION_TOKEN = "MTc";

test("antigravity subjects encode scope + session and carry the provider plane", () => {
  assert.equal(eventSubject("17"), "tank.session.ZGVmYXVsdA.MTc.events");
  assert.equal(
    eventSubject(STORAGE_KEY),
    `tank.session.${SCOPE_TOKEN}.${SESSION_TOKEN}.events`,
  );
  assert.equal(
    commandSubject(STORAGE_KEY, "antigravity"),
    `tank.session.${SCOPE_TOKEN}.${SESSION_TOKEN}.commands.antigravity`,
  );
  assert.equal(
    controlSubject(STORAGE_KEY, "antigravity"),
    `tank.session.${SCOPE_TOKEN}.${SESSION_TOKEN}.control.antigravity`,
  );
});

test("antigravity uses scoped, provider-prefixed durable consumer names on two planes", () => {
  const bus = new SessionBus(
    {
      sessionId: "17",
      sessionStorageKey: STORAGE_KEY,
      ownerEmail: "user@example.com",
      natsURL: "nats://example.invalid:4222",
      natsStream: "TANK_SESSION_BUS",
      operatorInternalURL: "",
      operatorTokenPath: "",
    },
    "antigravity",
  ) as unknown as {
    consumerName(): string;
    controlConsumerName(): string;
  };

  assert.equal(
    bus.consumerName(),
    `antigravity_${SCOPE_TOKEN}_${SESSION_TOKEN}`,
  );
  assert.equal(
    bus.controlConsumerName(),
    `antigravity_control_${SCOPE_TOKEN}_${SESSION_TOKEN}`,
  );

  // The data-plane and control-plane consumers must stay distinct durables —
  // folding them together restores the "Stop doesn't interrupt deep tool-use
  // loops" regression that PR #511 fixed.
  assert.notEqual(bus.consumerName(), bus.controlConsumerName());

  // Guard against the pre-scoping shape where the raw "scope:session" storage
  // key was used unencoded.
  const legacyToken = "dGFuay1vcGVyYXRvci1zbG90LTM6MTc";
  assert.notEqual(bus.consumerName(), `antigravity_${legacyToken}`);
});
