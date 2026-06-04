import type { SessionBusConfig } from "./sessionBus.js";

export function registerBackgroundTaskWake(
  cfg: SessionBusConfig,
  payload: {
    taskID: string;
    status: string;
    description?: string;
    summary?: string;
    lastToolName?: string;
    error?: string;
  },
): Promise<boolean>;
