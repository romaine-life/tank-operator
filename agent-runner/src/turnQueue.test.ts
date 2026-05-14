import { test } from "node:test";
import assert from "node:assert/strict";

import {
  buildClaimedRecord,
  buildDelayedTurnRecord,
  claimAttemptsExceeded,
  isInputReplyRecord,
  isInterruptRecord,
  type TurnRecord,
} from "./turnQueue.js";

function baseRecord(overrides: Partial<TurnRecord> = {}): TurnRecord {
  return {
    id: "turn:client-1",
    turn_id: "client-1",
    session_id: "63",
    email: "nelson@romaine.life",
    provider: "claude",
    source: "sdk",
    client_nonce: "client-1",
    prompt: "hello",
    status: "pending",
    created_at: "2026-05-12T10:00:00.000Z",
    ...overrides,
  };
}

test("claim stamps lease ownership and increments attempts", () => {
  const claimed = buildClaimedRecord(baseRecord({ attempt_count: 2 }), {
    claimID: "claim-1",
    claimedBy: "runner-1",
    now: new Date("2026-05-12T10:00:00.000Z"),
    leaseMs: 120_000,
  });

  assert.equal(claimed.status, "claimed");
  assert.equal(claimed.claim_id, "claim-1");
  assert.equal(claimed.claimed_by, "runner-1");
  assert.equal(claimed.claimed_at, "2026-05-12T10:00:00.000Z");
  assert.equal(claimed.claim_expires_at, "2026-05-12T10:02:00.000Z");
  assert.equal(claimed.attempt_count, 3);
});

test("attempt cap is evaluated after a claim attempt increments the row", () => {
  assert.equal(claimAttemptsExceeded(baseRecord({ attempt_count: 3 }), 3), false);
  assert.equal(claimAttemptsExceeded(baseRecord({ attempt_count: 4 }), 3), true);
});

test("schedule wakeups are durable delayed queue rows", () => {
  const record = buildDelayedTurnRecord({
    sessionID: "63",
    email: "nelson@romaine.life",
    provider: "claude",
    prompt: "continue",
    clientNonce: "schedule_wakeup-1",
    availableAt: "2026-05-12T10:05:00.000Z",
    now: new Date("2026-05-12T10:00:00.000Z"),
  });

  assert.equal(record.id, "turn:schedule_wakeup-1");
  assert.equal(record.source, "schedule-wakeup");
  assert.equal(record.status, "pending");
  assert.equal(record.available_at, "2026-05-12T10:05:00.000Z");
  assert.equal(record.attempt_count, 0);
});

test("interrupt rows are explicit control records", () => {
  assert.equal(isInterruptRecord(baseRecord({ source: "interrupt", target_turn_id: "client-1" })), true);
  assert.equal(isInterruptRecord(baseRecord({ source: "sdk" })), false);
});

test("input reply rows are explicit control records", () => {
  assert.equal(
    isInputReplyRecord(baseRecord({ source: "input-reply", target_provider_item_id: "toolu_1" })),
    true,
  );
  assert.equal(isInputReplyRecord(baseRecord({ source: "sdk" })), false);
});
