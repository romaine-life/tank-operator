export type Provider =
  | "anthropic"
  | "anthropic_secondary"
  | "codex";
export type SessionInteraction = "gui" | "cli";

export const SESSION_MODE_ORDER = [
  "api_key",
  "claude_cli",
  "claude_gui",
  "config",
  "claude_secondary_cli",
  "claude_secondary_gui",
  "claude_secondary_config",
  "codex_cli",
  "codex_gui",
  "codex_exec_gui",
  "codex_app_server",
  "codex_config",
] as const;

export type SessionMode = (typeof SESSION_MODE_ORDER)[number];
export type DefaultSessionMode = Extract<
  SessionMode,
  | "claude_cli"
  | "claude_gui"
  | "claude_secondary_cli"
  | "claude_secondary_gui"
  | "codex_cli"
  | "codex_gui"
>;

type RolloutContract = "claude" | "codex" | "gui" | null;

export interface SessionModeContract {
  provider: Provider;
  interaction: SessionInteraction | null;
  defaultSelectable: boolean;
  configCredentials: boolean;
  chatSurface: boolean;
  sdkChat: boolean;
  workspaceFiles: boolean;
  repos: boolean;
  rollout: RolloutContract;
}

export const SESSION_MODE_CONTRACT = {
  api_key: {
    provider: "anthropic",
    interaction: null,
    defaultSelectable: false,
    configCredentials: false,
    chatSurface: false,
    sdkChat: false,
    workspaceFiles: false,
    repos: false,
    rollout: "claude",
  },
  claude_cli: {
    provider: "anthropic",
    interaction: "cli",
    defaultSelectable: true,
    configCredentials: false,
    chatSurface: false,
    sdkChat: false,
    workspaceFiles: false,
    repos: false,
    rollout: "claude",
  },
  claude_gui: {
    provider: "anthropic",
    interaction: "gui",
    defaultSelectable: true,
    configCredentials: false,
    chatSurface: true,
    sdkChat: true,
    workspaceFiles: true,
    repos: true,
    rollout: "gui",
  },
  config: {
    provider: "anthropic",
    interaction: null,
    defaultSelectable: false,
    configCredentials: true,
    chatSurface: false,
    sdkChat: false,
    workspaceFiles: false,
    repos: false,
    rollout: null,
  },
  claude_secondary_cli: {
    provider: "anthropic_secondary",
    interaction: "cli",
    defaultSelectable: true,
    configCredentials: false,
    chatSurface: false,
    sdkChat: false,
    workspaceFiles: false,
    repos: false,
    rollout: "claude",
  },
  claude_secondary_gui: {
    provider: "anthropic_secondary",
    interaction: "gui",
    defaultSelectable: true,
    configCredentials: false,
    chatSurface: true,
    sdkChat: true,
    workspaceFiles: true,
    repos: true,
    rollout: "gui",
  },
  claude_secondary_config: {
    provider: "anthropic_secondary",
    interaction: null,
    defaultSelectable: false,
    configCredentials: true,
    chatSurface: false,
    sdkChat: false,
    workspaceFiles: false,
    repos: false,
    rollout: null,
  },
  codex_cli: {
    provider: "codex",
    interaction: "cli",
    defaultSelectable: true,
    configCredentials: false,
    chatSurface: false,
    sdkChat: false,
    workspaceFiles: false,
    repos: false,
    rollout: "codex",
  },
  codex_gui: {
    provider: "codex",
    interaction: "gui",
    defaultSelectable: true,
    configCredentials: false,
    chatSurface: true,
    sdkChat: true,
    workspaceFiles: true,
    repos: true,
    rollout: "gui",
  },
  codex_exec_gui: {
    provider: "codex",
    interaction: "gui",
    defaultSelectable: false,
    configCredentials: false,
    chatSurface: true,
    sdkChat: true,
    workspaceFiles: true,
    repos: false,
    rollout: "gui",
  },
  codex_app_server: {
    provider: "codex",
    interaction: "gui",
    defaultSelectable: false,
    configCredentials: false,
    chatSurface: true,
    sdkChat: true,
    workspaceFiles: true,
    repos: false,
    rollout: "gui",
  },
  codex_config: {
    provider: "codex",
    interaction: null,
    defaultSelectable: false,
    configCredentials: true,
    chatSurface: false,
    sdkChat: false,
    workspaceFiles: false,
    repos: false,
    rollout: null,
  },
} satisfies Record<SessionMode, SessionModeContract>;

