import { test } from "node:test";
import assert from "node:assert/strict";
import { once } from "node:events";

import {
  recordTurnStart,
  recordTurnTerminal,
  registry,
  scheduledWakeupRegisterTotal,
  startMetricsServer,
} from "./metrics.js";

test("metrics endpoint serves prom-format with tank_runner_ counters", async () => {
  const server = startMetricsServer(0);
  await once(server, "listening");
  const addr = server.address();
  assert.ok(addr && typeof addr === "object", "metrics server bound");
  const port = (addr as { port: number }).port;

  // Drive a counter so the scrape has something to assert.
  scheduledWakeupRegisterTotal.labels("ok").inc();

  try {
    const res = await fetch(`http://127.0.0.1:${port}/metrics`);
    assert.equal(res.status, 200);
    const body = await res.text();
    assert.match(
      body,
      /tank_runner_scheduled_wakeup_register_total\{(?=[^}]*result="ok")(?=[^}]*mode="claude")[^}]*\} 1/,
    );
    assert.match(body, /tank_runner_commands_consumed_total/);
    assert.match(body, /tank_runner_turn_duration_seconds/);
  } finally {
    server.close();
    await once(server, "close");
  }
});

test("recordTurnStart + recordTurnTerminal observes a duration", async () => {
  // Clear any prior observations so this test is deterministic when run
  // alongside other tests in the same process.
  await registry.resetMetrics();

  recordTurnStart("turn-123");
  recordTurnTerminal("turn-123", "completed");

  const dump = await registry.metrics();
  // The histogram's _count series increments by 1 per Observe regardless
  // of value, so this is robust against timing flakiness. Label order
  // is prom-client-internal and not part of our API; the regex matches
  // any order containing outcome="completed" and mode="claude".
  assert.match(
    dump,
    /tank_runner_turn_duration_seconds_count\{(?=[^}]*outcome="completed")(?=[^}]*mode="claude")[^}]*\} 1/,
  );
});

test("recordTurnTerminal without a matching start is a no-op", async () => {
  await registry.resetMetrics();
  recordTurnTerminal("never-started", "failed");
  const dump = await registry.metrics();
  // Either the metric isn't emitted at all, or its count is 0. The
  // prom-client default is to emit a 0 value once the metric has been
  // touched; resetMetrics() clears the per-label state too, so this
  // line shouldn't appear at all.
  assert.doesNotMatch(
    dump,
    /tank_runner_turn_duration_seconds_count\{(?=[^}]*outcome="failed")(?=[^}]*mode="claude")[^}]*\} [1-9]/,
  );
});
