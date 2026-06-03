import type { SessionBusConfig } from "./sessionBus.js";

export function reportRuntimeConfig(
  cfg: SessionBusConfig,
  payload: {
    model?: string;
    effort?: string;
    contextWindowTokens?: number;
    contextWindowSource?: string;
  },
): Promise<boolean>;
