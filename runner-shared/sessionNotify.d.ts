export interface SessionEventNotifierConfig {
  sessionId: string;
  operatorInternalURL?: string;
  operatorTokenPath?: string;
}

export interface SessionEventNotifierDependencies {
  fetch?: typeof fetch;
  readFile?: (path: string, encoding: "utf8") => Promise<string>;
}

export class SessionEventNotifier {
  constructor(
    cfg: SessionEventNotifierConfig,
    deps?: SessionEventNotifierDependencies,
  );
  notify(event: { order_key?: unknown } | null | undefined): Promise<boolean>;
}
