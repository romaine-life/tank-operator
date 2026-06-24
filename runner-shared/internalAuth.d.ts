import type { SessionBusConfig } from "./sessionBus.js";

export function hasInternalAuthConfig(cfg: Partial<SessionBusConfig>): boolean;
export function internalBearerToken(cfg: Partial<SessionBusConfig>): Promise<string>;
