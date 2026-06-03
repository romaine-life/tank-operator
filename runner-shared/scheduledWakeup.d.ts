import type { SessionBusConfig } from "./sessionBus.js";

export function registerScheduledWakeup(
  cfg: SessionBusConfig,
  payload: {
    delayMs: number;
    prompt: string;
    providerItemID: string;
    scheduledTurnID?: string;
  },
): Promise<boolean>;
