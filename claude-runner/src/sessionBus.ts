import { connect, nanos } from "@nats-io/transport-node";
import {
  AckPolicy,
  DeliverPolicy,
  ReplayPolicy,
  jetstream,
  jetstreamManager,
} from "@nats-io/jetstream";

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
    });
  }
}
