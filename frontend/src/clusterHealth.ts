export type ClusterHealthStatus = "healthy" | "warning" | "critical" | "unknown";

export interface ClusterHealthResponse {
  description: string;
  status: ClusterHealthStatus;
  checked_at: string;
  nodes: ClusterNodeHealth;
  sessions: ClusterSessionPodHealth;
  nats: ClusterNATSHealth;
}

export interface ClusterNodeHealth {
  status: ClusterHealthStatus;
  total: number;
  ready: number;
  not_ready: number;
  unschedulable: number;
  memory_pressure_nodes: number;
  disk_pressure_nodes: number;
  pid_pressure_nodes: number;
  error?: string;
}

export interface ClusterSessionPodHealth {
  status: ClusterHealthStatus;
  total: number;
  running: number;
  pending: number;
  succeeded: number;
  failed: number;
  unknown: number;
  ready: number;
  not_ready: number;
  restarts: number;
  error?: string;
}

export interface ClusterNATSHealth {
  status: ClusterHealthStatus;
  configured_monitor_urls: number;
  reachable_servers: number;
  expected_servers: number;
  servers: Array<{ name?: string; reachable: boolean; error?: string }>;
  jetstream: ClusterJetStreamHealth;
  warnings?: string[];
  error?: string;
}

export interface ClusterJetStreamHealth {
  memory_bytes: number;
  max_memory_bytes: number;
  memory_utilization: number;
  reserved_memory_bytes: number;
  meta_pending: number;
  slow_consumers: number;
  streams: number;
  consumers: number;
  messages: number;
  bytes: number;
  stream_name?: string;
  stream_replicas: number;
  expected_stream_replicas: number;
  stream_current_replicas: number;
  stream_lagging_replicas: number;
  stream_messages: number;
  stream_bytes: number;
  stream_consumers: number;
}

export function clusterHealthHeadline(health: ClusterHealthResponse | null): string {
  switch (health?.status) {
    case "healthy":
      return "Cluster healthy";
    case "warning":
      return "Cluster warning";
    case "critical":
      return "Cluster critical";
    default:
      return "Cluster unknown";
  }
}

export function clusterHealthIssueText(health: ClusterHealthResponse | null): string {
  if (!health) return "checking nodes and NATS";
  if (health.nodes.status === "critical") return "nodes unavailable";
  if (health.nats.status === "critical") return health.nats.error || "NATS unreachable";
  if (health.sessions.status === "critical") return `${health.sessions.failed} failed session pod${health.sessions.failed === 1 ? "" : "s"}`;
  if (health.nodes.error) return "node health unavailable";
  if (health.nats.error) return health.nats.error;
  if (health.sessions.error) return "session pod health unavailable";
  if (health.nodes.status === "warning") {
    if (health.nodes.not_ready > 0) return `${health.nodes.not_ready} node${health.nodes.not_ready === 1 ? "" : "s"} not ready`;
    if (health.nodes.memory_pressure_nodes > 0) return `${health.nodes.memory_pressure_nodes} node${health.nodes.memory_pressure_nodes === 1 ? "" : "s"} under memory pressure`;
    if (health.nodes.disk_pressure_nodes > 0) return `${health.nodes.disk_pressure_nodes} node${health.nodes.disk_pressure_nodes === 1 ? "" : "s"} under disk pressure`;
    if (health.nodes.pid_pressure_nodes > 0) return `${health.nodes.pid_pressure_nodes} node${health.nodes.pid_pressure_nodes === 1 ? "" : "s"} under PID pressure`;
    if (health.nodes.unschedulable > 0) return `${health.nodes.unschedulable} node${health.nodes.unschedulable === 1 ? "" : "s"} unschedulable`;
  }
  if (health.nats.status === "warning") return health.nats.warnings?.[0] || "NATS degraded";
  if (health.sessions.status === "warning") {
    if (health.sessions.pending > 0) return `${health.sessions.pending} session pod${health.sessions.pending === 1 ? "" : "s"} pending`;
    if (health.sessions.not_ready > 0) return `${health.sessions.not_ready} session pod${health.sessions.not_ready === 1 ? "" : "s"} not ready`;
  }
  if (health.status === "unknown") return "health partially unavailable";
  return "nodes, sessions, NATS";
}

export function clusterHealthNatsLoadLabel(nats: ClusterNATSHealth | undefined): string {
  const util = nats?.jetstream?.memory_utilization;
  if (typeof util !== "number" || !Number.isFinite(util) || util <= 0) return "n/a";
  return `${Math.round(util * 100)}%`;
}

export function clusterHealthStatusClass(status: ClusterHealthStatus | undefined): string {
  switch (status) {
    case "healthy":
      return "is-healthy";
    case "warning":
      return "is-warning";
    case "critical":
      return "is-critical";
    default:
      return "is-unknown";
  }
}
