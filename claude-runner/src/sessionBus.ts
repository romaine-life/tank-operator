import { connect, nanos } from "@nats-io/transport-node";
import {
  AckPolicy,
  DeliverPolicy,
  ReplayPolicy,
  jetstream,
  jetstreamManager,
} from "@nats-io/jetstream";

import {
  recordBusConsumerRestart,
  recordNatsConnectionStatus,
  setBusHealthCheck,
} from "./metrics.js";
import {
  SharedSessionBus,
  type SessionBusConfig,
} from "../../runner-shared/sessionBus.js";

export * from "../../runner-shared/sessionBus.js";

export class SessionBus extends SharedSessionBus {
  constructor(cfg: SessionBusConfig, provider: "claude" | "codex" | string) {
    super(cfg, provider, {
      connect,
      jetstream,
      jetstreamManager,
      AckPolicy,
      DeliverPolicy,
      ReplayPolicy,
      nanos,
      // Supervised-bus observability (issue #1076 item 1): connection
      // churn and consumer restarts surface as bounded counters; a
      // PERMANENT close exits the process (SharedSessionBus default) so
      // the kubelet restarts the container instead of leaving a
      // deaf-but-alive zombie holding the session.
      onConnectionStatus: (type: string) => recordNatsConnectionStatus(type),
      onConsumerRestart: (kind: "command" | "control") =>
        recordBusConsumerRestart(kind),
    });
    // Liveness: /healthz reports 503 once the connection is permanently
    // closed. Registered per-instance; all instances share the process.
    setBusHealthCheck(() => this.isHealthy());
  }
}
