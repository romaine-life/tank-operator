import { test } from "node:test";
import assert from "node:assert/strict";
import { once } from "node:events";

import {
  recordTurnStart,
  recordTurnTerminal,
  registry,
  startMetricsServer,
} from "./metrics.js";

test("metrics endpoint serves prom-format with tank_runner_ counters for codex", async () => {
  const server = startMetricsServer(0);
  await once(server, "listening");
  const addr = server.address();
  assert.ok(addr && typeof addr === "object", "metrics server bound");
  const port = (addr as { port: number }).port;

  try {
    const res = await fetch(`http://127.0.0.1:${port}/metrics`);
    assert.equal(res.status, 200);
    const body = await res.text();
    assert.match(body, /tank_runner_commands_consumed_total/);
    assert.match(body, /tank_runner_turn_duration_seconds/);
    assert.match(body, /mode="codex"/);
  } finally {
    server.close();
    await once(server, "close");
  }
});

test("recordTurnStart + recordTurnTerminal observes a duration on codex", async () => {
  await registry.resetMetrics();

  recordTurnStart("codex-turn-1");
  recordTurnTerminal("codex-turn-1", "completed");

  const dump = await registry.metrics();
  // Label order is prom-client-internal; match any ordering.
  assert.match(
    dump,
    /tank_runner_turn_duration_seconds_count\{(?=[^}]*outcome="completed")(?=[^}]*mode="codex")[^}]*\} 1/,
  );
});
