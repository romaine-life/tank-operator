import type { SessionBusConfig } from "./sessionBus.js";

export function reportProviderFatal(
  cfg: SessionBusConfig,
  payload: {
    provider: string;
    reason: string;
    exitCode?: number;
    message?: string;
  },
): Promise<boolean>;
