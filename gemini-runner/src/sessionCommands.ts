import {
  SessionBus,
  commandClientNonce,
  isInputReplyCommand,
  isInterruptCommand,
  isStopBackgroundTaskCommand,
  type SessionBusConfig,
  type SessionCommandRecord,
} from "./sessionBus.js";

export type { SessionCommandRecord };
export type SessionCommandBusConfig = SessionBusConfig;

export { commandClientNonce, isInputReplyCommand, isInterruptCommand, isStopBackgroundTaskCommand };

export class SessionCommandBus extends SessionBus {
  constructor(cfg: SessionCommandBusConfig) {
    super(cfg);
  }

  startCommandHeartbeat(record: SessionCommandRecord): () => void {
    return this.startWorkHeartbeat(record);
  }
}
