// Tank-event sink for the Codex runner. Wraps the JetStream session bus and
// publishes durable Tank conversation events. The runner is the producer; the
// backend persister consumes from `tank.session.<id>.events` and writes the
// session-events ledger after validating against the schema.

import type { Config } from "./config.js";
import type { TankConversationEvent } from "../../runner-shared/conversation.js";
import { SessionBus } from "./sessionBus.js";

// The raw Codex SDK event shape. The adapter consumes it; nothing
// downstream of the adapter sees it.
export interface CodexEvent {
  type: string;
  [k: string]: unknown;
}

export type StampedTankEvent = TankConversationEvent & {
  uuid: string;
  order_key: string;
  sequence: number;
  written_at: string;
};

export class SessionEventSink {
  private readonly bus: SessionBus;

  constructor(cfg: Config) {
    this.bus = new SessionBus(cfg, "codex");
  }

  async upsert(event: StampedTankEvent): Promise<void> {
    await this.bus.publishEvent(event);
  }

  async findTurnTerminal(turnID: string): Promise<TankConversationEvent | null> {
    return (await this.bus.findTurnTerminal(turnID)) as TankConversationEvent | null;
  }
}
