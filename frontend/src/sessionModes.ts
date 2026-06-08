export type Provider = "anthropic" | "codex" | "antigravity";
export type SessionInteraction = "gui" | "cli";

export const SESSION_MODE_ORDER = [
  "api_key",
  "claude_cli",
  "claude_gui",
  "config",
  "codex_cli",
  "codex_gui",
  "codex_exec_gui",
  "codex_app_server",
  "codex_config",
  "antigravity_config",
  "antigravity_cli",
  "antigravity_gui",
] as const;

export type SessionMode = (typeof SESSION_MODE_ORDER)[number];
export type DefaultSessionMode = Extract<
  SessionMode,
  | "claude_cli"
  | "claude_gui"
  | "codex_cli"
  | "codex_gui"
  | "antigravity_cli"
  | "antigravity_gui"
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

// "antigravity" is Gemini-Ultra via `agy`. It participates in the same GUI chat
// provider contract as Claude/Codex; only CLI and model/effort controls are
// absent until the runner exposes equivalent choices.
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
  antigravity_config: {
    provider: "antigravity",
    interaction: null,
    defaultSelectable: false,
    configCredentials: true,
    chatSurface: false,
    sdkChat: false,
    workspaceFiles: false,
    repos: false,
    rollout: null,
  },
  antigravity_cli: {
    provider: "antigravity",
    interaction: "cli",
    defaultSelectable: true,
    configCredentials: false,
    chatSurface: false,
    sdkChat: false,
    workspaceFiles: false,
    repos: false,
    rollout: null,
  },
  antigravity_gui: {
    provider: "antigravity",
    interaction: "gui",
    defaultSelectable: true,
    configCredentials: false,
    chatSurface: true,
    sdkChat: true,
    workspaceFiles: true,
    repos: true,
    rollout: "gui",
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
  "codex",
  "antigravity",
];

export const MODE_PROVIDERS: Record<SessionMode, Provider> =
  SESSION_MODE_ORDER.reduce(
    (out, mode) => {
      out[mode] = SESSION_MODE_CONTRACT[mode].provider;
      return out;
    },
    {} as Record<SessionMode, Provider>,
  );

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
