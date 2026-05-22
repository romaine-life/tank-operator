import type { SessionBusConfig } from "./sessionBus.js";

export function reportRuntimeConfig(
  cfg: SessionBusConfig,
  payload: { model?: string; effort?: string },
): Promise<boolean>;
