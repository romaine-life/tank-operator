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
    /**
     * Durable shell_task.exited event id whose observation registered this
     * wake — the backend's re-arm discriminator (same observation =
     * duplicate; new observation of a fired task = next wake generation).
     */
    observedEventID?: string;
  },
): Promise<boolean>;

export function cancelBackgroundTaskWake(
  cfg: SessionBusConfig,
  payload: {
    taskID: string;
    /** Audit reason recorded on the cancelled row (default delivered_mid_turn). */
    reason?: string;
  },
): Promise<boolean>;
