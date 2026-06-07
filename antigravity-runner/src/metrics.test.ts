import { test } from "node:test";
import assert from "node:assert/strict";
import { once } from "node:events";

import {
  interruptOutcomeTotal,
  registry,
  startMetricsServer,
} from "./metrics.js";

// The metrics surface is a contract: the orchestrator's alerts and the
// four-outcome Stop accounting (docs/tank-conversation-protocol.md → #532) read
// these `tank_antigravity_runner_*` series by name. A rename or accidental drop
// silently blinds that observability, so pin the names here.

test("the /metrics endpoint serves the tank_antigravity_runner_ counters", async () => {
  const server = startMetricsServer(0);
  await once(server, "listening");
  const addr = server.address();
  assert.ok(addr && typeof addr === "object", "metrics server bound to a port");
  const port = (addr as { port: number }).port;

  try {
    const res = await fetch(`http://127.0.0.1:${port}/metrics`);
    assert.equal(res.status, 200);
    const body = await res.text();
    // The load-bearing interrupt-outcome counter (every Stop must drain to one
    // terminal bucket) plus the other user-trust series.
    assert.match(body, /tank_antigravity_runner_interrupt_outcome_total/);
    assert.match(body, /tank_antigravity_runner_commands_consumed_total/);
    assert.match(body, /tank_antigravity_runner_turn_terminal_total/);
    assert.match(body, /tank_antigravity_runner_provider_error_total/);
  } finally {
    server.close();
    await once(server, "close");
  }
});

test("the metrics server answers /healthz and 404s unknown paths", async () => {
  const server = startMetricsServer(0);
  await once(server, "listening");
  const port = (server.address() as { port: number }).port;

  try {
    const health = await fetch(`http://127.0.0.1:${port}/healthz`);
    assert.equal(health.status, 200);
    assert.equal((await health.text()).trim(), "ok");

    const missing = await fetch(`http://127.0.0.1:${port}/not-a-route`);
    assert.equal(missing.status, 404);
  } finally {
    server.close();
    await once(server, "close");
  }
});

test("an interrupt outcome is recorded in exactly its labeled bucket", async () => {
  await registry.resetMetrics();

  interruptOutcomeTotal.labels("interrupted").inc();

  const dump = await registry.metrics();
  assert.match(
    dump,
    /tank_antigravity_runner_interrupt_outcome_total\{outcome="interrupted"\} 1/,
  );
});