function modeSet(
  predicate: (contract: SessionModeContract, mode: SessionMode) => boolean,
): ReadonlySet<SessionMode> {
  return new Set(
    SESSION_MODE_ORDER.filter((mode) =>
      predicate(SESSION_MODE_CONTRACT[mode], mode),
    ),
  );
}

export const PROVIDERS: readonly Provider[] = [
  "anthropic",
  "anthropic_secondary",
  "codex",
];

export const MODE_PROVIDERS: Record<SessionMode, Provider> =
  SESSION_MODE_ORDER.reduce(
    (out, mode) => {
      out[mode] = SESSION_MODE_CONTRACT[mode].provider;
      return out;
    },
    {} as Record<SessionMode, Provider>,
  );

export const SESSION_MODE_LABELS: Record<SessionMode, string> = {
  api_key: "Claude API key",
  claude_cli: "Claude CLI",
  claude_gui: "Claude GUI",
  config: "Claude config",
  claude_secondary_cli: "Claude secondary CLI",
  claude_secondary_gui: "Claude secondary GUI",
  claude_secondary_config: "Claude secondary config",
  codex_cli: "Codex CLI",
  codex_gui: "Codex GUI",
  codex_exec_gui: "Codex Legacy",
  codex_app_server: "Codex App Server",
  codex_config: "Codex config",
};

export function sessionModeLabel(mode: string): string {
  return SESSION_MODE_LABELS[mode as SessionMode] ?? mode;
}

export const DEFAULT_SESSION_MODES = modeSet(
  (contract) => contract.defaultSelectable,
);
export const CONFIG_MODES = modeSet((contract) => contract.configCredentials);
export const CHAT_MODES = modeSet((contract) => contract.chatSurface);
export const SDK_CHAT_MODES = modeSet((contract) => contract.sdkChat);
export const CREATE_TIME_INITIAL_TURN_MODES = new Set(SDK_CHAT_MODES);
export const WORKSPACE_FILE_MODES = modeSet((contract) => contract.workspaceFiles);
export const REPO_SUPPORTED_MODES = modeSet((contract) => contract.repos);
export const CLAUDE_ROLLOUT_MODES = modeSet(
  (contract) => contract.rollout === "claude",
);
export const CODEX_ROLLOUT_MODES = modeSet(
  (contract) => contract.rollout === "codex",
);
export const GUI_ROLLOUT_MODES = modeSet((contract) => contract.rollout === "gui");
export const ROLLOUT_MODES = new Set<SessionMode>([
  ...CLAUDE_ROLLOUT_MODES,
  ...CODEX_ROLLOUT_MODES,
]);

export const PROVIDER_INTERACTION_MODES = PROVIDERS.reduce(
  (out, provider) => {
    out[provider] = {};
    return out;
  },
  {} as Record<
    Provider,
    Partial<Record<SessionInteraction, DefaultSessionMode | null>>
  >,
);

export const PROVIDER_CONFIG_MODES: Partial<Record<Provider, SessionMode>> = {};

for (const mode of SESSION_MODE_ORDER) {
  const contract = SESSION_MODE_CONTRACT[mode];
  if (contract.defaultSelectable && contract.interaction != null) {
    PROVIDER_INTERACTION_MODES[contract.provider][contract.interaction] ??=
      mode as DefaultSessionMode;
  }
  if (contract.configCredentials) {
    PROVIDER_CONFIG_MODES[contract.provider] = mode;
  }
}

export function isDefaultSessionMode(
  value: string | null,
): value is DefaultSessionMode {
  return DEFAULT_SESSION_MODES.has(value as SessionMode);
}
