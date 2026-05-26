import assert from "node:assert/strict";
import test from "node:test";

import {
  clusterHealthHeadline,
  clusterHealthIssueText,
  clusterHealthNatsLoadLabel,
  type ClusterHealthResponse,
} from "./clusterHealth";

function baseHealth(): ClusterHealthResponse {
  return {
    description: "test",
    status: "healthy",
    checked_at: "2026-05-25T00:00:00Z",
    nodes: {
      status: "healthy",
      total: 4,
      ready: 4,
      not_ready: 0,
      unschedulable: 0,
      memory_pressure_nodes: 0,
      disk_pressure_nodes: 0,
      pid_pressure_nodes: 0,
    },
    sessions: {
      status: "healthy",
      total: 6,
      running: 6,
      pending: 0,
      succeeded: 0,
      failed: 0,
      unknown: 0,
      ready: 6,
      not_ready: 0,
      restarts: 0,
    },
    nats: {
      status: "healthy",
      configured_monitor_urls: 3,
      reachable_servers: 3,
      expected_servers: 3,
      servers: [],
      jetstream: {
        memory_bytes: 128,
        max_memory_bytes: 256,
        memory_utilization: 0.5,
        reserved_memory_bytes: 128,
        meta_pending: 0,
        slow_consumers: 0,
        streams: 1,
        consumers: 4,
        messages: 20,
        bytes: 128,
        stream_name: "TANK_SESSION_BUS",
        stream_replicas: 3,
        expected_stream_replicas: 3,
        stream_current_replicas: 3,
        stream_lagging_replicas: 0,
        stream_messages: 20,
        stream_bytes: 128,
        stream_consumers: 4,
      },
    },
  };
}

test("cluster health headline maps status", () => {
  const health = baseHealth();
  assert.equal(clusterHealthHeadline(health), "Cluster healthy");
  health.status = "warning";
  assert.equal(clusterHealthHeadline(health), "Cluster warning");
  health.status = "critical";
  assert.equal(clusterHealthHeadline(health), "Cluster critical");
});

test("cluster health issue text prioritizes node pressure", () => {
  const health = baseHealth();
  health.status = "warning";
  health.nodes.status = "warning";
  health.nodes.memory_pressure_nodes = 1;
  assert.equal(clusterHealthIssueText(health), "1 node under memory pressure");
});

test("cluster health issue text surfaces NATS warnings", () => {
  const health = baseHealth();
  health.status = "warning";
  health.nats.status = "warning";
  health.nats.warnings = ["Live delivery replicas 2/3 current"];
  assert.equal(clusterHealthIssueText(health), "Live delivery replicas 2/3 current");
});

test("cluster health NATS load formats utilization", () => {
  assert.equal(clusterHealthNatsLoadLabel(baseHealth().nats), "50%");
  const health = baseHealth();
  health.nats.jetstream.memory_utilization = 0;
  assert.equal(clusterHealthNatsLoadLabel(health.nats), "n/a");
});
