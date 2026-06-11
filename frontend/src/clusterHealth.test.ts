import { test, expect } from "vitest";

import {
  clusterHealthHeadline,
  clusterHealthIssueText,
  clusterHealthNatsReachabilityLabel,
  formatDurationShort,
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
    upgrade: {
      status: "healthy",
      state: "idle",
      summary: "No AKS upgrade signals",
      window: {
        configured: true,
        label: "AKS auto-upgrade",
        day_of_week: "Sunday",
        start_time: "06:00",
        utc_offset: "+00:00",
        duration_hours: 12,
        active: false,
      },
      node_image_versions: [],
      kubelet_versions: [],
    },
  };
}

test("cluster health headline maps status", () => {
  const health = baseHealth();
  expect(clusterHealthHeadline(health)).toBe("Cluster healthy");
  health.status = "warning";
  expect(clusterHealthHeadline(health)).toBe("Cluster warning");
  health.status = "critical";
  expect(clusterHealthHeadline(health)).toBe("Cluster critical");
});

test("cluster health issue text prioritizes node pressure", () => {
  const health = baseHealth();
  health.status = "warning";
  health.nodes.status = "warning";
  health.nodes.memory_pressure_nodes = 1;
  expect(clusterHealthIssueText(health)).toBe("1 node under memory pressure");
});

test("cluster health issue text surfaces NATS warnings", () => {
  const health = baseHealth();
  health.status = "warning";
  health.nats.status = "warning";
  health.nats.warnings = ["Live delivery replicas 2/3 current"];
  expect(clusterHealthIssueText(health)).toBe("Live delivery replicas 2/3 current");
});

test("cluster health issue text uses a non-label healthy summary", () => {
  expect(clusterHealthIssueText(baseHealth())).toBe("all checks passing");
});

test("cluster health issue text prioritizes active AKS upgrades", () => {
  const health = baseHealth();
  health.status = "warning";
  health.upgrade = {
    status: "warning",
    state: "active",
    summary: "AKS upgrade signals active",
    signals: ["node image versions are mixed"],
    window: {
      configured: true,
      label: "AKS auto-upgrade",
      day_of_week: "Sunday",
      start_time: "06:00",
      utc_offset: "+00:00",
      duration_hours: 12,
      active: true,
      seconds_remaining: 25_200,
    },
  };
  expect(clusterHealthIssueText(health)).toBe(
    "AKS upgrade active, 7h left in window",
  );
});

test("cluster health NATS reachability formats monitor availability", () => {
  expect(clusterHealthNatsReachabilityLabel(baseHealth().nats)).toBe("3/3");
  const health = baseHealth();
  health.nats.reachable_servers = 2;
  expect(clusterHealthNatsReachabilityLabel(health.nats)).toBe("2/3");
  health.nats.expected_servers = 0;
  health.nats.configured_monitor_urls = 0;
  expect(clusterHealthNatsReachabilityLabel(health.nats)).toBe("2/?");
});

test("formatDurationShort rounds up to the next minute", () => {
  expect(formatDurationShort(59)).toBe("1m");
  expect(formatDurationShort(3600)).toBe("1h");
  expect(formatDurationShort(3661)).toBe("1h 2m");
});
