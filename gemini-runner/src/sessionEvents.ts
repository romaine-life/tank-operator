// Tank-event sink for the Gemini runner. Wraps the JetStream session bus and
// publishes durable Tank conversation events.

import type { Config } from "./config.js";
import type { TankConversationEvent } from "../../runner-shared/conversation.js";
import { SessionBus } from "./sessionBus.js";

export type StampedTankEvent = TankConversationEvent & {
  uuid: string;
  order_key: string;
  sequence: number;
  written_at: string;
};

export class SessionEventSink {
  private readonly bus: SessionBus;

  constructor(cfg: Config) {
    this.bus = new SessionBus(cfg);
  }

  async upsert(message: StampedTankEvent): Promise<void> {
    await this.bus.publishEvent(message);
  }

  async findTurnTerminal(turnID: string): Promise<TankConversationEvent | null> {
    return (await this.bus.findTurnTerminal(turnID)) as TankConversationEvent | null;
  }

  async close(): Promise<void> {
    await this.bus.close();
  }
}
