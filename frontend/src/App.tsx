import { createContext, lazy, Suspense, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState, useSyncExternalStore } from "react";
import type {
  AnchorHTMLAttributes,
  ComponentProps,
  CSSProperties,
  DragEvent as ReactDragEvent,
  FocusEvent as ReactFocusEvent,
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
  ReactNode,
} from "react";
import { ProcessTerminal, type TranscriptEntry as SandboxTranscriptEntry } from "@sandbox-agent/react";
import { SandboxAgent } from "sandbox-agent";
import {
  Streamdown,
  type Components as StreamdownComponents,
} from "streamdown";
import type { UserMessageDisplay } from "../../runner-shared/conversation.js";
import {
  mergeSdkTranscript,
  pruneLocalRealtimeEchoes,
  pruneRealtimeEntries,
} from "./transcriptMerge";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import type { PromptInputMessage } from "@/components/ai-elements/prompt-input";
import { ChatComposer, type RunComposerMode } from "./ChatComposer";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "./components/ui/select";
import { AdminAvatarManager } from "./AdminAvatarManager";
import { SessionListDebugCaptureControls } from "./SessionListDebugCaptureControls";
import { WorkspaceShell } from "./WorkspaceShell";
import {
  initialTimelineBootstrapState,
  reduceTimelineBootstrap,
} from "./chatTimelineBootstrap";
import {
  ActivityIcon,
  AlertCircleIcon,
  ArrowLeftIcon,
  ArrowUpFromLineIcon,
  BellIcon,
  BotIcon,
  BrainIcon,
  CalendarIcon,
  CheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  ClipboardListIcon,
  Code2Icon,
  CopyIcon,
  FileDiffIcon,
  FileIcon,
  FileTextIcon,
  FlaskConicalIcon,
  FolderIcon,
  FolderOpenIcon,
  ExternalLinkIcon,
  GitBranchIcon,
  GlobeIcon,
  ImageIcon,
  InfoIcon,
  LinkIcon,
  ListChecksIcon,
  Loader2Icon,
  MessageSquareIcon,
  MinusIcon,
  MonitorIcon,
  NotebookPenIcon,
  PlayIcon,
  PlusIcon,
  RotateCcwIcon,
  SearchIcon,
  SettingsIcon,
  SquareTerminalIcon,
  SquareIcon,
  SquarePenIcon,
  TerminalIcon,
  TextQuoteIcon,
  TimerIcon,
  WrenchIcon,
  XIcon,
  type LucideIcon,
} from "lucide-react";
import { AUTH_TOKEN_UPDATED_EVENT, authedEventSource, authedFetch, bootstrapAuth, getStoredToken, logout, startLogin } from "./auth";
import {
  createSilenceWatchdog,
  logSessionEventStreamEvent,
  type SilenceWatchdog,
} from "./sessionEventStreamTelemetry";
import {
  DEFAULT_NAVIGATION_MODE,
  type NavigationMode,
  type NavigationModeReason,
  navigationModeTelemetryEvent,
  transitionNavigationMode,
} from "./navigationMode";
import { requiresGitHubOnboarding, type SessionRole } from "./authPolicy";
import {
  type ConversationBackgroundTaskStatus,
} from "./conversationReducer";
import { McpIcon } from "./McpIcon";
import { RepoPicker } from "./components/RepoPicker";
import {
  REPO_SUPPORTED_MODES,
  addRepoSlug,
  isValidRepoSlug,
  recentRepoShortcutSlugs,
  removeRepoSlug,
} from "./repos";
import {
  readHomeSelectedRepos,
  writeHomeSelectedRepos,
} from "./homeRepos";
import {
  composeAttachmentDisplayText,
  composeAttachmentPathText,
  labelAttachments,
  messageAttachmentDisplays,
  splitLegacyAttachmentDisplayText,
  type MessageAttachmentDisplay,
} from "./attachmentLabels";
import { ProviderIcon } from "./providerIcons";
import {
  SESSION_ACTIVITY_STATUS_LEGEND,
  normalizeSessionActivity,
  orderKeyAfter,
  sessionActivityDotStatus,
  sessionActivityStatusLabel,
  shouldRingForActivityTransition,
  type SessionActivitySummary,
} from "./sessionActivity";
import {
  SessionStore,
  normalizeSessionRowUpdate,
  type SessionRow,
} from "./sessionStore";
import { decideFollowupSubmit } from "./submitLatch";
import {
  logSessionListEvent,
  logSessionListSnapshot,
  logSessionListSseOpen,
  logSessionListStreamSignal,
} from "./sessionListTelemetry";
import {
  recordSessionListDebugEvent,
  updateSessionListDebugRender,
  type SessionListDebugRow,
} from "./sessionListDebug";
import {
  chatScrollElementSnapshot,
  logChatScrollEvent,
} from "./chatScrollTelemetry";
import {
  noteSessionSwitch,
  noteTankEvent,
  setActiveSessionMode,
} from "./longTaskTelemetry";
import {
  useRelativeMinutes,
  useRelativeSeconds,
} from "./timeService";
import {
  clusterHealthHeadline,
  clusterHealthIssueText,
  clusterHealthNatsReachabilityLabel,
  clusterHealthStatusClass,
  type ClusterHealthResponse,
  type ClusterHealthStatus,
} from "./clusterHealth";
import {
  turnActivityGroupIsActive,
  turnActivityShellIsDurablyActive,
} from "./turnActivityState";
import { ANSI_256_OVERRIDES, ANSI_STANDARD_OVERRIDES } from "./terminalTheme";
import {
  AgentAvatarIcon,
  getSessionAvatarByID,
  getSystemAvatarByID,
  loadRuntimeAvatarCatalog,
  type AgentAvatar,
} from "./sessionAvatars";
import { openAvatarPreview } from "./avatarPreview";
import {
  linkWorkspacePathsInMarkdown,
  normalizeWorkspacePathTarget,
  splitWorkspacePathsInText,
  workspacePathFromHref,
  type WorkspacePathTarget,
} from "./workspaceLinks";
import {
  sessionContainerAvailable,
  sessionFilesAvailable,
  sessionFilesTabTitle,
  sessionModeSupportsWorkspaceFiles,
} from "./sessionWorkspace";
import { shouldGroupTranscriptMessageWithPrevious } from "./transcriptAuthorGrouping";

const FileCodeViewer = lazy(() => import("./FileCodeViewer"));
const FileImageViewer = lazy(() => import("./FileImageViewer"));

type SessionMode =
  | "api_key"
  | "claude_cli"
  | "claude_gui"
  | "config"
  | "codex_cli"
  | "codex_gui"
  | "codex_exec_gui"
  | "codex_app_server"
  | "codex_config"
  | "hermes_gui"
  | "pi_cli"
  | "pi_config";
type DefaultSessionMode = Extract<
  SessionMode,
  | "claude_cli"
  | "claude_gui"
  | "codex_cli"
  | "codex_gui"
  | "codex_exec_gui"
  | "hermes_gui"
  | "pi_cli"
>;
type Provider = "anthropic" | "codex" | "hermes" | "pi";
type SessionInteraction = "gui" | "cli";
type ToolKind = "mcp" | "shell";
type AskUserQuestionAnswer = {
  labels: string[];
  notes?: string;
  preview?: string;
};
type TurnActivitySummary = {
  turnId?: string;
  status?: "active" | "completed" | string;
  active?: boolean;
  toolCount?: number;
  progressNoteCount?: number;
  reasoningCount?: number;
  backgroundTaskCount?: number;
  errorCount?: number;
  childCount?: number;
  compactedCount?: number;
  compactedEntryIds?: string[];
  startedAt?: string;
  completedAt?: string;
  startOrderKey?: string;
  endOrderKey?: string;
  sourceEventId?: string;
};
export type TranscriptEntry = Omit<SandboxTranscriptEntry, "role" | "kind"> & {
  kind: SandboxTranscriptEntry["kind"] | "background_task" | "turn_activity";
  role?: "user" | "assistant" | "system";
  toolKind?: ToolKind;
  toolServer?: string;
  toolAction?: string;
  transcriptSource?: "server" | "realtime";
  sourceEventId?: string;
  orderKey?: string;
  localOnly?: boolean;
  turnId?: string;
  clientNonce?: string;
  providerItemId?: string;
  display?: UserMessageDisplay;
  startedAt?: string;
  updatedAt?: string;
  completedAt?: string;
  turnTerminalStatus?: "completed" | "failed" | "interrupted";
  turnTerminalAt?: string;
  turnTerminalEventId?: string;
  turnTerminalOrderKey?: string;
  turnUsage?: unknown;
  attachments?: MessageAttachmentDisplay[];
  // For user-role messages authored by a sibling tank-operator session
  // via the mcp-tank-operator handoff path: the originating session id.
  // RunMessageBubble uses this as an agent-authored signal, but it must
  // not synthesize a local avatar identity from the id.
  originSessionId?: string;
  // For user-role messages submitted by a non-interactive principal (an
  // auth.romaine.life bot token): "system". RunMessageBubble renders the
  // session's system identity for these instead of the human owner's
  // Gravatar. originSessionId takes precedence when both are present.
  authorKind?: string;
  // Durable AskUserQuestion answers + annotations, sourced from the
  // `tool.approval_resolved` event payload via conversationProjection.
  // ToolAskUserBody reads this for the answered state so the UI matches
  // the durable ledger and not local React optimism.
  askUserAnswers?: Record<string, AskUserQuestionAnswer>;
  // metaKind specializes a meta-kind row into a distinguishable transcript
  // surface without growing the shared `kind` enum (which comes from the
  // sandbox-agent SDK). renderItem branches on metaKind before falling
  // through to the generic RunMetaBlock. Server projection sets metaKind
  // on rows it wants surfaced specially in chat — see
  // backend-go/cmd/tank-operator/transcript_projection.go →
  // projectNeedsInputAnnouncement.
  metaKind?: "needs_input_announcement";
  // For `needs_input_announcement` rows: the AskUserQuestion item's
  // provider id and the turn it lives on, so RunNeedsInputAnnouncement's
  // click handler can navigate the user to the Turns tab scrolled to the
  // right question. The handoff stays anchored to the durable item —
  // local React state never becomes the source of truth for "is the
  // agent still waiting on me."
  announcement?: {
    targetTurnId: string;
    targetProviderItemId: string;
    targetTimelineId?: string;
    questionSummary: string;
    questionCount: number;
    answered: boolean;
  };
  taskId?: string;
  taskStatus?: ConversationBackgroundTaskStatus;
  taskSummary?: string;
  taskDescription?: string;
  taskError?: unknown;
  taskToolUseId?: string;
  taskCommand?: string;
  taskCwd?: string;
  taskProcessId?: string;
  taskOutput?: string;
  taskExitCode?: number;
  taskDurationMs?: number;
  taskRawItem?: unknown;
  lastToolName?: string;
  activity?: TurnActivitySummary;
  activityIds?: string[];
};
type SdkTerminalStatus = "done" | "error" | "stopped";
type LocalRunStatus = "idle" | "running" | "stopping" | "done" | "error";
type SdkConnectionState =
  | "idle"
  | "connecting"
  | "connected"
  | "connection_lost"
  | "resyncing";
type SdkTerminalResult = {
  status: SdkTerminalStatus;
  detail?: string;
};
type SdkHistoryRefreshResult = {
  replayed: boolean;
  terminal?: SdkTerminalResult;
  stale?: boolean;
  error?: string;
};
type TranscriptTimelineBody = {
  session_id?: string;
  rows?: unknown[];
  next_cursor?: string;
  prev_cursor?: string;
  live_order_key?: string;
  found_oldest?: boolean;
  found_newest?: boolean;
  read_state?: { last_read_order_key?: unknown } | null;
  activity?: unknown;
  target_timeline_id?: string;
  target_cursor?: string;
};
type SdkHistoryRefreshSource =
  | "history"
  | "projected-refresh"
  | "visible-reactivation"
  | "resync"
  | "terminal-refresh";
type ScrollToLatestBehavior = "auto" | "smooth";
type ScrollToLatestReason = SdkHistoryRefreshSource | "submit" | "manual" | "keyboard";
type ScrollToLatestRequest = {
  signal: number;
  behavior: ScrollToLatestBehavior;
  reason: ScrollToLatestReason;
  enabled: boolean;
};
type SkillStateName = "test" | "rollout";
type InitialMessageMode = "direct" | "diagnose" | "quality_gaps" | "test";
type HomeTab = "chat" | "settings" | "help";

type ForkSessionRequest = {
  sourceSession: Session;
  forkedEntry: TranscriptEntry;
  model: string;
  // Empty string means the target runner should use its baked-in default,
  // so legacy forks created before this field existed keep working without
  // a migration.
  effort: string;
  permissionMode: string;
};

type AppPublicConfig = {
  session_scope?: string;
  fork_session_prompt_template?: string;
};

const DEFAULT_FORK_SESSION_PROMPT_TEMPLATE = [
  "The user forked this session from an assistant message in another Tank Operator session to deal with a divergent issue.",
  "",
  "Use the forked assistant message as the immediate starting point. The previous session data is identified below; read that session's transcript from Tank Operator data if it would help, but do not assume you need the entire prior conversation before making progress.",
  "",
  "Forked assistant message:",
  "{{forked_message}}",
  "",
  "Source session pointer:",
  "```json",
  "{{source_session_json}}",
  "```",
].join("\n");

let appConfigPromise: Promise<AppPublicConfig> | null = null;

const PROD_SESSION_SCOPE = "default";
const SESSION_VIEW_SCOPE_KEY = "tank-session-view-scope";

function normalizeSessionScopeValue(scope: unknown): string {
  const value = typeof scope === "string" ? scope.trim() : "";
  return value || PROD_SESSION_SCOPE;
}

function readSessionViewScopePreference(): string {
  try {
    return localStorage.getItem(SESSION_VIEW_SCOPE_KEY) ?? "";
  } catch {
    return "";
  }
}

function writeSessionViewScopePreference(scope: string): void {
  try {
    if (!scope) localStorage.removeItem(SESSION_VIEW_SCOPE_KEY);
    else localStorage.setItem(SESSION_VIEW_SCOPE_KEY, scope);
  } catch {
    // ignore
  }
}

// Initial value for the prod-session-view override. A `?session_view=prod`
// query param is an admin deep-link into the prod-session view: it sets the
// override before first render and persists it so in-app navigation keeps the
// choice. `?session_view=local` (or any other value) clears it. The deep-link
// exists so the view is reachable without the Settings toggle — bookmarkable
// for admins, and drivable by URL-only browser automation (the slot Playwright
// inspector can seed a URL but not click the toggle). The admin-capability
// gate (`canViewProdSessions`) still applies downstream, so a non-admin
// deep-link is inert and gets reconciled away by the clearing effect.
function readInitialSessionViewScopeOverride(): string {
  try {
    const raw = new URLSearchParams(window.location.search).get("session_view");
    if (raw !== null) {
      const next = raw === "prod" ? PROD_SESSION_SCOPE : "";
      writeSessionViewScopePreference(next);
      return next;
    }
  } catch {
    // ignore malformed URL / missing window
  }
  return readSessionViewScopePreference();
}

function appendQueryParam(path: string, key: string, value: string): string {
  const [base, query = ""] = path.split("?");
  const params = new URLSearchParams(query);
  params.set(key, value);
  const next = params.toString();
  return next ? `${base}?${next}` : base;
}

interface Session {
  id: string;
  session_scope?: string;
  pod_name: string | null;
  owner: string;
  status: string;
  mode: SessionMode;
  requested_at: string | null;
  created_at: string | null;
  ready_at: string | null;
  // User-set friendly name. Null when unset; UI falls back to the id slug.
  name: string | null;
  test_state?: TestState | null;
  rollout_state?: RolloutState | null;
  // Activity is the chat-derived sidebar indicator block. Backend
  // hydrates this from the latest session.activity_changed lifecycle
  // event in GET /api/sessions; the session-list SSE stream then keeps the
  // separately-tracked sessionActivities map up to date. The field is
  // here for the initial-state extraction in normalizeSession; runtime
  // reads go through sessionActivities[id], not session.activity.
  activity?: SessionActivitySummary | null;
  // repos is the durable owner/name slug list the user picked at
  // session creation. Always an array on the wire (empty when none
  // picked). The splash chips for existing sessions read from here
  // — never from localStorage — so the chip list never contradicts
  // the server's view.
  repos: string[];
  // clone_state is the per-repo repo-cloner init-container outcome.
  // Optional until the cloner writes back.
  clone_state?: Record<string, unknown> | null;
  model?: string;
  effort?: string;
  runtime_model?: string;
  runtime_effort?: string;
  runtime_configured_at?: string | null;
  agent_avatar_id?: string | null;
  system_avatar_id?: string | null;
  sidebar_position?: number;
  row_version?: number;
}

interface TestState {
  active?: boolean;
  slot_index?: number | null;
  url?: string | null;
}

interface RolloutState {
  active?: boolean;
}

const MODE_LABELS: Record<SessionMode, string> = {
  api_key: "Claude API key",
  claude_cli: "Claude CLI",
  claude_gui: "Claude GUI",
  config: "Claude config",
  codex_cli: "Codex CLI",
  codex_gui: "Codex GUI",
  codex_exec_gui: "Codex Legacy",
  codex_app_server: "Codex App Server",
  codex_config: "Codex config",
  hermes_gui: "Hermes",
  pi_cli: "Pi CLI",
  pi_config: "Pi config",
};

// Compact labels for the inline session-row chip. Falls back to MODE_LABELS
// elsewhere.
const MODE_CHIP_LABELS: Record<SessionMode, string> = {
  api_key: "api",
  claude_cli: "claude-cli",
  claude_gui: "claude-gui",
  config: "config",
  codex_cli: "codex-cli",
  codex_gui: "codex-gui",
  codex_exec_gui: "codex-exec",
  codex_app_server: "codex-app",
  codex_config: "codex-cfg",
  hermes_gui: "hermes",
  pi_cli: "pi-cli",
  pi_config: "pi-cfg",
};

const MODE_CHIP_ICONS: Partial<Record<SessionMode, Provider>> = {
  claude_cli: "anthropic",
  claude_gui: "anthropic",
  codex_cli: "codex",
  codex_gui: "codex",
  codex_exec_gui: "codex",
  codex_app_server: "codex",
  hermes_gui: "hermes",
  pi_cli: "pi",
};

const MODE_MENU_ICONS: Record<SessionMode, Provider> = {
  api_key: "anthropic",
  claude_cli: "anthropic",
  claude_gui: "anthropic",
  config: "anthropic",
  codex_cli: "codex",
  codex_gui: "codex",
  codex_exec_gui: "codex",
  codex_app_server: "codex",
  codex_config: "codex",
  hermes_gui: "hermes",
  pi_cli: "pi",
  pi_config: "pi",
};

const PROVIDER_INTERACTION_MODES: Record<
  Provider,
  Partial<Record<SessionInteraction, DefaultSessionMode | null>>
> = {
  anthropic: { gui: "claude_gui", cli: "claude_cli" },
  codex: { gui: "codex_gui", cli: "codex_cli" },
  hermes: { gui: "hermes_gui", cli: null },
  pi: { gui: null, cli: "pi_cli" },
};

const INTERACTION_LABELS: Record<SessionInteraction, string> = {
  gui: "gui",
  cli: "cli",
};

const INTERACTION_OPTIONS: SessionInteraction[] = ["gui", "cli"];

const PROVIDER_CONFIG_MODES: Partial<Record<Provider, SessionMode>> = {
  anthropic: "config",
  codex: "codex_config",
  pi: "pi_config",
};

const PROVIDER_LABELS: Record<Provider, string> = {
  anthropic: "Claude",
  codex: "Codex",
  hermes: "Hermes",
  pi: "Pi",
};

const MODE_HINTS: Record<SessionMode, string> = {
  claude_cli: "Uses claude.ai login",
  claude_gui: "GUI chat pane for claude -p output",
  api_key: "Specify an API key fallback",
  config: "Log in once · seeds KV for future sessions",
  codex_cli: "Uses ChatGPT login from KV",
  codex_gui: "GUI chat pane for Codex app-server transport",
  codex_exec_gui: "Fallback GUI for legacy codex exec transport",
  codex_app_server: "GUI chat pane for codex app-server transport",
  codex_config: "codex login --device-auth · seeds KV for Codex",
  hermes_gui: "Shared Hermes memory + MCP tools",
  pi_cli: "Uses Tank Claude/Codex subscriptions",
  pi_config: "Pi /login sandbox",
};

const DEMO_AGENT_AVATAR_IDS = [
  "jp1-grant",
  "jp1-sattler",
  "jp1-malcolm",
  "jp1-hammond",
  "jp1-nedry",
  "jp1-muldoon",
  "jp1-arnold",
  "jp1-raptor",
];

function demoAgentAvatarID(index: number): string {
  return DEMO_AGENT_AVATAR_IDS[Math.abs(index - 1) % DEMO_AGENT_AVATAR_IDS.length]!;
}

const DEMO_BASE_SESSIONS: Session[] = [
  {
    id: "claude-code",
    pod_name: "tank-demo-claude-code",
    owner: "preview",
    status: "Active",
    mode: "claude_gui",
    requested_at: new Date(Date.now() - 12 * 60 * 1000 - 2 * 1000).toISOString(),
    created_at: new Date(Date.now() - 12 * 60 * 1000).toISOString(),
    ready_at: new Date(Date.now() - 11.5 * 60 * 1000).toISOString(),
    name: "Claude Code",
    repos: [],
    agent_avatar_id: demoAgentAvatarID(1),
  },
  {
    id: "codex-cli",
    pod_name: "tank-demo-codex-cli",
    owner: "preview",
    status: "Active",
    mode: "codex_gui",
    requested_at: new Date(Date.now() - 68 * 60 * 1000 - 4 * 1000).toISOString(),
    created_at: new Date(Date.now() - 68 * 60 * 1000).toISOString(),
    ready_at: new Date(Date.now() - 67 * 60 * 1000).toISOString(),
    name: "Codex",
    repos: [],
    agent_avatar_id: demoAgentAvatarID(2),
  },
  {
    id: "pi-agent",
    pod_name: "tank-demo-pi-agent",
    owner: "preview",
    status: "Active",
    mode: "pi_cli",
    requested_at: new Date(Date.now() - 3 * 60 * 60 * 1000 - 3 * 1000).toISOString(),
    created_at: new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString(),
    ready_at: new Date(Date.now() - 3 * 60 * 60 * 1000 + 85 * 1000).toISOString(),
    name: "Pi",
    repos: [],
    agent_avatar_id: demoAgentAvatarID(3),
  },
];

type AnsiStyle = {
  bold?: boolean;
  dim?: boolean;
  inverse?: boolean;
  fg?: number;
  bg?: number;
};

type AnsiSegment = {
  text: string;
  style: AnsiStyle;
};

const DEMO_CLAUDE_LINES = [
  "\x1b[31m ▐\x1b[40m▛███▜\x1b[49m▌\x1b[39m   \x1b[1mClaude Code\x1b[22m \x1b[37mv2.1.126\x1b[39m",
  "\x1b[31m▝▜\x1b[40m█████\x1b[49m▛▘\x1b[39m  \x1b[37mOpus 4.8 (1M context) · Claude Max\x1b[39m",
  "\x1b[31m  ▘▘ ▝▝  \x1b[39m  \x1b[37m/workspace\x1b[39m",
  "",
  "  \x1b[1m\x1b[91mWelcome to Opus 4.8 xhigh!\x1b[22m\x1b[37m · /effort to tune speed vs. intelligence\x1b[39m",
  "",
  "",
  "",
  "                                                                                  \x1b[37m◉ xhigh · /effort\x1b[39m",
  "\x1b[38;5;244m────────────────────────────────────────────────────────────────────────────────────────────────────\x1b[39m",
  "❯\u00a0\x1b[7m \x1b[27m",
  "\x1b[38;5;244m────────────────────────────────────────────────────────────────────────────────────────────────────\x1b[39m",
  "  \x1b[95m⏵⏵ bypass permissions on\x1b[37m (shift+tab to cycle)\x1b[39m",
];

const DEMO_CODEX_LINES = [
  "\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m",
  "",
  "\x1b[2m  gpt-5.5 default · /workspace\x1b[0m",
  "",
  "\x1b[2m╭─────────────────────────────────────────╮\x1b[0m",
  "\x1b[2m│ >_ \x1b[0;1mOpenAI Codex\x1b[0;2m (v0.128.0)              │\x1b[0m",
  "\x1b[2m│                                         │\x1b[0m",
  "\x1b[2m│ model:       \x1b[0mgpt-5.5\x1b[2m   \x1b[0m\x1b[38;5;6m/model\x1b[2m\x1b[39m to change │\x1b[0m",
  "\x1b[2m│ directory:   \x1b[0m/workspace\x1b[2m                 │\x1b[0m",
  "\x1b[2m│ permissions: \x1b[0;1m\x1b[38;5;5mYOLO mode\x1b[0;2m                  │\x1b[0m",
  "\x1b[2m╰─────────────────────────────────────────╯\x1b[0m",
  "",
  "  \x1b[1mTip:\x1b[0m GPT-5.5 is now available in Codex. It's our strongest agentic coding model yet, built to reason",
  "  through large codebases, check assumptions with tools, and keep going until the work is done.",
  "",
  "  Learn more: https://openai.com/index/introducing-gpt-5-5/",
  "",
  "",
  "\x1b[1m›\x1b[0m \x1b[2mSummarize recent commits\x1b[0m",
  "",
  "\x1b[2m  gpt-5.5 default · /workspace\x1b[0m",
];

const DEMO_PI_LINES = [
  "Pi Coding Agent",
  "",
  "  working directory: /workspace",
  "  context files: AGENTS.md, CLAUDE.md",
  "  tools: read, write, edit, bash",
  "",
  "Type /login to manage providers, /model to switch models, or enter a task.",
  "",
  "> Summarize this repo and run the checks.",
];

const DEMO_HERMES_LINES = [
  "Hermes",
  "",
  "  shared memory: enabled",
  "  transport: app-server bridge",
  "  tools: MCP, shell",
  "",
  "Ask Hermes to inspect cluster state, tools, or project context.",
  "",
  "> Summarize the current task.",
];

const DEMO_LOGIN_MESSAGE = "You aren't logged in. Click the log in button on the bottom left.";

function demoTerminalLines(session: Session, promptText?: string): string[] {
  const template = session.mode === "codex_cli" || session.mode === "codex_gui" || session.mode === "codex_exec_gui" || session.mode === "codex_app_server"
    ? DEMO_CODEX_LINES
    : session.mode === "hermes_gui"
      ? DEMO_HERMES_LINES
    : session.mode === "pi_cli"
      ? DEMO_PI_LINES
      : DEMO_CLAUDE_LINES;
  const lines = [...template];
  if (promptText) {
    if (session.mode === "codex_cli" || session.mode === "codex_gui" || session.mode === "codex_exec_gui" || session.mode === "codex_app_server") {
      lines[lines.length - 1] = `\x1b[1m›\x1b[0m ${promptText}`;
    } else if (session.mode === "pi_cli" || session.mode === "hermes_gui") {
      lines[lines.length - 1] = `> ${promptText}`;
    } else {
      const promptIndex = lines.findIndex((line) => line.startsWith("❯"));
      if (promptIndex >= 0) {
        lines[promptIndex] = `❯\u00a0${promptText}\x1b[7m \x1b[27m`;
      }
    }
  }
  if (!session.id.includes("-preview-")) return lines;
  return [
    ...lines,
    "",
    "Preview session only. The real app creates a Kubernetes pod here.",
  ];
}

function ansiColorClass(code: number | undefined, prefix: "fg" | "bg"): string | null {
  if (code == null) return null;
  return `ansi-${prefix}-${code}`;
}

function ansiStyle(style: AnsiStyle): CSSProperties | undefined {
  const css: CSSProperties = {};
  if (style.fg != null && ANSI_STANDARD_OVERRIDES[style.fg]) {
    css.color = ANSI_STANDARD_OVERRIDES[style.fg];
  } else if (style.fg != null && ANSI_256_OVERRIDES[style.fg]) {
    css.color = ANSI_256_OVERRIDES[style.fg];
  }
  if (style.bg != null && ANSI_STANDARD_OVERRIDES[style.bg]) {
    css.backgroundColor = ANSI_STANDARD_OVERRIDES[style.bg];
  } else if (style.bg != null && ANSI_256_OVERRIDES[style.bg]) {
    css.backgroundColor = ANSI_256_OVERRIDES[style.bg];
  }
  return Object.keys(css).length > 0 ? css : undefined;
}

function applyAnsiCodes(style: AnsiStyle, rawCodes: string): AnsiStyle {
  const codes = rawCodes === "" ? [0] : rawCodes.split(";").map((code) => Number(code || "0"));
  let next = { ...style };
  for (let i = 0; i < codes.length; i += 1) {
    const code = codes[i];
    if (code === 0) next = {};
    else if (code === 1) next.bold = true;
    else if (code === 2) next.dim = true;
    else if (code === 7) next.inverse = true;
    else if (code === 22) {
      next.bold = false;
      next.dim = false;
    } else if (code === 27) next.inverse = false;
    else if (code === 39) delete next.fg;
    else if (code === 49) delete next.bg;
    else if (code >= 30 && code <= 37) next.fg = code - 30;
    else if (code >= 40 && code <= 47) next.bg = code - 40;
    else if (code >= 90 && code <= 97) next.fg = code - 90 + 8;
    else if (code >= 100 && code <= 107) next.bg = code - 100 + 8;
    else if (code === 38 && codes[i + 1] === 5) {
      next.fg = codes[i + 2];
      i += 2;
    } else if (code === 48 && codes[i + 1] === 5) {
      next.bg = codes[i + 2];
      i += 2;
    }
  }
  return next;
}

function ansiSegments(line: string): AnsiSegment[] {
  const segments: AnsiSegment[] = [];
  const re = /\x1b\[([0-9;]*)m/g;
  let style: AnsiStyle = {};
  let pos = 0;
  let match: RegExpExecArray | null;
  while ((match = re.exec(line)) != null) {
    if (match.index > pos) {
      segments.push({ text: line.slice(pos, match.index), style: { ...style } });
    }
    style = applyAnsiCodes(style, match[1]);
    pos = re.lastIndex;
  }
  if (pos < line.length) {
    segments.push({ text: line.slice(pos), style: { ...style } });
  }
  return segments.length > 0 ? segments : [{ text: "\u00a0", style }];
}

function AnsiLine({ line }: { line: string }) {
  return (
    <div className="demo-terminal-line">
      {ansiSegments(line).map((segment, index) => {
        const classes = [
          segment.style.bold ? "ansi-bold" : "",
          segment.style.dim ? "ansi-dim" : "",
          segment.style.inverse ? "ansi-inverse" : "",
          ansiColorClass(segment.style.fg, "fg") ?? "",
          ansiColorClass(segment.style.bg, "bg") ?? "",
        ].filter(Boolean).join(" ");
        return (
          <span key={index} className={classes || undefined} style={ansiStyle(segment.style)}>
            {segment.text}
          </span>
        );
      })}
    </div>
  );
}

function createDemoSession(mode: DefaultSessionMode, index: number): Session {
  const provider = MODE_MENU_ICONS[mode];
  const label = mode === "codex_cli" || mode === "codex_gui" || mode === "codex_exec_gui"
    ? "Codex"
    : mode === "hermes_gui"
      ? "Hermes"
    : mode === "pi_cli"
      ? "Pi"
      : "Claude Code";
  return {
    id: `${provider}-preview-${index}`,
    pod_name: `tank-demo-${provider}-${index}`,
    owner: "preview",
    status: "Active",
    mode,
    requested_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
    ready_at: null,
    name: `${label} ${index}`,
    repos: [],
    agent_avatar_id: demoAgentAvatarID(index),
  };
}

const DEMO_LANDING_LINES = [
  "$ tank-operator preview",
  "Welcome. This is the real app shell with demo sessions.",
  "",
  "Click the provider icon to switch between Claude, Codex, Hermes, and Pi.",
  "Click + to add a local preview session.",
  "The key and wrench buttons are present but disabled in preview mode.",
  "",
  "Sign in from the lower-left profile area when you want real pods.",
];

const DEFAULT_SESSION_MODE_KEY = "tank.defaultSessionMode";
const DEFAULT_INTERACTION_KEY = "tank.defaultInteraction";
const SESSION_INTERACTION_KEY_PREFIX = "tank.sessionInteraction:";

function normalizeSessionMode(value: string | null): string | null {
  return value;
}

function isDefaultSessionMode(value: string | null): value is DefaultSessionMode {
  return (
    value === "claude_cli" ||
    value === "claude_gui" ||
    value === "codex_cli" ||
    value === "codex_gui" ||
    value === "codex_exec_gui" ||
    value === "hermes_gui" ||
    value === "pi_cli"
  );
}

function readDefaultSessionMode(): DefaultSessionMode {
  try {
    const stored = normalizeSessionMode(localStorage.getItem(DEFAULT_SESSION_MODE_KEY));
    if (stored === "codex_app_server") return "codex_gui";
    if (isDefaultSessionMode(stored)) return stored;
  } catch {
    // localStorage can be unavailable in hardened/private browser contexts.
  }
  return "claude_gui";
}

function writeDefaultSessionMode(mode: DefaultSessionMode): void {
  try {
    localStorage.setItem(DEFAULT_SESSION_MODE_KEY, mode);
  } catch {
    // Preference persistence is best-effort; session creation should continue.
  }
}

function readDefaultInteraction(): SessionInteraction {
  try {
    const stored = localStorage.getItem(DEFAULT_INTERACTION_KEY);
    if (stored === "gui" || stored === "cli") return stored;
    if (stored === "terminal") return "cli";
  } catch {}
  // Derive from stored session mode when the interaction preference is absent.
  const mode = readDefaultSessionMode();
  return CHAT_MODES.has(mode) ? "gui" : "cli";
}

function writeDefaultInteraction(interaction: SessionInteraction): void {
  try {
    localStorage.setItem(DEFAULT_INTERACTION_KEY, interaction);
  } catch {}
}

function readSessionInteraction(id: string): SessionInteraction | null {
  try {
    const stored = localStorage.getItem(SESSION_INTERACTION_KEY_PREFIX + id);
    if (stored === "gui" || stored === "cli") return stored;
    if (stored === "run") return "gui";
    if (stored === "newterm" || stored === "terminal") return "cli";
  } catch {}
  return null;
}

function writeSessionInteraction(id: string, interaction: SessionInteraction): void {
  try {
    localStorage.setItem(SESSION_INTERACTION_KEY_PREFIX + id, interaction);
  } catch {}
}

// Recent-repos response shape, mirrored from
// backend cmd/tank-operator/handlers_repos.go → handleGitHubRecentRepos.
interface RecentReposResponse {
  repos: string[];
}

function normalizeSession(session: Session): Session {
  const mode = normalizeSessionMode(session.mode) as SessionMode;
  // The backend includes an activity block on GET /api/sessions
  // (hydrated from the latest session.activity_changed lifecycle event);
  // normalize through the same path snapshot poll responses use so the
  // initial-state hydration agrees with subsequent SSE deltas.
  const activity = session.activity
    ? normalizeSessionActivity({ ...session.activity, session_id: session.id })
    : null;
  const next = mode === session.mode ? { ...session } : { ...session, mode };
  next.session_scope = normalizeSessionScopeValue(session.session_scope);
  next.activity = activity;
  // Defend against degraded snapshots (older server, infoFromPod
  // fallback, hand-rolled JSON in tests): repos must always be an
  // array so downstream renderers can `.map` without a guard.
  next.repos = Array.isArray(session.repos) ? session.repos : [];
  next.clone_state = session.clone_state ?? null;
  next.model = typeof session.model === "string" ? session.model : "";
  next.effort = typeof session.effort === "string" ? session.effort : "";
  next.runtime_model = typeof session.runtime_model === "string" ? session.runtime_model : "";
  next.runtime_effort = typeof session.runtime_effort === "string" ? session.runtime_effort : "";
  next.runtime_configured_at =
    typeof session.runtime_configured_at === "string" ? session.runtime_configured_at : null;
  next.agent_avatar_id =
    typeof session.agent_avatar_id === "string" ? session.agent_avatar_id : null;
  next.system_avatar_id =
    typeof session.system_avatar_id === "string" ? session.system_avatar_id : null;
  return next;
}

function orderSessionsByIds(sessions: Session[], order: string[]): Session[] {
  if (sessions.length < 2 || order.length === 0) return sessions;
  const rank = new Map(order.map((id, index) => [id, index]));
  return [...sessions].sort((a, b) => {
    const aRank = rank.get(a.id);
    const bRank = rank.get(b.id);
    if (aRank == null && bRank == null) return 0;
    if (aRank == null) return 1;
    if (bRank == null) return -1;
    return aRank - bRank;
  });
}

function moveSessionId(order: string[], movedId: string, targetId: string): string[] {
  if (movedId === targetId) return order;
  const fromIndex = order.indexOf(movedId);
  const toIndex = order.indexOf(targetId);
  if (fromIndex < 0 || toIndex < 0) return order;
  const next = [...order];
  const [moved] = next.splice(fromIndex, 1);
  next.splice(toIndex, 0, moved);
  return next;
}

// Modes whose pods carry harvestable credentials — the "save" button
// surfaces on session rows in these modes. Kept as a Set so adding a third
// future config mode doesn't grow an OR chain.
const CONFIG_MODES = new Set<SessionMode>(["config", "codex_config"]);
const CHAT_MODES = new Set<SessionMode>(["claude_gui", "codex_gui", "codex_exec_gui", "codex_app_server", "hermes_gui"]);
const SDK_CHAT_MODES = new Set<SessionMode>(["claude_gui", "codex_gui", "codex_exec_gui", "codex_app_server"]);
const CREATE_TIME_INITIAL_TURN_MODES = new Set<SessionMode>([...SDK_CHAT_MODES, "hermes_gui"]);
const SDK_TIMELINE_TAIL_ROWS = 24;
const SDK_TIMELINE_OLDER_ROWS = 8;
const SDK_TIMELINE_DEEPLINK_ROWS_BEFORE = 12;
const SDK_TIMELINE_DEEPLINK_ROWS_AFTER = 12;
const CLAUDE_ROLLOUT_MODES = new Set<SessionMode>(["claude_cli", "api_key"]);
const CODEX_ROLLOUT_MODES = new Set<SessionMode>(["codex_cli"]);
const GUI_ROLLOUT_MODES = new Set<SessionMode>(["claude_gui", "codex_gui", "codex_exec_gui", "codex_app_server", "hermes_gui"]);
const ROLLOUT_MODES = new Set<SessionMode>([
  ...CLAUDE_ROLLOUT_MODES,
  ...CODEX_ROLLOUT_MODES,
]);
const PROVIDERS: Provider[] = ["anthropic", "codex", "hermes", "pi"];


function defaultModeFor(provider: Provider, interaction: SessionInteraction): DefaultSessionMode {
  const modes = PROVIDER_INTERACTION_MODES[provider];
  return modes[interaction] ?? modes.gui ?? modes.cli ?? "claude_gui";
}

function chatModeForHomePrompt(mode: SessionMode): SessionMode {
  if (CHAT_MODES.has(mode)) return mode;
  const provider = MODE_MENU_ICONS[mode];
  return PROVIDER_INTERACTION_MODES[provider].gui ?? mode;
}

function availableInteractionFor(
  provider: Provider,
  preferred: SessionInteraction,
): SessionInteraction {
  if (PROVIDER_INTERACTION_MODES[provider][preferred] != null) return preferred;
  return PROVIDER_INTERACTION_MODES[provider].gui != null ? "gui" : "cli";
}

function sessionStatusDotClass(
  session: Session,
  activity?: SessionActivitySummary,
): string {
  return `status-dot status-${sessionActivityDotStatus(
    session.status,
    CHAT_MODES.has(session.mode),
    activity,
  )}`;
}

function sessionStatusLabel(
  session: Session,
  activity?: SessionActivitySummary,
): string {
  return sessionActivityStatusLabel(session.status, CHAT_MODES.has(session.mode), activity);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function userIsAdmin(user: SessionUser | null | undefined): boolean {
  return user?.is_admin === true;
}

interface SessionUser {
  sub: string;
  email: string;
  name: string;
  // Platform role carried in the auth.romaine.life JWT. `admin` and
  // `service` bypass the OnboardingWall; `user` is the standard signed-in
  // caller. auth.romaine.life mints `pending` by default; tank-operator
  // rejects that role on direct JWT verification.
  role: SessionRole;
  is_admin: boolean;
  avatar_url: string;
  // Profile fields from /api/auth/me. Null until the user completes the
  // GitHub App install. installation_id presence drives the onboarding
  // wall — null means show the install CTA, non-null means full app.
  github_login: string | null;
  installation_id: number | null;
  // Phase E: cross-device run-pane prefs. Null when the user has never
  // saved prefs; SPA falls back to localStorage + defaults.
  run_prefs: Record<string, unknown> | null;
}

type GlimmungLaunchContext = {
  glimmung_run_ref: string;
  glimmung_issue_ref: string;
  glimmung_touchpoint_ref: string | null;
  validation_url: string | null;
};

const GLIMMUNG_LAUNCH_CONTEXT_KEY = "tank-glimmung-launch-context";

// One-line summaries for the install_error reasons the backend may surface
// via redirect query param after a failed install callback. Anything not in
// the map renders as the raw reason — keeps unknown errors visible without
// hardcoding every variant.
const INSTALL_ERROR_HINTS: Record<string, string> = {
  missing_state: "Install link expired before you returned. Try again.",
  invalid_state: "Install link didn't validate. Try again.",
  missing_installation_id: "GitHub didn't send an installation id. Re-run the install.",
  install_state_unavailable: "Install tracking is unavailable. Try again later.",
  install_state_failed: "Install tracking failed. Try again.",
  pending_approval: "Your install needs an org admin's approval. Once they approve, log in again.",
  session_expired: "Your session expired during install. Sign in again then re-run the install.",
  session_invalid: "Your session token didn't validate. Sign in again.",
  email_mismatch: "The signed-in account doesn't match the install link's email.",
};

function readInstallError(): string | null {
  const params = new URLSearchParams(window.location.search);
  return params.get("install_error");
}

function clearInstallError(): void {
  const url = new URL(window.location.href);
  url.searchParams.delete("install_error");
  window.history.replaceState({}, "", url.toString());
}

function readPendingGitHubInstallState(): string | null {
  const params = new URLSearchParams(window.location.search);
  return params.get("github_install_state");
}

function clearPendingGitHubInstallState(): void {
  const url = new URL(window.location.href);
  url.searchParams.delete("github_install_state");
  window.history.replaceState({}, "", url.toString());
}

function setInstallErrorParam(reason: string): void {
  const url = new URL(window.location.href);
  url.searchParams.delete("github_install_state");
  url.searchParams.set("install_error", reason);
  window.history.replaceState({}, "", url.toString());
}

async function completeGitHubInstall(state: string): Promise<SessionUser> {
  const res = await authedFetch("/api/github/install/complete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ state }),
  });
  if (!res.ok) {
    let detail = `install_complete_failed_${res.status}`;
    try {
      const body = await res.json();
      if (typeof body?.detail === "string") detail = body.detail;
    } catch {
      // Keep the status-derived detail when the response is not JSON.
    }
    throw new Error(detail);
  }
  const body = (await res.json()) as { user: SessionUser };
  return body.user;
}

type SessionRoute = {
  sessionId: string;
  tab: "chat" | "turns";
  turnId: string | null;
};

function decodeRouteSegment(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return "";
  }
}

function readSessionRouteFromPath(pathname = window.location.pathname): SessionRoute | null {
  const parts = pathname.split("/").filter(Boolean).map(decodeRouteSegment);
  if (parts[0] !== "sessions" || !parts[1]) return null;
  if (parts.length === 2) return { sessionId: parts[1], tab: "chat", turnId: null };
  if (parts[2] !== "turns") return { sessionId: parts[1], tab: "chat", turnId: null };
  return {
    sessionId: parts[1],
    tab: "turns",
    turnId: parts[3]?.trim() || null,
  };
}

function sessionRouteUrl(id: string, tab: "chat" | "turns" = "chat", turnId?: string | null): string {
  const url = new URL(window.location.href);
  url.pathname = `/sessions/${encodeURIComponent(id)}${
    tab === "turns"
      ? `/turns${turnId ? `/${encodeURIComponent(turnId)}` : ""}`
      : ""
  }`;
  url.search = "";
  url.hash = "";
  return url.toString();
}

function routeHasMessageTarget(): boolean {
  const params = new URLSearchParams(window.location.search);
  return params.has("message") || params.has("timeline_id");
}

function replaceSessionRoute(id: string, tab: "chat" | "turns" = "chat", turnId?: string | null): void {
  if (routeHasMessageTarget()) return;
  const next = sessionRouteUrl(id, tab, turnId);
  if (next !== window.location.href) window.history.replaceState({}, "", next);
}

function readInitialSessionId(): string | null {
  const route = readSessionRouteFromPath();
  if (route?.sessionId) return route.sessionId;
  const params = new URLSearchParams(window.location.search);
  return params.get("session");
}

function clearInitialSessionId(): void {
  const url = new URL(window.location.href);
  url.searchParams.delete("session");
  window.history.replaceState({}, "", url.toString());
}

function sessionUrl(id: string): string {
  return sessionRouteUrl(id);
}

// Deep link to a specific message inside a session. Read by
// readInitialMessageId() on cold start so we can scroll the active
// session's transcript to the referenced entry. Shape mirrors the
// existing ?session= contract so URLs compose cleanly when an agent
// or human pastes one as a shareable pointer.
function messageUrl(sessionId: string, entryId: string): string {
  const url = new URL(window.location.href);
  url.search = "";
  url.hash = "";
  url.searchParams.set("session", sessionId);
  url.searchParams.set("message", entryId);
  return url.toString();
}

function readInitialMessageId(): string | null {
  const params = new URLSearchParams(window.location.search);
  return params.get("message");
}

function clearInitialMessageId(): void {
  const url = new URL(window.location.href);
  url.searchParams.delete("message");
  window.history.replaceState({}, "", url.toString());
}

function defaultSessionName(session: Pick<Session, "id" | "pod_name">): string {
  return (session.pod_name ?? session.id).replace(/^session-/, "").slice(0, 8);
}

type SessionDisplayNameSource = "durable" | "generated";

function sessionDisplayNameParts(session: Session): {
  value: string;
  source: SessionDisplayNameSource;
} {
  if (session.name != null) {
    return { value: session.name, source: "durable" };
  }
  return { value: defaultSessionName(session), source: "generated" };
}

function sessionDisplayName(session: Session): string {
  return sessionDisplayNameParts(session).value;
}

function sessionListDebugRow(session: Session): SessionListDebugRow {
  const avatar = getSessionAvatarByID(session.agent_avatar_id);
  const displayName = sessionDisplayNameParts(session);
  return {
    id: session.id,
    name: session.name,
    display_name: displayName.value,
    display_name_source: displayName.source,
    pod_name: session.pod_name,
    mode: session.mode,
    status: session.status,
    visible: true,
    row_version: typeof session.row_version === "number" ? session.row_version : null,
    sidebar_position:
      typeof session.sidebar_position === "number" ? session.sidebar_position : null,
    agent_avatar_id: session.agent_avatar_id ?? null,
    system_avatar_id: session.system_avatar_id ?? null,
    rendered_avatar_id: avatar?.id ?? null,
    rendered_avatar_src: avatar?.src ?? null,
    rendered_avatar_custom: avatar?.custom === true,
    requested_at: session.requested_at,
    created_at: session.created_at,
    ready_at: session.ready_at,
  };
}

function sessionListDebugRows(sessions: readonly Session[]): SessionListDebugRow[] {
  return sessions.map(sessionListDebugRow);
}

function selectTitleInputText(event: ReactFocusEvent<HTMLInputElement>): void {
  const input = event.currentTarget;
  const select = () => {
    if (document.activeElement === input) input.select();
  };
  select();
  window.requestAnimationFrame(select);
}

function formatRuntime(ms: number): string {
  const minutes = Math.max(0, Math.floor(ms / 60_000));
  if (minutes < 1) return "<1m";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  return `${days}d`;
}

function formatBootTime(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 1) return "<1s";
  if (seconds < 60) return `${seconds}s`;
  return formatRuntime(ms);
}

function currentSessionSkillState(
  testState?: TestState | null,
  rolloutState?: RolloutState | null,
): SkillStateName | null {
  const testActive = testState?.active;
  const rolloutActive = rolloutState?.active;
  if (!testActive && !rolloutActive) return null;
  if (testActive && !rolloutActive) return "test";
  if (rolloutActive && !testActive) return "rollout";
  return null;
}

interface ComposerUsageRingProps {
  tokensUsed: number;
  contextWindow: number;
  placeholder?: boolean;
  ariaLabel?: string;
  title?: string;
}

function ComposerUsageRing({
  tokensUsed,
  contextWindow,
  placeholder = false,
  ariaLabel = "Context usage",
  title,
}: ComposerUsageRingProps) {
  const safeContextWindow = Math.max(contextWindow, 1);
  const usagePct = Math.min(100, (tokensUsed / safeContextWindow) * 100);
  const usageLevel = usagePct >= 75 ? "high" : usagePct >= 50 ? "mid" : "low";
  const displayPct = usagePct.toFixed(usagePct < 10 ? 1 : 0);

  return (
    <span
      className={`run-usage-ring${placeholder ? " is-placeholder" : ""}`}
      aria-label={ariaLabel}
      aria-disabled={placeholder || undefined}
      title={title ?? `${tokensUsed.toLocaleString()} / ${contextWindow.toLocaleString()} tokens`}
      data-level={placeholder ? undefined : usageLevel}
    >
      <svg className="run-usage-ring-svg" viewBox="0 0 32 32" aria-hidden="true">
        <circle
          cx="16"
          cy="16"
          r="13"
          fill="none"
          stroke="currentColor"
          strokeOpacity="0.18"
          strokeWidth="2.5"
        />
        <circle
          cx="16"
          cy="16"
          r="13"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.5"
          strokeLinecap="round"
          strokeDasharray={`${(usagePct / 100) * (2 * Math.PI * 13)} ${2 * Math.PI * 13}`}
          transform="rotate(-90 16 16)"
        />
      </svg>
      <span className="run-usage-ring-text">{displayPct}%</span>
    </span>
  );
}

function sessionSkillStateClass(session: Session): string {
  const currentSkill = currentSessionSkillState(session.test_state, session.rollout_state);
  if (currentSkill === "test") return " is-skill-test";
  if (currentSkill === "rollout") return " is-skill-rollout";
  return "";
}

function sessionBootStartMs(session: Session): number {
  const requestedMs = session.requested_at ? Date.parse(session.requested_at) : NaN;
  if (Number.isFinite(requestedMs)) return requestedMs;
  const createdMs = session.created_at ? Date.parse(session.created_at) : NaN;
  return Number.isFinite(createdMs) ? createdMs : NaN;
}

// SessionStats renders the sidebar's ↓ boot-time and ↑ runtime
// labels for one session row. The hooks (useRelativeSeconds /
// useRelativeMinutes) subscribe to the singleton timeService at the
// granularity the label actually displays, so re-renders only happen
// when *this row's* bucket boundary is crossed — not on every clock
// tick at the App root. Replaces the prior App-root `nowMs` ticker
// that cascaded a full-tree re-render every 30s (and every 1s while
// any session was Pending), which was the dominant
// `correlation=idle` long-task source observed in
// `tank_client_long_task_duration_seconds`.
function SessionStats({ session }: { session: Session }) {
  const bootStartMs = sessionBootStartMs(session);
  const readyMs = session.ready_at ? Date.parse(session.ready_at) : NaN;
  const bootFinalMs =
    Number.isFinite(readyMs) && Number.isFinite(bootStartMs)
      ? readyMs - bootStartMs
      : null;
  // Live boot ticker only runs while Pending and we don't yet have a
  // final ready_at. Passing null here keeps the hook subscribed but
  // makes its getSnapshot return null — useSyncExternalStore then
  // dedupes and no re-render fires.
  const liveBootStart =
    bootFinalMs === null && session.status === "Pending" && Number.isFinite(bootStartMs)
      ? bootStartMs
      : null;
  const liveBootSeconds = useRelativeSeconds(liveBootStart);
  let bootLabel: string | null = null;
  let bootTitle = "startup time unknown";
  if (bootFinalMs !== null) {
    bootLabel = formatBootTime(bootFinalMs);
    bootTitle = `ready ${bootLabel} after request`;
  } else if (liveBootStart !== null && liveBootSeconds !== null) {
    bootLabel = formatBootTime(liveBootSeconds * 1000);
    bootTitle = `starting for ${bootLabel} since request`;
  }

  const createdMs = session.created_at ? Date.parse(session.created_at) : NaN;
  const runtimeStartedAt = Number.isFinite(createdMs) ? createdMs : null;
  const runtimeMinutes = useRelativeMinutes(runtimeStartedAt);
  const runtimeLabel =
    runtimeStartedAt !== null && runtimeMinutes !== null
      ? formatRuntime(runtimeMinutes * 60_000)
      : null;
  const runtimeTitle = runtimeLabel ? `running ${runtimeLabel}` : "runtime unknown";

  if (!bootLabel && !runtimeLabel) return null;
  return (
    <span className="session-stats">
      {bootLabel && (
        <span className="session-stat" title={bootTitle} aria-label={bootTitle}>
          <span aria-hidden="true">↓</span>
          <span>{bootLabel}</span>
        </span>
      )}
      {runtimeLabel && (
        <span className="session-stat" title={runtimeTitle} aria-label={runtimeTitle}>
          <span aria-hidden="true">↑</span>
          <span>{runtimeLabel}</span>
        </span>
      )}
    </span>
  );
}

function readGlimmungLaunchContext(): GlimmungLaunchContext | null {
  const params = new URLSearchParams(window.location.search);
  const runRef = params.get("glimmung_run_ref");
  const issueRef = params.get("glimmung_issue_ref");
  const touchpointRef = params.get("glimmung_touchpoint_ref");
  if (!runRef || !issueRef) {
    try {
      const stored = window.sessionStorage.getItem(GLIMMUNG_LAUNCH_CONTEXT_KEY);
      if (!stored) return null;
      const parsed = JSON.parse(stored) as Partial<GlimmungLaunchContext>;
      if (!parsed.glimmung_run_ref || !parsed.glimmung_issue_ref) return null;
      return {
        glimmung_run_ref: parsed.glimmung_run_ref,
        glimmung_issue_ref: parsed.glimmung_issue_ref,
        glimmung_touchpoint_ref: parsed.glimmung_touchpoint_ref ?? null,
        validation_url: parsed.validation_url ?? null,
      };
    } catch {
      return null;
    }
  }

  const context = {
    glimmung_run_ref: runRef,
    glimmung_issue_ref: issueRef,
    glimmung_touchpoint_ref: touchpointRef,
    validation_url: params.get("validation_url"),
  };
  try {
    window.sessionStorage.setItem(GLIMMUNG_LAUNCH_CONTEXT_KEY, JSON.stringify(context));
  } catch {
    // Storage may be unavailable in hardened/private browser contexts.
  }
  return context;
}

function clearGlimmungLaunchContext(): void {
  try {
    window.sessionStorage.removeItem(GLIMMUNG_LAUNCH_CONTEXT_KEY);
  } catch {
    // Storage may be unavailable in hardened/private browser contexts.
  }
  const url = new URL(window.location.href);
  for (const key of [
    "glimmung_run_ref",
    "glimmung_issue_ref",
    "glimmung_touchpoint_ref",
    "validation_url",
  ]) {
    url.searchParams.delete(key);
  }
  window.history.replaceState({}, "", url.toString());
}

// The prior 1.5s pending-session polling constant was deleted in
// tank-operator#83. Sidebar updates flow through the typed-SSE stream
// on /api/sessions/events driven by the session_lifecycle_events
// ledger; the polling loop (only active while any session was
// non-Active) is no longer needed.
//
// Sidebar boot/runtime labels subscribe to the singleton timeService
// (see SessionStats above). The previous design held `nowMs` as App-
// root useState and ticked it from a setInterval, which cascaded a
// full-tree re-render every 30s (or every 1s while any session was
// Pending) — observed as 5+ second `correlation=idle` blocks in
// `tank_client_long_task_duration_seconds`. Per-row hooks now subscribe
// at the granularity each label actually renders at.
const HOME_DEFAULT_SESSION_TITLE = "New session";

function normalizedHomeTitleNameFrom(value: string, fromDefault: boolean): string | null {
  const trimmed = value.trim();
  if (fromDefault && (trimmed === "" || trimmed === HOME_DEFAULT_SESSION_TITLE)) {
    return null;
  }
  return trimmed === "" ? null : trimmed;
}

function WorkspaceTitleSpacer() {
  return <span className="workspace-title-spacer" aria-hidden="true" />;
}

function altArrowSessionDirection(event: KeyboardEvent): -1 | 1 | null {
  if (!event.altKey || event.shiftKey || event.ctrlKey || event.metaKey) return null;
  if (event.key === "ArrowUp") return -1;
  if (event.key === "ArrowDown") return 1;
  return null;
}

function isSessionShortcutEditableTarget(_target: EventTarget | null): boolean {
  return false;
}

function isTextEntryShortcutTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  const editable = target.closest("[contenteditable]");
  if (editable && editable.getAttribute("contenteditable") !== "false") return true;
  const field = target.closest("input, textarea, select");
  if (!field) return false;
  if (field instanceof HTMLInputElement) {
    const type = field.type.toLowerCase();
    return ![
      "button",
      "checkbox",
      "color",
      "file",
      "hidden",
      "image",
      "radio",
      "range",
      "reset",
      "submit",
    ].includes(type);
  }
  return true;
}

function shortcutSessionId(target: EventTarget | null): string | null {
  if (!(target instanceof Element)) return null;
  const sessionEl = target.closest("[data-session-id]") as HTMLElement | null;
  return sessionEl?.dataset.sessionId ?? null;
}

function adjacentSessionId(
  sessions: Session[],
  currentId: string | null,
  direction: -1 | 1,
  excludedIds = new Set<string>(),
): string | null {
  const selectable = sessions.filter((session) => !excludedIds.has(session.id));
  if (selectable.length === 0) return null;

  const currentIndex = currentId == null
    ? -1
    : selectable.findIndex((session) => session.id === currentId);
  if (currentIndex < 0) {
    return direction > 0 ? selectable[0].id : selectable[selectable.length - 1].id;
  }

  const nextIndex = (currentIndex + direction + selectable.length) % selectable.length;
  return selectable[nextIndex].id;
}

function IconPlus() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" fill="none"
         stroke="currentColor" strokeWidth="2" strokeLinecap="round">
      <line x1="8" y1="3.5" x2="8" y2="12.5" />
      <line x1="3.5" y1="8" x2="12.5" y2="8" />
    </svg>
  );
}

function IconWrench({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden="true"
    >
      <path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.8-3.8a6 6 0 0 1-7.9 7.9l-6.9 6.9a2.1 2.1 0 0 1-3-3l6.9-6.9a6 6 0 0 1 7.9-7.9l-3.8 3.8Z" />
    </svg>
  );
}

function IconKey({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 16 16"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden="true"
    >
      <circle cx="5.25" cy="8" r="2.6" />
      <path d="M7.85 8h5.15" />
      <path d="M11 8v2" />
      <path d="M13 8v2.2" />
    </svg>
  );
}

function IconPanelToggle({ collapsed }: { collapsed: boolean }) {
  return (
    <svg
      viewBox="0 0 16 16"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden="true"
    >
      <rect x="2.25" y="2.25" width="11.5" height="11.5" rx="2" />
      <path d="M6 2.5v11" />
      {collapsed ? <path d="M9 6 11 8 9 10" /> : <path d="M11 6 9 8l2 2" />}
    </svg>
  );
}

function IconKebab() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor">
      <circle cx="8" cy="3" r="1.3" />
      <circle cx="8" cy="8" r="1.3" />
      <circle cx="8" cy="13" r="1.3" />
    </svg>
  );
}

function IconClose() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" fill="none"
         stroke="currentColor" strokeWidth="2" strokeLinecap="round">
      <line x1="4" y1="4" x2="12" y2="12" />
      <line x1="12" y1="4" x2="4" y2="12" />
    </svg>
  );
}

function IconExternal() {
  return (
    <svg viewBox="0 0 16 16" width="12" height="12" fill="none"
         stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
      <path d="M10 3h3v3" />
      <path d="M13 3 8 8" />
      <path d="M11.5 9V13H3V4.5h4" />
    </svg>
  );
}

function IconGithub() {
  return (
    <svg viewBox="0 0 24 24" width="13" height="13" fill="currentColor" aria-hidden="true">
      <path d="M12 .5C5.7.5.7 5.6.7 11.9c0 5 3.3 9.3 7.8 10.8.6.1.8-.3.8-.6v-2c-3.2.7-3.9-1.4-3.9-1.4-.5-1.3-1.3-1.7-1.3-1.7-1-.7.1-.7.1-.7 1.1.1 1.7 1.2 1.7 1.2 1 .1.6 2.4 4 .7.1-.7.4-1.2.7-1.5-2.5-.3-5.2-1.3-5.2-5.6 0-1.2.4-2.3 1.2-3.1-.1-.3-.5-1.5.1-3.1 0 0 .9-.3 3.1 1.2.9-.3 1.8-.4 2.8-.4s1.9.1 2.8.4c2.1-1.5 3.1-1.2 3.1-1.2.6 1.6.2 2.8.1 3.1.7.8 1.2 1.9 1.2 3.1 0 4.3-2.6 5.3-5.2 5.6.4.3.8 1 .8 2.1V22c0 .3.2.7.8.6 4.6-1.5 7.8-5.8 7.8-10.8C23.3 5.6 18.3.5 12 .5Z" />
    </svg>
  );
}

function sessionInteractionForSession(session: Session): SessionInteraction | null {
  const stored = readSessionInteraction(session.id);
  if (stored) return stored;
  if (CHAT_MODES.has(session.mode)) return "gui";
  return session.mode === "claude_cli" || session.mode === "codex_cli" || session.mode === "pi_cli"
    ? "cli"
    : null;
}

function InteractionIcon({
  interaction,
  className,
}: {
  interaction: SessionInteraction;
  className?: string;
}) {
  const Icon: LucideIcon = interaction === "gui" ? MonitorIcon : TerminalIcon;
  return <Icon className={className} aria-hidden="true" />;
}

function ModeChip({ mode, interaction }: { mode: SessionMode; interaction?: SessionInteraction | null }) {
  const icon = MODE_CHIP_ICONS[mode];
  const label = MODE_CHIP_LABELS[mode] ?? mode;
  const interactionLabel = interaction ? INTERACTION_LABELS[interaction] : null;

  if (icon) {
    return (
      <>
        <span
          className="mode mode-icon-only mode-provider-chip"
          title={MODE_LABELS[mode]}
          aria-label={MODE_LABELS[mode]}
        >
          <ProviderIcon provider={icon} className="mode-provider-icon" />
          <span className="sr-only">{label}</span>
        </span>
        {interaction && (
          <span
            className="mode mode-icon-only mode-interaction-chip"
            title={interactionLabel ?? undefined}
            aria-label={interactionLabel ?? undefined}
          >
            <InteractionIcon interaction={interaction} className="mode-interaction-icon" />
          </span>
        )}
      </>
    );
  }

  return (
    <span
      className={`mode mode-${mode}`}
      title={MODE_LABELS[mode]}
      aria-label={MODE_LABELS[mode]}
    >
      {label}
    </span>
  );
}

function TankIcon({
  className,
  size,
  strokeWidth,
}: {
  className?: string;
  size?: number;
  strokeWidth?: number;
}) {
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox="0 0 64 64"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth ?? 2.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden="true"
    >
      <rect x="8" y="28" width="40" height="14" rx="3" />
      <circle cx="16" cy="46" r="5" />
      <circle cx="40" cy="46" r="5" />
      <line x1="48" y1="32" x2="58" y2="32" />
      <rect x="22" y="20" width="14" height="8" rx="1.5" />
    </svg>
  );
}

function initials(user: SessionUser): string {
  const source = (user.name || user.email || "?").trim();
  const parts = source.split(/[\s@._-]+/).filter(Boolean);
  const first = parts[0]?.[0] ?? source[0];
  const second = parts[1]?.[0] ?? "";
  return (first + second).toUpperCase().slice(0, 2);
}

function largerProfileAvatarURL(raw: string): string {
  try {
    const url = new URL(raw);
    if (url.hostname.endsWith("gravatar.com")) {
      url.searchParams.set("s", "512");
    }
    return url.toString();
  } catch {
    return raw;
  }
}

function Avatar({ user }: { user: SessionUser }) {
  const [failed, setFailed] = useState(false);
  if (failed || !user.avatar_url) {
    return <span className="avatar" aria-hidden="true">{initials(user)}</span>;
  }
  const openPreview = (
    event:
      | ReactMouseEvent<HTMLSpanElement>
      | ReactKeyboardEvent<HTMLSpanElement>,
  ) => {
    openAvatarPreview(
      {
        name: user.name || user.email,
        avatarSrc: largerProfileAvatarURL(user.avatar_url),
        kind: "personal",
      },
      event,
    );
  };
  return (
    <span
      className="avatar avatar-image"
      role="button"
      tabIndex={0}
      aria-label={`Preview ${user.name || user.email}`}
      onClick={openPreview}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") openPreview(event);
      }}
    >
      <img src={user.avatar_url} alt="" onError={() => setFailed(true)} />
    </span>
  );
}

function MissingSessionAvatarIcon({ className }: { className?: string }) {
  return (
    <span
      className={["session-avatar-missing", className].filter(Boolean).join(" ")}
      title="avatar assignment missing"
      aria-label="avatar assignment missing"
    >
      <ImageIcon size={17} strokeWidth={2.1} aria-hidden="true" />
    </span>
  );
}

function SessionAvatarIcon({
  avatar,
  className,
}: {
  avatar: AgentAvatar | null;
  className?: string;
}) {
  if (!avatar) return <MissingSessionAvatarIcon className={className} />;
  return <AgentAvatarIcon avatar={avatar} className={className} />;
}

// ClusterHealthWidget owns its own polling. The 30s setInterval +
// state setters previously lived at App-root, which cascaded a
// full-tree re-render every 30s — observed as `correlation=idle`
// blocks in `tank_client_long_task_duration_seconds`. Co-locating the
// polling here keeps the re-render scoped to this widget. `enabled`
// gates the poll (only run when the user is signed in); flipping it
// false tears down the state so a signed-out / styleguide render
// stays at zero work.
function ClusterHealthWidget({ enabled }: { enabled: boolean }) {
  const [health, setHealth] = useState<ClusterHealthResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    if (!enabled) return;
    setLoading(true);
    try {
      const res = await authedFetch("/api/cluster-health");
      if (!res.ok) throw new Error(`cluster health failed: ${res.status}`);
      setHealth((await res.json()) as ClusterHealthResponse);
      setError(null);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }, [enabled]);

  useEffect(() => {
    if (!enabled) {
      setHealth(null);
      setError(null);
      setLoading(false);
      return;
    }
    void load();
    const interval = window.setInterval(() => {
      void load();
    }, 30_000);
    return () => window.clearInterval(interval);
  }, [enabled, load]);

  const onRefresh = useCallback(() => {
    void load();
  }, [load]);

  const status: ClusterHealthStatus = error ? "unknown" : health?.status ?? "unknown";
  const headline = error ? "Cluster unknown" : clusterHealthHeadline(health);
  const issue = error || clusterHealthIssueText(health);
  const nodes = health?.nodes;
  const sessions = health?.sessions;
  const nats = health?.nats;
  return (
    <section
      className={`cluster-health ${clusterHealthStatusClass(status)}`}
      aria-label="Cluster health"
    >
      <div className="cluster-health-panel">
        <button
          type="button"
          className="cluster-health-main"
          onClick={onRefresh}
          title={issue}
          aria-label={`${headline}: ${issue}`}
        >
          <span className="cluster-health-status" aria-hidden="true">
            {loading ? (
              <Loader2Icon className="cluster-health-spin" />
            ) : status === "healthy" ? (
              <CheckIcon />
            ) : status === "critical" ? (
              <AlertCircleIcon />
            ) : (
              <ActivityIcon />
            )}
          </span>
          <span className="cluster-health-body">
            <span className="cluster-health-title">{headline}</span>
            <span className="cluster-health-sub">{issue}</span>
          </span>
          <span className="cluster-health-refresh" aria-hidden="true">
            <RotateCcwIcon />
          </span>
        </button>
        <dl
          className="cluster-health-metrics"
          aria-label="Cluster health metrics"
          aria-hidden={health ? undefined : "true"}
        >
          <div className="cluster-health-metric" title="Ready Kubernetes nodes">
            <dt>Nodes</dt>
            <dd>
              <MonitorIcon aria-hidden="true" />
              <span>{nodes ? `${nodes.ready}/${nodes.total}` : "-/-"}</span>
            </dd>
          </div>
          <div className="cluster-health-metric" title="Ready Tank session pods">
            <dt>Sessions</dt>
            <dd>
              <SquareTerminalIcon aria-hidden="true" />
              <span>{sessions ? `${sessions.ready}/${sessions.total}` : "-/-"}</span>
            </dd>
          </div>
          <div className="cluster-health-metric" title="Reachable NATS monitors">
            <dt>NATS</dt>
            <dd>
              <ActivityIcon aria-hidden="true" />
              <span>{clusterHealthNatsReachabilityLabel(nats)}</span>
            </dd>
          </div>
        </dl>
      </div>
    </section>
  );
}

function OnboardingWall({
  user,
  onLogout,
}: {
  user: SessionUser;
  onLogout: () => Promise<void>;
}) {
  const [installError, setInstallError] = useState<string | null>(readInstallError);

  function dismissError() {
    setInstallError(null);
    clearInstallError();
  }

  return (
    <div className="welcome">
      <div className="welcome-inner onboarding">
        <h2 className="welcome-title">Connect GitHub</h2>
        <p className="welcome-sub">
          tank-operator needs the <code>tank-operator</code> GitHub App installed on your account so
          your sessions can read and write your repos via mcp-github.
        </p>
        {installError && (
          <pre className="error onboarding-error" onClick={dismissError} title="dismiss">
            {INSTALL_ERROR_HINTS[installError] ?? installError}
          </pre>
        )}
        <a className="btn-primary onboarding-cta" href="/api/github/install/url">
          Install GitHub App
        </a>
        <p className="onboarding-meta">
          Signed in as <strong>{user.email}</strong>.{" "}
          <button className="link-button" onClick={onLogout}>
            sign out
          </button>
        </p>
      </div>
    </div>
  );
}

function DemoLanding() {
  const [demoSessions, setDemoSessions] = useState<Session[]>(DEMO_BASE_SESSIONS);
  const [activeDemoSession, setActiveDemoSession] = useState<string | null>(null);
  const [selectedProvider, setSelectedProvider] = useState<Provider>("anthropic");
  const [demoInteraction, setDemoInteraction] = useState<SessionInteraction>("cli");
  const [demoClaudeModelId, setDemoClaudeModelId] = useState(DEFAULT_CLAUDE_MODEL_ID);
  const [demoClaudeEffortId, setDemoClaudeEffortId] = useState(DEFAULT_CLAUDE_EFFORT_ID);
  const [demoCodexModelId, setDemoCodexModelId] = useState(DEFAULT_CODEX_MODEL_ID);
  const [demoCodexEffortId, setDemoCodexEffortId] = useState(DEFAULT_CODEX_EFFORT_ID);
  const [demoSessionOrdinal, setDemoSessionOrdinal] = useState(DEMO_BASE_SESSIONS.length);
  const [demoPromptMessages, setDemoPromptMessages] = useState<Record<string, string>>({});
  const [demoComposerMode, setDemoComposerMode] = useState<RunComposerMode>("default");
  const demoBodyRef = useRef<HTMLElement | null>(null);
  const demoComposerWrapRef = useRef<HTMLDivElement | null>(null);
  const selected = demoSessions.find((s) => s.id === activeDemoSession) ?? null;
  const selectedMode = defaultModeFor(selectedProvider, demoInteraction);
  const configMode = PROVIDER_CONFIG_MODES[selectedProvider];
  const demoModelOptions =
    selectedProvider === "anthropic"
      ? CLAUDE_MODELS
      : selectedProvider === "codex"
        ? CODEX_MODELS
        : [];
  const demoModelApplies = demoInteraction === "gui" && demoModelOptions.length > 0;
  const selectedDemoModelId =
    selectedProvider === "anthropic"
      ? demoClaudeModelId
      : selectedProvider === "codex"
        ? demoCodexModelId
        : CODEX_ACCOUNT_DEFAULT_MODEL_ID;
  const terminalLines = selected
    ? demoTerminalLines(selected, demoPromptMessages[selected.id])
    : DEMO_LANDING_LINES;

  useEffect(() => {
    const cycleTabs = (event: KeyboardEvent) => {
      const direction = altArrowSessionDirection(event);
      if (!activeDemoSession || direction == null || isSessionShortcutEditableTarget(event.target)) return;
      const nextId = adjacentSessionId(demoSessions, activeDemoSession, direction);
      if (nextId == null) return;
      event.preventDefault();
      event.stopPropagation();
      setActiveDemoSession(nextId);
    };
    window.addEventListener("keydown", cycleTabs, { capture: true });
    return () => window.removeEventListener("keydown", cycleTabs, { capture: true });
  }, [demoSessions, activeDemoSession]);

  const focusDemoComposerTextarea = useCallback((): boolean => {
    const textarea = demoComposerWrapRef.current?.querySelector("textarea") as
      | HTMLTextAreaElement
      | null;
    if (!textarea) return false;
    textarea.focus();
    const cursor = textarea.value.length;
    textarea.setSelectionRange(cursor, cursor);
    return true;
  }, []);

  const focusDemoSetupSection = useCallback((): boolean => {
    if (!demoBodyRef.current) return false;
    demoBodyRef.current.focus({ preventScroll: true });
    return document.activeElement === demoBodyRef.current;
  }, []);

  // Match the authenticated splash Tab toggle on the signed-out preview:
  // textarea ⇄ setup body. Other controls keep the browser's native tab order.
  useEffect(() => {
    if (activeDemoSession !== null) return;
    const toggleDemoFocus = (event: KeyboardEvent) => {
      if (
        event.key !== "Tab" ||
        event.altKey ||
        event.ctrlKey ||
        event.metaKey ||
        event.isComposing
      ) {
        return;
      }
      const textarea = demoComposerWrapRef.current?.querySelector("textarea") as
        | HTMLTextAreaElement
        | null;
      const body = demoBodyRef.current;
      if (!textarea || !body) return;
      if (event.target === textarea) {
        if (!focusDemoSetupSection()) return;
      } else if (event.target === body) {
        if (!focusDemoComposerTextarea()) return;
      } else {
        return;
      }
      event.preventDefault();
      event.stopImmediatePropagation();
    };
    window.addEventListener("keydown", toggleDemoFocus, { capture: true });
    return () => window.removeEventListener("keydown", toggleDemoFocus, { capture: true });
  }, [activeDemoSession, focusDemoComposerTextarea, focusDemoSetupSection]);

  function setDemoProvider(provider: Provider) {
    const interaction = availableInteractionFor(provider, demoInteraction);
    setDemoInteraction(interaction);
    setSelectedProvider(provider);
  }

  function createPreviewSession(mode: SessionMode = selectedMode) {
    const nextOrdinal = demoSessionOrdinal + 1;
    const next = isDefaultSessionMode(mode)
      ? { ...createDemoSession(mode, nextOrdinal), name: null }
      : {
          ...createDemoSession(selectedMode, nextOrdinal),
          id: `${MODE_MENU_ICONS[mode]}-preview-${nextOrdinal}`,
          mode,
          name: null,
        };
    setDemoSessionOrdinal(nextOrdinal);
    setDemoSessions((prev) => [...prev, next]);
    setActiveDemoSession(next.id);
  }

  function deletePreviewSession(id: string) {
    const next = demoSessions.filter((session) => session.id !== id);
    setDemoSessions(next);
    setDemoPromptMessages((prev) => {
      if (!(id in prev)) return prev;
      const nextMessages = { ...prev };
      delete nextMessages[id];
      return nextMessages;
    });
    if (activeDemoSession === id) {
      setActiveDemoSession(null);
    }
  }

  function handleDemoTerminalKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (!selected) return;
    if (event.key !== "Tab" && !event.metaKey && !event.ctrlKey && !event.altKey) {
      event.preventDefault();
    }
    setDemoPromptMessages((prev) => ({ ...prev, [selected.id]: DEMO_LOGIN_MESSAGE }));
  }

  return (
    <div className="shell shell-demo">
      <aside className="sidebar">
        <div className="sidebar-brand">
          <button
            className={`sidebar-home${activeDemoSession == null ? " is-active" : ""}`}
            onClick={() => setActiveDemoSession(null)}
            title="Home"
            aria-label="Home"
            aria-current={activeDemoSession == null ? "page" : undefined}
          >
            <span className="sidebar-home-label">tank-operator</span>
          </button>
        </div>

        <div className="sidebar-list">
          <div className="sidebar-list-head">
            <div className="sidebar-section-label">Preview sessions</div>
            <button
              className="sidebar-new-session"
              onClick={() => setActiveDemoSession(null)}
              aria-label="New session"
              title="new session"
            >
              <span className="row-icon"><IconPlus /></span>
            </button>
          </div>
          <ul className="sessions">
            {demoSessions.map((s) => {
              const isActive = s.id === selected?.id;
              const statusDotClass = sessionStatusDotClass(s);
              const avatar = getSessionAvatarByID(s.agent_avatar_id);
              return (
                <li
                  key={s.id}
                  className={isActive ? "is-open" : ""}
                  onClick={() => setActiveDemoSession(s.id)}
                >
                  <SessionAvatarIcon avatar={avatar} className="session-avatar" />
                  <div className="session-row-top">
                    <button className="session-open" onClick={() => setActiveDemoSession(s.id)}>
                      <span className="session-id">{sessionDisplayName(s)}</span>
                    </button>
                    <button
                      className="session-delete"
                      onClick={(e) => {
                        e.stopPropagation();
                        deletePreviewSession(s.id);
                      }}
                      title="delete session"
                      aria-label="delete session"
                    >
                      <IconClose />
                    </button>
                  </div>
                  <div className="session-row-bottom">
                    <span
                      className={statusDotClass}
                      title={s.status}
                      aria-label={`status: ${s.status}`}
                    />
                    <ModeChip mode={s.mode} interaction={sessionInteractionForSession(s)} />
                    <SessionStats session={s} />
                    {s.mode === "claude_cli" && (
                      <span className="session-action session-remote is-icon" title="remote control">
                        <IconExternal />
                      </span>
                    )}
                    {ROLLOUT_MODES.has(s.mode) && (
                      <span
                        className="session-action session-rollout is-icon"
                        title={CODEX_ROLLOUT_MODES.has(s.mode) ? "type $rollout into this Codex session" : "type /rollout into this Claude session"}
                      >
                        <TankIcon className="session-action-tank-icon" />
                      </span>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        </div>

        <div className="sidebar-footer">
          <button className="profile demo-profile demo-sign-in" onClick={() => { startLogin(); }}>
            <span className="profile-text">
              <span className="profile-name">sign in</span>
            </span>
          </button>
        </div>
      </aside>

      <main
        ref={demoBodyRef}
        className="workspace demo-workspace"
        tabIndex={-1}
        aria-label={activeDemoSession == null ? "New session setup" : "Transcript preview"}
      >
        {activeDemoSession == null ? (
          <div className="home">
            <div className="home-inner">
              <section className="home-hero" aria-labelledby="demo-home-title">
                <div>
                  <h2 id="demo-home-title" className="home-title">What do you want to build?</h2>
                  <p className="home-sub">
                    Type below to start a session with the selected runtime.
                  </p>
                </div>
                <span className="home-count">{demoSessions.length} preview session{demoSessions.length === 1 ? "" : "s"}</span>
              </section>

              <div className="home-grid">
                <section className="home-panel" aria-labelledby="demo-home-start-title">
                  <div className="home-panel-head">
                    <h3 id="demo-home-start-title">Configuration</h3>
                    <span className="home-panel-meta">{MODE_LABELS[selectedMode]}</span>
                  </div>
                  <div className="home-choice-grid" role="group" aria-label="provider">
                    {PROVIDERS.map((provider) => {
                      const mode = defaultModeFor(provider, demoInteraction);
                      const providerSelected = provider === selectedProvider;
                      return (
                        <button
                          key={provider}
                          className={`home-choice${providerSelected ? " is-selected" : ""}`}
                          onClick={() => setDemoProvider(provider)}
                          aria-pressed={providerSelected}
                          title={MODE_LABELS[mode]}
                        >
                          <ProviderIcon provider={provider} className="home-choice-icon" />
                          <span>{PROVIDER_LABELS[provider]}</span>
                        </button>
                      );
                    })}
                  </div>
                  <div className="home-choice-grid" role="group" aria-label="interaction">
                    {INTERACTION_OPTIONS.map((interaction) => {
                      const unavailable =
                        PROVIDER_INTERACTION_MODES[selectedProvider][interaction] == null;
                      const interactionSelected = demoInteraction === interaction && !unavailable;
                      return (
                        <button
                          key={interaction}
                          className={`home-choice${interactionSelected ? " is-selected" : ""}`}
                          onClick={() => setDemoInteraction(interaction)}
                          disabled={unavailable}
                          aria-pressed={interactionSelected}
                          title={unavailable ? "not available for this provider" : INTERACTION_LABELS[interaction]}
                        >
                          <InteractionIcon interaction={interaction} className="home-choice-icon" />
                          <span>{INTERACTION_LABELS[interaction]}</span>
                        </button>
                      );
                    })}
                  </div>
                  {demoModelApplies && (
                    <>
                      <div className="home-panel-head home-panel-subhead">
                        <h3>Model</h3>
                        <span className="home-panel-meta">
                          {selectedProvider === "anthropic"
                            ? CLAUDE_EFFORTS.find((effort) => effort.id === demoClaudeEffortId)?.label
                            : selectedProvider === "codex"
                              ? CODEX_EFFORTS.find((effort) => effort.id === demoCodexEffortId)?.label
                              : ""}
                        </span>
                      </div>
                      <div className="home-model-list" role="group" aria-label="model">
                        {demoModelOptions.map((model) => {
                          const modelSelected = model.id === selectedDemoModelId;
                          return (
                            <button
                              key={model.id}
                              className={`home-model${modelSelected ? " is-selected" : ""}`}
                              onClick={() => {
                                if (selectedProvider === "anthropic") setDemoClaudeModelId(model.id);
                                if (selectedProvider === "codex") setDemoCodexModelId(model.id);
                              }}
                              aria-pressed={modelSelected}
                            >
                              <span className="home-model-title">{model.label}</span>
                            </button>
                          );
                        })}
                      </div>
                      {(selectedProvider === "anthropic" || selectedProvider === "codex") && (
                        <div className="home-effort-grid" role="group" aria-label="effort">
                          {(selectedProvider === "anthropic" ? CLAUDE_EFFORTS : CODEX_EFFORTS).map((effort) => {
                            const effortSelected =
                              effort.id === (selectedProvider === "anthropic" ? demoClaudeEffortId : demoCodexEffortId);
                            return (
                              <button
                                key={effort.id}
                                className={`home-model home-effort${effortSelected ? " is-selected" : ""}`}
                                onClick={() => {
                                  if (selectedProvider === "anthropic") setDemoClaudeEffortId(effort.id);
                                  if (selectedProvider === "codex") setDemoCodexEffortId(effort.id);
                                }}
                                aria-pressed={effortSelected}
                                title={effort.hint}
                              >
                                <span className="home-model-title">{effort.label}</span>
                                {effort.hint && <span className="home-model-sub">{effort.hint}</span>}
                              </button>
                            );
                          })}
                        </div>
                      )}
                    </>
                  )}
                </section>

                <section className="home-panel home-panel-actions" aria-labelledby="demo-home-actions-title">
                  <div className="home-panel-head">
                    <h3 id="demo-home-actions-title">Setup</h3>
                  </div>
                  <div className="home-quick-actions">
                    <button className="home-quick-action" onClick={() => createPreviewSession("api_key")}>
                      <IconKey className="home-quick-icon" />
                      <span className="home-quick-main">
                        <span className="home-quick-title">API key</span>
                        <span className="home-quick-sub">{MODE_HINTS["api_key"]}</span>
                      </span>
                    </button>
                    {configMode && (
                      <button className="home-quick-action" onClick={() => createPreviewSession(configMode)}>
                        <IconWrench className="home-quick-icon" />
                        <span className="home-quick-main">
                          <span className="home-quick-title">{MODE_LABELS[configMode]}</span>
                          <span className="home-quick-sub">{MODE_HINTS[configMode]}</span>
                        </span>
                      </button>
                    )}
                  </div>
                </section>
              </div>

              {/* Demo preview of the chat composer. Same `ChatComposer`
                  component the authenticated home and the run pane use;
                  submitting redirects to sign-in instead of creating a
                  session. The icon row mirrors the authenticated home so
                  the demo accurately previews the chat surface. */}
              <div ref={demoComposerWrapRef}>
                <ChatComposer
                  className="run-composer-home run-composer-interactive"
                  placeholder="Sign in to start a session…"
                  onSubmit={() => {
                    startLogin();
                  }}
                  permissionMode={demoComposerMode}
                  onPermissionModeChange={setDemoComposerMode}
                  sendByCtrlEnter={false}
                  toolButtons={
                    <>
                      <button
                        type="button"
                        className="run-composer-icon-btn"
                        disabled
                        aria-label="Attach files"
                        title="Sign in to attach files"
                      >
                        <ImageIcon className="run-composer-icon" aria-hidden="true" />
                      </button>
                      <ComposerUsageRing
                        tokensUsed={0}
                        contextWindow={getContextWindow(selectedDemoModelId)}
                        placeholder
                        ariaLabel="Context usage preview"
                        title="Context usage appears after sign in"
                      />
                      {GUI_ROLLOUT_MODES.has(selectedMode) && (
                        <button
                          type="button"
                          className="run-composer-icon-btn run-composer-action-btn run-rollout-action-btn"
                          disabled
                          aria-label="Start rollout"
                          title="Sign in to use /rollout"
                        >
                          <TankIcon className="run-composer-icon" aria-hidden="true" />
                        </button>
                      )}
                      <button
                        type="button"
                        className="run-composer-icon-btn run-composer-action-btn run-test-action-btn"
                        disabled
                        aria-label="Start test skill"
                        title="Sign in to start a session"
                      >
                        <FlaskConicalIcon className="run-composer-icon" aria-hidden="true" />
                      </button>
                      <button
                        type="button"
                        className="run-composer-icon-btn run-command-menu-btn"
                        disabled
                        aria-label="Show slash commands"
                        title="Sign in to use slash commands"
                      >
                        <MessageSquareIcon className="run-composer-icon" aria-hidden="true" />
                      </button>
                      <button
                        type="button"
                        className="run-composer-icon-btn run-command-menu-btn"
                        disabled
                        aria-label="Show MCP servers"
                        title="Sign in to use MCP servers"
                      >
                        <McpIcon className="run-composer-icon" aria-hidden="true" />
                      </button>
                    </>
                  }
                />
              </div>
            </div>
          </div>
        ) : (
          <div
            className={`demo-terminal${selected?.mode === "claude_cli" || selected?.mode === "claude_gui" ? " is-claude" : " is-codex"}`}
            role="img"
            aria-label="tank-operator terminal preview"
            tabIndex={0}
            onKeyDown={handleDemoTerminalKeyDown}
          >
            <div className="demo-terminal-screen">
              {terminalLines.map((line, index) => (
                <AnsiLine key={index} line={line} />
              ))}
            </div>
          </div>
        )}
      </main>
    </div>
  );
}

type JsonObject = Record<string, unknown>;

function isJsonObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function shortJson(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function nowIso(): string {
  return new Date().toISOString();
}

function appendMeta(
  entries: TranscriptEntry[],
  id: string,
  title: string,
  detail?: string,
  severity: "info" | "error" = "info",
  time: string = nowIso(),
): TranscriptEntry[] {
  return [
    ...entries,
    {
      id,
      kind: "meta",
      time,
      meta: { title, detail, severity },
    },
  ];
}

function skillInvocationTitle(name: string): string {
  return `You started ${name} skill`;
}

function skillActionText(name: string): string {
  return `${name.charAt(0).toUpperCase()}${name.slice(1)} skill`;
}

function skillTrigger(providerIsClaude: boolean, name: string): string {
  return `${providerIsClaude ? "/" : "$"}${name}`;
}

function composeSkillPrompt(mode: SessionMode, name: SkillStateName, text: string): string {
  const trigger = skillTrigger(MODE_MENU_ICONS[mode] === "anthropic", name);
  const trimmed = text.trim();
  return trimmed ? `${trigger}\n\n${trimmed}` : trigger;
}

function composeLaunchUserPrompt(text: string, attachments: { label?: string; name: string }[]): string {
  return composeAttachmentDisplayText(text, attachments);
}

function attachmentDisplayTitle(attachment: { errorMsg?: string; label?: string; name: string }): string {
  if (attachment.errorMsg) return attachment.errorMsg;
  const label = attachment.label || attachment.name;
  return label === attachment.name ? label : `${label} (${attachment.name})`;
}

function initialMessageModeDirective(mode: InitialMessageMode): string {
  if (mode === "diagnose") {
    return [
      "Initial message type: diagnose issue without writing code.",
      "Investigate, gather evidence, and report findings only.",
      "Do not edit files or make code changes unless I explicitly ask in a later message.",
    ].join(" ");
  }
  if (mode === "quality_gaps") {
    return [
      "Initial message type: address this issue and inspect the quality/migration gaps it exposes.",
      "Read /workspace/.tank/docs/quality-timeframes.md and /workspace/.tank/docs/migration-policy.md before planning.",
      "If either policy doc is missing, report that as a session setup gap before proceeding.",
      "Make the required code changes and call out any gaps against those docs.",
    ].join(" ");
  }
  if (mode === "test") {
    return [
      "Initial message type: make code changes and immediately run the test skill for this.",
      "Use the test skill workflow as part of implementation and keep the test environment updated while validating.",
    ].join(" ");
  }
  return "";
}

function composeInitialMessageModePrompt(mode: InitialMessageMode, text: string): string {
  const trimmed = text.trim();
  const directive = initialMessageModeDirective(mode);
  if (!directive) return trimmed;
  return trimmed ? `${directive}\n\n${trimmed}` : directive;
}

function initialMessageModeSkillName(mode: InitialMessageMode): SkillStateName | undefined {
  return mode === "test" ? "test" : undefined;
}

function stripSkillTrigger(name: string, text: string): string {
  const trimmed = text.trim();
  const triggerPattern = new RegExp(`^[$/]${name}(?:\\s+|\\n+)?`, "i");
  return trimmed.replace(triggerPattern, "").trim();
}

function appendSkillInvocation(
  entries: TranscriptEntry[],
  name: string,
  supplementalText = "",
  time: string = nowIso(),
): TranscriptEntry[] {
  if (!name) return entries;
  const suffix = Date.parse(time) || Date.now();
  return [
    ...entries,
    {
      id: `skill-action-${name}-${suffix}`,
      kind: "message" as const,
      role: "user" as const,
      text: skillActionText(name),
      time,
      messageKind: "skill-action",
      skillName: name,
      skillSupplementalText: supplementalText.trim(),
    } as TranscriptEntry,
  ];
}

function advanceTimelineCursor(current: string | null, next: string | null): string | null {
  if (!next) return current;
  if (!current || next > current) return next;
  return current;
}

function sdkConnectionLabel(state: SdkConnectionState): string | null {
  switch (state) {
    case "connecting":
      return "Connecting";
    case "connection_lost":
      return "Connection lost";
    case "resyncing":
      return "Resyncing";
    case "idle":
    case "connected":
      return null;
  }
}

function isScheduleWakeupToolName(name: string | undefined): boolean {
  return (name ?? "").toLowerCase() === "schedulewakeup";
}

function isProviderAbortMessage(message: unknown): boolean {
  return typeof message === "string" && /operation was aborted/i.test(message);
}

function transcriptEntryTerminalResult(entry: TranscriptEntry): SdkTerminalResult | null {
  if (entry.turnTerminalStatus === "completed") return { status: "done" };
  if (entry.turnTerminalStatus === "interrupted") return { status: "stopped" };
  if (entry.turnTerminalStatus === "failed") {
    const metaDetail = entry.kind === "meta" ? entry.meta?.detail : undefined;
    if (isProviderAbortMessage(metaDetail)) return { status: "stopped" };
    return { status: "error", detail: metaDetail };
  }
  return null;
}

function terminalResultForTurn(
  entries: TranscriptEntry[],
  turnId: string,
): SdkTerminalResult | null {
  const trimmedTurnId = turnId.trim();
  if (!trimmedTurnId) return null;
  for (let i = entries.length - 1; i >= 0; i -= 1) {
    const entry = entries[i];
    if (entry.turnId !== trimmedTurnId && entry.activity?.turnId !== trimmedTurnId) continue;
    const terminal = transcriptEntryTerminalResult(entry);
    if (terminal) return terminal;
    if (entry.kind === "turn_activity" && entry.activity?.status === "completed") {
      return { status: "done" };
    }
  }
  return null;
}

function turnIdForBrowserClientNonce(clientNonce: string): string {
  const trimmed = clientNonce.trim();
  let safe = trimmed.replace(/[^A-Za-z0-9_.:-]+/g, "-");
  safe = safe.replace(/-+/g, "-").replace(/^-|-$/g, "");
  if (safe.length >= 6 && safe.length <= 80) return `turn_${safe}`;
  if (safe.length > 80) return `turn_${safe.slice(0, 64)}`;
  return `turn_${trimmed}`;
}

function isClaudeRunMode(mode: SessionMode): boolean {
  return mode === "claude_gui";
}

function isCodexRunMode(mode: SessionMode): boolean {
  return mode === "codex_gui" || mode === "codex_app_server";
}

// (formerly: getRunToolGroupSummary — replaced by RunToolGroup's inline
// summary computation now that AgentTranscript is unused.)

interface ToolVisualConfig {
  Icon: LucideIcon;
  /** CSS class added to the icon span — drives the icon badge treatment. */
  colorClass: string;
  tooltip: string;
}

function isToolSearchEntry(entry: TranscriptEntry): boolean {
  const normalized = [
    entry.toolServer,
    entry.toolAction,
    entry.toolName,
  ]
    .filter((part): part is string => typeof part === "string")
    .join(" ")
    .toLowerCase()
    .replace(/[^a-z0-9]/g, "");
  return normalized.includes("toolsearch");
}

/** Map a tool entry to a Lucide icon + badge treatment. */
function getToolVisualConfig(entry: TranscriptEntry): ToolVisualConfig {
  const name = entry.toolName ?? "";
  if (isToolSearchEntry(entry)) {
    return { Icon: SearchIcon, colorClass: "tool-color-search", tooltip: "Search tool call" };
  }
  if (entry.toolKind === "mcp") {
    return { Icon: McpIcon, colorClass: "tool-color-mcp", tooltip: "MCP connector tool call" };
  }
  if (entry.toolKind === "shell") {
    return { Icon: SquareTerminalIcon, colorClass: "tool-color-bash", tooltip: "Shell command tool call" };
  }
  if (name === "Bash" || name === "command" || name.toLowerCase().includes("bash")) {
    return { Icon: SquareTerminalIcon, colorClass: "tool-color-bash", tooltip: "Shell command tool call" };
  }
  if (name === "Read") {
    return { Icon: FileTextIcon, colorClass: "tool-color-read", tooltip: "File read tool call" };
  }
  if (name === "Write" || name === "Edit" || name === "MultiEdit" || name === "ApplyPatch") {
    return { Icon: SquarePenIcon, colorClass: "tool-color-edit", tooltip: "File edit tool call" };
  }
  if (name === "file change") {
    return { Icon: FileDiffIcon, colorClass: "tool-color-edit", tooltip: "File change" };
  }
  if (name === "Glob" || name === "Grep") {
    return { Icon: SearchIcon, colorClass: "tool-color-search", tooltip: "Search tool call" };
  }
  if (name === "TodoWrite" || name === "Todo") {
    return { Icon: ListChecksIcon, colorClass: "tool-color-todo", tooltip: "Todo list tool call" };
  }
  if (name === "Task" || name === "Agent" || name === "TaskOutput" || name === "TaskStop") {
    return { Icon: BotIcon, colorClass: "tool-color-task", tooltip: "Agent task tool call" };
  }
  if (isScheduleWakeupToolName(name)) {
    return { Icon: TimerIcon, colorClass: "tool-color-plan", tooltip: "Scheduled wakeup tool call" };
  }
  if (name === "ExitPlanMode" || name === "EnterPlanMode") {
    return { Icon: ClipboardListIcon, colorClass: "tool-color-plan", tooltip: "Planning mode tool call" };
  }
  if (name === "WebFetch" || name === "WebSearch") {
    return { Icon: GlobeIcon, colorClass: "tool-color-search", tooltip: "Web tool call" };
  }
  if (name === "Monitor") {
    return { Icon: MonitorIcon, colorClass: "tool-color-bash", tooltip: "Monitor tool call" };
  }
  if (name === "AskUserQuestion") {
    return { Icon: MessageSquareIcon, colorClass: "tool-color-todo", tooltip: "User question tool call" };
  }
  if (name === "RemoteTrigger") {
    return { Icon: PlayIcon, colorClass: "tool-color-plan", tooltip: "Remote trigger tool call" };
  }
  if (name === "NotebookEdit") {
    return { Icon: NotebookPenIcon, colorClass: "tool-color-edit", tooltip: "Notebook edit tool call" };
  }
  if (name === "EnterWorktree" || name === "ExitWorktree") {
    return { Icon: GitBranchIcon, colorClass: "tool-color-plan", tooltip: "Worktree tool call" };
  }
  if (name === "CronCreate" || name === "CronDelete" || name === "CronList") {
    return { Icon: CalendarIcon, colorClass: "tool-color-plan", tooltip: "Cron schedule tool call" };
  }
  if (name === "PushNotification") {
    return { Icon: BellIcon, colorClass: "tool-color-todo", tooltip: "Push notification tool call" };
  }
  if (name.toLowerCase().includes("mcp")) {
    return { Icon: McpIcon, colorClass: "tool-color-mcp", tooltip: "MCP connector tool call" };
  }
  return { Icon: WrenchIcon, colorClass: "tool-color-default", tooltip: "Tool call" };
}

function normalizeToolState(status: string | undefined): string {
  const normalized = (status ?? "completed").toLowerCase();
  if (
    normalized === "started" ||
    normalized === "running" ||
    normalized === "pending" ||
    normalized === "in_progress" ||
    normalized === "in-progress"
  ) {
    return "running";
  }
  if (normalized === "done" || normalized === "success" || normalized === "succeeded") {
    return "completed";
  }
  if (normalized === "warned" || normalized === "warning" || normalized === "result_failed") {
    return "failed";
  }
  return normalized;
}

function isAskUserQuestionTool(entry: TranscriptEntry): boolean {
  return entry.toolName === "AskUserQuestion";
}

function isPendingAskUserQuestionTool(entry: TranscriptEntry): boolean {
  return isAskUserQuestionTool(entry) && normalizeToolState(entry.toolStatus) === "running";
}

// (formerly: transcriptClassNames slot map for AgentTranscript — gone
// now that the inline RunMessages renderer owns class names directly.)

type RunTab = "chat" | "turns" | "background" | "files" | "settings" | "help";
type BackgroundView = "shells" | "detached";

/** A file the user picked / dropped / pasted on the home composer before
 *  a session pod exists. The `file` is kept on the object so it can be
 *  uploaded to `/api/sessions/{id}/files/upload` after the pod is Ready;
 *  `previewUrl` is a `URL.createObjectURL` blob for image thumbnails and
 *  is revoked on remove. */
interface HomePendingAttachment {
  id: string;
  file: File;
  name: string;
  label: string;
  size: number;
  previewUrl?: string;
}

interface ComposerAttachment {
  id: string; // local-only id for keying
  name: string;
  label: string;
  /** Path relative to /workspace. Images land in `screenshots/<n>.<ext>`
   *  (server picks the next free id atomically); other uploads land in
   *  `.attachments/<unix-ns>-<sanitized-name>`. Server-decided either way. */
  path: string;
  /** Full path inside the pod, e.g. "/workspace/screenshots/3.png". */
  absPath: string;
  size: number;
  /** Browser blob URL for the thumbnail (if image). */
  previewUrl?: string;
  /** Either "uploading" while the POST is in flight, or "ready" / "error". */
  status: "uploading" | "ready" | "error";
  errorMsg?: string;
}

interface FileEntry {
  name: string;
  type: "file" | "dir" | "symlink" | "other";
  size: number;
  github_url?: string | null;
}

interface SelectedFile {
  path: string;
  size: number;
  truncated: boolean;
  text: string;
  binary: boolean;
}

interface SkillEntry {
  name: string;
  path: string;
  source: string;
  description: string;
  body_preview: string;
}

interface QueuedMessage {
  id: string;
  text: string;
  displayText?: string;
  displayAttachments?: MessageAttachmentDisplay[];
  skillName?: string;
}

interface ComposedAttachmentPrompt {
  prompt: string;
  displayText: string;
  displayAttachments: MessageAttachmentDisplay[];
}

interface McpServerEntry {
  name: string;
  transport: string;
  target: string;
  source: string;
  enabled: boolean;
}

function joinFilesPath(parent: string, name: string): string {
  if (!parent) return name;
  return `${parent}/${name}`;
}

function parentFilesPath(path: string): string {
  const idx = path.lastIndexOf("/");
  return idx <= 0 ? "" : path.slice(0, idx);
}

function humanFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} kB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "webp", "gif", "svg", "bmp"]);
function isImagePath(path: string): boolean {
  const ext = path.toLowerCase().split(".").pop() ?? "";
  return IMAGE_EXTS.has(ext);
}
// RunComposerMode + PERMISSION_MODE_INFO have moved to ./ChatComposer.tsx
// alongside the shared composer component. They are re-imported above so
// existing call sites in this file continue to compile unchanged.

// Verbs cycled by the streaming status pill. Matches cloudcli's
// ClaudeStatus rotation so the user sees motion even when the model
// hasn't sent any text deltas yet.
// Context-window sizes per model. Used for the usage % ring. Opus 4.8
// ships with the 1M context window enabled by default on the Claude API
// (no separate id), so the menu only exposes a single claude-opus-4-8
// entry. Opus 4.7 keeps its split between the standard 200k id and the
// dedicated claude-opus-4-7-1m id because that is how Anthropic shipped
// it; for everything else, 200k is the shipping default.
const CONTEXT_WINDOW_BY_MODEL: Record<string, number> = {
  "claude-opus-4-8": 1_000_000,
  "claude-opus-4-7-1m": 1_000_000,
  "claude-opus-4-7": 200_000,
  "claude-sonnet-4-6": 200_000,
  "claude-haiku-4-5": 200_000,
  "gpt-5": 128_000,
};

function getContextWindow(modelId: string): number {
  return CONTEXT_WINDOW_BY_MODEL[modelId] ?? 200_000;
}

function contextTokensFromUsage(usage: unknown): number {
  if (!isJsonObject(usage)) return 0;
  const tokenField = (key: string): number => {
    const value = usage[key];
    return typeof value === "number" && Number.isFinite(value) ? value : 0;
  };
  return (
    tokenField("input_tokens") +
    tokenField("cache_creation_input_tokens") +
    tokenField("cache_read_input_tokens")
  );
}

function latestContextTokens(entries: TranscriptEntry[]): number {
  for (let i = entries.length - 1; i >= 0; i -= 1) {
    const total = contextTokensFromUsage(entries[i].turnUsage);
    if (total > 0) return total;
  }
  return 0;
}

interface SlashCommand {
  name: string; // includes leading slash, e.g. "/clear"
  desc: string;
}

// Built-ins plus dynamically discovered SKILL.md entries from the session pod.
// Mirrors CloudCLI's command-palette pattern: expose installed agent
// affordances at the point where users type slash commands.
const SLASH_COMMANDS: SlashCommand[] = [
  { name: "/clear", desc: "Clear the conversation history" },
  { name: "/compact", desc: "Compact the conversation context" },
  { name: "/context", desc: "Show context window usage" },
  { name: "/help", desc: "List available commands" },
  { name: "/init", desc: "Initialize a project" },
  { name: "/model", desc: "Switch model" },
  { name: "/review", desc: "Review the pending changes" },
  { name: "/security-review", desc: "Run a security review" },
  { name: "/usage", desc: "Show token / billing usage" },
];

/**
 * Walks backwards from the cursor to find an active trigger context:
 * `/` for slash-commands or `@` for file-mention. The trigger must be
 * at the start of the textarea or follow whitespace, and there must be
 * no whitespace between it and the cursor. Returns the start offset
 * and the typed query (sans leading trigger). Null if not in context.
 */
function findTriggerContext(
  textarea: HTMLTextAreaElement,
  trigger: "/" | "@",
): { start: number; query: string } | null {
  const value = textarea.value;
  const cursor = textarea.selectionStart ?? 0;
  for (let i = cursor - 1; i >= 0; i--) {
    const c = value[i];
    if (c === trigger) {
      if (i === 0 || /\s/.test(value[i - 1])) {
        return { start: i, query: value.slice(i + 1, cursor) };
      }
      return null;
    }
    if (/\s/.test(c)) return null;
  }
  return null;
}

function findSlashContext(
  textarea: HTMLTextAreaElement,
): { start: number; query: string } | null {
  return findTriggerContext(textarea, "/");
}

function findMentionContext(
  textarea: HTMLTextAreaElement,
): { start: number; query: string } | null {
  return findTriggerContext(textarea, "@");
}

/** Loose fuzzy filter — substring match against the path's lowercased
 *  full string OR basename. Order is: basename matches first, then full
 *  path. Capped to keep the menu tractable. */
function filterMentionPaths(paths: string[], query: string, limit = 30): string[] {
  if (!query) return paths.slice(0, limit);
  const q = query.toLowerCase();
  const basenameMatches: string[] = [];
  const fullMatches: string[] = [];
  for (const p of paths) {
    const lower = p.toLowerCase();
    const base = lower.slice(lower.lastIndexOf("/") + 1);
    if (base.includes(q)) basenameMatches.push(p);
    else if (lower.includes(q)) fullMatches.push(p);
    if (basenameMatches.length + fullMatches.length >= limit * 2) break;
  }
  return [...basenameMatches, ...fullMatches].slice(0, limit);
}

function filterSlashCommands(commands: SlashCommand[], query: string): SlashCommand[] {
  if (!query) return commands;
  const q = query.toLowerCase();
  return commands.filter(
    (c) => c.name.slice(1).toLowerCase().includes(q) || c.desc.toLowerCase().includes(q),
  );
}

interface ModelOption {
  id: string; // value passed to backend (claude-cli's --model arg)
  label: string; // display name in dropdown + provider card
}

const CODEX_ACCOUNT_DEFAULT_MODEL_ID = "codex-account-default";

// CLAUDE_MODELS is ordered with the agent-runner's DEFAULT_MODEL first
// (claude-opus-4-8) so a fresh session lands on the strongest model by
// default. Opus 4.7 stays listed as a secondary option for the first
// few weeks after 4.8 ships so users have an escape hatch if 4.8
// misbehaves on day one; drop it once 4.8 has bedded in. The id
// strings are forwarded straight to the SDK's options.model via the
// bus; tightening to an allowlist lives in the agent-runner's pinning
// code, not here, because adding a model should be a one-line UI
// change.
const CLAUDE_MODELS: ModelOption[] = [
  { id: "claude-opus-4-8", label: "Claude · Opus 4.8" },
  { id: "claude-opus-4-7", label: "Claude · Opus 4.7" },
  { id: "claude-sonnet-4-6", label: "Claude · Sonnet 4.6" },
  { id: "claude-haiku-4-5", label: "Claude · Haiku 4.5" },
];
const CODEX_MODELS: ModelOption[] = [
  { id: "gpt-5.5", label: "Codex · GPT-5.5" },
  { id: "gpt-5.4", label: "Codex · GPT-5.4" },
  { id: "gpt-5.4-mini", label: "Codex · GPT-5.4 Mini" },
  { id: "gpt-5.3-codex", label: "Codex · GPT-5.3 Codex" },
  { id: "gpt-5.3-codex-spark", label: "Codex · GPT-5.3 Codex Spark" },
  { id: CODEX_ACCOUNT_DEFAULT_MODEL_ID, label: "Codex · Account default" },
];

// Extended-thinking effort levels exposed by the Claude Agent SDK
// (EffortLevel union). The ids are the wire values; the labels carry
// the cost guidance so users picking xhigh/max know what they're
// opting into. Keep in lockstep with:
//   - backend-go/cmd/tank-operator/middleware.go allowedClaudeEfforts
//     (server-side allowlist)
//   - agent-runner/src/runner.ts DEFAULT_EFFORT (the "high" fallback)
interface EffortOption {
  id: string;
  label: string;
  hint?: string;
}
const CLAUDE_EFFORTS: EffortOption[] = [
  { id: "low", label: "Low", hint: "Fastest, minimal thinking" },
  { id: "medium", label: "Medium", hint: "Moderate thinking" },
  { id: "high", label: "High", hint: "Deep reasoning (default)" },
  { id: "xhigh", label: "Extra High", hint: "Opus 4.7/4.8 only; ~2× tokens" },
  { id: "max", label: "Max", hint: "Opus 4.6/4.7/4.8, Sonnet 4.6 only" },
];
const DEFAULT_CLAUDE_MODEL_ID = "claude-opus-4-8";
const DEFAULT_CLAUDE_EFFORT_ID = "high";
const CODEX_EFFORTS: EffortOption[] = [
  { id: "low", label: "Low", hint: "Fast responses with lighter reasoning" },
  { id: "medium", label: "Medium", hint: "Balanced reasoning" },
  { id: "high", label: "High", hint: "Greater reasoning depth" },
  { id: "xhigh", label: "Extra High", hint: "Strongest reasoning" },
];
const DEFAULT_CODEX_MODEL_ID = "gpt-5.5";
const DEFAULT_CODEX_EFFORT_ID = "xhigh";

function modelOptionsForMode(mode: SessionMode): ModelOption[] {
  if (mode === "claude_gui") return CLAUDE_MODELS;
  if (mode === "codex_gui" || mode === "codex_exec_gui" || mode === "codex_app_server") {
    return CODEX_MODELS;
  }
  return [];
}

function effortOptionsForMode(mode: SessionMode): EffortOption[] {
  if (mode === "claude_gui") return CLAUDE_EFFORTS;
  if (mode === "codex_gui" || mode === "codex_exec_gui" || mode === "codex_app_server") {
    return CODEX_EFFORTS;
  }
  return [];
}

function modelDisplayLabel(mode: SessionMode, modelId: string): string {
  const trimmed = modelId.trim();
  if (!trimmed && (mode === "codex_gui" || mode === "codex_exec_gui" || mode === "codex_app_server")) {
    return "Codex account default";
  }
  if (!trimmed) return "";
  return modelOptionsForMode(mode).find((option) => option.id === trimmed)?.label ?? trimmed;
}

function effortDisplayLabel(mode: SessionMode, effortId: string): string {
  const trimmed = effortId.trim();
  if (!trimmed) return "";
  return effortOptionsForMode(mode).find((option) => option.id === trimmed)?.label ?? trimmed;
}

// Per-user run-pane preferences. localStorage-backed, shared across all
// sessions in this browser. Keys mirror cloudcli's QuickSettings.
const RUN_PREF_PREFIX = "tank-run-pref-";
const TURN_COMPLETE_SOUND_SRC = "/assets/upgrade-complete.mp3";
const DEFAULT_INITIAL_MESSAGE_MODE: InitialMessageMode = "direct";

interface RunPrefs {
  sendByCtrlEnter: boolean;
  showThinking: boolean;
  autoExpandTools: boolean;
  condenseCompletedTurns: boolean;
  showTimestamps: boolean;
  showDuration: boolean;
  turnCompleteSound: boolean;
  turnCompleteSoundVolume: number;
  // Mattermost/Element suppress the chat ping when the user is already
  // viewing the channel — seeing the new message *is* the notification.
  // Zulip doesn't. For Tank, the sound is a "your turn now" state-transition
  // signal (closer to a build-complete chime than a chat ping), so the
  // default is on. The toggle exists for users who'd rather mirror the
  // chat-app convention. See docs/product-inspirations.md analysis.
  turnCompleteSoundOnVisible: boolean;
  chatFontScale: number;
  // Provider model + effort prefs persist the user's last picks across
  // sessions so a fresh session opens with them pre-selected. They drive
  // initial selectedModelId / selectedEffort state in RunPane and are
  // also written back on every change. Once a turn is submitted the
  // model + effort are sealed for that session pod's lifetime (see
  // agent-runner/src/runner.ts and codex-runner/src/runner.ts), so these
  // prefs only affect the *next* session created in this browser.
  claudeModelId: string;
  claudeEffort: string;
  codexModelId: string;
  codexEffort: string;
  initialMessageMode: InitialMessageMode;
}

const DEFAULT_RUN_PREFS: RunPrefs = {
  sendByCtrlEnter: false,
  showThinking: true,
  autoExpandTools: false,
  condenseCompletedTurns: true,
  showTimestamps: true,
  showDuration: true,
  turnCompleteSound: true,
  turnCompleteSoundVolume: 0.8,
  turnCompleteSoundOnVisible: true,
  chatFontScale: 1,
  claudeModelId: DEFAULT_CLAUDE_MODEL_ID,
  claudeEffort: DEFAULT_CLAUDE_EFFORT_ID,
  codexModelId: DEFAULT_CODEX_MODEL_ID,
  codexEffort: DEFAULT_CODEX_EFFORT_ID,
  initialMessageMode: DEFAULT_INITIAL_MESSAGE_MODE,
};

const CHAT_FONT_SCALE_MIN = 0.8;
const CHAT_FONT_SCALE_MAX = 2.0;
const CHAT_FONT_SCALE_STEP = 0.1;
const TURN_COMPLETE_SOUND_VOLUME_MIN = 0;
const TURN_COMPLETE_SOUND_VOLUME_MAX = 1;
const TURN_COMPLETE_SOUND_VOLUME_STEP = 0.05;
const RUN_COMPOSER_PLACEHOLDER = "Ask anything...";
const RUN_COMPOSER_HINT_SUFFIX = " · / for slash commands";

interface InitialMessageModeOption {
  id: InitialMessageMode;
  label: string;
  hint: string;
  icon: LucideIcon;
}

const INITIAL_MESSAGE_MODE_OPTIONS: InitialMessageModeOption[] = [
  {
    id: "direct",
    label: "Direct",
    hint: "No first-turn preset",
    icon: MessageSquareIcon,
  },
  {
    id: "diagnose",
    label: "Diagnose",
    hint: "No code writes",
    icon: SearchIcon,
  },
  {
    id: "quality_gaps",
    label: "Quality gaps",
    hint: "Fix plus policy check",
    icon: FileDiffIcon,
  },
  {
    id: "test",
    label: "Code + test",
    hint: "Starts the test skill",
    icon: FlaskConicalIcon,
  },
];

function clampChatFontScale(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_RUN_PREFS.chatFontScale;
  return Math.min(CHAT_FONT_SCALE_MAX, Math.max(CHAT_FONT_SCALE_MIN, value));
}

function clampTurnCompleteSoundVolume(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_RUN_PREFS.turnCompleteSoundVolume;
  return Math.min(
    TURN_COMPLETE_SOUND_VOLUME_MAX,
    Math.max(TURN_COMPLETE_SOUND_VOLUME_MIN, value),
  );
}

// allowlistFromOptions narrows a localStorage string to the canonical
// id set for a typed run-pref. The allowlist is what makes a stale or
// hand-edited pref safe: an unknown value falls back to the default
// instead of being forwarded to the backend (where it would either be
// rejected or, worse, accepted as opaque). Returning the trimmed
// candidate only when the option set contains it keeps the SPA in
// lockstep with backend-go's allowedClaudeEfforts without re-listing
// the values here. Empty / unknown → caller's fallback.
function pickAllowedPrefId(raw: string | null, options: { id: string }[], fallback: string): string {
  if (raw == null) return fallback;
  const trimmed = raw.trim();
  if (!trimmed) return fallback;
  return options.some((opt) => opt.id === trimmed) ? trimmed : fallback;
}

function pickInitialMessageMode(raw: string | null, fallback: InitialMessageMode): InitialMessageMode {
  return pickAllowedPrefId(raw, INITIAL_MESSAGE_MODE_OPTIONS, fallback) as InitialMessageMode;
}

function loadRunPrefs(): RunPrefs {
  const out = { ...DEFAULT_RUN_PREFS };
  try {
    for (const key of Object.keys(out) as (keyof RunPrefs)[]) {
      const raw = localStorage.getItem(RUN_PREF_PREFIX + key);
      if (key === "chatFontScale") {
        if (raw != null) out[key] = clampChatFontScale(Number(raw));
      } else if (key === "turnCompleteSoundVolume") {
        if (raw != null) out[key] = clampTurnCompleteSoundVolume(Number(raw));
      } else if (key === "claudeModelId") {
        out[key] = pickAllowedPrefId(raw, CLAUDE_MODELS, DEFAULT_CLAUDE_MODEL_ID);
      } else if (key === "claudeEffort") {
        out[key] = pickAllowedPrefId(raw, CLAUDE_EFFORTS, DEFAULT_CLAUDE_EFFORT_ID);
      } else if (key === "codexModelId") {
        out[key] = pickAllowedPrefId(raw, CODEX_MODELS, DEFAULT_CODEX_MODEL_ID);
      } else if (key === "codexEffort") {
        out[key] = pickAllowedPrefId(raw, CODEX_EFFORTS, DEFAULT_CODEX_EFFORT_ID);
      } else if (key === "initialMessageMode") {
        out[key] = pickInitialMessageMode(raw, DEFAULT_INITIAL_MESSAGE_MODE);
      } else if (raw === "true" || raw === "false") {
        out[key] = raw === "true";
      }
    }
  } catch {
    /* ignore */
  }
  return out;
}

type SetRunPref = <K extends keyof RunPrefs>(key: K, value: RunPrefs[K]) => void;

// Phase E: type-narrow the opaque server-side run_prefs blob into the
// SPA's RunPrefs shape. Unknown keys are dropped (a future SPA may have
// written them); unknown values for known keys are ignored (defensive
// against type drift).
function mergeServerRunPrefs(prev: RunPrefs, server: Record<string, unknown>): RunPrefs {
  const out: RunPrefs = { ...prev };
  for (const key of Object.keys(prev) as (keyof RunPrefs)[]) {
    const raw = server[key];
    if (raw === undefined) continue;
    if (key === "chatFontScale") {
      if (typeof raw === "number") out[key] = clampChatFontScale(raw);
    } else if (key === "turnCompleteSoundVolume") {
      if (typeof raw === "number") out[key] = clampTurnCompleteSoundVolume(raw);
    } else if (key === "claudeModelId") {
      if (typeof raw === "string") {
        out[key] = pickAllowedPrefId(raw, CLAUDE_MODELS, prev.claudeModelId);
      }
    } else if (key === "claudeEffort") {
      if (typeof raw === "string") {
        out[key] = pickAllowedPrefId(raw, CLAUDE_EFFORTS, prev.claudeEffort);
      }
    } else if (key === "codexModelId") {
      if (typeof raw === "string") {
        out[key] = pickAllowedPrefId(raw, CODEX_MODELS, prev.codexModelId);
      }
    } else if (key === "codexEffort") {
      if (typeof raw === "string") {
        out[key] = pickAllowedPrefId(raw, CODEX_EFFORTS, prev.codexEffort);
      }
    } else if (key === "initialMessageMode") {
      if (typeof raw === "string") {
        out[key] = pickInitialMessageMode(raw, prev.initialMessageMode);
      }
    } else if (typeof raw === "boolean") {
      (out as unknown as Record<string, unknown>)[key] = raw;
    }
  }
  return out;
}

// transcriptComparable returns a stable JSON of the transcript's load-bearing
// fields, used to short-circuit no-op replay updates. Chat sessions rebuild
// from server-owned transcript rows; live SSE delivers the same projected rows.
function transcriptComparable(entries: TranscriptEntry[]): string {
  return JSON.stringify(
    entries.map((entry) => {
      if (entry.kind === "turn_activity") {
        return {
          kind: entry.kind,
          id: entry.id,
          turnId: entry.turnId,
          activity: entry.activity,
          activityIds: entry.activityIds,
          orderKey: entry.orderKey,
          turnTerminalOrderKey: entry.turnTerminalOrderKey,
        };
      }
      if (entry.kind === "message") {
        return {
          kind: entry.kind,
          id: entry.id,
          role: entry.role,
          text: entry.text,
          attachments: entry.attachments,
          durationMs: (entry as Record<string, unknown>).durationMs,
          turnTerminalStatus: entry.turnTerminalStatus,
          turnTerminalAt: entry.turnTerminalAt,
          turnTerminalOrderKey: entry.turnTerminalOrderKey,
          turnUsage: entry.turnUsage,
        };
      }
      if (entry.kind === "tool") {
        return {
          kind: entry.kind,
          id: entry.id,
          toolName: entry.toolName,
          toolInput: entry.toolInput,
          toolOutput: entry.toolOutput,
          toolStatus: entry.toolStatus,
          startedAt: entry.startedAt,
          completedAt: entry.completedAt,
          turnTerminalStatus: entry.turnTerminalStatus,
          turnTerminalAt: entry.turnTerminalAt,
          turnTerminalOrderKey: entry.turnTerminalOrderKey,
          turnUsage: entry.turnUsage,
        };
      }
      if (entry.kind === "reasoning") {
        return {
          kind: entry.kind,
          id: entry.id,
          reasoning: entry.reasoning,
          turnTerminalStatus: entry.turnTerminalStatus,
          turnTerminalAt: entry.turnTerminalAt,
          turnTerminalOrderKey: entry.turnTerminalOrderKey,
          turnUsage: entry.turnUsage,
        };
      }
      if (entry.kind === "background_task") {
        return {
          kind: entry.kind,
          id: entry.id,
          taskId: entry.taskId,
          taskStatus: entry.taskStatus,
          taskSummary: entry.taskSummary,
          taskDescription: entry.taskDescription,
          taskError: entry.taskError,
          taskCommand: entry.taskCommand,
          taskCwd: entry.taskCwd,
          taskProcessId: entry.taskProcessId,
          taskOutput: entry.taskOutput,
          taskExitCode: entry.taskExitCode,
          taskDurationMs: entry.taskDurationMs,
          startedAt: entry.startedAt,
          updatedAt: entry.updatedAt,
          completedAt: entry.completedAt,
          turnTerminalStatus: entry.turnTerminalStatus,
          turnTerminalAt: entry.turnTerminalAt,
          turnTerminalOrderKey: entry.turnTerminalOrderKey,
          turnUsage: entry.turnUsage,
        };
      }
      return {
        kind: entry.kind,
        meta: entry.meta,
        metaKind: entry.metaKind,
        announcement: entry.announcement,
        turnTerminalStatus: entry.turnTerminalStatus,
        turnTerminalAt: entry.turnTerminalAt,
        turnTerminalOrderKey: entry.turnTerminalOrderKey,
        turnUsage: entry.turnUsage,
      };
    }),
  );
}

function transcriptRowsFromTimelineBody(body: TranscriptTimelineBody): TranscriptEntry[] {
  if (!Array.isArray(body.rows)) {
    throw new Error("timeline response missing server transcript rows");
  }
  return normalizeProjectedTranscriptEntries(body.rows);
}

function normalizeProjectedTranscriptEntries(entries: unknown[]): TranscriptEntry[] {
  return entries
    .map(normalizeProjectedTranscriptEntry)
    .filter((entry): entry is TranscriptEntry => entry !== null);
}

function normalizeProjectedTranscriptEntry(raw: unknown): TranscriptEntry | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const record = raw as Record<string, unknown>;
  const kind = typeof record.kind === "string" ? record.kind : "";
  const id = typeof record.id === "string" ? record.id : "";
  if (!id) return null;
  if (
    kind !== "message" &&
    kind !== "tool" &&
    kind !== "reasoning" &&
    kind !== "meta" &&
    kind !== "background_task" &&
    kind !== "turn_activity"
  ) {
    return null;
  }
  if (kind === "turn_activity") {
    return {
      ...record,
      id,
      kind,
      turnId: stringRecordValue(record, "turnId"),
      activity: normalizeTurnActivitySummary(record.activity),
      activityIds: normalizeStringArray(record.activityIds),
    } as TranscriptEntry;
  }
  return {
    ...record,
    id,
    kind,
    ...(kind === "message" ? { attachments: normalizeMessageAttachments(record.attachments) } : {}),
  } as TranscriptEntry;
}

function normalizeTurnActivitySummary(raw: unknown): TurnActivitySummary | undefined {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
  const record = raw as Record<string, unknown>;
  return {
    turnId: stringRecordValue(record, "turnId"),
    status: stringRecordValue(record, "status"),
    active: typeof record.active === "boolean" ? record.active : undefined,
    toolCount: numericRecordValue(record, "toolCount"),
    progressNoteCount: numericRecordValue(record, "progressNoteCount"),
    reasoningCount: numericRecordValue(record, "reasoningCount"),
    backgroundTaskCount: numericRecordValue(record, "backgroundTaskCount"),
    errorCount: numericRecordValue(record, "errorCount"),
    childCount: numericRecordValue(record, "childCount"),
    compactedCount: numericRecordValue(record, "compactedCount"),
    compactedEntryIds: normalizeStringArray(record.compactedEntryIds),
    startedAt: stringRecordValue(record, "startedAt"),
    completedAt: stringRecordValue(record, "completedAt"),
    startOrderKey: stringRecordValue(record, "startOrderKey"),
    endOrderKey: stringRecordValue(record, "endOrderKey"),
    sourceEventId: stringRecordValue(record, "sourceEventId"),
  };
}

function normalizeStringArray(raw: unknown): string[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const out = raw.filter((entry): entry is string => typeof entry === "string" && entry.length > 0);
  return out.length > 0 ? out : undefined;
}

function normalizeMessageAttachments(raw: unknown): MessageAttachmentDisplay[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const out = raw
    .map((item): MessageAttachmentDisplay | null => {
      if (!item || typeof item !== "object" || Array.isArray(item)) return null;
      const record = item as Record<string, unknown>;
      const label = typeof record.label === "string" ? record.label.trim() : "";
      const name = typeof record.name === "string" ? record.name.trim() : "";
      if (!label && !name) return null;
      const kind = record.kind === "image" ? "image" : "file";
      const path = typeof record.path === "string" ? record.path.trim() : "";
      const absPath = typeof record.absPath === "string" ? record.absPath.trim() : "";
      const previewUrl = typeof record.previewUrl === "string" ? record.previewUrl.trim() : "";
      const size = typeof record.size === "number" && Number.isFinite(record.size)
        ? record.size
        : undefined;
      return {
        label: label || name,
        name: name || label,
        kind,
        ...(path ? { path } : {}),
        ...(absPath ? { absPath } : {}),
        ...(size != null ? { size } : {}),
        ...(previewUrl ? { previewUrl } : {}),
      };
    })
    .filter((item): item is MessageAttachmentDisplay => item !== null);
  return out.length > 0 ? out : undefined;
}

function stringRecordValue(record: Record<string, unknown>, key: string): string | undefined {
  const value = record[key];
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function numericRecordValue(record: Record<string, unknown>, key: string): number | undefined {
  const value = record[key];
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

// Transcript merge/dedup helpers live in ./transcriptMerge (extracted for unit tests).

function mergeProjectedTranscriptWindows(
  older: TranscriptEntry[],
  current: TranscriptEntry[],
): TranscriptEntry[] {
  const seen = new Set<string>();
  const out: TranscriptEntry[] = [];
  for (const entry of [...older, ...current]) {
    const key = entry.id;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(entry);
  }
  return out;
}

function projectedTranscriptEntryOrderKey(entry: TranscriptEntry): string {
  if (entry.kind === "turn_activity") {
    return entry.activity?.startOrderKey ?? entry.orderKey ?? "";
  }
  return entry.orderKey ?? "";
}

function mergeProjectedTranscriptRowUpdates(
  current: TranscriptEntry[],
  updates: TranscriptEntry[],
): TranscriptEntry[] {
  if (updates.length === 0) return current;
  const terminalTurns = new Set<string>();
  const activityTurns = new Set<string>();
  const compactedRowIds = new Set<string>();
  for (const entry of updates) {
    const turnId = entry.turnId ?? entry.activity?.turnId ?? "";
    if (!turnId) continue;
    if (entry.kind === "turn_activity") {
      activityTurns.add(turnId);
      for (const id of entry.activityIds ?? entry.activity?.compactedEntryIds ?? []) {
        compactedRowIds.add(id);
      }
    }
    if (transcriptEntryTerminalResult(entry)) terminalTurns.add(turnId);
  }
  const byId = new Map<string, TranscriptEntry>();
  for (const entry of current) {
    const turnId = entry.turnId ?? entry.activity?.turnId ?? "";
    if (compactedRowIds.has(entry.id)) {
      continue;
    }
    if (
      entry.kind === "turn_activity" &&
      turnId &&
      terminalTurns.has(turnId) &&
      !activityTurns.has(turnId)
    ) {
      continue;
    }
    byId.set(entry.id, entry);
  }
  for (const entry of updates) byId.set(entry.id, entry);
  return Array.from(byId.values()).sort((a, b) => {
    const aKey = projectedTranscriptEntryOrderKey(a);
    const bKey = projectedTranscriptEntryOrderKey(b);
    if (aKey && bKey && aKey !== bKey) return aKey < bKey ? -1 : 1;
    if (aKey && !bKey) return -1;
    if (!aKey && bKey) return 1;
    return a.id.localeCompare(b.id);
  });
}

// ---------------------------------------------------------------------------
// Inline message renderer. Replaces AgentTranscript so we can ship per-tool
// body renderers (diff viewer, todo list, bash, etc), per-message copy
// buttons, timestamps, reasoning accordions, and a proper error box —
// none of which the library exposes via render-prop slots. The CSS class
// names match the AgentTranscript slot map so the existing message /
// avatar / tool styling continues to apply.

type EntryGroup =
  | { kind: "message" | "reasoning" | "meta" | "background_task"; entry: TranscriptEntry }
  | { kind: "message_group"; entries: TranscriptEntry[] }
  | { kind: "tools"; entries: TranscriptEntry[] }
  | { kind: "thinking"; id: string; turnId: string; shell?: TranscriptEntry; startedAt?: string }
  | {
      kind: "activity";
      id: string;
      turnId: string;
      entries: TranscriptEntry[];
      compactedEntryIds: string[];
      active?: boolean;
      shell?: TranscriptEntry;
      loaded?: boolean;
    };
type FlatEntryGroup = Exclude<EntryGroup, { kind: "activity" }>;

function entryGroupKey(g: EntryGroup): string {
  if (g.kind === "tools") {
    return toolGroupStateKey(g.entries);
  }
  if (g.kind === "message_group") {
    return `messages-${g.entries[0]?.id ?? "group"}`;
  }
  if (g.kind === "thinking") return g.id;
  if (g.kind === "activity") return g.id;
  return g.entry.id;
}

function messageEntryForAvatarGrouping(
  group: EntryGroup | FlatEntryGroup | null | undefined,
): TranscriptEntry | null {
  if (!group) return null;
  return group.kind === "message" ? group.entry : null;
}

function isMessageAvatarContinuation(
  groups: readonly (EntryGroup | FlatEntryGroup)[],
  index: number,
): boolean {
  const current = messageEntryForAvatarGrouping(groups[index]!);
  const previous = messageEntryForAvatarGrouping(groups[index - 1]!);
  return shouldGroupTranscriptMessageWithPrevious(previous, current);
}

function isAbsorbableSystemMessage(entry: TranscriptEntry): boolean {
  if (entry.kind !== "message" || entry.role !== "system") return false;
  const messageKind = (entry as Record<string, unknown>).messageKind;
  const action = (entry as Record<string, unknown>).action;
  return (!messageKind || messageKind === "message") && !action;
}

function lastSystemMessageGroupEntry(group: EntryGroup | undefined): TranscriptEntry | null {
  if (!group) return null;
  if (group.kind === "message" && isAbsorbableSystemMessage(group.entry)) return group.entry;
  if (group.kind === "message_group") return group.entries[group.entries.length - 1] ?? null;
  return null;
}

function isUserMessageEntry(entry: TranscriptEntry): boolean {
  return entry.kind === "message" && entry.role === "user";
}

function turnThinkingGroup(turnId: string, shell?: TranscriptEntry): Extract<EntryGroup, { kind: "thinking" }> {
  // Prefer the projected TurnActivitySummary.startedAt: that is the durable
  // turn-start order key derived from the first event in the turn and stays
  // constant across re-projections. The shell entry's own `time` field gets
  // refreshed each time the activity shell is re-projected (it tracks the
  // latest event, not the turn start) — using it caused the duration to
  // appear stuck at 0s while the turn was running. Fall through to the
  // shell's own startedAt and time only when the activity summary is
  // missing them (older fixtures, hand-built test entries).
  return {
    kind: "thinking",
    id: `turn-thinking-${turnId}`,
    turnId,
    shell,
    startedAt: shell?.activity?.startedAt ?? shell?.startedAt ?? shell?.time,
  };
}

function turnActivityOwnedAssistantEntries(
  group: Extract<EntryGroup, { kind: "activity" }>,
): TranscriptEntry[] {
  const compactedEntryIds = new Set(group.compactedEntryIds);
  return group.entries.filter(
    (entry) =>
      entry.kind === "message" &&
      entry.role === "assistant" &&
      !compactedEntryIds.has(entry.id),
  );
}

function toolGroupStateKey(entries: TranscriptEntry[]): string {
  const head = entries[0]?.id ?? "tools";
  return `tools-${head}`;
}

function pushTranscriptEntryGroup(
  groups: EntryGroup[],
  entry: TranscriptEntry,
  bucket: { entries: TranscriptEntry[] },
): void {
  if (entry.kind === "tool") {
    bucket.entries.push(entry);
    return;
  }
  if (bucket.entries.length) {
    groups.push({ kind: "tools", entries: bucket.entries });
    bucket.entries = [];
  }
  if (isAbsorbableSystemMessage(entry)) {
    const lastIndex = groups.length - 1;
    const previous = lastSystemMessageGroupEntry(groups[lastIndex]);
    if (previous && shouldGroupTranscriptMessageWithPrevious(previous, entry)) {
      const last = groups[lastIndex]!;
      if (last.kind === "message_group") {
        last.entries = [...last.entries, entry];
      } else if (last.kind === "message") {
        groups[lastIndex] = { kind: "message_group", entries: [last.entry, entry] };
      }
      return;
    }
  }
  if (entry.kind === "message") groups.push({ kind: "message", entry });
  else if (entry.kind === "reasoning") groups.push({ kind: "reasoning", entry });
  else if (entry.kind === "background_task") groups.push({ kind: "background_task", entry });
  else groups.push({ kind: "meta", entry });
}

function isTurnActivityEntry(entry: TranscriptEntry): boolean {
  return entry.kind === "turn_activity" && Boolean(entry.turnId);
}

function createTurnActivityEntryGroup(
  entry: TranscriptEntry,
  activityEntriesByTurn: Record<string, TranscriptEntry[] | undefined>,
  activeTurnId: string | null,
): Extract<EntryGroup, { kind: "activity" }> | null {
  const turnId = entry.turnId ?? entry.activity?.turnId ?? "";
  if (!turnId) return null;
  const entries = activityEntriesByTurn[turnId] ?? [];
  return {
    kind: "activity",
    id: entry.id,
    turnId,
    entries,
    compactedEntryIds: entry.activityIds ?? entry.activity?.compactedEntryIds ?? [],
    active: turnActivityGroupIsActive(entry.activity, turnId, activeTurnId),
    shell: entry,
    loaded: Boolean(activityEntriesByTurn[turnId]),
  };
}

function flushTranscriptToolBucket(
  groups: EntryGroup[],
  bucket: { entries: TranscriptEntry[] },
): void {
  if (!bucket.entries.length) return;
  groups.push({ kind: "tools", entries: bucket.entries });
  bucket.entries = [];
}

function groupFlatTranscriptEntries(entries: TranscriptEntry[]): FlatEntryGroup[] {
  const groups: FlatEntryGroup[] = [];
  const bucket = { entries: [] as TranscriptEntry[] };
  for (const e of entries) {
    pushTranscriptEntryGroup(groups, e, bucket);
  }
  flushTranscriptToolBucket(groups, bucket);
  return groups;
}

function groupTranscriptEntries(
  entries: TranscriptEntry[],
  condenseCompletedTurns = true,
  activeTurnId: string | null = null,
  activityEntriesByTurn: Record<string, TranscriptEntry[] | undefined> = {},
): EntryGroup[] {
  void condenseCompletedTurns;
  const hasProjectedTurnActivity = entries.some(isTurnActivityEntry);
  const groups: EntryGroup[] = [];
  const bucket = { entries: [] as TranscriptEntry[] };
  const activityHiddenEntryIds = new Set<string>();
  if (hasProjectedTurnActivity) {
    const insertedThinkingTurnIds = new Set<string>();
    for (const entry of entries) {
      if (isTurnActivityEntry(entry)) {
        flushTranscriptToolBucket(groups, bucket);
        const group = createTurnActivityEntryGroup(entry, activityEntriesByTurn, activeTurnId);
        if (group) {
          for (const id of group.compactedEntryIds) activityHiddenEntryIds.add(id);
          if (group.active && !insertedThinkingTurnIds.has(group.turnId)) {
            groups.push(turnThinkingGroup(group.turnId, entry));
            insertedThinkingTurnIds.add(group.turnId);
          }
        }
        continue;
      }
      if (activityHiddenEntryIds.has(entry.id)) continue;
      pushTranscriptEntryGroup(groups, entry, bucket);
    }
    flushTranscriptToolBucket(groups, bucket);
    return groups;
  }
  for (const entry of entries) {
    pushTranscriptEntryGroup(groups, entry, bucket);
  }
  flushTranscriptToolBucket(groups, bucket);
  return groups;
}

function chatScrollGroupSnapshot(
  groups: EntryGroup[],
  entryCount: number,
  sourceEntries?: TranscriptEntry[],
): Record<string, unknown> {
  let messages = 0;
  let reasoning = 0;
  let meta = 0;
  let backgroundTasks = 0;
  let thinkingGroups = 0;
  let toolGroups = 0;
  let toolEntries = 0;
  let activityGroups = 0;
  let activeActivityGroups = 0;
  let durableActiveActivityGroups = 0;
  let activityEntries = 0;
  for (const group of groups) {
    if (group.kind === "tools") {
      toolGroups += 1;
      toolEntries += group.entries.length;
    } else if (group.kind === "message") {
      messages += 1;
    } else if (group.kind === "message_group") {
      messages += group.entries.length;
    } else if (group.kind === "reasoning") {
      reasoning += 1;
    } else if (group.kind === "background_task") {
      backgroundTasks += 1;
    } else if (group.kind === "thinking") {
      thinkingGroups += 1;
    } else if (group.kind === "activity") {
      activityGroups += 1;
      if (group.active) activeActivityGroups += 1;
      if (group.shell && turnActivityShellIsDurablyActive(group.shell.activity)) {
        durableActiveActivityGroups += 1;
      }
      activityEntries += group.entries.length;
      toolEntries += group.entries.filter((entry) => entry.kind === "tool").length;
      backgroundTasks += group.entries.filter((entry) => entry.kind === "background_task").length;
    } else {
      meta += 1;
    }
  }
  let turnActivityShells = 0;
  let durableActiveTurnActivityShells = 0;
  if (sourceEntries) {
    for (const entry of sourceEntries) {
      if (!isTurnActivityEntry(entry)) continue;
      turnActivityShells += 1;
      if (turnActivityShellIsDurablyActive(entry.activity)) {
        durableActiveTurnActivityShells += 1;
      }
    }
  }
  return {
    entries: entryCount,
    groups: groups.length,
    messages,
    reasoning,
    meta,
    backgroundTasks,
    thinkingGroups,
    toolGroups,
    toolEntries,
    activityGroups,
    activeActivityGroups,
    durableActiveActivityGroups,
    activityEntries,
    turnActivityShells,
    durableActiveTurnActivityShells,
    firstGroupKey: groups[0] ? entryGroupKey(groups[0]) : "",
    lastGroupKey: groups.length > 0 ? entryGroupKey(groups[groups.length - 1]!) : "",
  };
}

function chatScrollEntrySnapshot(entries: TranscriptEntry[]): Record<string, unknown> {
  return chatScrollGroupSnapshot(groupTranscriptEntries(entries), entries.length, entries);
}

function chatScrollSnapshotNumber(
  snapshot: Record<string, unknown>,
  key: string,
): number {
  const value = snapshot[key];
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function chatScrollSnapshotString(
  snapshot: Record<string, unknown>,
  key: string,
): string {
  const value = snapshot[key];
  return typeof value === "string" ? value : "";
}

function chatScrollPreviousEntrySnapshot(
  previous: Record<string, unknown>,
): Record<string, unknown> {
  return {
    previousEntries: chatScrollSnapshotNumber(previous, "entries"),
    previousGroups: chatScrollSnapshotNumber(previous, "groups"),
    previousMessages: chatScrollSnapshotNumber(previous, "messages"),
    previousFirstGroupKey: chatScrollSnapshotString(previous, "firstGroupKey"),
    previousLastGroupKey: chatScrollSnapshotString(previous, "lastGroupKey"),
  };
}

function chatScrollEntryDelta(
  previous: Record<string, unknown>,
  nextEntries: TranscriptEntry[],
): Record<string, unknown> {
  const next = chatScrollEntrySnapshot(nextEntries);
  const entriesDelta =
    chatScrollSnapshotNumber(next, "entries") -
    chatScrollSnapshotNumber(previous, "entries");
  const groupsDelta =
    chatScrollSnapshotNumber(next, "groups") -
    chatScrollSnapshotNumber(previous, "groups");
  const messagesDelta =
    chatScrollSnapshotNumber(next, "messages") -
    chatScrollSnapshotNumber(previous, "messages");
  return {
    ...chatScrollPreviousEntrySnapshot(previous),
    entriesDelta,
    groupsDelta,
    messagesDelta,
    visibleRowsAdded: Math.max(0, groupsDelta),
    visibleRowsRemoved: Math.max(0, -groupsDelta),
  };
}

function logChatScrollGroups(
  event: string,
  groups: EntryGroup[],
  entryCount: number,
  detail: Record<string, unknown> = {},
): void {
  logChatScrollEvent(event, {
    ...detail,
    ...chatScrollGroupSnapshot(groups, entryCount),
  });
}

function logChatScrollEntries(
  event: string,
  entries: TranscriptEntry[],
  detail: Record<string, unknown> = {},
): void {
  logChatScrollEvent(event, {
    ...detail,
    ...chatScrollEntrySnapshot(entries),
  });
}

function tryParseJson(s: string | undefined): unknown {
  if (!s) return null;
  try {
    return JSON.parse(s);
  } catch {
    return null;
  }
}

function formatMessageTime(iso: string): string {
  try {
    return new Date(iso).toLocaleTimeString([], {
      hour: "numeric",
      minute: "2-digit",
    });
  } catch {
    return "";
  }
}

function formatToolClockTime(iso: string | undefined): string {
  if (!iso) return "";
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return "";
  return new Date(ms).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

function formatToolFullTime(iso: string | undefined): string {
  if (!iso) return "";
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return "";
  return new Date(ms).toLocaleString([], {
    dateStyle: "medium",
    timeStyle: "medium",
  });
}

function toolTimingTitle(
  startedAt: string | undefined,
  completedAt: string | undefined,
  running: boolean,
): string | undefined {
  const start = formatToolFullTime(startedAt);
  const end = formatToolFullTime(completedAt);
  if (!start && !end && !running) return undefined;
  if (running) return start ? `Started ${start}; still running` : "Still running";
  if (start && end) return `Started ${start}; ended ${end}`;
  if (start) return `Started ${start}`;
  return end ? `Ended ${end}` : undefined;
}

function ToolTiming({
  startedAt,
  completedAt,
  running,
}: {
  startedAt?: string;
  completedAt?: string;
  running: boolean;
}) {
  const start = formatToolClockTime(startedAt);
  const end = formatToolClockTime(completedAt);
  if (!start && !end && !running) return null;
  const title = toolTimingTitle(startedAt, completedAt, running);
  return (
    <span className="run-tool-timing" title={title} aria-label={title}>
      {start && <span className="run-tool-timing-start">{start}</span>}
      {start && (end || running) && (
        <span className="run-tool-timing-arrow" aria-hidden="true">
          →
        </span>
      )}
      {running ? (
        <span className="run-tool-timing-running">
          <Loader2Icon
            size={11}
            className="run-spin run-tool-timing-spinner"
            aria-hidden="true"
          />
          <span className="sr-only">running</span>
        </span>
      ) : (
        end && <span className="run-tool-timing-end">{end}</span>
      )}
    </span>
  );
}

function formatTurnDuration(ms: number): string {
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.floor((ms % 60_000) / 1000);
  return `${m}m ${s}s`;
}

/** Simple LCS-based line diff. Returns lines marked context/del/add. */
function computeLineDiff(
  oldStr: string,
  newStr: string,
): { kind: "ctx" | "del" | "add"; text: string }[] {
  const a = oldStr.split("\n");
  const b = newStr.split("\n");
  const m = a.length;
  const n = b.length;
  const dp: number[][] = Array.from({ length: m + 1 }, () =>
    new Array(n + 1).fill(0),
  );
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const out: { kind: "ctx" | "del" | "add"; text: string }[] = [];
  let i = 0;
  let j = 0;
  while (i < m && j < n) {
    if (a[i] === b[j]) {
      out.push({ kind: "ctx", text: a[i] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ kind: "del", text: a[i] });
      i++;
    } else {
      out.push({ kind: "add", text: b[j] });
      j++;
    }
  }
  while (i < m) out.push({ kind: "del", text: a[i++] });
  while (j < n) out.push({ kind: "add", text: b[j++] });
  return out;
}

type QuoteStyle = "fence" | "blockquote";

function quoteMessageText(text: string, style: QuoteStyle): string {
  if (style === "blockquote") {
    return text.split("\n").map((line) => (line.length > 0 ? `> ${line}` : ">")).join("\n");
  }
  const longestBacktickRun = Math.max(2, ...Array.from(text.matchAll(/`+/g), (match) => match[0].length));
  const fence = "`".repeat(longestBacktickRun + 1);
  return `${fence}\n${text}\n${fence}`;
}

function replaceAllLiteral(input: string, search: string, replacement: string): string {
  return input.split(search).join(replacement);
}

async function fetchAppPublicConfig(): Promise<AppPublicConfig> {
  if (!appConfigPromise) {
    appConfigPromise = fetch("/api/config")
      .then((res) => {
        if (!res.ok) throw new Error(`config fetch failed: ${res.status}`);
        return res.json() as Promise<AppPublicConfig>;
      })
      .catch(() => ({}));
  }
  return appConfigPromise;
}

async function forkSessionPromptTemplate(): Promise<string> {
  const config = await fetchAppPublicConfig();
  const template = config.fork_session_prompt_template;
  return typeof template === "string" && template.trim() ? template : DEFAULT_FORK_SESSION_PROMPT_TEMPLATE;
}

async function buildForkSessionPrompt(request: ForkSessionRequest): Promise<string> {
  const sourceName = sessionDisplayName(request.sourceSession);
  const payload = {
    source_session_id: request.sourceSession.id,
    source_session_name: sourceName,
    source_session_mode: request.sourceSession.mode,
    forked_message_id: request.forkedEntry.id,
    forked_message_time: request.forkedEntry.time,
    forked_message_order_key: request.forkedEntry.orderKey ?? null,
    forked_message_source_event_id: request.forkedEntry.sourceEventId ?? null,
  };
  const template = await forkSessionPromptTemplate();
  return replaceAllLiteral(
    replaceAllLiteral(template, "{{forked_message}}", quoteMessageText(request.forkedEntry.text ?? "", "fence")),
    "{{source_session_json}}",
    JSON.stringify(payload, null, 2),
  );
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="run-msg-action run-msg-copy"
      title="Copy"
      aria-label={copied ? "Copied" : "Copy message"}
      onClick={async (e) => {
        e.stopPropagation();
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          /* ignore */
        }
      }}
    >
      {copied ? (
        <CheckIcon size={12} aria-hidden="true" />
      ) : (
        <CopyIcon size={12} aria-hidden="true" />
      )}
    </button>
  );
}

// LinkButton copies a deep-link URL to this specific transcript entry.
// The URL is the same shape the SPA reads on cold start
// (?session=<id>&message=<entry.id>) so a human pasting it lands on the
// session and scrolls/highlights the entry, while an agent can parse
// the query params to fetch the underlying event from the API.
function LinkButton({
  sessionId,
  entryId,
}: {
  sessionId: string;
  entryId: string;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="run-msg-action run-msg-link"
      title="Copy link to message"
      aria-label={copied ? "Link copied" : "Copy link to message"}
      onClick={async (e) => {
        e.stopPropagation();
        try {
          await navigator.clipboard.writeText(messageUrl(sessionId, entryId));
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          /* ignore */
        }
      }}
    >
      {copied ? (
        <CheckIcon size={12} aria-hidden="true" />
      ) : (
        <LinkIcon size={12} aria-hidden="true" />
      )}
    </button>
  );
}

function TurnViewButton({
  turnId,
  onOpenTurn,
}: {
  turnId: string;
  onOpenTurn: (turnId: string) => void;
}) {
  return (
    <button
      type="button"
      className="run-msg-action run-msg-turn"
      title="Open turn"
      aria-label="Open turn"
      onClick={(e) => {
        e.stopPropagation();
        onOpenTurn(turnId);
      }}
    >
      <ActivityIcon size={12} aria-hidden="true" />
    </button>
  );
}

function ForkButton({
  entry,
  onFork,
}: {
  entry: TranscriptEntry;
  onFork: (entry: TranscriptEntry) => Promise<void>;
}) {
  const [forking, setForking] = useState(false);
  return (
    <button
      type="button"
      className="run-msg-action run-msg-fork"
      title={forking ? "Forking" : "Fork session"}
      aria-label={forking ? "Forking session" : "Fork session from this message"}
      disabled={forking}
      onClick={async (e) => {
        e.stopPropagation();
        if (forking) return;
        setForking(true);
        try {
          await onFork(entry);
        } finally {
          setForking(false);
        }
      }}
    >
      {forking ? (
        <Loader2Icon size={12} className="run-spin" aria-hidden="true" />
      ) : (
        <GitBranchIcon size={12} aria-hidden="true" />
      )}
    </button>
  );
}

function QuoteButton({
  text,
  style,
  onQuote,
}: {
  text: string;
  style: QuoteStyle;
  onQuote: (text: string, style: QuoteStyle) => void;
}) {
  const title = style === "blockquote" ? "Quote as blockquote" : "Quote as code block";
  const Icon = style === "blockquote" ? TextQuoteIcon : Code2Icon;
  return (
    <button
      type="button"
      className="run-msg-action run-msg-quote"
      title={title}
      aria-label={title}
      onClick={(e) => {
        e.stopPropagation();
        onQuote(text, style);
      }}
    >
      <Icon size={12} aria-hidden="true" />
    </button>
  );
}

const URL_IN_TEXT_RE = /https?:\/\/[^\s<>"'`]+/g;
const TRAILING_URL_PUNCTUATION_RE = /[.,;:!?]+$/;

function splitTrailingUrlPunctuation(url: string): { href: string; trailing: string } {
  let href = url;
  let trailing = "";
  const punctuation = href.match(TRAILING_URL_PUNCTUATION_RE)?.[0] ?? "";
  if (punctuation) {
    href = href.slice(0, -punctuation.length);
    trailing = punctuation;
  }
  while (href.endsWith(")") && (href.match(/\(/g)?.length ?? 0) < (href.match(/\)/g)?.length ?? 0)) {
    href = href.slice(0, -1);
    trailing = `)${trailing}`;
  }
  while (href.endsWith("]") && (href.match(/\[/g)?.length ?? 0) < (href.match(/\]/g)?.length ?? 0)) {
    href = href.slice(0, -1);
    trailing = `]${trailing}`;
  }
  return { href, trailing };
}

function linkifyUrls(text: string): ReactNode[] {
  const parts: ReactNode[] = [];
  let lastIndex = 0;
  URL_IN_TEXT_RE.lastIndex = 0;
  for (const match of text.matchAll(URL_IN_TEXT_RE)) {
    const rawUrl = match[0];
    const start = match.index ?? 0;
    if (start > lastIndex) parts.push(text.slice(lastIndex, start));
    const { href, trailing } = splitTrailingUrlPunctuation(rawUrl);
    parts.push(
      <RunMarkdownLink
        key={`${start}-${href}`}
        className="run-markdown-code-link"
        href={href}
      >
        {href}
      </RunMarkdownLink>,
    );
    if (trailing) parts.push(trailing);
    lastIndex = start + rawUrl.length;
  }
  if (lastIndex < text.length) parts.push(text.slice(lastIndex));
  return parts.length ? parts : [text];
}

function hasUrl(text: string): boolean {
  URL_IN_TEXT_RE.lastIndex = 0;
  const found = URL_IN_TEXT_RE.test(text);
  URL_IN_TEXT_RE.lastIndex = 0;
  return found;
}

function PreWithLinks({ children, className }: { children: string; className?: string }) {
  if (!hasUrl(children)) return <pre className={className}>{children}</pre>;
  return <pre className={className}>{linkifyUrls(children)}</pre>;
}

function textFromCodeChildren(children: ReactNode): string {
  if (Array.isArray(children)) return children.map(textFromCodeChildren).join("");
  if (typeof children === "string" || typeof children === "number") return String(children);
  return "";
}

type RunMarkdownInlineCodeProps = ComponentProps<"code"> & {
  node?: unknown;
};

function RunMarkdownInlineCode({ children, className, node: _node, ...props }: RunMarkdownInlineCodeProps) {
  const code = textFromCodeChildren(children);
  return (
    <code className={`run-markdown-inline-code${className ? ` ${className}` : ""}`} {...props}>
      {hasUrl(code) ? linkifyUrls(code) : children}
    </code>
  );
}

function RunMarkdownLink(props: AnchorHTMLAttributes<HTMLAnchorElement>) {
  const { openWorkspacePath } = useContext(RunContext);
  const workspaceTarget = typeof props.href === "string"
    ? workspacePathFromHref(props.href)
    : null;
  return (
    <a
      {...props}
      rel={workspaceTarget ? undefined : "noreferrer"}
      target={workspaceTarget ? undefined : "_blank"}
      onClick={(e) => {
        props.onClick?.(e);
        if (e.defaultPrevented || !workspaceTarget) return;
        e.preventDefault();
        openWorkspacePath(workspaceTarget);
      }}
    />
  );
}

const RUN_MARKDOWN_COMPONENTS: StreamdownComponents = {
  a: RunMarkdownLink,
  inlineCode: RunMarkdownInlineCode,
} as StreamdownComponents;

const STREAMDOWN_DARK_THEME: [string, string] = ["github-dark", "github-dark"];

function RunMarkdown({ children }: { children: string }) {
  const linkedChildren = useMemo(() => linkWorkspacePathsInMarkdown(children), [children]);
  return (
    <Streamdown
      components={RUN_MARKDOWN_COMPONENTS}
      linkSafety={{ enabled: false }}
      shikiTheme={STREAMDOWN_DARK_THEME}
    >
      {linkedChildren}
    </Streamdown>
  );
}

function RunPlainText({ children }: { children: string }) {
  const segments = useMemo(() => splitWorkspacePathsInText(children), [children]);
  return (
    <span className="run-plain-message-text">
      {segments.map((segment, index) => {
        if (segment.kind === "workspace_path") {
          return (
            <RunMarkdownLink key={index} href={segment.href}>
              {segment.text}
            </RunMarkdownLink>
          );
        }
        return segment.text;
      })}
    </span>
  );
}

function attachmentWorkspaceTarget(attachment: MessageAttachmentDisplay): WorkspacePathTarget | null {
  return normalizeWorkspacePathTarget(attachment.path || attachment.absPath || "");
}

function attachmentTitle(attachment: MessageAttachmentDisplay): string {
  const bits = [attachment.label];
  if (attachment.name && attachment.name !== attachment.label) bits.push(attachment.name);
  if (typeof attachment.size === "number") bits.push(humanFileSize(attachment.size));
  return bits.join(" · ");
}

function attachmentPayloadsForApi(attachments: readonly MessageAttachmentDisplay[]) {
  return attachments.map((attachment) => ({
    label: attachment.label,
    name: attachment.name,
    kind: attachment.kind,
    ...(attachment.path ? { path: attachment.path } : {}),
    ...(attachment.absPath ? { abs_path: attachment.absPath } : {}),
    ...(typeof attachment.size === "number" ? { size: attachment.size } : {}),
  }));
}

function AttachmentPreview({
  attachment,
  sessionId,
}: {
  attachment: MessageAttachmentDisplay;
  sessionId: string;
}) {
  const target = attachmentWorkspaceTarget(attachment);
  const [objectUrl, setObjectUrl] = useState<string | null>(null);
  useEffect(() => {
    if (attachment.previewUrl || attachment.kind !== "image" || !target?.path) {
      setObjectUrl(null);
      return () => undefined;
    }
    let cancelled = false;
    let url: string | null = null;
    void authedFetch(
      `/api/sessions/${sessionId}/files/raw?path=${encodeURIComponent(target.path)}`,
    )
      .then(async (res) => {
        if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
        return res.blob();
      })
      .then((blob) => {
        if (cancelled) return;
        url = URL.createObjectURL(blob);
        setObjectUrl(url);
      })
      .catch(() => {
        if (!cancelled) setObjectUrl(null);
      });
    return () => {
      cancelled = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [attachment.kind, attachment.previewUrl, sessionId, target?.path]);

  const src = attachment.previewUrl || objectUrl;
  if (attachment.kind === "image" && src) {
    return (
      <img
        className="run-message-attachment-thumb"
        src={src}
        alt=""
        aria-hidden="true"
      />
    );
  }
  const Icon = attachment.kind === "image" ? ImageIcon : FileIcon;
  return (
    <span className="run-message-attachment-icon" aria-hidden="true">
      <Icon size={16} />
    </span>
  );
}

function RunMessageAttachments({
  attachments,
  sessionId,
}: {
  attachments: readonly MessageAttachmentDisplay[];
  sessionId: string;
}) {
  const { openWorkspacePath } = useContext(RunContext);
  if (attachments.length === 0) return null;
  return (
    <div className="run-message-attachments" aria-label="Attachments">
      {attachments.map((attachment, index) => {
        const target = attachmentWorkspaceTarget(attachment);
        const content = (
          <>
            <AttachmentPreview attachment={attachment} sessionId={sessionId} />
            <span className="run-message-attachment-meta">
              <span className="run-message-attachment-name">{attachment.label}</span>
              {typeof attachment.size === "number" && (
                <span className="run-message-attachment-size">
                  {humanFileSize(attachment.size)}
                </span>
              )}
            </span>
          </>
        );
        const key = `${attachment.label}-${attachment.name}-${attachment.path ?? ""}-${index}`;
        if (!target) {
          return (
            <div
              key={key}
              className="run-message-attachment"
              title={attachmentTitle(attachment)}
            >
              {content}
            </div>
          );
        }
        return (
          <button
            key={key}
            type="button"
            className="run-message-attachment run-message-attachment-action"
            title={attachmentTitle(attachment)}
            onClick={(e) => {
              e.stopPropagation();
              openWorkspacePath(target);
            }}
          >
            {content}
          </button>
        );
      })}
    </div>
  );
}

interface InputReplyPayload {
  answers: Record<string, string[]>;
  annotations?: Record<string, { preview?: string; notes?: string }>;
}

const RunContext = createContext<{
  openWorkspacePath: (target: WorkspacePathTarget | string) => void;
  sendInputReply: (entry: TranscriptEntry, payload: InputReplyPayload) => Promise<void>;
  user: SessionUser | null;
}>({
  openWorkspacePath: () => {},
  sendInputReply: async () => {},
  user: null,
});

function RunMessageBubble({
  entry,
  avatar,
  systemAvatar,
  sessionId,
  highlighted,
  showTimestamps,
  showDuration,
  onQuote,
  onFork,
  onOpenTurn,
  canonicalMessage = true,
  ownedByTurnActivity = false,
  showAssistantAvatar = !ownedByTurnActivity,
  isAvatarContinuation = false,
}: {
  entry: TranscriptEntry;
  avatar: AgentAvatar | null;
  systemAvatar: AgentAvatar | null;
  sessionId: string;
  highlighted: boolean;
  showTimestamps: boolean;
  showDuration: boolean;
  onQuote?: (text: string, style: QuoteStyle) => void;
  onFork?: (entry: TranscriptEntry) => Promise<void>;
  onOpenTurn?: (turnId: string) => void;
  canonicalMessage?: boolean;
  ownedByTurnActivity?: boolean;
  showAssistantAvatar?: boolean;
  isAvatarContinuation?: boolean;
}) {
  const variant =
    entry.role === "user" ? "user" : entry.role === "system" ? "system" : "assistant";
  const { user } = useContext(RunContext);
  const entryText = entry.text ?? "";
  const messageKind = (entry as Record<string, unknown>).messageKind;
  const durableSkillDisplay =
    variant === "user" && entry.display?.kind === "skill_invocation"
      ? entry.display
      : null;
  const explicitSkillName = (entry as Record<string, unknown>).skillName;
  const skillName =
    (typeof explicitSkillName === "string" && explicitSkillName.length > 0
      ? explicitSkillName
      : durableSkillDisplay?.skill_name) ?? "";
  const text = durableSkillDisplay ? skillActionText(skillName) : entryText;
  // session.status:failed transcripts events carry severity="error" and
  // an optional action (e.g. "Re-sign-in to Codex"). The renderer
  // surfaces both: data-severity drives error-bubble styling; action
  // becomes a button next to the text.
  const messageSeverity =
    typeof (entry as Record<string, unknown>).severity === "string"
      ? ((entry as Record<string, unknown>).severity as "info" | "error")
      : undefined;
  const messageAction = (entry as Record<string, unknown>).action as
    | { label?: unknown; href?: unknown }
    | undefined;
  const messageActionLabel =
    messageAction && typeof messageAction.label === "string" && messageAction.label.length > 0
      ? messageAction.label
      : undefined;
  const messageActionHref =
    messageAction && typeof messageAction.href === "string" && messageAction.href.length > 0
      ? messageAction.href
      : undefined;
  const isSkillAction = messageKind === "skill-action" || durableSkillDisplay !== null;
  const skillSupplementalText =
    typeof (entry as Record<string, unknown>).skillSupplementalText === "string"
      ? ((entry as Record<string, unknown>).skillSupplementalText as string)
      : durableSkillDisplay?.supplemental_text ?? "";
  const skillActionIcon =
    skillName === "test"
      ? FlaskConicalIcon
      : skillName === "rollout"
        ? TankIcon
        : ListChecksIcon;
  const SkillActionIcon = skillActionIcon;
  const explicitAttachments = entry.attachments ?? [];
  const legacyAttachmentParts =
    variant === "user" && !isSkillAction && explicitAttachments.length === 0
      ? splitLegacyAttachmentDisplayText(text)
      : null;
  const visibleText = legacyAttachmentParts?.text ?? text;
  const visibleAttachments = explicitAttachments.length > 0
    ? explicitAttachments
    : legacyAttachmentParts?.attachments ?? [];
  const time = formatMessageTime(entry.time);
  const durationMs = (entry as Record<string, unknown>).durationMs as number | undefined;
  const alwaysVisible = showTimestamps || showDuration;
  return (
    <div
      className="run-transcript-message"
      data-slot="message"
      data-variant={variant}
      data-role={variant}
      data-kind={isSkillAction ? "skill-action" : "message"}
      data-skill={isSkillAction && typeof skillName === "string" ? skillName : undefined}
      data-severity={variant === "system" ? messageSeverity : undefined}
      data-message-id={canonicalMessage ? entry.id : undefined}
      data-activity-entry-id={canonicalMessage ? undefined : entry.id}
      data-owner={ownedByTurnActivity ? "activity" : undefined}
      data-continuation={isAvatarContinuation ? "true" : undefined}
      data-highlight={highlighted ? "true" : undefined}
    >
      {variant === "assistant" && showAssistantAvatar && !isAvatarContinuation && (
        <span className="run-msg-ai-avatar" aria-hidden="true">
          <SessionAvatarIcon avatar={avatar} className="run-msg-ai-icon" />
        </span>
      )}
      {variant === "system" && !isAvatarContinuation && (
        <span className="run-msg-system-avatar" aria-hidden={systemAvatar ? undefined : "true"}>
          {systemAvatar ? (
            <AgentAvatarIcon avatar={systemAvatar} className="run-msg-ai-icon" />
          ) : (
            <BotIcon size={16} strokeWidth={2.1} />
          )}
        </span>
      )}
      <div
        className="run-transcript-message-content"
        data-slot="message-content"
      >
        <div className="run-transcript-message-text" data-slot="message-text">
          {isSkillAction ? (
            <span className="run-skill-action">
              <span className="run-skill-action-text">
                <SkillActionIcon size={15} strokeWidth={2.2} aria-hidden="true" />
                <span>{text}</span>
              </span>
              {skillSupplementalText && (
                <span className="run-skill-action-detail">
                  <RunMarkdown>{skillSupplementalText}</RunMarkdown>
                </span>
              )}
            </span>
          ) : (
            variant === "user" ? (
              <RunPlainText>{visibleText}</RunPlainText>
            ) : (
              <RunMarkdown>{visibleText}</RunMarkdown>
            )
          )}
          {variant === "system" && messageActionLabel && messageActionHref && (
            <a
              className="run-msg-system-action"
              href={messageActionHref}
              target="_self"
              rel="noopener"
            >
              {messageActionLabel}
            </a>
          )}
        </div>
        {variant === "user" && visibleAttachments.length > 0 && (
          <RunMessageAttachments
            attachments={visibleAttachments}
            sessionId={sessionId}
          />
        )}
        <div
          className="run-msg-footer"
          data-always-visible={alwaysVisible ? "" : undefined}
        >
          {variant === "assistant" && entry.turnId && onOpenTurn && (
            <TurnViewButton turnId={entry.turnId} onOpenTurn={onOpenTurn} />
          )}
          {canonicalMessage && variant === "assistant" && onFork && (
            <ForkButton entry={entry} onFork={onFork} />
          )}
          {variant !== "system" && (
            <>
              {onQuote && (
                <>
                  <QuoteButton text={visibleText} style="fence" onQuote={onQuote} />
                  <QuoteButton text={visibleText} style="blockquote" onQuote={onQuote} />
                </>
              )}
              <CopyButton text={visibleText} />
              {canonicalMessage && !entry.localOnly && (
                <LinkButton sessionId={sessionId} entryId={entry.id} />
              )}
            </>
          )}
          <div className="run-msg-timings">
            {showDuration && durationMs != null && (
              <span className="run-msg-timing-row">
                {formatTurnDuration(durationMs)}
                <TimerIcon size={9} aria-hidden="true" />
              </span>
            )}
            {showTimestamps && time && (
              <span className="run-msg-timing-row">
                {time}
              </span>
            )}
          </div>
        </div>
      </div>
      {variant === "user" && !isAvatarContinuation && (() => {
        // Cross-session handoff: a sibling tank-operator session posted
        // this turn via mcp-tank-operator. Only render a session avatar
        // when the durable message/session data names one; otherwise use
        // the explicit missing-avatar glyph instead of inventing a local
        // identity from the origin id.
        const originId = entry.originSessionId;
        if (originId) {
          return (
            <span
              className="run-msg-avatar"
              data-origin-session-id={originId}
            >
              <SessionAvatarIcon
                avatar={getSessionAvatarByID(null)}
                className="run-msg-ai-icon"
              />
            </span>
          );
        }
        // Bot-authored turn (auth.romaine.life bot token). Attribute it to
        // the session's system identity rather than borrowing the human
        // owner's Gravatar. Mirrors the system-message avatar (the
        // configured system avatar, BotIcon fallback) so automation reads
        // as the system user that launches the session.
        if (entry.authorKind === "system") {
          return (
            <span className="run-msg-avatar" data-author-kind="system">
              {systemAvatar ? (
                <AgentAvatarIcon avatar={systemAvatar} className="run-msg-ai-icon" />
              ) : (
                <BotIcon size={16} strokeWidth={2.1} />
              )}
            </span>
          );
        }
        return user ? (
          <span className="run-msg-avatar">
            <Avatar user={user} />
          </span>
        ) : null;
      })()}
    </div>
  );
}

function RunSystemMessageGroupBubble({
  entries,
  systemAvatar,
  highlightedEntryId,
  showTimestamps,
}: {
  entries: TranscriptEntry[];
  systemAvatar: AgentAvatar | null;
  highlightedEntryId: string | null;
  showTimestamps: boolean;
}) {
  const highlighted = entries.some((entry) => highlightedEntryId === entry.id);
  return (
    <div
      className="run-transcript-message"
      data-slot="message"
      data-variant="system"
      data-role="system"
      data-kind="system-group"
      data-highlight={highlighted ? "true" : undefined}
    >
      <span className="run-msg-system-avatar" aria-hidden={systemAvatar ? undefined : "true"}>
        {systemAvatar ? (
          <AgentAvatarIcon avatar={systemAvatar} className="run-msg-ai-icon" />
        ) : (
          <BotIcon size={16} strokeWidth={2.1} />
        )}
      </span>
      <div className="run-transcript-message-content" data-slot="message-content">
        <div className="run-transcript-system-group">
          {entries.map((entry) => {
            const time = formatMessageTime(entry.time);
            return (
              <div
                key={entry.id}
                className="run-transcript-system-group-item"
                data-message-id={entry.id}
                data-highlight={highlightedEntryId === entry.id ? "true" : undefined}
              >
                <div className="run-transcript-message-text" data-slot="message-text">
                  <RunMarkdown>{entry.text ?? ""}</RunMarkdown>
                </div>
                {showTimestamps && time && (
                  <div className="run-msg-footer" data-always-visible="">
                    <div className="run-msg-timings">
                      <span className="run-msg-timing-row">{time}</span>
                    </div>
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function RunReasoningBlock({
  entry,
  showThinking,
}: {
  entry: TranscriptEntry;
  showThinking: boolean;
}) {
  if (!showThinking) return null;
  const text = entry.reasoning?.text ?? entry.text ?? "";
  if (!text.trim()) return null;
  return (
    <details className="run-reasoning">
      <summary className="run-reasoning-summary">
        <BrainIcon size={14} aria-hidden="true" />
        <span>Reasoning</span>
        <ChevronDownIcon size={12} className="run-reasoning-chevron" aria-hidden="true" />
      </summary>
      <div className="run-reasoning-body">
        <RunMarkdown>{text}</RunMarkdown>
      </div>
    </details>
  );
}

// RunNeedsInputAnnouncement renders the AskUserQuestion handoff row in
// the main transcript stream. The question card itself stays inside Turn
// activity (where every other tool entry lives); this row is the
// conversational handoff back to the user — "Claude is waiting on you"
// — so it has to surface where the user is reading.
//
// The click target switches to the Turns tab scrolled to the right turn
// so the user reaches the question card with no extra navigation. The
// announcement state (`Claude is waiting on you` vs `Answered`) is read
// off the durable projection's `announcement.answered` flag, not a local
// React flag, so a fresh tab opened after the answer arrived shows the
// resolved state.
function RunNeedsInputAnnouncement({
  entry,
  onOpenTurn,
}: {
  entry: TranscriptEntry;
  onOpenTurn?: (turnId: string) => void;
}) {
  const announcement = entry.announcement;
  const answered = announcement?.answered ?? false;
  const summary = announcement?.questionSummary ?? entry.meta?.detail ?? "";
  const title = entry.meta?.title ?? (answered ? "Answered" : "Claude is waiting on you");
  const targetTurnId = announcement?.targetTurnId ?? entry.turnId ?? "";
  const handleOpen = (): void => {
    if (!targetTurnId) return;
    onOpenTurn?.(targetTurnId);
  };
  const interactive = !answered && Boolean(targetTurnId && onOpenTurn);
  return (
    <div
      className={`run-needs-input-announcement${answered ? " run-needs-input-announcement-answered" : ""}`}
      data-answered={answered ? "true" : "false"}
      role="group"
      aria-label={title}
    >
      <span className="run-needs-input-announcement-icon" aria-hidden="true">
        {answered ? (
          <CheckIcon size={14} aria-hidden="true" />
        ) : (
          <MessageSquareIcon size={14} aria-hidden="true" />
        )}
      </span>
      <div className="run-needs-input-announcement-body">
        <div className="run-needs-input-announcement-title">{title}</div>
        {summary && (
          <p className="run-needs-input-announcement-detail">{summary}</p>
        )}
      </div>
      {interactive && (
        <button
          type="button"
          className="run-needs-input-announcement-cta"
          onClick={handleOpen}
          aria-label="Open the question in Turns"
        >
          Open in Turns
        </button>
      )}
      {answered && targetTurnId && onOpenTurn && (
        <button
          type="button"
          className="run-needs-input-announcement-cta run-needs-input-announcement-cta-secondary"
          onClick={handleOpen}
          aria-label="View answered question in Turns"
        >
          View in Turns
        </button>
      )}
    </div>
  );
}

function RunMetaBlock({ entry }: { entry: TranscriptEntry }) {
  const isError = entry.meta?.severity === "error";
  const title = entry.meta?.title ?? entry.text ?? "";
  const detail = entry.meta?.detail;
  // JSON pretty-print fallback for detail strings that look like JSON.
  let prettyDetail: string | null = null;
  if (detail) {
    try {
      const parsed = JSON.parse(detail);
      prettyDetail = JSON.stringify(parsed, null, 2);
    } catch {
      prettyDetail = null;
    }
  }
  return (
    <div className={`run-meta${isError ? " run-meta-error" : ""}`}>
      <span className="run-meta-icon">
        {isError ? (
          <AlertCircleIcon size={14} aria-hidden="true" />
        ) : (
          <InfoIcon size={14} aria-hidden="true" />
        )}
      </span>
      <div className="run-meta-body">
        <div className="run-meta-title">{title}</div>
        {detail && (
          <pre className="run-meta-detail">{prettyDetail ?? detail}</pre>
        )}
      </div>
    </div>
  );
}

function isBackgroundTaskRunning(entry: TranscriptEntry): boolean {
  return entry.taskStatus === "running" || entry.taskStatus === "unknown";
}

function isBackgroundTaskEntry(entry: TranscriptEntry): boolean {
  return entry.kind === "background_task";
}

function isShellToolEntry(entry: TranscriptEntry): boolean {
  return entry.kind === "tool" && entry.toolKind === "shell";
}

function isRunningShellInvocationEntry(entry: TranscriptEntry): boolean {
  return (
    isShellToolEntry(entry) &&
    normalizeToolState(entry.toolStatus) === "running"
  );
}

function isDetachedShellCandidateEntry(entry: TranscriptEntry): boolean {
  return (
    isShellToolEntry(entry) &&
    normalizeToolState(entry.toolStatus) !== "running" &&
    Boolean(detachedShellLaunchReason(entry))
  );
}

function backgroundTaskStatusLabel(status: ConversationBackgroundTaskStatus | undefined): string {
  switch (status) {
    case "completed":
      return "completed";
    case "failed":
      return "failed";
    case "stopped":
      return "stopped";
    case "running":
      return "running";
    default:
      return "active";
  }
}

function backgroundTaskTitle(entry: TranscriptEntry): string {
  return (
    entry.taskCommand ??
    entry.taskSummary ??
    entry.taskDescription ??
    entry.lastToolName ??
    "Shell task"
  );
}

function backgroundTaskSubtitle(entry: TranscriptEntry): string {
  const process = entry.taskProcessId ?? entry.taskId;
  const parts = [
    process ? `process ${process}` : "",
    entry.taskCwd ? entry.taskCwd : "",
  ].filter(Boolean);
  return parts.join(" · ");
}

function backgroundActivityKindLabel(entry: TranscriptEntry): string {
  if (isDetachedShellCandidateEntry(entry)) return "Detached process";
  return isBackgroundTaskEntry(entry) ? "Managed task" : "Shell command";
}

function backgroundActivityTitle(entry: TranscriptEntry): string {
  if (isBackgroundTaskEntry(entry)) return backgroundTaskTitle(entry);
  return shellInvocationCommand(entry) ?? "Shell command";
}

function backgroundActivitySubtitle(entry: TranscriptEntry): string {
  if (isBackgroundTaskEntry(entry)) return backgroundTaskSubtitle(entry) || "managed background task";
  const parts = [
    entry.toolName && entry.toolInput && entry.toolName !== entry.toolInput ? entry.toolName : "",
    entry.providerItemId ? `item ${entry.providerItemId}` : "",
  ].filter(Boolean);
  return parts.join(" · ") || "active shell invocation";
}

function backgroundActivityStatusLabel(entry: TranscriptEntry): string {
  if (isDetachedShellCandidateEntry(entry)) return "untracked";
  return isBackgroundTaskEntry(entry)
    ? backgroundTaskStatusLabel(entry.taskStatus)
    : normalizeToolState(entry.toolStatus);
}

function canStopBackgroundActivity(
  entry: TranscriptEntry,
  codexBackgroundStopAvailable: boolean,
): boolean {
  if (isDetachedShellCandidateEntry(entry)) return false;
  if (isRunningShellInvocationEntry(entry)) return Boolean(entry.turnId?.trim());
  return (
    isBackgroundTaskEntry(entry) &&
    isBackgroundTaskRunning(entry) &&
    codexBackgroundStopAvailable &&
    Boolean(entry.turnId?.trim() && entry.taskId?.trim())
  );
}

function backgroundStopLabel(entry: TranscriptEntry): string {
  return isBackgroundTaskEntry(entry) ? "Stop all" : "Stop";
}

function backgroundStopTitle(entry: TranscriptEntry): string {
  if (isBackgroundTaskEntry(entry)) {
    return "Stop all Codex background terminals for this session";
  }
  return "Stop the turn running this shell command";
}

function backgroundActivityCommand(entry: TranscriptEntry): string | undefined {
  return isBackgroundTaskEntry(entry) ? entry.taskCommand : shellInvocationCommand(entry);
}

function backgroundActivityOutput(entry: TranscriptEntry): string | undefined {
  return isBackgroundTaskEntry(entry) ? entry.taskOutput : entry.toolOutput;
}

function backgroundActivityStartedAt(entry: TranscriptEntry): string | undefined {
  return isBackgroundTaskEntry(entry) ? entry.startedAt : entry.startedAt ?? entry.time;
}

function shellInvocationCommand(entry: TranscriptEntry): string | undefined {
  const input = tryParseJson(entry.toolInput);
  if (isJsonObject(input) && typeof input.command === "string" && input.command) {
    return input.command;
  }
  return entry.toolInput ?? entry.toolName;
}

function detachedShellLaunchReason(entry: TranscriptEntry): string | undefined {
  const command = shellInvocationCommand(entry) ?? "";
  if (!command) return undefined;
  if (/\bnohup\b/i.test(command)) return "nohup";
  if (/\bdisown\b/i.test(command)) return "disown";
  if (/\bsetsid\b/i.test(command)) return "setsid";
  if (/\btmux\b[\s\S]{0,80}\b(?:new|new-session)\b[\s\S]{0,80}(?:\s-d\b|\s-detached\b)/i.test(command)) {
    return "tmux detached session";
  }
  if (/\bscreen\b[\s\S]{0,80}\s-dm/i.test(command)) return "screen detached session";
  if (/&\s*(?:echo|printf)\b[\s\S]{0,40}\$!/.test(command)) return "background child PID";
  if (/[^\s&]\s&\s*(?:$|[;)])/.test(command)) return "background child";
  return undefined;
}

function detachedShellPid(entry: TranscriptEntry): string | undefined {
  const output = entry.toolOutput ?? "";
  const pidLine = output
    .split(/\r?\n/)
    .map((line) => line.trim())
    .find((line) => /^\d{2,}$/.test(line));
  if (pidLine) return pidLine;
  const match = /\bpid\b\s*[:=]?\s*(\d{2,})\b/i.exec(output);
  return match?.[1];
}

function RunBackgroundTaskBlock({
  entry,
  showTimestamps,
  onOpenTask,
}: {
  entry: TranscriptEntry;
  showTimestamps: boolean;
  onOpenTask?: (entry: TranscriptEntry) => void;
}) {
  const running = isBackgroundTaskRunning(entry);
  const label = backgroundTaskStatusLabel(entry.taskStatus);
  const summary = backgroundTaskTitle(entry);
  const detail = entry.taskDescription && entry.taskDescription !== summary ? entry.taskDescription : "";
  const errorText = entry.taskError == null ? "" : shortJson(entry.taskError);
  return (
    <button
      type="button"
      className="run-background-task"
      data-state={entry.taskStatus ?? "unknown"}
      data-running={running ? "true" : undefined}
      onClick={() => onOpenTask?.(entry)}
      title="Open background activity"
    >
      <div className="run-background-task-icon" title="Managed background task">
        {running ? (
          <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
        ) : (
          <SquareTerminalIcon size={14} aria-hidden="true" />
        )}
      </div>
      <div className="run-background-task-body">
        <div className="run-background-task-top">
          <span className="run-background-task-title">{summary}</span>
          <span className="run-background-task-status">{label}</span>
          {showTimestamps && (
            <ToolTiming
              startedAt={entry.startedAt ?? entry.time}
              completedAt={entry.completedAt}
              running={running}
            />
          )}
        </div>
        {(detail || entry.taskId || errorText) && (
          <div className="run-background-task-detail">
            {detail && <span>{detail}</span>}
            {(entry.taskProcessId ?? entry.taskId) && (
              <span>{entry.taskProcessId ? `process ${entry.taskProcessId}` : `task ${entry.taskId}`}</span>
            )}
            {errorText && <span className="run-background-task-error">{errorText}</span>}
          </div>
        )}
      </div>
    </button>
  );
}

function BackgroundLedger({
  entries,
  active,
  onOpen,
  disabled = false,
  title = "Background",
}: {
  entries: TranscriptEntry[];
  active: boolean;
  onOpen: () => void;
  disabled?: boolean;
  title?: string;
}) {
  const activeCount = entries.length;
  return (
    <button
      type="button"
      className={`run-tab run-shell-tasks-trigger${active ? " run-tab-active" : ""}`}
      onClick={disabled ? undefined : onOpen}
      aria-pressed={active}
      disabled={disabled}
      title={title}
    >
      <ActivityIcon className="run-tab-icon" aria-hidden="true" />
      <span>Background</span>
      <span
        className="run-shell-tasks-count"
        data-active={activeCount > 0 ? "true" : undefined}
        aria-label={`${activeCount} background items`}
      >
        {activeCount}
      </span>
    </button>
  );
}

function TurnsTab({
  active,
  count = 0,
  hasActiveTurn = false,
  disabled = false,
  title = disabled ? "Turns are available once the agent has turn activity" : "Turns",
  onOpen,
}: {
  active: boolean;
  count?: number;
  hasActiveTurn?: boolean;
  disabled?: boolean;
  title?: string;
  onOpen: () => void;
}) {
  return (
    <button
      type="button"
      className={`run-tab run-turns-trigger${active ? " run-tab-active" : ""}`}
      onClick={disabled ? undefined : onOpen}
      aria-pressed={active}
      disabled={disabled}
      title={title}
    >
      <ActivityIcon className="run-tab-icon" aria-hidden="true" />
      <span>Turns</span>
      {count > 0 && (
        <span
          className="run-shell-tasks-count"
          data-active={hasActiveTurn ? "true" : undefined}
        >
          {count}
        </span>
      )}
    </button>
  );
}

function BackgroundMeta({
  label,
  value,
}: {
  label: string;
  value: string | number | undefined;
}) {
  if (value === undefined || value === "") return null;
  return (
    <div className="run-shell-task-meta-item">
      <span className="run-shell-task-meta-label">{label}</span>
      <span className="run-shell-task-meta-value">{value}</span>
    </div>
  );
}

function BackgroundScreen({
  shellEntries,
  detachedEntries,
  view,
  onViewChange,
  selectedId,
  onSelect,
  canStopEntry,
  onStop,
}: {
  shellEntries: TranscriptEntry[];
  detachedEntries: TranscriptEntry[];
  view: BackgroundView;
  onViewChange: (view: BackgroundView) => void;
  selectedId: string | null;
  onSelect: (id: string) => void;
  canStopEntry: (entry: TranscriptEntry) => boolean;
  onStop: (entry: TranscriptEntry) => void;
}) {
  const displayEntries = view === "shells" ? shellEntries : detachedEntries;
  const managedTaskCount = shellEntries.filter(isBackgroundTaskEntry).length;
  const shellInvocationCount = shellEntries.filter(isRunningShellInvocationEntry).length;
  const selected =
    displayEntries.find((entry) => entry.id === selectedId) ??
    displayEntries[0] ??
    null;
  const listLabel = view === "shells" ? "Active" : "Detected";
  const emptyText = view === "shells" ? "No active shells." : "No detached process candidates.";
  const selectedStopAvailable = selected ? canStopEntry(selected) : false;
  return (
    <div className="run-shell-tasks-page">
      <div className="run-shell-tasks-list">
        <div className="run-shell-tasks-list-head">
          <span>{listLabel}</span>
          <span>{displayEntries.length}</span>
        </div>
        <div className="run-background-breakdown" aria-label="Background activity types">
          <span>Tasks {managedTaskCount}</span>
          <span>Shell {shellInvocationCount}</span>
        </div>
        <div className="run-background-tabs" role="tablist" aria-label="Background views">
          <button
            type="button"
            className={`run-background-tab${view === "shells" ? " run-background-tab-active" : ""}`}
            role="tab"
            aria-selected={view === "shells"}
            onClick={() => onViewChange("shells")}
          >
            <span>Shells</span>
            <span>{shellEntries.length}</span>
          </button>
          <button
            type="button"
            className={`run-background-tab${view === "detached" ? " run-background-tab-active" : ""}`}
            role="tab"
            aria-selected={view === "detached"}
            onClick={() => onViewChange("detached")}
          >
            <span>Detached</span>
            <span>{detachedEntries.length}</span>
          </button>
        </div>
        {displayEntries.length === 0 ? (
          <div className="run-shell-tasks-empty">{emptyText}</div>
        ) : (
          displayEntries.map((entry) => (
            <button
              key={entry.id}
              type="button"
              className={`run-shell-task-row${selected?.id === entry.id ? " run-shell-task-row-active" : ""}`}
              data-state={
                isDetachedShellCandidateEntry(entry)
                  ? "unknown"
                  : isBackgroundTaskEntry(entry)
                    ? entry.taskStatus ?? "unknown"
                    : "running"
              }
              onClick={() => onSelect(entry.id)}
            >
              <span className="run-shell-task-row-dot" aria-hidden="true" />
              <span className="run-shell-task-row-main">
                <span className="run-shell-task-row-title">{backgroundActivityTitle(entry)}</span>
                <span className="run-shell-task-row-sub">{backgroundActivitySubtitle(entry)}</span>
              </span>
              <span className="run-shell-task-row-status">
                {backgroundActivityStatusLabel(entry)}
              </span>
            </button>
          ))
        )}
      </div>
      <div className="run-shell-task-detail-pane">
        {!selected ? (
          <div className="run-shell-task-detail-empty">
            <ActivityIcon size={28} aria-hidden="true" />
            <span>{emptyText}</span>
          </div>
        ) : (
          <>
            <div className="run-shell-task-detail-head">
              <div className="run-shell-task-detail-title">
                {isBackgroundTaskEntry(selected) ? (
                  <SquareTerminalIcon size={16} aria-hidden="true" />
                ) : isDetachedShellCandidateEntry(selected) ? (
                  <ActivityIcon size={16} aria-hidden="true" />
                ) : (
                  <TerminalIcon size={16} aria-hidden="true" />
                )}
                <span>{backgroundActivityTitle(selected)}</span>
              </div>
              <div className="run-shell-task-detail-actions">
                {selectedStopAvailable && (
                  <button
                    type="button"
                    className="run-shell-task-stop"
                    onClick={() => onStop(selected)}
                    title={backgroundStopTitle(selected)}
                  >
                    <SquareIcon size={13} aria-hidden="true" />
                    <span>{backgroundStopLabel(selected)}</span>
                  </button>
                )}
                <span
                  className="run-shell-task-detail-status"
                  data-state={
                    isDetachedShellCandidateEntry(selected)
                      ? "unknown"
                      : isBackgroundTaskEntry(selected)
                        ? selected.taskStatus ?? "unknown"
                        : "running"
                  }
                >
                  {backgroundActivityStatusLabel(selected)}
                </span>
              </div>
            </div>
            <div className="run-shell-task-meta">
              <BackgroundMeta label="Type" value={backgroundActivityKindLabel(selected)} />
              {isBackgroundTaskEntry(selected) ? (
                <>
                  <BackgroundMeta label="Task" value={selected.taskId} />
                  <BackgroundMeta label="Process" value={selected.taskProcessId} />
                  <BackgroundMeta label="Cwd" value={selected.taskCwd} />
                </>
              ) : isDetachedShellCandidateEntry(selected) ? (
                <>
                  <BackgroundMeta label="PID" value={detachedShellPid(selected)} />
                  <BackgroundMeta label="Reason" value={detachedShellLaunchReason(selected)} />
                  <BackgroundMeta label="Item" value={selected.providerItemId} />
                </>
              ) : (
                <BackgroundMeta label="Item" value={selected.providerItemId} />
              )}
              <BackgroundMeta label="Started" value={formatToolFullTime(backgroundActivityStartedAt(selected))} />
              <BackgroundMeta
                label="Duration"
                value={selected.taskDurationMs == null ? undefined : formatTurnDuration(selected.taskDurationMs)}
              />
              <BackgroundMeta label="Exit" value={selected.taskExitCode} />
            </div>
            {backgroundActivityCommand(selected) && (
              <div className="run-shell-task-section">
                <div className="run-shell-task-section-label">Command</div>
                <pre className="run-shell-task-code">{backgroundActivityCommand(selected)}</pre>
              </div>
            )}
            <div className="run-shell-task-section run-shell-task-section-output">
              <div className="run-shell-task-section-label">Output</div>
              <pre className="run-shell-task-output">
                {backgroundActivityOutput(selected)?.trim()
                  ? backgroundActivityOutput(selected)
                  : "No output yet."}
              </pre>
            </div>
            {isBackgroundTaskEntry(selected) && selected.taskError != null && (
              <div className="run-shell-task-section">
                <div className="run-shell-task-section-label">Error</div>
                <pre className="run-shell-task-output run-shell-task-output-error">
                  {shortJson(selected.taskError)}
                </pre>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

function ToolBody({ entry }: { entry: TranscriptEntry }) {
  const name = entry.toolName ?? "";
  const input = tryParseJson(entry.toolInput) as Record<string, unknown> | null;
  if (
    name === "Edit" ||
    name === "MultiEdit" ||
    name === "Write" ||
    name === "ApplyPatch"
  ) {
    return <ToolDiffBody entry={entry} input={input} />;
  }
  if (name === "TodoWrite") {
    return <ToolTodoBody input={input} />;
  }
  if (name === "Bash") {
    return <ToolBashBody entry={entry} input={input} />;
  }
  if (name === "Read") {
    return <ToolReadBody input={input} />;
  }
  if (name === "AskUserQuestion") {
    return <ToolAskUserBody entry={entry} input={input} />;
  }
  return <ToolDefaultBody entry={entry} input={input} />;
}

interface AskUserQuestion {
  question: string;
  header?: string;
  multiSelect: boolean;
  options: Array<{ label: string; description?: string; preview?: string }>;
  // allowFreeForm and secret are projected from the Tank-canonical
  // question shape produced by the runner adapters. Claude maps every
  // question to allowFreeForm=true (mirroring Claude Code's host UI's
  // built-in "Other" affordance); codex maps from its native `isOther`
  // and `isSecret` flags. Older durable events without these fields
  // render as if both flags were false — they predate the adapter
  // normalization and intentionally do not get a free-form path
  // retrofitted at read time. See docs/migration-policy.md → "Old data
  // does not justify runtime support."
  allowFreeForm: boolean;
  secret: boolean;
}

function parseAskUserQuestions(input: Record<string, unknown> | null): AskUserQuestion[] {
  if (!Array.isArray(input?.questions)) return [];
  return (input.questions as Array<Record<string, unknown>>).map((q) => {
    const rawOptions = Array.isArray(q.options) ? q.options : [];
    const options = (rawOptions as Array<Record<string, unknown>>).map((opt) => ({
      label: String(opt.label ?? ""),
      description: typeof opt.description === "string" ? opt.description : undefined,
      preview: typeof opt.preview === "string" ? opt.preview : undefined,
    }));
    return {
      question: String(q.question ?? ""),
      header: typeof q.header === "string" && q.header ? q.header : undefined,
      multiSelect: q.multiSelect === true,
      options,
      allowFreeForm: q.allowFreeForm === true,
      secret: q.secret === true,
    } satisfies AskUserQuestion;
  });
}

function ToolAskUserBody({
  entry,
  input,
}: {
  entry: TranscriptEntry;
  input: Record<string, unknown> | null;
}) {
  const { sendInputReply } = useContext(RunContext);
  // Per-question selections (multi-select carries an array; single-select
  // is a single-element array or empty). Submission converts to the wire
  // shape `Record<questionText, string[]>` so single + multi share a
  // payload.
  const [selections, setSelections] = useState<Record<string, string[]>>({});
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const [replyError, setReplyError] = useState<string | null>(null);
  // optimisticSubmit snapshots the answer the user *just submitted* so the
  // UI can lock the card the moment the click lands — before the runner's
  // tool.approval_resolved makes the round trip back through SSE. Without
  // this, three regressions surface in the live slot:
  //   (a) the submit button re-enables as soon as the HTTP POST resolves,
  //       so the user can keep clicking
  //   (b) the free-form textarea stays open because `answered` is still
  //       false until the durable resolve arrives
  //   (c) the selected option dot "vanishes" when the parent re-renders
  //       with a stale activity-cache entry whose toolStatus is still
  //       "started" — the durable cache for the Turns tab doesn't refresh
  //       on its own (one-shot fetch in loadActivityForTurn).
  // The optimistic snapshot is a *fallback*: durable state still wins when
  // it eventually arrives, so a fresh tab opened later renders the same
  // answer from the projection.
  const [optimisticSubmit, setOptimisticSubmit] = useState<{
    answers: Record<string, string[]>;
    annotations: Record<string, { preview?: string; notes?: string }>;
  } | null>(null);

  const questions = parseAskUserQuestions(input);

  // durableAnswers is the canonical source of truth for the answered
  // state — it comes from the `tool.approval_resolved` event's payload
  // via projection, so a fresh tab opened after the user answered (in
  // this or any other tab) still renders the selections. Local
  // `selections` state only powers the pre-submit click-to-pick UX.
  const durableAnswers = entry.askUserAnswers;
  const hasDurableAnswers =
    !!durableAnswers && Object.keys(durableAnswers).length > 0;
  // `answered` flips to true the instant the user submits, not just when
  // the durable event lands. Otherwise the post-submit window leaves the
  // card behaving as if the user hasn't answered: button re-clickable,
  // textbox open, options re-selectable. The optimisticSubmit snapshot
  // is what makes that lock honest about the user's pick.
  const answered =
    hasDurableAnswers ||
    entry.toolStatus === "completed" ||
    optimisticSubmit !== null;

  // After answering, the per-question UI stays rendered so the user
  // can scroll back in chat history and see exactly what was offered
  // and what they picked. Selection priority: durable answer (truth) →
  // optimistic snapshot (user just submitted, durable not yet back) →
  // local in-flight selection. This keeps the historical record honest
  // for both the live submit window AND the activity-cache stale window.
  function selectedLabelsFor(q: AskUserQuestion): string[] {
    if (durableAnswers && durableAnswers[q.question]) {
      return durableAnswers[q.question].labels;
    }
    if (optimisticSubmit && optimisticSubmit.answers[q.question]) {
      return optimisticSubmit.answers[q.question];
    }
    return selections[q.question] ?? [];
  }

  function answeredNoteFor(question: string): string | undefined {
    if (durableAnswers && durableAnswers[question]) {
      return durableAnswers[question].notes;
    }
    if (optimisticSubmit && optimisticSubmit.annotations[question]?.notes) {
      return optimisticSubmit.annotations[question].notes;
    }
    return undefined;
  }

  // A question is answerable when the user has picked at least one
  // option OR provided free-form text (when the question's allowFreeForm
  // flag is set). The legacy gate required ≥1 selection per question,
  // which left codex questions with options=null+isOther=true
  // unanswerable. The Tank-canonical projection makes that gap explicit:
  // the renderer surfaces a free-form textarea whenever allowFreeForm
  // is true, and submit accepts text without an option pick.
  function questionHasResponse(q: AskUserQuestion): boolean {
    if ((selections[q.question]?.length ?? 0) > 0) return true;
    if (q.allowFreeForm && (notes[q.question]?.trim().length ?? 0) > 0) return true;
    return false;
  }
  const isReady =
    !answered &&
    !submitting &&
    questions.length > 0 &&
    questions.every((q) => questionHasResponse(q));

  function toggleSelection(q: AskUserQuestion, label: string): void {
    if (answered || submitting) return;
    setSelections((prev) => {
      const current = prev[q.question] ?? [];
      if (q.multiSelect) {
        const next = current.includes(label)
          ? current.filter((l) => l !== label)
          : [...current, label];
        return { ...prev, [q.question]: next };
      }
      // Single-select: clicking always selects exactly that label
      // (re-clicking selected option is a no-op submit affordance).
      return { ...prev, [q.question]: [label] };
    });
  }

  function setNoteFor(question: string, value: string): void {
    setNotes((prev) => ({ ...prev, [question]: value }));
  }

  async function submit(): Promise<void> {
    if (submitting || answered || !isReady) return;
    const answers: Record<string, string[]> = {};
    const annotations: Record<string, { preview?: string; notes?: string }> = {};
    for (const q of questions) {
      const labels = selections[q.question] ?? [];
      const notesText = q.allowFreeForm ? notes[q.question]?.trim() ?? "" : "";
      // The wire shape requires `answers[question]` to be a non-empty
      // string array. When the user only typed free-form text, we
      // synthesize a single "Other" label so the answers map stays
      // valid; the actual free-form text rides in
      // `annotations[question].notes`, which is the Claude Agent SDK's
      // blessed channel for caller-supplied context on a tool answer.
      // The runner's canUseTool allow path returns both halves to the
      // SDK, and `tool.approval_resolved` mirrors them back into the
      // durable ledger.
      if (labels.length > 0) {
        answers[q.question] = labels;
      } else if (notesText) {
        answers[q.question] = ["Other"];
      } else {
        continue;
      }
      const preview = q.options.find((opt) => labels.includes(opt.label))?.preview;
      const ann: { preview?: string; notes?: string } = {};
      if (preview) ann.preview = preview;
      if (notesText) ann.notes = notesText;
      if (ann.preview || ann.notes) annotations[q.question] = ann;
    }
    // Lock the card BEFORE the await: the optimistic snapshot is the
    // user-visible truth until the durable resolve arrives. We do not
    // clear it on success — durable answers will take precedence in
    // selectedLabelsFor as soon as the projection delivers them; on
    // failure we clear it so the user can retry.
    setSubmitting(true);
    setReplyError(null);
    setOptimisticSubmit({ answers, annotations });
    try {
      await sendInputReply(entry, { answers, annotations });
    } catch (err) {
      setReplyError(err instanceof Error ? err.message : String(err));
      setOptimisticSubmit(null);
    } finally {
      setSubmitting(false);
    }
  }

  // Edge case: tool completed without a durable answer payload (legacy
  // events, or a non-input_reply completion path). The question UI is
  // still useful for context, but we tag the body so the styles can
  // make the unanswered options look inert.
  const completedWithoutAnswers =
    entry.toolStatus === "completed" && !hasDurableAnswers && !optimisticSubmit;
  // `confirmingSubmit` distinguishes "you just submitted, durable resolve
  // pending" from "durable answer landed." Drives the status row copy so
  // the user gets a clear "Submitted, waiting for Claude..." → "Your
  // answer" progression instead of an instant green check that lies
  // about durability.
  const confirmingSubmit = optimisticSubmit !== null && !hasDurableAnswers;

  return (
    <div
      className={`run-tool-body run-tool-ask${answered ? " run-tool-ask-locked" : ""}`}
      data-answered={answered ? "true" : "false"}
    >
      {answered && (
        <div
          className={`run-tool-ask-status${confirmingSubmit ? " run-tool-ask-status-pending" : ""}`}
          role="status"
          aria-live="polite"
        >
          <span className="run-tool-ask-status-icon" aria-hidden="true">
            {confirmingSubmit ? (
              <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
            ) : (
              "✓"
            )}
          </span>
          <span className="run-tool-ask-status-label">
            {confirmingSubmit
              ? "Submitted — waiting for Claude…"
              : completedWithoutAnswers
                ? "Answered"
                : "Your answer"}
          </span>
        </div>
      )}
      {questions.map((q, qi) => {
        const selectedLabels = selectedLabelsFor(q);
        const answeredNote = answeredNoteFor(q.question);
        const liveNote = notes[q.question] ?? "";
        // The free-form textarea is always reachable when the question
        // declares allowFreeForm=true. The legacy code gated this on
        // "an option with a preview has been selected," which made the
        // free-form path unreachable for the wild codex case
        // (options=null + isOther=true → no preview, no selection → no
        // textarea). Tank's Other path is a first-class affordance,
        // not a side-effect of preview content.
        const showFreeForm = !answered && q.allowFreeForm;
        return (
          <div key={qi} className="run-tool-ask-question">
            {q.header && <span className="run-tool-ask-chip">{q.header}</span>}
            {q.question && <p className="run-tool-ask-text">{q.question}</p>}
            {q.options.length === 0 && q.allowFreeForm && !answered && (
              <p className="run-tool-ask-text run-tool-ask-text-muted">
                No options offered — answer below.
              </p>
            )}
            <div
              className="run-tool-ask-options"
              role={q.multiSelect ? "group" : "radiogroup"}
              aria-label={q.question}
            >
              {q.options.map((opt, oi) => {
                const selected = selectedLabels.includes(opt.label);
                const muted = answered && !selected;
                const optionClass =
                  "run-tool-ask-option" +
                  (selected ? " run-tool-ask-option-selected" : "") +
                  (muted ? " run-tool-ask-option-muted" : "") +
                  (answered ? " run-tool-ask-option-locked" : "");
                return (
                  <button
                    key={oi}
                    type="button"
                    className={optionClass}
                    aria-pressed={selected}
                    disabled={submitting || answered}
                    onClick={() => toggleSelection(q, opt.label)}
                  >
                    <span
                      className="run-tool-ask-option-marker"
                      aria-hidden="true"
                      data-selected={selected ? "true" : "false"}
                    >
                      {q.multiSelect
                        ? selected
                          ? "☑"
                          : "☐"
                        : selected
                          ? "●"
                          : "○"}
                    </span>
                    <span className="run-tool-ask-option-body">
                      <span className="run-tool-ask-option-label">{opt.label}</span>
                      {opt.description && (
                        <span className="run-tool-ask-option-desc">{opt.description}</span>
                      )}
                      {opt.preview && selected && (
                        <span
                          className="run-tool-ask-option-preview"
                          // eslint-disable-next-line react/no-danger -- SDK-vetted preview HTML fragment; <script>/<style> are blocked by the SDK's own Ki_ validator before the question is rendered.
                          dangerouslySetInnerHTML={{ __html: opt.preview }}
                        />
                      )}
                    </span>
                  </button>
                );
              })}
            </div>
            {answered && answeredNote && (
              <div className="run-tool-ask-notes-readonly">
                <span className="run-tool-ask-notes-readonly-label">Notes</span>
                <p className="run-tool-ask-notes-readonly-text">{answeredNote}</p>
              </div>
            )}
            {showFreeForm && (
              <label className="run-tool-ask-notes-label">
                <span>
                  {q.options.length === 0
                    ? "Your answer"
                    : "Say something else (optional)"}
                </span>
                <textarea
                  className="run-tool-ask-notes"
                  rows={q.options.length === 0 ? 4 : 2}
                  value={liveNote}
                  disabled={submitting}
                  onChange={(e) => setNoteFor(q.question, e.target.value)}
                  placeholder={
                    q.options.length === 0
                      ? "Type your answer…"
                      : "Add a free-form reply or extra context…"
                  }
                  {...(q.secret ? { spellCheck: false, autoComplete: "off" } : {})}
                />
              </label>
            )}
          </div>
        );
      })}
      {!answered && (
        <div className="run-tool-ask-submit-row">
          <button
            type="button"
            className="run-tool-ask-submit"
            disabled={!isReady || submitting}
            onClick={() => void submit()}
          >
            {submitting ? "Sending…" : "Submit answer"}
          </button>
        </div>
      )}
      {replyError && <p className="run-tool-ask-error">{replyError}</p>}
    </div>
  );
}

function ToolDiffBody({
  entry,
  input,
}: {
  entry: TranscriptEntry;
  input: Record<string, unknown> | null;
}) {
  const filePath = (input?.file_path as string) ?? "";
  // Edit: old_string + new_string. Write: content (treat as full add).
  // MultiEdit: edits[]. ApplyPatch: patches/diff text.
  let blocks: { kind: "ctx" | "del" | "add"; text: string }[] = [];
  if ((entry.toolName === "Edit") && input) {
    blocks = computeLineDiff(
      String(input.old_string ?? ""),
      String(input.new_string ?? ""),
    );
  } else if (entry.toolName === "Write" && input) {
    blocks = String(input.content ?? "")
      .split("\n")
      .map((t) => ({ kind: "add" as const, text: t }));
  } else if (entry.toolName === "MultiEdit" && Array.isArray(input?.edits)) {
    for (const ed of input.edits as Array<Record<string, unknown>>) {
      blocks.push({ kind: "ctx", text: "..." });
      const sub = computeLineDiff(
        String(ed.old_string ?? ""),
        String(ed.new_string ?? ""),
      );
      blocks.push(...sub);
    }
  } else {
    // Fallback to raw input dump.
    return <ToolDefaultBody entry={entry} input={input} />;
  }
  return (
    <div className="run-tool-body run-tool-diff">
      {filePath && <div className="run-tool-diff-path">{filePath}</div>}
      <pre className="run-tool-diff-pre">
        {blocks.map((l, i) => (
          <div key={i} className={`run-diff-line run-diff-${l.kind}`}>
            <span className="run-diff-marker">
              {l.kind === "del" ? "-" : l.kind === "add" ? "+" : " "}
            </span>
            <span className="run-diff-text">{l.text}</span>
          </div>
        ))}
      </pre>
      {entry.toolOutput && (
        <details className="run-tool-output">
          <summary>Output</summary>
          <PreWithLinks>{entry.toolOutput}</PreWithLinks>
        </details>
      )}
    </div>
  );
}

function ToolTodoBody({ input }: { input: Record<string, unknown> | null }) {
  const todos = (input?.todos as Array<Record<string, unknown>>) ?? [];
  if (!todos.length) return <div className="run-tool-body">no todos</div>;
  return (
    <ul className="run-tool-body run-tool-todos">
      {todos.map((t, i) => {
        const status = String(t.status ?? "pending");
        const content = String(t.content ?? t.activeForm ?? "");
        return (
          <li key={i} className={`run-tool-todo run-tool-todo-${status}`}>
            <span className="run-tool-todo-marker" aria-hidden="true">
              {status === "completed" ? "✓" : status === "in_progress" ? "→" : "○"}
            </span>
            <span className="run-tool-todo-text">{content}</span>
          </li>
        );
      })}
    </ul>
  );
}

function ToolBashBody({
  entry,
  input,
}: {
  entry: TranscriptEntry;
  input: Record<string, unknown> | null;
}) {
  const command = String(input?.command ?? "");
  return (
    <div className="run-tool-body run-tool-bash">
      <div className="run-tool-section-title">$ command</div>
      <pre className="run-tool-bash-cmd">{command}</pre>
      {entry.toolOutput && (
        <>
          <div className="run-tool-section-title">output</div>
          <PreWithLinks className="run-tool-bash-out">{entry.toolOutput}</PreWithLinks>
        </>
      )}
    </div>
  );
}

function ToolReadBody({ input }: { input: Record<string, unknown> | null }) {
  const filePath = String(input?.file_path ?? "");
  const offset = input?.offset != null ? String(input.offset) : "";
  const limit = input?.limit != null ? String(input.limit) : "";
  return (
    <div className="run-tool-body run-tool-read">
      <span className="run-tool-read-path">{filePath}</span>
      {(offset || limit) && (
        <span className="run-tool-read-meta">
          {offset && `offset=${offset}`} {limit && `limit=${limit}`}
        </span>
      )}
    </div>
  );
}

function ToolDefaultBody({
  entry,
  input,
}: {
  entry: TranscriptEntry;
  input: Record<string, unknown> | null;
}) {
  const inputText = input
    ? JSON.stringify(input, null, 2)
    : (entry.toolInput ?? "");
  return (
    <div className="run-tool-body">
      {inputText && (
        <>
          <div className="run-tool-section-title">input</div>
          <pre className="run-tool-default-pre">{inputText}</pre>
        </>
      )}
      {entry.toolOutput && (
        <>
          <div className="run-tool-section-title">output</div>
          <PreWithLinks className="run-tool-default-pre">{entry.toolOutput}</PreWithLinks>
        </>
      )}
    </div>
  );
}

function RunToolItem({
  entry,
  showTimestamps,
  expanded,
  onExpandedChange,
}: {
  entry: TranscriptEntry;
  showTimestamps: boolean;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
}) {
  const cfg = getToolVisualConfig(entry);
  const state = normalizeToolState(entry.toolStatus);
  const running = state === "running";
  return (
    <div
      className="run-transcript-tool"
      data-slot="tool-item"
      data-kind="tool"
      data-state={state}
    >
      <div
        className="run-transcript-tool-connector"
        data-slot="tool-item-connector"
      >
        <div
          className="run-transcript-tool-dot"
          data-slot="tool-item-dot"
        />
      </div>
      <div className="run-transcript-tool-content">
        <button
          type="button"
          className="run-transcript-tool-header"
          data-slot="tool-item-header"
          onClick={() => onExpandedChange(!expanded)}
          aria-expanded={expanded}
        >
          <span
            className="run-transcript-tool-icon"
            data-slot="tool-item-icon"
            title={cfg.tooltip}
            aria-label={cfg.tooltip}
          >
            <span className={`run-tool-icon-glyph ${cfg.colorClass}`} aria-hidden="true">
              <cfg.Icon size={14} strokeWidth={2} />
            </span>
          </span>
          <span
            className="run-transcript-tool-label"
            data-slot="tool-item-label"
          >
            {entry.toolName ?? "tool"}
          </span>
          {showTimestamps && (
            <ToolTiming
              startedAt={entry.startedAt ?? entry.time}
              completedAt={entry.completedAt}
              running={running}
            />
          )}
          {running && !showTimestamps && (
            <Loader2Icon
              size={12}
              className="run-spin run-tool-spinner"
              aria-hidden="true"
            />
          )}
          <span
            className="run-transcript-tool-chevron"
            data-slot="tool-item-chevron"
          >
            {expanded ? (
              <ChevronUpIcon
                size={14}
                strokeWidth={2}
                className="run-chevron-icon"
              />
            ) : (
              <ChevronDownIcon
                size={14}
                strokeWidth={2}
                className="run-chevron-icon"
              />
            )}
          </span>
        </button>
        {expanded && <ToolBody entry={entry} />}
      </div>
    </div>
  );
}

function RunToolGroup({
  entries,
  autoExpand,
  showTimestamps,
  open,
  onOpenChange,
  toolExpansionOverrides,
  onToolExpandedChange,
}: {
  entries: TranscriptEntry[];
  autoExpand: boolean;
  showTimestamps: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  toolExpansionOverrides: Record<string, boolean>;
  onToolExpandedChange: (entryId: string, expanded: boolean) => void;
}) {
  if (entries.length === 0) return null;
  if (entries.length === 1) {
    const entry = entries[0];
    return (
      <div className="run-transcript-tool-single" data-slot="tool-group-single">
        <RunToolItem
          entry={entry}
          showTimestamps={showTimestamps}
          expanded={toolItemExpanded(entry, autoExpand, toolExpansionOverrides)}
          onExpandedChange={(expanded) => onToolExpandedChange(entry.id, expanded)}
        />
      </div>
    );
  }
  const runningCount = entries.filter(
    (e) => normalizeToolState(e.toolStatus) === "running",
  ).length;
  const pendingAskUserCount = entries.filter(isPendingAskUserQuestionTool).length;
  const errorCount = entries.filter(
    (e) => (e.toolStatus ?? "") === "failed" || (e.toolStatus ?? "") === "error",
  ).length;
  const summaryParts = [`${entries.length} tool calls`];
  if (runningCount > 0) {
    summaryParts.push(`${runningCount} running`);
  }
  if (pendingAskUserCount > 0) {
    summaryParts.push("needs input");
  }
  if (errorCount > 0) {
    summaryParts.push(`${errorCount} error${errorCount === 1 ? "" : "s"}`);
  }
  const summary = summaryParts.join(" · ");
  const groupStartedAt = entries.find((entry) => entry.startedAt || entry.time);
  const groupCompletedAt = [...entries].reverse().find((entry) => entry.completedAt);
  return (
    <div
      className="run-transcript-tools"
      data-slot="tool-group"
      data-state={runningCount > 0 ? "running" : undefined}
    >
      <button
        type="button"
        className="run-transcript-tools-header"
        onClick={() => onOpenChange(!open)}
        aria-expanded={open}
      >
        <span
          className="run-transcript-tools-icon"
          title="Tool usage summary"
          aria-label="Tool usage summary"
        >
          <WrenchIcon
            size={14}
            strokeWidth={2}
            aria-hidden="true"
          />
        </span>
        <span className="run-transcript-tools-label">{summary}</span>
        {showTimestamps && (
          <ToolTiming
            startedAt={groupStartedAt?.startedAt ?? groupStartedAt?.time}
            completedAt={groupCompletedAt?.completedAt}
            running={runningCount > 0}
          />
        )}
        {runningCount > 0 && !showTimestamps && (
          <Loader2Icon
            size={12}
            className="run-spin run-tool-spinner"
            aria-hidden="true"
          />
        )}
        <span className="run-transcript-tools-chevron">
          {open ? (
            <ChevronUpIcon size={14} className="run-chevron-icon" />
          ) : (
            <ChevronDownIcon size={14} className="run-chevron-icon" />
          )}
        </span>
      </button>
      {open && (
        <div className="run-transcript-tools-body">
          {entries.map((e) => (
            <RunToolItem
              key={e.id}
              entry={e}
              showTimestamps={showTimestamps}
              expanded={toolItemExpanded(e, autoExpand, toolExpansionOverrides)}
              onExpandedChange={(expanded) => onToolExpandedChange(e.id, expanded)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function toolItemDefaultExpanded(entry: TranscriptEntry, autoExpand: boolean): boolean {
  return autoExpand || isAskUserQuestionTool(entry);
}

function toolItemExpanded(
  entry: TranscriptEntry,
  autoExpand: boolean,
  overrides: Record<string, boolean>,
): boolean {
  return overrides[entry.id] ?? toolItemDefaultExpanded(entry, autoExpand);
}

function toolGroupDefaultOpen(
  entries: TranscriptEntry[],
  autoExpand: boolean,
  toolExpansionOverrides: Record<string, boolean>,
): boolean {
  return (
    autoExpand ||
    entries.some(isPendingAskUserQuestionTool) ||
    entries.some((entry) => toolExpansionOverrides[entry.id] === true)
  );
}

function plural(count: number, singular: string, pluralLabel = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : pluralLabel}`;
}

function turnActivitySummary(entries: TranscriptEntry[]): string {
  const toolCount = entries.filter((entry) => entry.kind === "tool").length;
  const noteCount = entries.filter(
    (entry) => entry.kind === "message" && entry.role === "assistant",
  ).length;
  const reasoningCount = entries.filter((entry) => entry.kind === "reasoning").length;
  const taskCount = entries.filter((entry) => entry.kind === "background_task").length;
  const errorCount = entries.filter(
    (entry) =>
      (entry.kind === "tool" &&
        ((entry.toolStatus ?? "") === "failed" || (entry.toolStatus ?? "") === "error")) ||
      (entry.kind === "background_task" && entry.taskStatus === "failed") ||
      (entry.kind === "meta" && entry.meta?.severity === "error"),
  ).length;
  const parts: string[] = [];
  if (toolCount > 0) parts.push(plural(toolCount, "tool call"));
  if (taskCount > 0) parts.push(plural(taskCount, "background task"));
  if (noteCount > 0) parts.push(plural(noteCount, "progress note"));
  if (reasoningCount > 0) parts.push(plural(reasoningCount, "reasoning block"));
  if (errorCount > 0) parts.push(plural(errorCount, "error", "errors"));
  return parts.length > 0 ? parts.join(" / ") : plural(entries.length, "update");
}

function turnActivityShellSummary(summary: TurnActivitySummary | undefined): string {
  if (!summary) return "Activity";
  const parts: string[] = [];
  if ((summary.toolCount ?? 0) > 0) parts.push(plural(summary.toolCount ?? 0, "tool call"));
  if ((summary.backgroundTaskCount ?? 0) > 0) {
    parts.push(plural(summary.backgroundTaskCount ?? 0, "background task"));
  }
  if ((summary.progressNoteCount ?? 0) > 0) {
    parts.push(plural(summary.progressNoteCount ?? 0, "progress note"));
  }
  if ((summary.reasoningCount ?? 0) > 0) {
    parts.push(plural(summary.reasoningCount ?? 0, "reasoning block"));
  }
  if ((summary.errorCount ?? 0) > 0) parts.push(plural(summary.errorCount ?? 0, "error", "errors"));
  const childCount = summary.childCount ?? 0;
  return parts.length > 0 ? parts.join(" / ") : plural(childCount, "update");
}

type TurnViewItem = {
  turnId: string;
  label: string;
  summary: string;
  entries: TranscriptEntry[];
  shell?: TranscriptEntry;
  active: boolean;
  loaded: boolean;
  startedAt?: string;
  completedAt?: string;
};

function turnEntryTimestamp(entry: TranscriptEntry): string | undefined {
  return entry.startedAt ?? entry.time ?? entry.completedAt ?? entry.updatedAt;
}

function buildTurnViewItems(
  entries: TranscriptEntry[],
  activeTurnId: string | null,
  activityEntriesByTurn: Record<string, TranscriptEntry[] | undefined>,
): TurnViewItem[] {
  const order = new Map<string, number>();
  const shells = new Map<string, TranscriptEntry>();
  const rawEntries = new Map<string, TranscriptEntry[]>();
  entries.forEach((entry, index) => {
    const turnId = (entry.turnId ?? entry.activity?.turnId ?? "").trim();
    if (!turnId) return;
    if (!order.has(turnId)) order.set(turnId, index);
    if (isTurnActivityEntry(entry)) {
      shells.set(turnId, entry);
      return;
    }
    if (isUserMessageEntry(entry)) return;
    const bucket = rawEntries.get(turnId) ?? [];
    bucket.push(entry);
    rawEntries.set(turnId, bucket);
  });

  const active = activeTurnId?.trim() ?? "";
  if (active && !order.has(active)) order.set(active, entries.length);

  return Array.from(order.entries())
    .sort((a, b) => a[1] - b[1])
    .map(([turnId], index) => {
      const shell = shells.get(turnId);
      const loadedEntries = activityEntriesByTurn[turnId];
      const turnEntries = loadedEntries ?? rawEntries.get(turnId) ?? [];
      const shellSummary = shell?.activity;
      const startedAt =
        shellSummary?.startedAt ??
        turnEntries.find((entry) => turnEntryTimestamp(entry))?.startedAt ??
        turnEntries.find((entry) => turnEntryTimestamp(entry))?.time;
      const completedAt =
        shellSummary?.completedAt ??
        [...turnEntries].reverse().find((entry) => entry.completedAt || entry.turnTerminalAt || entry.time)?.completedAt ??
        [...turnEntries].reverse().find((entry) => entry.completedAt || entry.turnTerminalAt || entry.time)?.turnTerminalAt ??
        [...turnEntries].reverse().find((entry) => entry.completedAt || entry.turnTerminalAt || entry.time)?.time;
      return {
        turnId,
        label: `Turn ${index + 1}`,
        summary: shell ? turnActivityShellSummary(shellSummary) : turnActivitySummary(turnEntries),
        entries: turnEntries,
        shell,
        active: turnId === active,
        loaded: Boolean(loadedEntries),
        startedAt,
        completedAt,
      };
    });
}

// Formats elapsed milliseconds for the thinking indicator. Stays compact so
// it sits naturally alongside the bouncing dots: sub-minute renders as
// "12s", under an hour renders as "6m 12s", and an hour or longer collapses
// the seconds slot to keep the visual width bounded ("1h 4m"). Negative
// inputs clamp to zero so a clock skew between client and server never
// produces a weird "-3s" while a turn is genuinely live.
function formatThinkingElapsed(ms: number): string {
  const clamped = ms > 0 ? ms : 0;
  const totalSeconds = Math.floor(clamped / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (totalMinutes < 60) return `${totalMinutes}m ${seconds}s`;
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return `${hours}h ${minutes}m`;
}

// =====================================================================
// Thinking-bubble elapsed-time timer.
//
// Earlier revisions tried to anchor the timer to the backend's
// projected `activity.startedAt`. That source kept ambushing the timer
// — it could be empty before the first activity event landed, it could
// flip values mid-flight when the projection's `first` entry changed,
// and any stale `sessionStorage` from a previous tab could end up
// pinned at an "earlier than reality" baseline. Symptoms included
// "stuck at 0s forever", "stuck at a value that's higher than time
// since submit", and "lag before the seconds start ticking up".
//
// New approach (per user direction): the timer is a purely client-side
// stopwatch anchored to **the first moment the UI renders the bubble
// for a given (user, turn) pair**. Backend timestamps are no longer
// consulted. The anchor is captured eagerly in module-level state on
// the first read for the pair and mirrored to `sessionStorage` so a
// mid-turn refresh keeps the same anchor. A `useSyncExternalStore`
// ticker drives one-second re-renders for every concurrent bubble off
// a single shared interval — there is no per-component setInterval,
// so Virtuoso remounting an item can never clear the interval before
// it fires.
//
// Keying by user email avoids cross-account contamination: if user A
// signs out and user B signs in within the same browser tab,
// `sessionStorage` still has A's anchors but they're under A's
// namespace and never read by B.
// =====================================================================

const TURN_THINKING_START_CACHE_KEY_PREFIX = "tank.thinking-turn-start.v3.";
const turnThinkingStartCache = new Map<string, number>();

function turnThinkingStartCacheKey(userKey: string, turnId: string): string {
  return `${TURN_THINKING_START_CACHE_KEY_PREFIX}${userKey}:${turnId}`;
}

function readPersistedTurnThinkingStart(cacheKey: string): number | null {
  if (typeof window === "undefined" || !window.sessionStorage) return null;
  try {
    const raw = window.sessionStorage.getItem(cacheKey);
    if (!raw) return null;
    const value = Number(raw);
    return Number.isFinite(value) ? value : null;
  } catch {
    return null;
  }
}

function writePersistedTurnThinkingStart(cacheKey: string, value: number): void {
  if (typeof window === "undefined" || !window.sessionStorage) return;
  try {
    window.sessionStorage.setItem(cacheKey, String(value));
  } catch {
    // sessionStorage may be unavailable (private mode, quota). The
    // in-memory Map is still authoritative for the current tab; we
    // just lose the refresh-survival guarantee.
  }
}

// Lazily capture the very first wall-clock moment the UI renders the
// thinking bubble for a (user, turn) pair. The capture is idempotent —
// subsequent calls always return the same number. This is the entire
// anchor for the stopwatch; no backend timestamp participates.
function resolveTurnThinkingStart(userKey: string, turnId: string): number {
  const cacheKey = turnThinkingStartCacheKey(userKey, turnId);
  const fromMemory = turnThinkingStartCache.get(cacheKey);
  if (fromMemory != null) return fromMemory;
  const fromPersisted = readPersistedTurnThinkingStart(cacheKey);
  if (fromPersisted != null) {
    turnThinkingStartCache.set(cacheKey, fromPersisted);
    return fromPersisted;
  }
  const now = Date.now();
  turnThinkingStartCache.set(cacheKey, now);
  writePersistedTurnThinkingStart(cacheKey, now);
  return now;
}

// Shared 1-second ticker driven through `useSyncExternalStore`. One
// interval, any number of subscribers; React handles snapshot
// consistency and re-render scheduling. This is the bit that
// guarantees "the counter actually counts up one second at a time"
// even when the parent (or Virtuoso) is remounting the bubble
// frequently — there is no per-component interval to lose.
let turnThinkingTickerNow = Date.now();
const turnThinkingTickerListeners = new Set<() => void>();
let turnThinkingTickerInterval: number | null = null;

function turnThinkingTickerNotifyAll(): void {
  for (const listener of turnThinkingTickerListeners) {
    try {
      listener();
    } catch {
      // A single bad subscriber must not break the ticker for everyone.
    }
  }
}

function startTurnThinkingTicker(): void {
  if (turnThinkingTickerInterval != null) return;
  if (typeof window === "undefined") return;
  turnThinkingTickerInterval = window.setInterval(() => {
    turnThinkingTickerNow = Date.now();
    turnThinkingTickerNotifyAll();
  }, 1000);
}

function stopTurnThinkingTickerIfIdle(): void {
  if (turnThinkingTickerListeners.size > 0) return;
  if (turnThinkingTickerInterval == null) return;
  if (typeof window === "undefined") return;
  window.clearInterval(turnThinkingTickerInterval);
  turnThinkingTickerInterval = null;
}

function subscribeTurnThinkingTicker(listener: () => void): () => void {
  turnThinkingTickerListeners.add(listener);
  startTurnThinkingTicker();
  // Snap to the current wall clock immediately so a remount that
  // lands mid-tick still sees the latest value on its first render.
  turnThinkingTickerNow = Date.now();
  return () => {
    turnThinkingTickerListeners.delete(listener);
    stopTurnThinkingTickerIfIdle();
  };
}

function getTurnThinkingTickerSnapshot(): number {
  return turnThinkingTickerNow;
}

function useTurnThinkingNow(): number {
  return useSyncExternalStore(
    subscribeTurnThinkingTicker,
    getTurnThinkingTickerSnapshot,
    getTurnThinkingTickerSnapshot,
  );
}

// Live-ticking elapsed-time readout for the "thinking" bubble.
// `aria-live="off"` is intentional: the duration changes every second
// and would otherwise spam screen readers with useless announcements.
// The underlying turn button already has an `aria-label="Open turn"`.
function RunTurnThinkingDuration({
  userKey,
  turnId,
}: {
  userKey: string;
  turnId: string;
}) {
  const startMs = resolveTurnThinkingStart(userKey, turnId);
  const now = useTurnThinkingNow();
  const elapsed = formatThinkingElapsed(now - startMs);
  return (
    <span
      className="run-turn-thinking-duration"
      aria-live="off"
      data-design-element="thinking-duration"
      data-turn-id={turnId}
      title={`UI timer started ${new Date(startMs).toLocaleTimeString()}`}
    >
      {elapsed}
    </span>
  );
}

function RunTurnThinkingBubble({
  userKey,
  turnId,
  avatar,
  onOpenTurn,
}: {
  userKey: string;
  turnId: string;
  avatar: AgentAvatar | null;
  onOpenTurn?: (turnId: string) => void;
}) {
  return (
    <div
      className="run-transcript-message run-turn-thinking"
      data-slot="message"
      data-variant="assistant"
      data-role="assistant"
      data-kind="turn-thinking"
    >
      <span className="run-msg-ai-avatar" aria-hidden="true">
        <SessionAvatarIcon avatar={avatar} className="run-msg-ai-icon" />
      </span>
      <button
        type="button"
        className="run-transcript-message-content run-turn-thinking-content"
        title="Open turn"
        aria-label="Open turn"
        onClick={() => onOpenTurn?.(turnId)}
      >
        <span className="run-turn-thinking-dots" aria-hidden="true">
          <span>.</span>
          <span>.</span>
          <span>.</span>
        </span>
        <RunTurnThinkingDuration userKey={userKey} turnId={turnId} />
      </button>
    </div>
  );
}

function RunTurnActivityGroup({
  group,
  open,
  onOpenChange,
  avatar,
  systemAvatar,
  sessionId,
  showThinking,
  autoExpandTools,
  showTimestamps,
  showDuration,
  toolGroupOpenOverrides,
  onToolGroupOpenChange,
  toolExpansionOverrides,
  onToolExpandedChange,
  highlightedEntryId,
  onQuote,
  onFork,
  onOpenBackgroundTask,
  loading,
}: {
  group: Extract<EntryGroup, { kind: "activity" }>;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  avatar: AgentAvatar | null;
  systemAvatar: AgentAvatar | null;
  sessionId: string;
  showThinking: boolean;
  autoExpandTools: boolean;
  showTimestamps: boolean;
  showDuration: boolean;
  toolGroupOpenOverrides: Record<string, boolean>;
  onToolGroupOpenChange: (groupKey: string, open: boolean) => void;
  toolExpansionOverrides: Record<string, boolean>;
  onToolExpandedChange: (entryId: string, expanded: boolean) => void;
  highlightedEntryId: string | null;
  onQuote?: (text: string, style: QuoteStyle) => void;
  onFork?: (entry: TranscriptEntry) => Promise<void>;
  onOpenBackgroundTask?: (entry: TranscriptEntry) => void;
  loading?: boolean;
}) {
  const ownedAssistantEntries = useMemo(
    () => turnActivityOwnedAssistantEntries(group),
    [group],
  );
  const ownedAssistantEntryIds = useMemo(
    () => new Set(ownedAssistantEntries.map((entry) => entry.id)),
    [ownedAssistantEntries],
  );
  const compactedEntryIds = useMemo(
    () => new Set(group.compactedEntryIds),
    [group.compactedEntryIds],
  );
  const childGroups = useMemo(
    () => groupFlatTranscriptEntries(
      group.entries.filter((entry) => !ownedAssistantEntryIds.has(entry.id)),
    ),
    [group.entries, ownedAssistantEntryIds],
  );
  const startedAt = group.entries.find((entry) => entry.startedAt || entry.time);
  const completedAt = [...group.entries]
    .reverse()
    .find((entry) => entry.completedAt || entry.turnTerminalAt || entry.time);
  const shellSummary = group.shell?.activity;
  return (
    <div
      className="run-turn-activity"
      data-state={open ? "open" : "closed"}
      data-active={group.active === true ? "true" : undefined}
      data-inline-response={ownedAssistantEntries.length > 0 ? "true" : undefined}
    >
      <span className="run-turn-activity-avatar" aria-hidden="true">
        <SessionAvatarIcon avatar={avatar} className="run-msg-ai-icon" />
      </span>
      <div className="run-turn-activity-stack">
        <div className="run-turn-activity-content">
          <button
            type="button"
            className="run-turn-activity-header"
            onClick={() => onOpenChange(!open)}
            aria-expanded={open}
          >
            <span
              className="run-turn-activity-icon"
              title="Condensed turn activity"
              aria-label="Condensed turn activity"
            >
              <ActivityIcon size={14} strokeWidth={2} aria-hidden="true" />
            </span>
            <span className="run-turn-activity-label">Turn activity</span>
            <span className="run-turn-activity-summary">
              {group.shell ? turnActivityShellSummary(shellSummary) : turnActivitySummary(group.entries)}
            </span>
            {showTimestamps && (
              <ToolTiming
                startedAt={shellSummary?.startedAt ?? startedAt?.startedAt ?? startedAt?.time}
                completedAt={
                  shellSummary?.completedAt ??
                  completedAt?.completedAt ??
                  completedAt?.turnTerminalAt ??
                  completedAt?.time
                }
                running={group.active === true}
              />
            )}
            <span className="run-turn-activity-chevron">
              {open ? (
                <ChevronUpIcon size={14} className="run-chevron-icon" />
              ) : (
                <ChevronDownIcon size={14} className="run-chevron-icon" />
              )}
            </span>
          </button>
          {open && (
            <div className="run-turn-activity-body">
              {group.shell && !group.loaded ? (
                <div className="run-shell-loading run-turn-activity-loading" role="status" aria-live="polite">
                  <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                  <span>{loading ? "Loading activity..." : "Activity details unavailable."}</span>
                </div>
              ) : childGroups.map((child, childIndex) => {
                if (child.kind === "tools") {
                  const childGroupKey = toolGroupStateKey(child.entries);
                  return (
                    <RunToolGroup
                      key={childGroupKey}
                      entries={child.entries}
                      autoExpand={autoExpandTools}
                      showTimestamps={showTimestamps}
                      open={
                        toolGroupOpenOverrides[childGroupKey] ??
                        toolGroupDefaultOpen(
                          child.entries,
                          autoExpandTools,
                          toolExpansionOverrides,
                        )
                      }
                      onOpenChange={(nextOpen) =>
                        onToolGroupOpenChange(childGroupKey, nextOpen)
                      }
                      toolExpansionOverrides={toolExpansionOverrides}
                      onToolExpandedChange={onToolExpandedChange}
                    />
                  );
                }
                if (child.kind === "reasoning") {
                  return (
                    <RunReasoningBlock
                      key={child.entry.id}
                      entry={child.entry}
                      showThinking={showThinking}
                    />
                  );
                }
                if (child.kind === "meta") {
                  return <RunMetaBlock key={child.entry.id} entry={child.entry} />;
                }
                if (child.kind === "background_task") {
                  return (
                    <RunBackgroundTaskBlock
                      key={child.entry.id}
                      entry={child.entry}
                      showTimestamps={showTimestamps}
                      onOpenTask={onOpenBackgroundTask}
                    />
                  );
                }
                if (child.kind === "message_group") {
                  return (
                    <RunSystemMessageGroupBubble
                      key={entryGroupKey(child)}
                      entries={child.entries}
                      systemAvatar={systemAvatar}
                      highlightedEntryId={highlightedEntryId}
                      showTimestamps={showTimestamps}
                    />
                  );
                }
                if (child.kind === "thinking") return null;
                return (
                  <RunMessageBubble
                    key={child.entry.id}
                    entry={child.entry}
                    avatar={avatar}
                    systemAvatar={systemAvatar}
                    sessionId={sessionId}
                    highlighted={
                      compactedEntryIds.has(child.entry.id) &&
                      highlightedEntryId === child.entry.id
                    }
                    showTimestamps={showTimestamps}
                    showDuration={showDuration}
                    onQuote={onQuote}
                    canonicalMessage={false}
                    isAvatarContinuation={isMessageAvatarContinuation(childGroups, childIndex)}
                  />
                );
              })}
            </div>
          )}
        </div>
        {ownedAssistantEntries.map((entry) => (
          <RunMessageBubble
            key={entry.id}
            entry={entry}
            avatar={avatar}
            systemAvatar={systemAvatar}
            sessionId={sessionId}
            highlighted={highlightedEntryId === entry.id}
            showTimestamps={showTimestamps}
            showDuration={showDuration}
            onQuote={onQuote}
            onFork={onFork}
            ownedByTurnActivity
          />
        ))}
      </div>
    </div>
  );
}

function RunTurnActivityScreen({
  turns,
  selectedTurnId,
  onSelectTurn,
  avatar,
  systemAvatar,
  sessionId,
  showThinking,
  autoExpandTools,
  showTimestamps,
  showDuration,
  userKey,
  loadingActivityTurns,
  onOpenBackgroundTask,
}: {
  turns: TurnViewItem[];
  selectedTurnId: string | null;
  onSelectTurn: (turnId: string) => void;
  avatar: AgentAvatar | null;
  systemAvatar: AgentAvatar | null;
  sessionId: string;
  showThinking: boolean;
  autoExpandTools: boolean;
  showTimestamps: boolean;
  showDuration: boolean;
  // Stable identifier for the currently signed-in user. See the
  // matching prop on RunMessages for the rationale.
  userKey: string;
  loadingActivityTurns: Record<string, boolean | undefined>;
  onOpenBackgroundTask?: (entry: TranscriptEntry) => void;
}) {
  const selected = turns.find((turn) => turn.turnId === selectedTurnId) ?? turns[turns.length - 1] ?? null;
  const detailEntries = useMemo(
    () =>
      (selected?.entries ?? []).filter(
        (entry) => !isTurnActivityEntry(entry) && !isUserMessageEntry(entry),
      ),
    [selected?.entries],
  );
  const detailGroups = useMemo(
    () => groupFlatTranscriptEntries(detailEntries),
    [detailEntries],
  );
  const [toolGroupOpenOverrides, setToolGroupOpenOverrides] = useState<Record<string, boolean>>({});
  const [toolExpansionOverrides, setToolExpansionOverrides] = useState<Record<string, boolean>>({});
  const setToolGroupOpen = useCallback((groupKey: string, open: boolean) => {
    setToolGroupOpenOverrides((prev) => (
      prev[groupKey] === open ? prev : { ...prev, [groupKey]: open }
    ));
  }, []);
  const setToolExpanded = useCallback((entryId: string, expanded: boolean) => {
    setToolExpansionOverrides((prev) => (
      prev[entryId] === expanded ? prev : { ...prev, [entryId]: expanded }
    ));
  }, []);
  const loading = selected ? loadingActivityTurns[selected.turnId] === true : false;
  const renderGroup = (group: FlatEntryGroup, groupIndex: number) => {
    if (group.kind === "tools") {
      const groupKey = toolGroupStateKey(group.entries);
      return (
        <RunToolGroup
          key={groupKey}
          entries={group.entries}
          autoExpand={autoExpandTools}
          showTimestamps={showTimestamps}
          open={
            toolGroupOpenOverrides[groupKey] ??
            toolGroupDefaultOpen(group.entries, autoExpandTools, toolExpansionOverrides)
          }
          onOpenChange={(open) => setToolGroupOpen(groupKey, open)}
          toolExpansionOverrides={toolExpansionOverrides}
          onToolExpandedChange={setToolExpanded}
        />
      );
    }
    if (group.kind === "reasoning") {
      return <RunReasoningBlock key={group.entry.id} entry={group.entry} showThinking={showThinking} />;
    }
    if (group.kind === "meta") {
      return <RunMetaBlock key={group.entry.id} entry={group.entry} />;
    }
    if (group.kind === "background_task") {
      return (
        <RunBackgroundTaskBlock
          key={group.entry.id}
          entry={group.entry}
          showTimestamps={showTimestamps}
          onOpenTask={onOpenBackgroundTask}
        />
      );
    }
    if (group.kind === "message_group") {
      return (
        <RunSystemMessageGroupBubble
          key={entryGroupKey(group)}
          entries={group.entries}
          systemAvatar={systemAvatar}
          highlightedEntryId={null}
          showTimestamps={showTimestamps}
        />
      );
    }
    if (group.kind === "thinking") return null;
    return (
      <RunMessageBubble
        key={group.entry.id}
        entry={group.entry}
        avatar={avatar}
        systemAvatar={systemAvatar}
        sessionId={sessionId}
        highlighted={false}
        showTimestamps={showTimestamps}
        showDuration={showDuration}
        canonicalMessage={false}
        ownedByTurnActivity
        showAssistantAvatar
        isAvatarContinuation={isMessageAvatarContinuation(detailGroups, groupIndex)}
      />
    );
  };

  return (
    <div className="run-turn-view" aria-label="Turn view">
      <div className="run-turn-view-head">
        <div className="run-turn-view-title">
          <ActivityIcon size={16} strokeWidth={2.1} aria-hidden="true" />
          <h2>Turns</h2>
        </div>
        <Select
          value={selected?.turnId ?? ""}
          onValueChange={onSelectTurn}
          disabled={turns.length === 0}
        >
          <SelectTrigger
            className="run-turn-view-select"
            size="sm"
            aria-label="Select turn"
          >
            <SelectValue placeholder="No turns" />
          </SelectTrigger>
          <SelectContent
            className="run-turn-view-select-menu"
            position="popper"
            align="end"
          >
            {turns.map((turn) => (
              <SelectItem
                key={turn.turnId}
                value={turn.turnId}
                className="run-turn-view-select-item"
              >
                {turn.label}{turn.active ? " (running)" : ""}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {selected ? (
        <>
          <div className="run-turn-view-summary">
            <span data-active={selected.active ? "true" : undefined}>
              {selected.active ? "running" : "complete"}
            </span>
            <span>{selected.summary}</span>
            {selected.startedAt && <span>{formatToolFullTime(selected.startedAt)}</span>}
            {selected.completedAt && !selected.active && <span>{formatToolFullTime(selected.completedAt)}</span>}
          </div>
          <div className="run-turn-view-body run-transcript run-transcript-claude">
            {loading && detailGroups.length === 0 ? (
              <div className="run-shell-loading run-turn-view-loading" role="status" aria-live="polite">
                <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                <span>Loading activity...</span>
              </div>
            ) : selected.active && detailGroups.length === 0 ? (
              <div className="run-turn-view-thinking" aria-label="Turn is running">
                <span className="run-turn-thinking-dots" aria-hidden="true">
                  <span>.</span>
                  <span>.</span>
                  <span>.</span>
                </span>
                <RunTurnThinkingDuration userKey={userKey} turnId={selected.turnId} />
              </div>
            ) : detailGroups.length === 0 ? (
              <div className="run-shell-tasks-empty">No turn activity.</div>
            ) : (
              detailGroups.map(renderGroup)
            )}
          </div>
        </>
      ) : (
        <div className="run-shell-tasks-empty">No turns.</div>
      )}
    </div>
  );
}

// RunMessages renders the durable transcript through react-virtuoso so the
// DOM stays bounded regardless of session length — Mattermost / Element /
// Slack / Discord all converge on this architecture (see
// docs/product-inspirations.md). Without virtualization the DOM grows
// 15-50 nodes per event; long sessions reach 100K+ nodes and layout cost
// dominates. With Virtuoso only the visible window plus a small overscan
// buffer mount at any moment.
//
// `customScrollParent` makes Virtuoso reuse the existing <main> as its
// scroll container — no wrapping/sizing layout changes vs. the prior
// inline render. The `followOutput` + `startReached` +
// `atBottomStateChange` props replace the hand-rolled scroll-detect /
// auto-scroll effects deleted from ChatPane in this stage.
export function RunMessages({
  entries,
  avatar,
  systemAvatar = null,
  sessionId,
  sessionMode = "unknown",
  telemetrySurface = "session",
  pendingScrollMessageId,
  onScrollConsumed,
  showThinking,
  autoExpandTools,
  condenseCompletedTurns = true,
  activeTurnId = null,
  showTimestamps,
  showDuration,
  userKey,
  onQuote,
  onFork,
  onOpenTurn,
  onOpenBackgroundTask,
  scrollParent,
  onStartReached,
  onAtBottomChange,
  followLiveTail = true,
  scrollToLatestSignal,
  scrollToLatestBehavior = "smooth",
  scrollToLatestReason = "manual",
  onScrollToLatestConsumed,
  scrollToOldestSignal,
  activityEntriesByTurn = {},
  loadingActivityTurns = {},
  onActivityOpen,
}: {
  entries: TranscriptEntry[];
  avatar: AgentAvatar | null;
  systemAvatar?: AgentAvatar | null;
  sessionId: string;
  sessionMode?: string;
  telemetrySurface?: string;
  // Stable identifier for the currently signed-in user. Used to
  // namespace the thinking-bubble timer's per-(user, turn) anchor so
  // a second account signed in on the same browser tab can't inherit
  // anchors written by the first account.
  userKey: string;
  // Set when the SPA cold-started with ?message=<entry.id>. RunMessages
  // searches the loaded groups for that id and, when found, scrolls
  // Virtuoso to it and lights up a highlight pulse on the bubble. If
  // the entry hasn't loaded yet (it sits before the current backfill
  // window) we leave the id armed; a later `entries` change retries.
  pendingScrollMessageId?: string | null;
  // Fired after a successful scroll so the parent can clear the URL
  // param and stop re-applying the highlight on subsequent renders.
  onScrollConsumed?: () => void;
  showThinking: boolean;
  autoExpandTools: boolean;
  condenseCompletedTurns?: boolean;
  activeTurnId?: string | null;
  showTimestamps: boolean;
  showDuration: boolean;
  onQuote?: (text: string, style: QuoteStyle) => void;
  onFork?: (entry: TranscriptEntry) => Promise<void>;
  onOpenTurn?: (turnId: string) => void;
  onOpenBackgroundTask?: (entry: TranscriptEntry) => void;
  scrollParent: HTMLElement | null;
  onStartReached?: () => void;
  onAtBottomChange?: (atBottom: boolean) => void;
  // Durable navigation mode projected to a single layout decision: should
  // the virtualized list stick to the live tail as new rows append? This is
  // sourced from the NavigationMode state machine (live-tail vs
  // historical-anchor), NOT from a DOM at-bottom heuristic. When false the
  // list never auto-scrolls on output — a reader anchored in history is not
  // yanked to the tail by load/ready/resync row growth.
  followLiveTail?: boolean;
  scrollToLatestSignal?: number;
  scrollToLatestBehavior?: ScrollToLatestBehavior;
  scrollToLatestReason?: ScrollToLatestReason;
  onScrollToLatestConsumed?: () => void;
  scrollToOldestSignal?: number;
  activityEntriesByTurn?: Record<string, TranscriptEntry[] | undefined>;
  loadingActivityTurns?: Record<string, boolean | undefined>;
  onActivityOpen?: (turnId: string) => void;
}) {
  const groups = useMemo(
    () => groupTranscriptEntries(entries, condenseCompletedTurns, activeTurnId, activityEntriesByTurn),
    [activeTurnId, activityEntriesByTurn, condenseCompletedTurns, entries],
  );
  const virtuosoRef = useRef<VirtuosoHandle | null>(null);
  const previousGroupKeysRef = useRef<string[]>([]);
  const thinkingInvariantRef = useRef<string>("");
  // Keep disclosure choices above virtualized row components so streaming
  // events and offscreen remounts do not reset expanded tool details.
  const [activityOpenOverrides, setActivityOpenOverrides] = useState<Record<string, boolean>>({});
  const [toolGroupOpenOverrides, setToolGroupOpenOverrides] = useState<Record<string, boolean>>({});
  const [toolExpansionOverrides, setToolExpansionOverrides] = useState<Record<string, boolean>>({});
  // Highlighted entry is the bubble that should pulse after a deep-link
  // scroll. We clear it on a timer so re-renders during streaming don't
  // re-trigger the animation on entries the user is just reading.
  const [highlightedEntryId, setHighlightedEntryId] = useState<string | null>(null);
  // Track which message id we've already handled so we don't re-scroll
  // every time entries change during streaming.
  const consumedScrollIdRef = useRef<string | null>(null);
  const consumedScrollToLatestSignalRef = useRef(0);
  const consumedScrollToOldestSignalRef = useRef(0);
  const setToolGroupOpen = useCallback((groupKey: string, open: boolean) => {
    setToolGroupOpenOverrides((prev) => (
      prev[groupKey] === open ? prev : { ...prev, [groupKey]: open }
    ));
  }, []);
  const setToolExpanded = useCallback((entryId: string, expanded: boolean) => {
    setToolExpansionOverrides((prev) => (
      prev[entryId] === expanded ? prev : { ...prev, [entryId]: expanded }
    ));
  }, []);
  const setActivityOpen = useCallback((groupKey: string, open: boolean) => {
    setActivityOpenOverrides((prev) => (
      prev[groupKey] === open ? prev : { ...prev, [groupKey]: open }
    ));
  }, []);
  useEffect(() => {
    const snapshot = chatScrollGroupSnapshot(groups, entries.length, entries);
    const durableActiveActivityGroups = chatScrollSnapshotNumber(
      snapshot,
      "durableActiveActivityGroups",
    );
    const durableActiveTurnActivityShells = chatScrollSnapshotNumber(
      snapshot,
      "durableActiveTurnActivityShells",
    );
    const durableActivePlaceholders = Math.max(
      durableActiveActivityGroups,
      durableActiveTurnActivityShells,
    );
    const thinkingGroups = chatScrollSnapshotNumber(snapshot, "thinkingGroups");
    if (durableActivePlaceholders <= thinkingGroups) {
      thinkingInvariantRef.current = "";
      return;
    }
    const key = [
      sessionId,
      chatScrollSnapshotString(snapshot, "lastGroupKey"),
      durableActiveActivityGroups,
      durableActiveTurnActivityShells,
      thinkingGroups,
    ].join("\u001f");
    if (thinkingInvariantRef.current === key) return;
    thinkingInvariantRef.current = key;
    logChatScrollEvent("thinking-row-missing", {
      surface: telemetrySurface,
      sessionId,
      sessionMode,
      ...snapshot,
      ...chatScrollElementSnapshot(scrollParent),
    });
  }, [entries, groups, scrollParent, sessionId, sessionMode, telemetrySurface]);
  useEffect(() => {
    const target = pendingScrollMessageId;
    if (!target) return;
    if (consumedScrollIdRef.current === target) return;
    let activityGroupKey: string | null = null;
    let targetToolGroupKey: string | null = null;
    const groupIndex = groups.findIndex((g) => {
      if (g.kind === "tools") {
        const contains = g.entries.some((e) => e.id === target);
        if (contains) targetToolGroupKey = toolGroupStateKey(g.entries);
        return contains;
      }
      if (g.kind === "activity") {
        const compactedContains = g.compactedEntryIds.includes(target);
        const visibleAssistantContains = turnActivityOwnedAssistantEntries(g).some(
          (entry) => entry.id === target,
        );
        if (compactedContains) {
          activityGroupKey = entryGroupKey(g);
          if (g.shell) onActivityOpen?.(g.turnId);
          const targetEntry = g.entries.find((entry) => entry.id === target);
          if (targetEntry?.kind === "tool") {
            const childToolGroup = groupFlatTranscriptEntries(g.entries).find(
              (child) =>
                child.kind === "tools" &&
                child.entries.some((entry) => entry.id === target),
            );
            if (childToolGroup?.kind === "tools") {
              targetToolGroupKey = toolGroupStateKey(childToolGroup.entries);
            }
          }
        }
        return compactedContains || visibleAssistantContains;
      }
      if (g.kind === "thinking") return false;
      if (g.kind === "message_group") return g.entries.some((entry) => entry.id === target);
      return g.entry.id === target;
    });
    if (groupIndex < 0) return; // entry not yet loaded; try again on next entries change
    consumedScrollIdRef.current = target;
    setHighlightedEntryId(target);
    if (activityGroupKey) {
      setActivityOpen(activityGroupKey, true);
    }
    if (targetToolGroupKey) {
      setToolGroupOpen(targetToolGroupKey, true);
      setToolExpanded(target, true);
    }
    const handle = virtuosoRef.current;
    if (handle) {
      // align: "center" puts the bubble in the middle of the viewport
      // so the user can see surrounding context, not just the bubble
      // wedged against the top edge.
      handle.scrollToIndex({ index: groupIndex, align: "center", behavior: "smooth" });
    }
    onScrollConsumed?.();
    const timer = window.setTimeout(() => {
      setHighlightedEntryId((current) => (current === target ? null : current));
    }, 2400);
    return () => window.clearTimeout(timer);
  }, [
    pendingScrollMessageId,
    groups,
    onScrollConsumed,
    onActivityOpen,
    setActivityOpen,
    setToolExpanded,
    setToolGroupOpen,
  ]);
  useLayoutEffect(() => {
    const previousKeys = previousGroupKeysRef.current;
    const currentKeys = groups.map(entryGroupKey);
    const previousFirst = previousKeys[0];
    previousGroupKeysRef.current = currentKeys;
    if (!previousFirst) return;
    const nextIndex = currentKeys.indexOf(previousFirst);
    if (nextIndex <= 0) return;
    logChatScrollGroups("prepend-preserve-scroll", groups, entries.length, {
      surface: telemetrySurface,
      sessionId,
      sessionMode,
      previousFirst,
      nextIndex,
      ...chatScrollElementSnapshot(scrollParent),
    });
    virtuosoRef.current?.scrollToIndex({
      index: nextIndex,
      align: "start",
      behavior: "auto",
    });
  }, [entries.length, groups, scrollParent, sessionId, sessionMode, telemetrySurface]);
  useEffect(() => {
    logChatScrollGroups("virtuoso-window", groups, entries.length, {
      surface: telemetrySurface,
      sessionId,
      sessionMode,
      initialTopMostItemIndex: Math.max(groups.length - 1, 0),
      ...chatScrollElementSnapshot(scrollParent),
    });
  }, [entries.length, groups, scrollParent, sessionId, sessionMode, telemetrySurface]);
  useLayoutEffect(() => {
    if (!scrollToLatestSignal || groups.length === 0) return;
    if (consumedScrollToLatestSignalRef.current === scrollToLatestSignal) return;
    consumedScrollToLatestSignalRef.current = scrollToLatestSignal;
    logChatScrollGroups("scroll-to-latest", groups, entries.length, {
      surface: telemetrySurface,
      sessionId,
      sessionMode,
      signal: scrollToLatestSignal,
      behavior: scrollToLatestBehavior,
      reason: scrollToLatestReason,
      ...chatScrollElementSnapshot(scrollParent),
    });
    virtuosoRef.current?.scrollToIndex({
      index: groups.length - 1,
      align: "end",
      behavior: scrollToLatestBehavior,
    });
    onScrollToLatestConsumed?.();
  }, [
    entries.length,
    groups,
    onScrollToLatestConsumed,
    scrollParent,
    scrollToLatestBehavior,
    scrollToLatestReason,
    scrollToLatestSignal,
    sessionId,
    sessionMode,
    telemetrySurface,
  ]);
  useEffect(() => {
    if (!scrollToOldestSignal || groups.length === 0) return;
    if (consumedScrollToOldestSignalRef.current === scrollToOldestSignal) return;
    consumedScrollToOldestSignalRef.current = scrollToOldestSignal;
    logChatScrollGroups("scroll-to-oldest", groups, entries.length, {
      surface: telemetrySurface,
      sessionId,
      sessionMode,
      signal: scrollToOldestSignal,
      ...chatScrollElementSnapshot(scrollParent),
    });
    virtuosoRef.current?.scrollToIndex({
      index: 0,
      align: "start",
      behavior: "smooth",
    });
  }, [entries.length, groups, scrollParent, scrollToOldestSignal, sessionId, sessionMode, telemetrySurface]);
  // computeItemKey stabilizes Virtuoso's per-item identity across renders.
  // Tool groups have no single id, so the first child id identifies the
  // group while later tool entries append during a streaming turn.
  const computeKey = useCallback(
    (_index: number, g: EntryGroup) => entryGroupKey(g),
    [],
  );
  const renderItem = useCallback(
    (index: number, g: EntryGroup) => {
      if (g.kind === "tools") {
        const groupKey = toolGroupStateKey(g.entries);
        return (
          <RunToolGroup
            entries={g.entries}
            autoExpand={autoExpandTools}
            showTimestamps={showTimestamps}
            open={
              toolGroupOpenOverrides[groupKey] ??
              toolGroupDefaultOpen(g.entries, autoExpandTools, toolExpansionOverrides)
            }
            onOpenChange={(open) => setToolGroupOpen(groupKey, open)}
            toolExpansionOverrides={toolExpansionOverrides}
            onToolExpandedChange={setToolExpanded}
          />
        );
      }
      if (g.kind === "reasoning") {
        return <RunReasoningBlock entry={g.entry} showThinking={showThinking} />;
      }
      if (g.kind === "meta") {
        if (g.entry.metaKind === "needs_input_announcement") {
          return (
            <RunNeedsInputAnnouncement entry={g.entry} onOpenTurn={onOpenTurn} />
          );
        }
        return <RunMetaBlock entry={g.entry} />;
      }
      if (g.kind === "background_task") {
        return (
          <RunBackgroundTaskBlock
            entry={g.entry}
            showTimestamps={showTimestamps}
            onOpenTask={onOpenBackgroundTask}
          />
        );
      }
      if (g.kind === "message_group") {
        return (
          <RunSystemMessageGroupBubble
            entries={g.entries}
            systemAvatar={systemAvatar}
            highlightedEntryId={highlightedEntryId}
            showTimestamps={showTimestamps}
          />
        );
      }
      if (g.kind === "thinking") {
        return (
          <RunTurnThinkingBubble
            userKey={userKey}
            turnId={g.turnId}
            avatar={avatar}
            onOpenTurn={onOpenTurn}
          />
        );
      }
      if (g.kind === "activity") {
        const groupKey = entryGroupKey(g);
        return (
          <RunTurnActivityGroup
            group={g}
            open={activityOpenOverrides[groupKey] ?? false}
            onOpenChange={(open) => {
              if (open && g.shell) onActivityOpen?.(g.turnId);
              setActivityOpen(groupKey, open);
            }}
            avatar={avatar}
            systemAvatar={systemAvatar}
            sessionId={sessionId}
            showThinking={showThinking}
            autoExpandTools={autoExpandTools}
            showTimestamps={showTimestamps}
            showDuration={showDuration}
            toolGroupOpenOverrides={toolGroupOpenOverrides}
            onToolGroupOpenChange={setToolGroupOpen}
            toolExpansionOverrides={toolExpansionOverrides}
            onToolExpandedChange={setToolExpanded}
            highlightedEntryId={highlightedEntryId}
            onQuote={onQuote}
            onFork={onFork}
            onOpenBackgroundTask={onOpenBackgroundTask}
            loading={loadingActivityTurns[g.turnId] === true}
          />
        );
      }
      return (
        <RunMessageBubble
          entry={g.entry}
          avatar={avatar}
          systemAvatar={systemAvatar}
          sessionId={sessionId}
          highlighted={highlightedEntryId === g.entry.id}
          showTimestamps={showTimestamps}
          showDuration={showDuration}
          onQuote={onQuote}
          onFork={onFork}
          onOpenTurn={onOpenTurn}
          isAvatarContinuation={isMessageAvatarContinuation(groups, index)}
        />
      );
    },
    [
      activityOpenOverrides,
      autoExpandTools,
      avatar,
      groups,
      systemAvatar,
      highlightedEntryId,
      loadingActivityTurns,
      onFork,
      onOpenTurn,
      onActivityOpen,
      onOpenBackgroundTask,
      onQuote,
      setActivityOpen,
      sessionId,
      setToolExpanded,
      setToolGroupOpen,
      showDuration,
      showThinking,
      showTimestamps,
      toolExpansionOverrides,
      toolGroupOpenOverrides,
    ],
  );
  const handleStartReached = useCallback(() => {
    logChatScrollGroups("start-reached", groups, entries.length, {
      surface: telemetrySurface,
      sessionId,
      sessionMode,
      ...chatScrollElementSnapshot(scrollParent),
    });
    onStartReached?.();
  }, [entries.length, groups, onStartReached, scrollParent, sessionId, sessionMode, telemetrySurface]);
  const handleAtBottomChange = useCallback(
    (atBottom: boolean) => {
      logChatScrollGroups("at-bottom-change", groups, entries.length, {
        surface: telemetrySurface,
        sessionId,
        sessionMode,
        atBottom,
        ...chatScrollElementSnapshot(scrollParent),
      });
      onAtBottomChange?.(atBottom);
    },
    [entries.length, groups, onAtBottomChange, scrollParent, sessionId, sessionMode, telemetrySurface],
  );
  // followOutput is gated on the durable navigation mode, not on a
  // hardcoded "smooth" or a DOM at-bottom heuristic:
  //
  //   - live-tail  → "auto" (instant stick to the tail as rows append).
  //     Instant, not smooth: smooth animated every length change during
  //     the open/load/resync row storm, which read to users as the
  //     transcript "zipping around" before it settled. An instant snap to
  //     the measured bottom reads as "it's just at the bottom" and
  //     satisfies the contract's "load/ready/resync must not introduce
  //     scroll jumps" check (a settle to the correct bottom is not a
  //     jump; an animated chase is).
  //   - historical-anchor → false (never auto-scroll). A reader anchored
  //     in history is never yanked to the tail by row growth; the buffered
  //     pill in ChatPane surfaces new activity instead.
  //
  // Explicit user gestures (End key, scroll-to-latest button, jump-latest)
  // still animate via the scrollToLatest signal with behavior="smooth" —
  // that motion is intended and requested, unlike the implicit follow.
  // startReached fires when the user scrolls within ~`overscan` of the
  // top — Virtuoso debounces this so rapid scroll doesn't spam fetches.
  return (
    <Virtuoso
      ref={virtuosoRef}
      className="run-transcript run-transcript-claude"
      data-slot="root"
      data={groups}
      customScrollParent={scrollParent ?? undefined}
      computeItemKey={computeKey}
      itemContent={renderItem}
      followOutput={followLiveTail ? "auto" : false}
      startReached={handleStartReached}
      atBottomStateChange={handleAtBottomChange}
      // Render two extra screens worth above and below the viewport so
      // tool-group expansion and markdown reflow don't expose unrendered
      // gaps mid-scroll.
      overscan={{ main: 800, reverse: 800 }}
      // Land deterministically at the bottom on first data application:
      // bottom-align the last group rather than making it the topmost item.
      // Virtuoso resolves this after it measures row heights, so a caught-up
      // session lands at the true live tail in one step instead of landing
      // short and then correcting. The Math.max safeguards empty arrays.
      initialTopMostItemIndex={{ index: Math.max(groups.length - 1, 0), align: "end" }}
    />
  );
}

function RunSettingsPanel({
  runPrefs,
  setRunPref,
  soundControlId,
  turnCompleteSoundVolumePct,
  setTurnCompleteSoundVolume,
  playTurnCompleteSound,
  paneFontScale,
  paneFontScalePct,
  setPaneFontScale,
  adminControls,
}: {
  runPrefs: RunPrefs;
  setRunPref: SetRunPref;
  soundControlId: string;
  turnCompleteSoundVolumePct: number;
  setTurnCompleteSoundVolume: (value: number) => void;
  playTurnCompleteSound: () => void;
  paneFontScale: number;
  paneFontScalePct: number;
  setPaneFontScale: (value: number) => void;
  adminControls?: {
    visible: boolean;
    canViewProdSessions: boolean;
    viewingProdSessions: boolean;
    currentScope: string;
    prodScope: string;
    onAvatarCatalogChanged: () => Promise<void>;
    onViewingProdSessionsChange: (value: boolean) => void;
  };
}) {
  const [settingsTab, setSettingsTab] = useState<"preferences" | "admin">("preferences");
  const [adminView, setAdminView] = useState<"controls" | "avatars">("controls");
  const showAdminTab = adminControls?.visible === true;
  useEffect(() => {
    if (!showAdminTab && settingsTab === "admin") {
      setSettingsTab("preferences");
    }
  }, [settingsTab, showAdminTab]);
  useEffect(() => {
    if (!showAdminTab || settingsTab !== "admin") {
      setAdminView("controls");
    }
  }, [settingsTab, showAdminTab]);
  const settingsScreenClassName =
    settingsTab === "admin" && showAdminTab && adminView === "avatars"
      ? "run-settings-screen run-settings-screen-wide"
      : "run-settings-screen";

  return (
    <div className={settingsScreenClassName}>
      {showAdminTab && (
        <div className="run-settings-tabs" role="tablist" aria-label="Settings sections">
          <button
            type="button"
            className={`run-settings-tab${settingsTab === "preferences" ? " is-active" : ""}`}
            role="tab"
            aria-selected={settingsTab === "preferences"}
            onClick={() => setSettingsTab("preferences")}
          >
            Preferences
          </button>
          <button
            type="button"
            className={`run-settings-tab${settingsTab === "admin" ? " is-active" : ""}`}
            role="tab"
            aria-selected={settingsTab === "admin"}
            onClick={() => setSettingsTab("admin")}
          >
            Admin
          </button>
        </div>
      )}
      {settingsTab === "admin" && showAdminTab ? (
        adminView === "avatars" ? (
          <>
            <section className="run-settings-section">
              <div className="run-settings-admin-heading">
                <button
                  type="button"
                  className="run-settings-back-btn"
                  onClick={() => setAdminView("controls")}
                >
                  <ArrowLeftIcon aria-hidden="true" />
                  <span>Admin</span>
                </button>
                <h2 className="run-settings-title">Avatar editor</h2>
              </div>
            </section>
            <AdminAvatarManager onCatalogChanged={adminControls.onAvatarCatalogChanged} />
          </>
        ) : (
          <section className="run-settings-section">
            <h2 className="run-settings-title">Admin Controls</h2>
            <button
              type="button"
              className="run-settings-link"
              onClick={() => setAdminView("avatars")}
            >
              <span className="run-settings-link-label">
                <ImageIcon className="run-settings-link-icon" aria-hidden="true" />
                <span>Avatar editor</span>
              </span>
              <span className="run-settings-scope-value">
                Manage
              </span>
            </button>
            <a
              className="run-settings-link"
              href="/_styleguide"
              target="_blank"
              rel="noreferrer"
            >
              <span className="run-settings-link-label">
                <ExternalLinkIcon className="run-settings-link-icon" aria-hidden="true" />
                <span>Design portfolio</span>
              </span>
              <span className="run-settings-scope-value">
                Open
              </span>
            </a>
            {adminControls.canViewProdSessions && (
              <button
                type="button"
                className="run-settings-toggle"
                onClick={() =>
                  adminControls.onViewingProdSessionsChange(
                    !adminControls.viewingProdSessions,
                  )
                }
                aria-pressed={adminControls.viewingProdSessions}
              >
                <span>Prod sessions</span>
                <span className="run-settings-scope-value">
                  {adminControls.viewingProdSessions
                    ? adminControls.prodScope
                    : adminControls.currentScope}
                </span>
                {adminControls.viewingProdSessions && (
                  <CheckIcon className="run-settings-check" aria-hidden="true" />
                )}
              </button>
            )}
            <div className="run-settings-diagnostics">
              <div className="run-settings-diagnostics-head">
                <span className="run-settings-link-label">
                  <ClipboardListIcon className="run-settings-link-icon" aria-hidden="true" />
                  <span>Session-list diagnostics</span>
                </span>
              </div>
              <SessionListDebugCaptureControls source="SettingsAdmin" />
            </div>
          </section>
        )
      ) : (
        <>
        <section className="run-settings-section">
        <h2 className="run-settings-title">Composer</h2>
        <button
          type="button"
          className="run-settings-toggle"
          onClick={() => setRunPref("sendByCtrlEnter", !runPrefs.sendByCtrlEnter)}
          aria-pressed={runPrefs.sendByCtrlEnter}
        >
          <span>Send with Cmd/Ctrl+Enter</span>
          {runPrefs.sendByCtrlEnter && (
            <CheckIcon className="run-settings-check" aria-hidden="true" />
          )}
        </button>
      </section>
      <section className="run-settings-section">
        <h2 className="run-settings-title">Sound</h2>
        <button
          type="button"
          className="run-settings-toggle"
          onClick={() => setRunPref("turnCompleteSound", !runPrefs.turnCompleteSound)}
          aria-pressed={runPrefs.turnCompleteSound}
        >
          <span>Turn complete sound</span>
          {runPrefs.turnCompleteSound && (
            <CheckIcon className="run-settings-check" aria-hidden="true" />
          )}
        </button>
        <div className="run-settings-panel-row run-settings-sound-row">
          <label className="run-settings-label" htmlFor={soundControlId}>
            Volume
          </label>
          <div className="run-settings-sound-controls">
            <input
              id={soundControlId}
              className="run-settings-volume-slider"
              type="range"
              min={TURN_COMPLETE_SOUND_VOLUME_MIN}
              max={TURN_COMPLETE_SOUND_VOLUME_MAX}
              step={TURN_COMPLETE_SOUND_VOLUME_STEP}
              value={runPrefs.turnCompleteSoundVolume}
              disabled={!runPrefs.turnCompleteSound}
              onChange={(event) =>
                setTurnCompleteSoundVolume(Number(event.currentTarget.value))
              }
              aria-label="Turn complete sound volume"
            />
            <span className="run-settings-volume-value" aria-live="polite">
              {turnCompleteSoundVolumePct}%
            </span>
            <button
              type="button"
              className="run-settings-test-btn"
              disabled={!runPrefs.turnCompleteSound}
              onClick={playTurnCompleteSound}
            >
              <PlayIcon aria-hidden="true" />
              <span>Test</span>
            </button>
          </div>
        </div>
        <button
          type="button"
          className="run-settings-toggle"
          onClick={() =>
            setRunPref(
              "turnCompleteSoundOnVisible",
              !runPrefs.turnCompleteSoundOnVisible,
            )
          }
          aria-pressed={runPrefs.turnCompleteSoundOnVisible}
          disabled={!runPrefs.turnCompleteSound}
        >
          <span>Ping on open chats</span>
          {runPrefs.turnCompleteSoundOnVisible && (
            <CheckIcon className="run-settings-check" aria-hidden="true" />
          )}
        </button>
      </section>
      <section className="run-settings-section">
        <h2 className="run-settings-title">Transcript</h2>
        <div className="run-settings-panel-row">
          <span className="run-settings-label">Text zoom</span>
          <span className="run-settings-zoom-controls">
            <button
              type="button"
              className="run-settings-zoom-btn"
              onClick={() => setPaneFontScale(paneFontScale - CHAT_FONT_SCALE_STEP)}
              disabled={paneFontScale <= CHAT_FONT_SCALE_MIN}
              aria-label="Decrease pane text size"
              title="Decrease text size"
            >
              <MinusIcon aria-hidden="true" />
            </button>
            <span className="run-settings-zoom-value" aria-live="polite">
              {paneFontScalePct}%
            </span>
            <button
              type="button"
              className="run-settings-zoom-btn"
              onClick={() => setPaneFontScale(paneFontScale + CHAT_FONT_SCALE_STEP)}
              disabled={paneFontScale >= CHAT_FONT_SCALE_MAX}
              aria-label="Increase pane text size"
              title="Increase text size"
            >
              <PlusIcon aria-hidden="true" />
            </button>
            <button
              type="button"
              className="run-settings-zoom-btn"
              onClick={() => setPaneFontScale(DEFAULT_RUN_PREFS.chatFontScale)}
              disabled={paneFontScale === DEFAULT_RUN_PREFS.chatFontScale}
              aria-label="Reset pane text size"
              title="Reset text size"
            >
              <RotateCcwIcon aria-hidden="true" />
            </button>
          </span>
        </div>
        {([
          ["showThinking", "Show reasoning"],
          ["condenseCompletedTurns", "Condense finished turns"],
          ["autoExpandTools", "Auto-expand tools"],
          ["showTimestamps", "Show timestamps"],
          ["showDuration", "Show duration"],
        ] as const).map(([key, label]) => (
          <button
            key={key}
            type="button"
            className="run-settings-toggle"
            onClick={() => setRunPref(key, !runPrefs[key])}
            aria-pressed={runPrefs[key]}
          >
            <span>{label}</span>
            {runPrefs[key] && (
              <CheckIcon className="run-settings-check" aria-hidden="true" />
            )}
          </button>
        ))}
      </section>
      </>
      )}
    </div>
  );
}

function RunHelpScreen() {
  return (
    <div className="run-help-screen">
      <section className="run-help-section">
        <h2 className="run-help-title">Session Help</h2>
        <div className="run-help-list">
          <div className="run-help-row">
            <span className="run-help-key">/</span>
            <span>Open commands from the composer.</span>
          </div>
          <div className="run-help-row">
            <span className="run-help-key">@</span>
            <span>Mention files from the workspace.</span>
          </div>
          <div className="run-help-row">
            <span className="run-help-key">MCP</span>
            <span>Inspect available MCP servers from the lower toolbar.</span>
          </div>
        </div>
      </section>
      <section className="run-help-section" aria-labelledby="run-help-sidebar-status-title">
        <h2 className="run-help-title" id="run-help-sidebar-status-title">Sidebar Status</h2>
        <div className="run-help-status-list">
          {SESSION_ACTIVITY_STATUS_LEGEND.map((item) => (
            <div className="run-help-status-row" key={item.key}>
              <div className="run-help-status-sample" aria-hidden="true">
                {item.dotStatus ? (
                  <span className={`status-dot status-${item.dotStatus}`} />
                ) : (
                  <span className="run-help-status-spacer" />
                )}
              </div>
              <div className="run-help-status-copy">
                <span className="run-help-status-label">{item.label}</span>
                <span className="run-help-status-detail">{item.detail}</span>
              </div>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

function ChatPane({
  session,
  visible,
  onSessionPatch,
  onConnectionLabelChange,
  onForkMessage,
  pendingScrollMessageId,
  onScrollConsumed,
  runPrefs,
  setRunPref,
  user,
  autoFocusComposer,
  onAutoFocusComposerConsumed,
  primeTurnCompleteSound,
  playTurnCompleteSound,
  adminControls,
  readOnly = false,
  sessionScope,
  avatarCatalogVersion,
}: {
  session: Session;
  visible: boolean;
  onSessionPatch: (id: string, patch: Partial<Session>) => void;
  onConnectionLabelChange: (id: string, label: string | null) => void;
  onForkMessage: (request: ForkSessionRequest) => Promise<void>;
  // Deep-link target the parent extracted from ?message=<id>. Only set
  // for the ChatPane whose session matches ?session=<id>; other panes
  // receive null and skip the scroll logic.
  pendingScrollMessageId?: string | null;
  onScrollConsumed?: () => void;
  runPrefs: RunPrefs;
  setRunPref: SetRunPref;
  user: SessionUser;
  autoFocusComposer: boolean;
  onAutoFocusComposerConsumed: () => void;
  // App-owned audio: the SSE consumer in App.tsx rings on the
  // always-on /api/sessions/events stream's activity_changed events.
  // ChatPane gets these props for two narrower uses: primeTurnCompleteSound
  // on the Send-button gesture (audio-unlock backup beyond the sidebar
  // click primer in activate()), and playTurnCompleteSound for the
  // Test button in the settings panel. ChatPane no longer fires the
  // sound off chat events — see App.tsx commentary at the audio refs.
  primeTurnCompleteSound: () => void;
  playTurnCompleteSound: () => void;
  adminControls?: {
    visible: boolean;
    canViewProdSessions: boolean;
    viewingProdSessions: boolean;
    currentScope: string;
    prodScope: string;
    onAvatarCatalogChanged: () => Promise<void>;
    onViewingProdSessionsChange: (value: boolean) => void;
  };
  readOnly?: boolean;
  sessionScope: string;
  avatarCatalogVersion: number;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[]>([]);
  const [activityEntriesByTurn, setActivityEntriesByTurn] =
    useState<Record<string, TranscriptEntry[] | undefined>>({});
  const [loadingActivityTurns, setLoadingActivityTurns] =
    useState<Record<string, boolean | undefined>>({});
  const [renderedActiveTurnId, setRenderedActiveTurnId] = useState<string | null>(null);
  const sdkServerEntriesRef = useRef<TranscriptEntry[]>([]);
  const sdkServerProjectedEntriesRef = useRef<TranscriptEntry[]>([]);
  const sdkRealtimeEntriesRef = useRef<TranscriptEntry[]>([]);
  const sdkAssistantDurationsRef = useRef<Map<string, number>>(new Map());
  const [running, setRunning] = useState(false);
  const [runStatus, setRunStatus] = useState<LocalRunStatus>("idle");
  const activeToolUseIdRef = useRef<string | null>(null);
  const scheduledWakeupRef = useRef(false);
  const initialSessionRoute = useMemo(() => readSessionRouteFromPath(), []);
  const initialRunRoute =
    initialSessionRoute?.sessionId === session.id ? initialSessionRoute : null;
  const [activeTab, setActiveTab] = useState<RunTab>(initialRunRoute?.tab ?? "chat");
  const [selectedTurnId, setSelectedTurnId] = useState<string | null>(
    initialRunRoute?.tab === "turns" ? initialRunRoute.turnId : null,
  );
  const [pendingRouteTurnId, setPendingRouteTurnId] = useState<string | null>(
    initialRunRoute?.tab === "turns" ? initialRunRoute.turnId : null,
  );
  const [backgroundView, setBackgroundView] = useState<BackgroundView>("shells");
  const [selectedBackgroundId, setSelectedBackgroundId] = useState<string | null>(null);
  const [testState, setTestState] = useState<TestState | null>(session.test_state ?? null);
  const [rolloutState, setRolloutState] = useState<RolloutState | null>(session.rollout_state ?? null);
  const [composerMode, setComposerMode] = useState<RunComposerMode>("default");
  const isClaude = isClaudeRunMode(session.mode);
  const isCodex = isCodexRunMode(session.mode);
  const ready = session.mode === "hermes_gui"
    ? session.status === "Active"
    : sessionContainerAvailable(session);
  const scopedSessionPathForPane = useCallback(
    (path: string) => appendQueryParam(path, "session_scope", sessionScope),
    [sessionScope],
  );
  const supportsFileAttachments = !readOnly && sessionModeSupportsWorkspaceFiles(session.mode);
  const filesAvailable = !readOnly && sessionFilesAvailable(session);
  const filesTabTitle = sessionFilesTabTitle(session);
  // Seed model + effort from RunPrefs (browser-persisted). State is local
  // because the runners seal model + effort from the first submit_turn —
  // the splash screen is where the user adjusts the defaults before a
  // session is created.
  // Existing sessions prefer the durable session-owned run config; browser
  // prefs are only the fallback for older rows that do not have it.
  const configuredModelId = (session.model ?? "").trim();
  const configuredEffortId = (session.effort ?? "").trim();
  const hasConfiguredSessionRunConfig = Boolean(configuredModelId || configuredEffortId);
  const modelOptions = modelOptionsForMode(session.mode);
  const effortOptions = effortOptionsForMode(session.mode);
  const preferredModelId = isClaude
    ? runPrefs.claudeModelId
    : isCodex
      ? runPrefs.codexModelId
      : "";
  const preferredEffortId = isClaude
    ? runPrefs.claudeEffort
    : isCodex
      ? runPrefs.codexEffort
      : "";
  const fallbackModelId = isClaude
    ? DEFAULT_CLAUDE_MODEL_ID
    : isCodex
      ? DEFAULT_CODEX_MODEL_ID
      : "";
  const fallbackEffortId = isClaude
    ? DEFAULT_CLAUDE_EFFORT_ID
    : isCodex
      ? DEFAULT_CODEX_EFFORT_ID
      : "";
  const initialModelId = hasConfiguredSessionRunConfig
    ? (configuredModelId || (isCodex ? CODEX_ACCOUNT_DEFAULT_MODEL_ID : fallbackModelId))
    : (modelOptions.some((opt) => opt.id === preferredModelId) ? preferredModelId : fallbackModelId);
  const initialEffortId = hasConfiguredSessionRunConfig
    ? (configuredEffortId || fallbackEffortId)
    : (effortOptions.some((opt) => opt.id === preferredEffortId) ? preferredEffortId : fallbackEffortId);
  const [selectedModelId] = useState<string>(initialModelId);
  const [selectedEffortId] = useState<string>(initialEffortId);
  // Context tokens used in the most recent assistant turn.
  const [tokensUsed, setTokensUsed] = useState(0);
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([]);
  // Slash-command palette state. `slashOpen` gates rendering; `slashQuery`
  // and `slashIndex` drive filtering and keyboard selection.
  const [slashOpen, setSlashOpen] = useState(false);
  const [slashQuery, setSlashQuery] = useState("");
  const [slashIndex, setSlashIndex] = useState(0);

  const [slashCommands, setSlashCommands] = useState<SlashCommand[]>(SLASH_COMMANDS);
  const [mcpOpen, setMcpOpen] = useState(false);
  // @filename mention palette state. paths is lazily loaded from
  // /api/sessions/{id}/files/walk on first `@` keystroke.
  const [mentionOpen, setMentionOpen] = useState(false);
  const [mentionQuery, setMentionQuery] = useState("");
  const [mentionIndex, setMentionIndex] = useState(0);
  const [mentionPaths, setMentionPaths] = useState<string[] | null>(null);
  const mentionLoadedRef = useRef(false);
  // Composer text mirror — used to know when the input has content (drives
  // hint fade + clear-X visibility) without making the textarea controlled.
  const [composerText, setComposerText] = useState("");
  // Files-tab state — read-only browse of /workspace inside the session pod.
  const [filesPath, setFilesPath] = useState<string>("");
  const [filesEntries, setFilesEntries] = useState<FileEntry[] | null>(null);
  const [filesLoading, setFilesLoading] = useState(false);
  const [filesError, setFilesError] = useState<string | null>(null);
  const [selectedFile, setSelectedFile] = useState<SelectedFile | null>(null);
  const [selectedFileLine, setSelectedFileLine] = useState<number | null>(null);
  const [fileContentLoading, setFileContentLoading] = useState(false);
  const [fileRawImageUrl, setFileRawImageUrl] = useState<string | null>(null);
  const [fileRawImageLoading, setFileRawImageLoading] = useState(false);
  const [fileRawImageError, setFileRawImageError] = useState<string | null>(null);
  // Edit-mode bookkeeping for the file viewer.
  const [fileDraft, setFileDraft] = useState<string | null>(null);
  const [fileSaving, setFileSaving] = useState(false);
  const [fileSaveError, setFileSaveError] = useState<string | null>(null);
  const [mcpServers, setMcpServers] = useState<McpServerEntry[] | null>(null);
  const [mcpLoading, setMcpLoading] = useState(false);
  const [mcpError, setMcpError] = useState<string | null>(null);
  // Transcript navigation mode — see ./navigationMode.ts and
  // docs/features/transcript-navigation/contract.md. The mode owns "is
  // the user reading the live tail or anchored to history" as an
  // explicit state-transition machine; it is NOT derived from DOM
  // measurements of the scroll container (the retired bug class was a
  // DOM-distance heuristic that latched true during react-virtuoso's
  // followOutput smooth-scroll catch-up window, leaving the
  // scroll-to-bottom affordance visible at the live tail and freezing
  // the durable conversation_read_state cursor — observed on session
  // 269, 2026-05-27).
  const [navigationMode, setNavigationMode] = useState<NavigationMode>(
    DEFAULT_NAVIGATION_MODE,
  );
  const navigationModeRef = useRef<NavigationMode>(DEFAULT_NAVIGATION_MODE);
  // Composer attachments — uploaded to /workspace/.attachments and referenced
  // in the prompt so Claude can Read them via tool use.
  const [attachments, setAttachments] = useState<ComposerAttachment[]>([]);

  useEffect(() => {
    setTestState(session.test_state ?? null);
  }, [session.test_state]);

  useEffect(() => {
    setRolloutState(session.rollout_state ?? null);
  }, [session.rollout_state]);

  const [dragActive, setDragActive] = useState(false);
  const setChatFontScale = (value: number) => {
    setRunPref("chatFontScale", Number(clampChatFontScale(value).toFixed(2)));
  };
  const setTurnCompleteSoundVolume = (value: number) => {
    setRunPref(
      "turnCompleteSoundVolume",
      Number(clampTurnCompleteSoundVolume(value).toFixed(2)),
    );
  };
  const paneFontScale = runPrefs.chatFontScale;
  const paneFontScalePct = Math.round(paneFontScale * 100);
  const turnCompleteSoundVolumePct = Math.round(runPrefs.turnCompleteSoundVolume * 100);
  const setPaneFontScale = setChatFontScale;
  const chatFontScaleStyle = {
    "--run-chat-font-scale": runPrefs.chatFontScale,
    "--run-chat-font-xs": `${(0.75 * runPrefs.chatFontScale).toFixed(3)}rem`,
    "--run-chat-font-sm": `${(0.875 * runPrefs.chatFontScale).toFixed(3)}rem`,
    "--run-chat-font-meta": `${(0.72 * runPrefs.chatFontScale).toFixed(3)}rem`,
    "--run-chat-font-code-xs": `${(0.7 * runPrefs.chatFontScale).toFixed(3)}rem`,
    "--run-chat-font-code-sm": `${(0.78 * runPrefs.chatFontScale).toFixed(3)}rem`,
    "--run-chat-font-star": `${(0.95 * runPrefs.chatFontScale).toFixed(3)}rem`,
  } as CSSProperties;
  const composerWrapRef = useRef<HTMLDivElement | null>(null);
  const pendingComposerFocusRef = useRef(false);
  // transcriptScrollEl is a state-backed reference to the <main> element.
  // Virtuoso's customScrollParent expects the actual DOM node, and React
  // refs populate AFTER render — passing `ref.current` would give Virtuoso
  // `null` on first render and the prop wouldn't reactively update when
  // the ref hydrated. State + callback ref forces a re-render once <main>
  // mounts so Virtuoso receives the element on the next pass.
  const [transcriptScrollEl, setTranscriptScrollEl] = useState<HTMLElement | null>(null);
  const transcriptScrollCallbackRef = useCallback((node: HTMLElement | null) => {
    setTranscriptScrollEl(node);
    logChatScrollEvent(node ? "scroll-parent-mounted" : "scroll-parent-unmounted", {
      surface: "session",
      sessionId: session.id,
      sessionMode: session.mode,
      ...chatScrollElementSnapshot(node),
    });
  }, [session.id, session.mode]);
  const focusComposerTextarea = useCallback((): boolean => {
    const textarea = composerWrapRef.current?.querySelector("textarea") as HTMLTextAreaElement | null;
    if (!textarea) return false;
    textarea.focus();
    const cursor = textarea.value.length;
    textarea.setSelectionRange(cursor, cursor);
    return true;
  }, []);
  const focusTranscriptSection = useCallback((): boolean => {
    if (!transcriptScrollEl) return false;
    transcriptScrollEl.focus({ preventScroll: true });
    return document.activeElement === transcriptScrollEl;
  }, [transcriptScrollEl]);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const sdkEventSourceRef = useRef<EventSource | null>(null);
  const sdkEventReconnectTimerRef = useRef<number | null>(null);
  const historyRefreshRef = useRef<Promise<unknown> | null>(null);
  const sdkWindowEpochRef = useRef(0);
  const wasVisibleRef = useRef(visible);
  const timelineBootstrapSourceRef = useRef<SdkHistoryRefreshSource>("history");
  const timelineBootstrapClearRealtimeRef = useRef(false);
  const timelineBootstrapScrollToLatestRef = useRef(false);
  const sdkTimelineCursorRef = useRef<string | null>(null);
  const sdkLastReadSentRef = useRef<string | null>(null);
  // Windowed-transcript bookkeeping. `/timeline` pages server-projected
  // transcript rows; `sdkOldestLoadedCursorRef` tracks the prev row cursor so
  // "Load earlier" asks for rows the user can actually see.
  const sdkOldestLoadedCursorRef = useRef<string | null>(null);
  const sdkFoundOldestRef = useRef(false);
  const sdkFoundNewestRef = useRef(false);
  const sdkLoadingOlderRef = useRef(false);
  const sdkTimelineRequestSeqRef = useRef(0);
  const sdkTranscriptKeyboardNavInFlightRef = useRef<"oldest" | "newest" | null>(null);
  const [sdkFoundOldest, setSdkFoundOldest] = useState(false);
  const [sdkFoundNewest, setSdkFoundNewest] = useState(false);
  const [sdkLoadingOlder, setSdkLoadingOlder] = useState(false);
  const [sdkOlderError, setSdkOlderError] = useState<string | null>(null);
  const [scrollToLatestRequest, setScrollToLatestRequest] =
    useState<ScrollToLatestRequest>({
      signal: 0,
      behavior: "smooth",
      reason: "manual",
      enabled: false,
    });
  const [scrollToOldestSignal, setScrollToOldestSignal] = useState(0);
  // Streaming-while-back-reading bookkeeping. While the user is in
  // historical-anchor mode, incoming SSE events get appended to the
  // window — Virtuoso correctly renders them at the bottom of the
  // virtualized list without moving the user's scroll position. The
  // pill counter surfaces those events as a clickable "N new messages
  // below ↓" affordance so the user knows the conversation moved.
  // Cleared when the navigation mode transitions back to live-tail.
  // Slack/Discord ship the same pattern; what we don't do (and what
  // bit us pre-#XXX) is count non-message rows (tool calls, activity
  // shells) toward the pill — the counter is gated on row.kind ===
  // "message" because a "2 new" badge that represents two tool ticks
  // misrepresents the conversation's actual unread surface.
  const sdkPendingTailRowIdsRef = useRef<Set<string>>(new Set());
  const [sdkPendingTailCount, setSdkPendingTailCount] = useState(0);
  const sdkReadStateInFlightRef = useRef<string | null>(null);
  const sdkReadStateTimerRef = useRef<number | null>(null);
  const sessionIdRef = useRef(session.id);
  const visibleRef = useRef(visible);
  visibleRef.current = visible;
  // runningRef is read from the SSE silence watchdog so we can tell
  // whether a silent stream was silent *during a turn* (the
  // candidate-B signature) or just idle. Mirrors the visibleRef
  // pattern — kept in sync on every render.
  const runningRef = useRef(false);
  runningRef.current = running;
  // silenceWatchdogRef holds the per-open-stream SilenceWatchdog
  // instance; the SSE open path arms it, the cleanup path stops it.
  const silenceWatchdogRef = useRef<SilenceWatchdog | null>(null);
  const [sdkConnectionState, setSdkConnectionState] =
    useState<SdkConnectionState>("idle");
  const currentRunRef = useRef<{
    id: string;
    turnId: string;
    prompt: string;
    displayText: string;
    displayAttachments: MessageAttachmentDisplay[];
    skillName?: string;
    followUp: boolean;
    model: string;
    // effort is the reasoning level the user picked in the launchpad
    // dropdown. The runners pin effort from the first turn that carries a
    // non-empty value, so this is only load-bearing on the session's very
    // first run object — but every run object carries it for telemetry
    // parity with model.
    effort: string;
    permissionMode: string;
    turnStart: number;
    submitAccepted: boolean;
  } | null>(null);
  const terminalCorrelationReportedRef = useRef<Set<string>>(new Set());
  const queuedFollowupBlockedReportedRef = useRef<string | null>(null);
  // Mirror of the durable activity summary's active turn. The main transcript
  // no longer reduces raw item events in the browser; projected row updates and
  // session activity summaries own visible run state.
  const activeInterruptTargetRef = useRef<string | null>(null);
  const slashManualOpenRef = useRef(false);
  // Monotonic counter for entry ids — Date.now() collides during fast
  // bursts (sub-ms) and React's key reconciler keeps a stable component
  // tree only as long as keys are stable across renders.
  const entryIdSeqRef = useRef(0);
  function nextEntryId(prefix: string): string {
    entryIdSeqRef.current += 1;
    return `${prefix}-${session.id}-${entryIdSeqRef.current}`;
  }
  function localOrderKey(prefix: string): string {
    return [
      String(Date.now()).padStart(13, "0"),
      String(entryIdSeqRef.current + 1).padStart(8, "0"),
      prefix,
    ].join("-");
  }
  function markLocalEntries(localEntries: TranscriptEntry[], nonce: string): TranscriptEntry[] {
    return localEntries.map((entry, index) => ({
      ...entry,
      transcriptSource: "realtime",
      localOnly: true,
      clientNonce: nonce,
      orderKey: entry.orderKey ?? `${localOrderKey(nonce)}-${index}`,
    }));
  }
  function applySdkAssistantDurations(entries: TranscriptEntry[]): TranscriptEntry[] {
    if (sdkAssistantDurationsRef.current.size === 0) return entries;
    return entries.map((entry) => {
      if (entry.kind !== "message" || entry.role !== "assistant") return entry;
      const durationMs = sdkAssistantDurationsRef.current.get(entry.id);
      return durationMs == null ? entry : ({ ...entry, durationMs } as TranscriptEntry);
    });
  }
  function syncSdkRenderedEntries(): void {
    sdkServerEntriesRef.current = applySdkAssistantDurations(
      sdkServerProjectedEntriesRef.current,
    );
    const latestUsageTokens = latestContextTokens(sdkServerEntriesRef.current);
    if (latestUsageTokens > 0) setTokensUsed(latestUsageTokens);
    sdkRealtimeEntriesRef.current = pruneRealtimeEntries(
      sdkServerEntriesRef.current,
      sdkRealtimeEntriesRef.current,
    );
    const merged = mergeSdkTranscript(
      sdkServerEntriesRef.current,
      sdkRealtimeEntriesRef.current,
    );
    setEntries((prev) =>
      transcriptComparable(prev) === transcriptComparable(merged) ? prev : merged,
    );
    scheduleSdkReadStateUpdate();
  }
  function applySdkActivitySummaryToUi(rawActivity: unknown): void {
    if (!rawActivity || typeof rawActivity !== "object" || Array.isArray(rawActivity)) {
      return;
    }
    const activity = normalizeSessionActivity({
      ...(rawActivity as Record<string, unknown>),
      session_id: session.id,
    });
    if (!activity) return;
    const sdkActive =
      activity.status === "submitted" ||
      activity.status === "streaming" ||
      activity.status === "needs_input" ||
      activity.status === "stopping";
    setRenderedActiveTurnId(sdkActive ? activity.active_turn_id : null);
    activeInterruptTargetRef.current = sdkActive ? activity.active_turn_id : null;
    if (sdkActive) {
      setRunStatus(activity.status === "stopping" ? "stopping" : "running");
      setRunning(true);
      return;
    }
    setActiveTool(null);
    if (currentRunRef.current) return;
    setRunning(false);
    if (activity.failed || activity.status === "error") {
      setRunStatus("error");
    } else if (activity.status === "stopped") {
      setRunStatus("done");
    } else {
      setRunStatus((prev) => (prev === "running" ? "done" : prev));
    }
  }
  function applySdkTurnActivityRowsToUi(rows: TranscriptEntry[]): void {
    let activeTurnId: string | null = null;
    const inactiveTurns = new Set<string>();
    for (const row of rows) {
      const turnId = row.turnId ?? row.activity?.turnId ?? "";
      if (!turnId) continue;
      if (row.kind === "turn_activity") {
        const activity = row.activity;
        if (activity?.active === true || activity?.status === "active") {
          activeTurnId = turnId;
          continue;
        }
        if (activity?.status === "completed") inactiveTurns.add(turnId);
      }
      if (transcriptEntryTerminalResult(row)) inactiveTurns.add(turnId);
    }
    if (activeTurnId) {
      setRenderedActiveTurnId(activeTurnId);
      activeInterruptTargetRef.current = activeTurnId;
      setRunStatus((prev) => (prev === "stopping" ? prev : "running"));
      setRunning(true);
      return;
    }
    if (inactiveTurns.size === 0) return;
    setRenderedActiveTurnId((prev) => (prev && inactiveTurns.has(prev) ? null : prev));
    if (
      activeInterruptTargetRef.current &&
      inactiveTurns.has(activeInterruptTargetRef.current)
    ) {
      activeInterruptTargetRef.current = null;
      setActiveTool(null);
      if (!currentRunRef.current) {
        setRunning(false);
        setRunStatus((prev) =>
          prev === "running" || prev === "stopping" ? "done" : prev,
        );
      }
    }
  }
  // dispatchNavigationMode is the only function that mutates the
  // transcript's navigation mode. Every call site that represents a
  // user intent — submit, button click, keyboard nav, session open,
  // explicit user-scroll gesture, or virtuoso's "back at bottom"
  // signal — names a NavigationModeReason and routes through here.
  // The mode is never derived from continuous DOM measurement of the
  // scroll container (see ./navigationMode.ts for the migration
  // rationale).
  //
  // Side effects on entering live-tail:
  //   - Clear the "N new messages below" pending set + pill count.
  //   - Schedule a durable read-state advance so
  //     conversation_read_state.last_read_order_key catches the live
  //     tail (the durable footprint of "user has caught up").
  //
  // Side effects on entering historical-anchor: none beyond mode
  // state. The pending-tail counter starts incrementing as new
  // message-kind rows arrive (applySdkTranscriptRows).
  //
  // Every dispatch — regardless of whether the mode actually changed —
  // emits a chat-scroll telemetry event so the durable client-metric
  // surface records every user-intent transition. The bounded event
  // name is the new ./navigationMode.ts label; the reason rides in the
  // structured-log payload. Cardinality stays inside the existing
  // chat-scroll allowlist.
  function dispatchNavigationMode(reason: NavigationModeReason): void {
    const transition = transitionNavigationMode(navigationModeRef.current, reason);
    if (transition.changed) {
      navigationModeRef.current = transition.to;
      setNavigationMode(transition.to);
      if (transition.to === "live-tail") {
        sdkPendingTailRowIdsRef.current.clear();
        setSdkPendingTailCount(0);
        scheduleSdkReadStateUpdate();
      }
    }
    logChatScrollEvent(navigationModeTelemetryEvent(transition.to), {
      surface: "session",
      sessionId: session.id,
      sessionMode: session.mode,
      reason,
      ...chatScrollElementSnapshot(transcriptScrollEl),
    });
  }
  function replaceSdkServerRows(
    projectedEntries: TranscriptEntry[],
    clearRealtime = false,
  ): void {
    sdkServerProjectedEntriesRef.current = projectedEntries;
    if (clearRealtime) {
      sdkRealtimeEntriesRef.current = [];
    }
    syncSdkRenderedEntries();
  }
  function requestScrollToLatest(
    behavior: ScrollToLatestBehavior = "smooth",
    reason: ScrollToLatestReason = "manual",
  ): void {
    setScrollToLatestRequest((prev) => ({
      signal: prev.signal + 1,
      behavior,
      reason,
      enabled: true,
    }));
  }
  function clearScrollToLatestRequest(): void {
    setScrollToLatestRequest((prev) =>
      prev.enabled ? { ...prev, enabled: false } : prev,
    );
  }
  function resetSdkTimelineBootstrapState(
    reason: string,
    options: {
      source?: SdkHistoryRefreshSource;
      clearRealtime?: boolean;
      scrollToLatestOnReady?: boolean;
    } = {},
  ): void {
    sdkWindowEpochRef.current += 1;
    const epoch = sdkWindowEpochRef.current;
    timelineBootstrapSourceRef.current = options.source ?? "history";
    timelineBootstrapClearRealtimeRef.current = options.clearRealtime ?? false;
    timelineBootstrapScrollToLatestRef.current =
      options.scrollToLatestOnReady === true;
    historyRefreshRef.current = null;
    sdkEventSourceRef.current?.close();
    sdkEventSourceRef.current = null;
    if (sdkEventReconnectTimerRef.current !== null) {
      window.clearTimeout(sdkEventReconnectTimerRef.current);
      sdkEventReconnectTimerRef.current = null;
    }
    sdkServerEntriesRef.current = [];
    sdkServerProjectedEntriesRef.current = [];
    sdkRealtimeEntriesRef.current = [];
    setRenderedActiveTurnId(null);
    sdkTimelineCursorRef.current = null;
    sdkOldestLoadedCursorRef.current = null;
    sdkFoundOldestRef.current = false;
    sdkFoundNewestRef.current = false;
    setSdkFoundOldest(false);
    setSdkFoundNewest(false);
    setSdkLoadingOlder(false);
    clearScrollToLatestRequest();
    setScrollToOldestSignal(0);
    // Reset the navigation mode synchronously alongside the rest of the
    // window state. The mode is dispatched below via
    // dispatchNavigationMode for the *intent* (session-open-tail vs
    // session-open-anchored) so telemetry records the open.
    navigationModeRef.current = DEFAULT_NAVIGATION_MODE;
    setNavigationMode(DEFAULT_NAVIGATION_MODE);
    sdkPendingTailRowIdsRef.current.clear();
    setSdkPendingTailCount(0);
    setSdkOlderError(null);
    setEntries([]);
    setTokensUsed(0);
    setActivityEntriesByTurn({});
    setLoadingActivityTurns({});
    setSdkConnectionState("idle");
    dispatchTimelineBootstrap({
      type: "reset",
      sessionId: session.id,
      epoch,
    });
    logChatScrollEvent("tail-bootstrap-reset", {
      surface: "session",
      sessionId: session.id,
      sessionMode: session.mode,
      reason,
      epoch,
      source: timelineBootstrapSourceRef.current,
      clearRealtime: timelineBootstrapClearRealtimeRef.current,
      scrollToLatestOnReady: timelineBootstrapScrollToLatestRef.current,
    });
  }
  function appendSdkRealtimeEntries(localEntries: TranscriptEntry[]): void {
    sdkRealtimeEntriesRef.current = pruneRealtimeEntries(
      sdkServerEntriesRef.current,
      pruneLocalRealtimeEchoes(
        [...sdkRealtimeEntriesRef.current, ...localEntries].slice(-500),
      ),
    );
    syncSdkRenderedEntries();
  }
  function scheduleSdkReadStateUpdate(): void {
    if (!visibleRef.current) return;
    if (document.visibilityState !== "visible") return;
    // Read-cursor advancement is gated on navigation mode. The durable
    // contract is "the cursor moves only when the user is reading the
    // live tail" — historical-anchor mode is by definition the user
    // reading earlier history, so advancing the cursor would say they
    // have read events they're explicitly anchored away from.
    if (navigationModeRef.current !== "live-tail") return;
    const cursor = sdkTimelineCursorRef.current;
    if (!cursor) return;
    if (sdkLastReadSentRef.current && cursor <= sdkLastReadSentRef.current) return;
    if (sdkReadStateInFlightRef.current && cursor <= sdkReadStateInFlightRef.current) return;
    if (sdkReadStateTimerRef.current !== null) {
      window.clearTimeout(sdkReadStateTimerRef.current);
    }
    sdkReadStateTimerRef.current = window.setTimeout(() => {
      sdkReadStateTimerRef.current = null;
      void flushSdkReadStateUpdate();
    }, 400);
  }
  async function flushSdkReadStateUpdate(): Promise<void> {
    if (!visibleRef.current) return;
    if (document.visibilityState !== "visible") return;
    if (navigationModeRef.current !== "live-tail") return;
    const cursor = sdkTimelineCursorRef.current;
    if (!cursor) return;
    if (sdkLastReadSentRef.current && cursor <= sdkLastReadSentRef.current) return;
    sdkReadStateInFlightRef.current = cursor;
    try {
      const res = await authedFetch(
        scopedSessionPathForPane(`/api/sessions/${encodeURIComponent(session.id)}/read-state`),
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ last_read_order_key: cursor }),
        },
      );
      if (!res.ok) return;
      const body = (await res.json().catch(() => null)) as {
        read_state?: { last_read_order_key?: unknown } | null;
      } | null;
      const serverCursor =
        typeof body?.read_state?.last_read_order_key === "string" &&
        body.read_state.last_read_order_key
          ? body.read_state.last_read_order_key
          : cursor;
      sdkLastReadSentRef.current = advanceTimelineCursor(
        sdkLastReadSentRef.current,
        serverCursor,
      );
    } catch {
      // Replay/live updates can retry from the latest cursor; backend writes
      // are monotonic.
    } finally {
      if (sdkReadStateInFlightRef.current === cursor) {
        sdkReadStateInFlightRef.current = null;
      }
      const latest = sdkTimelineCursorRef.current;
      if (
        latest &&
        (!sdkLastReadSentRef.current || latest > sdkLastReadSentRef.current)
      ) {
        scheduleSdkReadStateUpdate();
      }
    }
  }
  function applySdkTranscriptRows(rows: TranscriptEntry[], orderKey: string): void {
    const cursor = orderKey.trim();
    if (cursor) {
      sdkTimelineCursorRef.current = advanceTimelineCursor(
        sdkTimelineCursorRef.current,
        cursor,
      );
    }
    // The "N new messages below ↓" pill counts user-facing messages
    // only — not tool calls, reasoning blocks, meta entries,
    // background-task placeholders, or turn-activity shells. A
    // streaming turn produces many non-message rows; surfacing them
    // in the pill misrepresents "what's below" because the user
    // thinks of unread conversation as messages, not as the projected
    // transcript-row count. The kind === "message" filter encodes
    // that user-facing semantic.
    if (navigationModeRef.current === "historical-anchor") {
      let added = 0;
      for (const row of rows) {
        if (row.kind !== "message") continue;
        if (sdkPendingTailRowIdsRef.current.has(row.id)) continue;
        sdkPendingTailRowIdsRef.current.add(row.id);
        added += 1;
      }
      if (added > 0) setSdkPendingTailCount((count) => count + added);
    }

    if (sdkFoundNewestRef.current) {
      sdkServerProjectedEntriesRef.current = mergeProjectedTranscriptRowUpdates(
        sdkServerProjectedEntriesRef.current,
        rows,
      );
      syncSdkRenderedEntries();
    }
    applySdkTurnActivityRowsToUi(rows);

    const run = currentRunRef.current;
    if (run) {
      const terminal =
        terminalResultForTurn(rows, run.turnId) ??
        terminalResultForTurn(sdkServerProjectedEntriesRef.current, run.turnId);
      if (terminal) {
        if (!terminalCorrelationReportedRef.current.has(run.turnId)) {
          terminalCorrelationReportedRef.current.add(run.turnId);
          logSessionEventStreamEvent("terminal_matched_by_turn_id", {
            sessionMode: session.mode,
            eventType: "transcript_rows",
          });
        }
        finalizeSdkRun(run, terminal, { refreshHistory: false });
      }
    }
  }

  // handleSdkAtBottomChange consumes react-virtuoso's at-bottom signal
  // asymmetrically: a true edge transitions us back to live-tail (the
  // user has manually scrolled all the way down — a legitimate
  // "return to tail" intent that no other gesture path covers), but a
  // false edge is intentionally ignored. The leaving-live-tail
  // direction is owned exclusively by explicit user gestures
  // (wheel/keydown/touchmove) and explicit nav controls; trusting
  // Virtuoso's "false" signal as a mode input is what created the
  // retired bug class — followOutput's smooth-scroll catch-up window
  // briefly reports false even when the user is at the visual bottom,
  // and that false transition latched the old layout-state mirror
  // into "true" indefinitely (see ./navigationMode.ts for the named
  // retired symbols and the migration guard that blocks them).
  function handleSdkAtBottomChange(atBottom: boolean): void {
    if (atBottom) {
      dispatchNavigationMode("virtuoso-at-bottom-true");
    }
  }
  function updateSdkLastAssistantDuration(durationMs: number): void {
    for (let i = sdkServerEntriesRef.current.length - 1; i >= 0; i -= 1) {
      const entry = sdkServerEntriesRef.current[i];
      if (entry.kind === "message" && entry.role === "assistant") {
        sdkAssistantDurationsRef.current.set(entry.id, durationMs);
        break;
      }
    }
    syncSdkRenderedEntries();
  }
  const queuedMessageSeqRef = useRef(0);
  function nextQueuedMessageId(): string {
    queuedMessageSeqRef.current += 1;
    return `queued-${session.id}-${queuedMessageSeqRef.current}`;
  }

  const slashFiltered = slashOpen ? filterSlashCommands(slashCommands, slashQuery) : [];
  const mentionFiltered =
    mentionOpen && mentionPaths
      ? filterMentionPaths(mentionPaths, mentionQuery)
      : [];

  useEffect(() => {
    return () => {
      sdkEventSourceRef.current?.close();
      sdkEventSourceRef.current = null;
      if (sdkEventReconnectTimerRef.current !== null) {
        window.clearTimeout(sdkEventReconnectTimerRef.current);
        sdkEventReconnectTimerRef.current = null;
      }
      if (sdkReadStateTimerRef.current !== null) {
        window.clearTimeout(sdkReadStateTimerRef.current);
        sdkReadStateTimerRef.current = null;
      }
    };
  }, []);

  // Mounted chat panes are hidden, not unmounted, when the user switches
  // sessions. On reactivation the product contract is still a durable
  // navigation: tail by default, explicit ?message= window when present.
  // Reset before paint so a stale in-memory scroll/window is never shown as
  // the active timeline authority.
  useLayoutEffect(() => {
    const wasVisible = wasVisibleRef.current;
    wasVisibleRef.current = visible;
    if (!visible) {
      return;
    }
    if (wasVisible) return;
    if (session.status !== "Active") return;
    const hasExplicitTarget = Boolean(pendingScrollMessageId?.trim());
    resetSdkTimelineBootstrapState(
      hasExplicitTarget ? "visible-message-target" : "visible-reactivation",
      {
        source: "visible-reactivation",
        clearRealtime: true,
        scrollToLatestOnReady: !hasExplicitTarget,
      },
    );
    dispatchNavigationMode(
      hasExplicitTarget ? "session-open-anchored" : "session-open-tail",
    );
  }, [pendingScrollMessageId, session.id, session.status, visible]);

  useEffect(() => {
    visibleRef.current = visible;
    if (!visible && sdkReadStateTimerRef.current !== null) {
      window.clearTimeout(sdkReadStateTimerRef.current);
      sdkReadStateTimerRef.current = null;
      return;
    }
    if (visible) scheduleSdkReadStateUpdate();
  }, [session.id, visible]);

  useEffect(() => {
    if (readOnly || !visible || session.status !== "Active") return;
    const touch = () => {
      void authedFetch(`/api/sessions/${session.id}/touch`, {
        method: "POST",
      }).catch(() => undefined);
    };
    touch();
    const interval = window.setInterval(touch, 30_000);
    return () => window.clearInterval(interval);
  }, [readOnly, session.id, session.status, visible]);


  // Auto-send the next queued message once the current run finishes.
  useEffect(() => {
    if (!running && queuedMessages.length > 0) {
      const [nextMessage, ...remaining] = queuedMessages;
      setQueuedMessages(remaining);
      startRun(
        nextMessage.text,
        nextMessage.displayText,
        nextMessage.skillName,
        nextMessage.displayAttachments,
      );
    }
  // startRun is intentionally omitted — it's redefined each render, and
  // useEffect's closure gives us the fresh version when deps actually change.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running, queuedMessages]);

  useEffect(() => {
    const run = currentRunRef.current;
    if (!run || !running || queuedMessages.length === 0) return;
    if (queuedFollowupBlockedReportedRef.current === run.id) return;
    const terminal = terminalResultForTurn(sdkServerProjectedEntriesRef.current, run.turnId);
    if (!terminal) return;
    queuedFollowupBlockedReportedRef.current = run.id;
    logSessionEventStreamEvent("queued_followup_blocked_after_terminal", {
      sessionMode: session.mode,
    });
  }, [queuedMessages.length, running, session.mode]);

  // Scroll behavior is owned by react-virtuoso now: followOutput is gated
  // on the durable navigation mode (live-tail → instant follow,
  // historical-anchor → no follow) via the followLiveTail prop, and
  // `atBottomStateChange(true)` is the one-way signal that returns the
  // navigation mode to live-tail when the user manually scrolls all
  // the way down. `atBottomStateChange(false)` is intentionally NOT
  // wired — see handleSdkAtBottomChange for the asymmetry rationale.
  // See RunMessages for the wiring.

  // History replay is intentionally not limited to empty transcript state: a
  // run can finish while the tab is closed, leaving a stale partial transcript.
  const [timelineBootstrap, dispatchTimelineBootstrap] = useReducer(
    reduceTimelineBootstrap,
    session.id,
    (initialSessionId) =>
      initialTimelineBootstrapState(initialSessionId, sdkWindowEpochRef.current),
  );
  const historyBootstrapped = timelineBootstrap.status === "ready";
  const applyCurrentSessionRoute = useCallback(() => {
    if (!visible) return;
    const route = readSessionRouteFromPath();
    if (route?.sessionId !== session.id) return;
    if (route.tab === "turns") {
      setActiveTab("turns");
      setPendingRouteTurnId(route.turnId);
      if (route.turnId) setSelectedTurnId(route.turnId);
      return;
    }
    setActiveTab("chat");
    setPendingRouteTurnId(null);
  }, [session.id, visible]);
  useEffect(() => {
    applyCurrentSessionRoute();
  }, [applyCurrentSessionRoute]);
  useEffect(() => {
    if (!visible) return;
    window.addEventListener("popstate", applyCurrentSessionRoute);
    return () => window.removeEventListener("popstate", applyCurrentSessionRoute);
  }, [applyCurrentSessionRoute, visible]);

  // History replay and live tail both hit the server-owned transcript-row read
  // model. Raw Tank item events stay behind the Turn activity detail endpoint
  // and never drive the main transcript browser state.
  function refreshSdkRunHistory(
    clearRealtime = false,
    source: SdkHistoryRefreshSource = "history",
  ): Promise<boolean> {
    return refreshSdkRunHistoryResult(clearRealtime, source).then(
      (result) => result.replayed,
    );
  }

  function nextSdkTimelineRequestId(source: string): string {
    sdkTimelineRequestSeqRef.current += 1;
    return `${session.id}:${sdkWindowEpochRef.current}:${sdkTimelineRequestSeqRef.current}:${source}`;
  }

  // Normal session navigation opens the transcript row tail. The only
  // non-tail bootstrap is an explicit ?message= transcript deep link, which
  // the backend resolves to a durable row cursor and returns as a bounded row
  // window around that target.
  function refreshSdkRunHistoryResult(
    clearRealtime = false,
    source: SdkHistoryRefreshSource = "history",
  ): Promise<SdkHistoryRefreshResult> {
    const refreshSessionId = session.id;
    const refreshEpoch = sdkWindowEpochRef.current;
    const startedAt = performance.now();
    const requestId = nextSdkTimelineRequestId(source);
    const previousSnapshot = chatScrollEntrySnapshot(sdkServerProjectedEntriesRef.current);
    const load = async (): Promise<SdkHistoryRefreshResult> => {
      const targetTimelineId = pendingScrollMessageId?.trim() ?? "";
      const params = new URLSearchParams();
      let anchor = "newest";
      if (targetTimelineId) {
        params.set("timeline_id", targetTimelineId);
        params.set("rows_before", String(SDK_TIMELINE_DEEPLINK_ROWS_BEFORE));
        params.set("rows_after", String(SDK_TIMELINE_DEEPLINK_ROWS_AFTER));
        anchor = "timeline_id";
      } else {
        params.set("anchor", "newest");
        params.set("rows", String(SDK_TIMELINE_TAIL_ROWS));
      }
      const scrollToLatestOnReady =
        timelineBootstrapScrollToLatestRef.current && anchor === "newest";
      const stickToLatestAfterLoad =
        source === "projected-refresh" && navigationModeRef.current === "live-tail";
      if (timelineBootstrapScrollToLatestRef.current && anchor !== "newest") {
        timelineBootstrapScrollToLatestRef.current = false;
      }
      logChatScrollEvent("timeline-request", {
        surface: "session",
        sessionId: refreshSessionId,
        sessionMode: session.mode,
        source,
        anchor,
        requestId,
        clearRealtime,
        epoch: refreshEpoch,
        scrollToLatestOnReady,
        ...chatScrollPreviousEntrySnapshot(previousSnapshot),
        ...chatScrollElementSnapshot(transcriptScrollEl),
      });
      const res = await authedFetch(
        scopedSessionPathForPane(`/api/sessions/${encodeURIComponent(refreshSessionId)}/timeline?${params.toString()}`),
      );
      if (!res.ok) {
        logChatScrollEvent("timeline-error", {
          surface: "session",
          sessionId: refreshSessionId,
          sessionMode: session.mode,
          source,
          anchor,
          requestId,
          status: res.status,
          durationMs: Math.round(performance.now() - startedAt),
          ...chatScrollElementSnapshot(transcriptScrollEl),
        });
        return {
          replayed: false,
          error: `timeline request failed: ${res.status}`,
        };
      }
      const body = (await res.json()) as TranscriptTimelineBody;
      if (sessionIdRef.current !== refreshSessionId) return { replayed: false };
      if (sdkWindowEpochRef.current !== refreshEpoch) {
        logChatScrollEvent("timeline-stale", {
          surface: "session",
          sessionId: refreshSessionId,
          sessionMode: session.mode,
          source,
          anchor,
          requestId,
          epoch: refreshEpoch,
          currentEpoch: sdkWindowEpochRef.current,
          ...chatScrollElementSnapshot(transcriptScrollEl),
        });
        return { replayed: false, stale: true };
      }
      const lastReadOrderKey = body.read_state?.last_read_order_key;
      if (typeof lastReadOrderKey === "string" && lastReadOrderKey) {
        sdkLastReadSentRef.current = advanceTimelineCursor(
          sdkLastReadSentRef.current,
          lastReadOrderKey,
        );
      }
      let projectedEntries: TranscriptEntry[];
      try {
        projectedEntries = transcriptRowsFromTimelineBody(body);
      } catch (err) {
        return {
          replayed: false,
          error: String((err as Error).message ?? err),
        };
      }
      const liveOrderKey =
        typeof body.live_order_key === "string" ? body.live_order_key : "";
      if (liveOrderKey) {
        sdkTimelineCursorRef.current = advanceTimelineCursor(
          sdkTimelineCursorRef.current,
          liveOrderKey,
        );
      }
      const prevAfter =
        typeof body.prev_cursor === "string" ? body.prev_cursor : "";
      if (prevAfter) {
        sdkOldestLoadedCursorRef.current = prevAfter;
      }
      const nextAfter =
        typeof body.next_cursor === "string" ? body.next_cursor : "";
      const foundOldest = body.found_oldest === true;
      const foundNewest = body.found_newest === true;
      sdkFoundOldestRef.current = foundOldest;
      sdkFoundNewestRef.current = foundNewest;
      setSdkFoundOldest(foundOldest);
      setSdkFoundNewest(foundNewest);
      logChatScrollEntries("timeline-loaded", projectedEntries, {
        surface: "session",
        sessionId: refreshSessionId,
        sessionMode: session.mode,
        source,
        anchor,
        requestId,
        eventCount: projectedEntries.length,
        canonicalEventCount: projectedEntries.length,
        foundOldest,
        foundNewest,
        hasPrevCursor: Boolean(prevAfter),
        hasNextCursor: Boolean(nextAfter),
        clearRealtime,
        terminalStatus: "",
        durationMs: Math.round(performance.now() - startedAt),
        ...chatScrollEntryDelta(previousSnapshot, projectedEntries),
        ...chatScrollElementSnapshot(transcriptScrollEl),
      });
      replaceSdkServerRows(projectedEntries, clearRealtime);
      applySdkActivitySummaryToUi(body.activity);
      if (scrollToLatestOnReady) {
        timelineBootstrapScrollToLatestRef.current = false;
        requestScrollToLatest("auto", source);
      } else if (stickToLatestAfterLoad) {
        requestScrollToLatest("auto", source);
      }
      const run = currentRunRef.current;
      return {
        replayed: projectedEntries.length > 0,
        terminal: run
          ? terminalResultForTurn(projectedEntries, run.turnId) ?? undefined
          : undefined,
      };
    };
    return load().catch((err) => ({
      replayed: false,
      error: `timeline request failed: ${String((err as Error).message ?? err)}`,
    }));
  }

  // loadSdkOlderEvents fetches transcript rows strictly older than the
  // current window's first rendered row. A click maps to one row-page request;
  // raw tool chatter inside Turn activity rows is loaded only when that row is
  // expanded through the turn-activity endpoint.
  async function loadSdkOlderEvents(): Promise<void> {
    const refreshSessionId = session.id;
    if (sdkFoundOldestRef.current) return;
    if (sdkLoadingOlderRef.current) return;
    sdkLoadingOlderRef.current = true;
    setSdkOlderError(null);
    setSdkLoadingOlder(true);
    const requestId = nextSdkTimelineRequestId("older");
    const beforeCursor = sdkOldestLoadedCursorRef.current;
    const previousSnapshot = chatScrollEntrySnapshot(sdkServerProjectedEntriesRef.current);
    if (!beforeCursor) {
      logChatScrollEvent("older-missing-cursor", {
        surface: "session",
        sessionId: refreshSessionId,
        sessionMode: session.mode,
        requestId,
        ...chatScrollPreviousEntrySnapshot(previousSnapshot),
        ...chatScrollElementSnapshot(transcriptScrollEl),
      });
      try {
        // Older-missing-cursor recovery: the user was back-paginating
        // and the cursor went stale. They are by definition reading
        // history, so the jump-oldest reason transitions / confirms
        // historical-anchor mode.
        dispatchNavigationMode("jump-oldest");
        await jumpSdkToOldest("older-missing-cursor");
        setScrollToOldestSignal((value) => value + 1);
      } catch (err) {
        setSdkOlderError(
          `Could not load earlier messages: ${String((err as Error).message ?? err)}`,
        );
      } finally {
        sdkLoadingOlderRef.current = false;
        setSdkLoadingOlder(false);
      }
      return;
    }
    try {
      const startedAt = performance.now();
      logChatScrollEvent("older-request", {
        surface: "session",
        sessionId: refreshSessionId,
        sessionMode: session.mode,
        requestId,
        beforeCursor,
        ...chatScrollPreviousEntrySnapshot(previousSnapshot),
        ...chatScrollElementSnapshot(transcriptScrollEl),
      });
      const params = new URLSearchParams({
        before_cursor: beforeCursor,
        rows: String(SDK_TIMELINE_OLDER_ROWS),
      });
      const res = await authedFetch(
        scopedSessionPathForPane(`/api/sessions/${encodeURIComponent(refreshSessionId)}/timeline?${params.toString()}`),
      );
      if (!res.ok) {
        logChatScrollEvent("older-error", {
          surface: "session",
          sessionId: refreshSessionId,
          sessionMode: session.mode,
          requestId,
          beforeCursor,
          status: res.status,
          durationMs: Math.round(performance.now() - startedAt),
          ...chatScrollElementSnapshot(transcriptScrollEl),
        });
        setSdkOlderError(`Could not load earlier messages: ${res.status}`);
        return;
      }
      const body = (await res.json()) as TranscriptTimelineBody;
      if (sessionIdRef.current !== refreshSessionId) return;
      let projectedOlderEntries: TranscriptEntry[];
      try {
        projectedOlderEntries = transcriptRowsFromTimelineBody(body);
      } catch (err) {
        setSdkOlderError(`Could not load earlier messages: ${String((err as Error).message ?? err)}`);
        return;
      }
      const nextProjectedEntries = mergeProjectedTranscriptWindows(
        projectedOlderEntries,
        sdkServerProjectedEntriesRef.current,
      );
      const prevAfter =
        typeof body.prev_cursor === "string" ? body.prev_cursor : "";
      if (prevAfter) {
        sdkOldestLoadedCursorRef.current = prevAfter;
      }
      const liveOrderKey =
        typeof body.live_order_key === "string" ? body.live_order_key : "";
      if (liveOrderKey) {
        sdkTimelineCursorRef.current = advanceTimelineCursor(
          sdkTimelineCursorRef.current,
          liveOrderKey,
        );
      }
      const foundOldest = body.found_oldest === true;
      if (foundOldest) {
        sdkFoundOldestRef.current = true;
        setSdkFoundOldest(true);
      }
      const delta = chatScrollEntryDelta(previousSnapshot, nextProjectedEntries);
      logChatScrollEntries("older-loaded", nextProjectedEntries, {
        surface: "session",
        sessionId: refreshSessionId,
        sessionMode: session.mode,
        requestId,
        eventCount: projectedOlderEntries.length,
        canonicalEventCount: projectedOlderEntries.length,
        beforeCursor,
        foundOldest,
        hasPrevCursor: Boolean(prevAfter),
        durationMs: Math.round(performance.now() - startedAt),
        ...delta,
        ...chatScrollElementSnapshot(transcriptScrollEl),
      });
      if (projectedOlderEntries.length > 0) {
        sdkServerProjectedEntriesRef.current = nextProjectedEntries;
        syncSdkRenderedEntries();
        applySdkActivitySummaryToUi(body.activity);
      }
      if (projectedOlderEntries.length === 0) {
        logChatScrollEntries("older-no-visible-change", nextProjectedEntries, {
          surface: "session",
          sessionId: refreshSessionId,
          sessionMode: session.mode,
          requestId,
          beforeCursor,
          eventCount: 0,
          ...delta,
          ...chatScrollElementSnapshot(transcriptScrollEl),
        });
      }
      setSdkOlderError(null);
    } finally {
      sdkLoadingOlderRef.current = false;
      setSdkLoadingOlder(false);
    }
  }

  // jumpSdkToOldest resets the window to the first transcript rows. It never
  // walks raw events; the server pages the materialized row read model.
  async function jumpSdkToOldest(source = "jump-oldest"): Promise<void> {
    const refreshSessionId = session.id;
    const params = new URLSearchParams({
      anchor: "oldest",
      rows: String(SDK_TIMELINE_TAIL_ROWS),
    });
    const startedAt = performance.now();
    const requestId = nextSdkTimelineRequestId(source);
    const previousSnapshot = chatScrollEntrySnapshot(sdkServerProjectedEntriesRef.current);
    logChatScrollEvent("timeline-request", {
      surface: "session",
      sessionId: refreshSessionId,
      sessionMode: session.mode,
      source,
      anchor: "oldest",
      requestId,
      ...chatScrollPreviousEntrySnapshot(previousSnapshot),
      ...chatScrollElementSnapshot(transcriptScrollEl),
    });
    const res = await authedFetch(
      scopedSessionPathForPane(`/api/sessions/${encodeURIComponent(refreshSessionId)}/timeline?${params.toString()}`),
    );
    if (!res.ok) {
      logChatScrollEvent("timeline-error", {
        surface: "session",
        sessionId: refreshSessionId,
        sessionMode: session.mode,
        source,
        anchor: "oldest",
        requestId,
        status: res.status,
        durationMs: Math.round(performance.now() - startedAt),
        ...chatScrollElementSnapshot(transcriptScrollEl),
      });
      throw new Error(`timeline request failed: ${res.status}`);
    }
    const body = (await res.json()) as TranscriptTimelineBody;
    if (sessionIdRef.current !== refreshSessionId) return;
    const projectedEntries = transcriptRowsFromTimelineBody(body);
    const liveOrderKey =
      typeof body.live_order_key === "string" ? body.live_order_key : "";
    if (liveOrderKey) {
      sdkTimelineCursorRef.current = advanceTimelineCursor(
        sdkTimelineCursorRef.current,
        liveOrderKey,
      );
    }
    const nextAfter =
      typeof body.next_cursor === "string" ? body.next_cursor : "";
    const prevAfter =
      typeof body.prev_cursor === "string" ? body.prev_cursor : "";
    if (prevAfter) sdkOldestLoadedCursorRef.current = prevAfter;
    sdkFoundOldestRef.current = body.found_oldest === true;
    sdkFoundNewestRef.current = body.found_newest === true;
    setSdkFoundOldest(body.found_oldest === true);
    setSdkFoundNewest(body.found_newest === true);
    logChatScrollEntries("timeline-loaded", projectedEntries, {
      surface: "session",
      sessionId: refreshSessionId,
      sessionMode: session.mode,
      source,
      anchor: "oldest",
      requestId,
      eventCount: projectedEntries.length,
      canonicalEventCount: projectedEntries.length,
      foundOldest: body.found_oldest === true,
      foundNewest: body.found_newest === true,
      hasPrevCursor: Boolean(prevAfter),
      hasNextCursor: Boolean(nextAfter),
      durationMs: Math.round(performance.now() - startedAt),
      ...chatScrollEntryDelta(previousSnapshot, projectedEntries),
      ...chatScrollElementSnapshot(transcriptScrollEl),
    });
    replaceSdkServerRows(projectedEntries, false);
    applySdkActivitySummaryToUi(body.activity);
  }

  // jumpSdkToLatest resets the window to the live tail. If the SPA never
  // back-paginated (foundNewest=true), this is a no-op fast path: the
  // existing DOM bottom is the live tail, so the caller can just scroll.
  // If the user back-paginated past the live tail, we drop the window
  // and refetch — that's the "stale tail" path mentioned in the plan.
  async function jumpSdkToLatest(source = "jump-latest"): Promise<void> {
    if (sdkFoundNewestRef.current) return;
    const refreshSessionId = session.id;
    const params = new URLSearchParams({
      anchor: "newest",
      rows: String(SDK_TIMELINE_TAIL_ROWS),
    });
    const startedAt = performance.now();
    const requestId = nextSdkTimelineRequestId(source);
    const previousSnapshot = chatScrollEntrySnapshot(sdkServerProjectedEntriesRef.current);
    logChatScrollEvent("timeline-request", {
      surface: "session",
      sessionId: refreshSessionId,
      sessionMode: session.mode,
      source,
      anchor: "newest",
      requestId,
      ...chatScrollPreviousEntrySnapshot(previousSnapshot),
      ...chatScrollElementSnapshot(transcriptScrollEl),
    });
    const res = await authedFetch(
      scopedSessionPathForPane(`/api/sessions/${encodeURIComponent(refreshSessionId)}/timeline?${params.toString()}`),
    );
    if (!res.ok) {
      logChatScrollEvent("timeline-error", {
        surface: "session",
        sessionId: refreshSessionId,
        sessionMode: session.mode,
        source,
        anchor: "newest",
        requestId,
        status: res.status,
        durationMs: Math.round(performance.now() - startedAt),
        ...chatScrollElementSnapshot(transcriptScrollEl),
      });
      return;
    }
    const body = (await res.json()) as TranscriptTimelineBody;
    if (sessionIdRef.current !== refreshSessionId) return;
    let projectedEntries: TranscriptEntry[];
    try {
      projectedEntries = transcriptRowsFromTimelineBody(body);
    } catch {
      return;
    }
    const liveOrderKey =
      typeof body.live_order_key === "string" ? body.live_order_key : "";
    if (liveOrderKey) {
      sdkTimelineCursorRef.current = advanceTimelineCursor(
        sdkTimelineCursorRef.current,
        liveOrderKey,
      );
    }
    const nextAfter =
      typeof body.next_cursor === "string" ? body.next_cursor : "";
    const prevAfter =
      typeof body.prev_cursor === "string" ? body.prev_cursor : "";
    if (prevAfter) sdkOldestLoadedCursorRef.current = prevAfter;
    sdkFoundOldestRef.current = body.found_oldest === true;
    sdkFoundNewestRef.current = body.found_newest === true;
    setSdkFoundOldest(body.found_oldest === true);
    setSdkFoundNewest(body.found_newest === true);
    logChatScrollEntries("timeline-loaded", projectedEntries, {
      surface: "session",
      sessionId: refreshSessionId,
      sessionMode: session.mode,
      source,
      anchor: "newest",
      requestId,
      eventCount: projectedEntries.length,
      canonicalEventCount: projectedEntries.length,
      foundOldest: body.found_oldest === true,
      foundNewest: body.found_newest === true,
      hasPrevCursor: Boolean(prevAfter),
      hasNextCursor: Boolean(nextAfter),
      durationMs: Math.round(performance.now() - startedAt),
      ...chatScrollEntryDelta(previousSnapshot, projectedEntries),
      ...chatScrollElementSnapshot(transcriptScrollEl),
    });
    replaceSdkServerRows(projectedEntries, false);
    applySdkActivitySummaryToUi(body.activity);
  }

  async function scrollTranscriptToConversationStart(): Promise<void> {
    if (sdkTranscriptKeyboardNavInFlightRef.current) return;
    sdkTranscriptKeyboardNavInFlightRef.current = "oldest";
    setSdkOlderError(null);
    try {
      dispatchNavigationMode("keyboard-home");
      if (!sdkFoundOldestRef.current) {
        await jumpSdkToOldest("keyboard");
      }
      setScrollToOldestSignal((value) => value + 1);
    } catch (err) {
      setSdkOlderError(
        `Could not load beginning of conversation: ${String((err as Error).message ?? err)}`,
      );
    } finally {
      if (sdkTranscriptKeyboardNavInFlightRef.current === "oldest") {
        sdkTranscriptKeyboardNavInFlightRef.current = null;
      }
    }
  }

  async function scrollTranscriptToConversationEnd(): Promise<void> {
    if (sdkTranscriptKeyboardNavInFlightRef.current) return;
    sdkTranscriptKeyboardNavInFlightRef.current = "newest";
    try {
      dispatchNavigationMode("keyboard-end");
      if (!sdkFoundNewestRef.current) {
        await jumpSdkToLatest("keyboard");
      }
      requestScrollToLatest("smooth", "keyboard");
    } finally {
      if (sdkTranscriptKeyboardNavInFlightRef.current === "newest") {
        sdkTranscriptKeyboardNavInFlightRef.current = null;
      }
    }
  }

  useEffect(() => {
    if (!visible || !CHAT_MODES.has(session.mode)) return;
    if (timelineBootstrap.status !== "idle") return;
    if (historyRefreshRef.current) return;
    const refreshSessionId = session.id;
    const refreshEpoch = sdkWindowEpochRef.current;
    const source = timelineBootstrapSourceRef.current;
    const clearRealtime = timelineBootstrapClearRealtimeRef.current;
    const run = currentRunRef.current;
    dispatchTimelineBootstrap({
      type: "loading",
      sessionId: refreshSessionId,
      epoch: refreshEpoch,
    });
    const refresh = refreshSdkRunHistoryResult(
      clearRealtime,
      source,
    )
      .then((result) => {
        if (sessionIdRef.current !== refreshSessionId) return;
        if (sdkWindowEpochRef.current !== refreshEpoch) return;
        if (result.stale) {
          dispatchTimelineBootstrap({
            type: "reset",
            sessionId: refreshSessionId,
            epoch: sdkWindowEpochRef.current,
          });
          return;
        }
        if (result.error) {
          dispatchTimelineBootstrap({
            type: "error",
            sessionId: refreshSessionId,
            epoch: refreshEpoch,
            error: result.error,
          });
          return;
        }
        if (run && result.terminal) {
          finalizeSdkRun(run, result.terminal, { refreshHistory: false });
        }
        dispatchTimelineBootstrap({
          type: "ready",
          sessionId: refreshSessionId,
          epoch: refreshEpoch,
        });
      })
      .finally(() => {
        if (sessionIdRef.current !== refreshSessionId) return;
        if (sdkWindowEpochRef.current !== refreshEpoch) return;
        if (historyRefreshRef.current === refresh) {
          historyRefreshRef.current = null;
        }
      });
    historyRefreshRef.current = refresh;
  // refreshSdkRunHistoryResult/finalizeSdkRun close over current refs and
  // should not resubscribe an in-flight bootstrap.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session.id, session.mode, timelineBootstrap.epoch, timelineBootstrap.status, visible]);

  useEffect(() => {
    if (activeTab !== "files" || filesAvailable) return;
    setActiveTab("chat");
  }, [activeTab, filesAvailable]);

  // Files tab — fetch directory listing whenever the path changes or the
  // user opens the tab on a ready session.
  useEffect(() => {
    if (activeTab !== "files" || !filesAvailable) return;
    let cancelled = false;
    setFilesLoading(true);
    setFilesError(null);
    void authedFetch(
      `/api/sessions/${session.id}/files?path=${encodeURIComponent(filesPath)}`,
    )
      .then(async (res) => {
        if (!res.ok) {
          throw new Error(`${res.status} ${await res.text()}`);
        }
        return (await res.json()) as { path: string; entries: FileEntry[] };
      })
      .then((body) => {
        if (cancelled) return;
        setFilesEntries(body.entries);
        setFilesLoading(false);
      })
      .catch((err) => {
        if (cancelled) return;
        setFilesError(String(err.message ?? err));
        setFilesEntries([]);
        setFilesLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activeTab, filesAvailable, filesPath, session.id]);

  // Selected-file content fetch.
  useEffect(() => {
    if (!filesAvailable || !selectedFile || selectedFile.text || selectedFile.binary) return;
    // text empty + not binary == placeholder created by openFile; load it.
    let cancelled = false;
    setFileContentLoading(true);
    void authedFetch(
      `/api/sessions/${session.id}/files/content?path=${encodeURIComponent(selectedFile.path)}`,
    )
      .then(async (res) => {
        if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
        return (await res.json()) as SelectedFile;
      })
      .then((body) => {
        if (cancelled) return;
        setSelectedFile(body);
        setFileContentLoading(false);
      })
      .catch(() => {
        if (cancelled) return;
        setFileContentLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [filesAvailable, selectedFile, session.id]);

  useEffect(() => {
    let cancelled = false;
    let objectUrl: string | null = null;
    setFileRawImageUrl(null);
    setFileRawImageError(null);
    setFileRawImageLoading(false);
    if (!filesAvailable || !selectedFile?.binary || !isImagePath(selectedFile.path)) {
      return () => undefined;
    }
    setFileRawImageLoading(true);
    void authedFetch(
      `/api/sessions/${session.id}/files/raw?path=${encodeURIComponent(selectedFile.path)}`,
    )
      .then(async (res) => {
        if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
        return res.blob();
      })
      .then((blob) => {
        if (cancelled) return;
        objectUrl = URL.createObjectURL(blob);
        setFileRawImageUrl(objectUrl);
        setFileRawImageLoading(false);
      })
      .catch((err) => {
        if (cancelled) return;
        setFileRawImageError(String((err as Error).message ?? err));
        setFileRawImageLoading(false);
      });
    return () => {
      cancelled = true;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [filesAvailable, selectedFile?.binary, selectedFile?.path, session.id]);

  useEffect(() => {
    if (readOnly || session.status !== "Active") {
      setMcpServers(null);
      setMcpError(null);
      return;
    }
    let cancelled = false;
    setMcpLoading(true);
    setMcpError(null);
    void authedFetch(`/api/sessions/${session.id}/mcp-servers`)
      .then(async (res) => {
        if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
        return (await res.json()) as { entries: McpServerEntry[] };
      })
      .then((body) => {
        if (cancelled) return;
        setMcpServers(body.entries ?? []);
        setMcpLoading(false);
      })
      .catch((err) => {
        if (cancelled) return;
        setMcpServers([]);
        setMcpError(String(err));
        setMcpLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [readOnly, session.id, session.status]);

  useEffect(() => {
    if (readOnly || session.status !== "Active") {
      setSlashCommands(SLASH_COMMANDS);
      return;
    }
    let cancelled = false;
    void authedFetch(`/api/sessions/${session.id}/skills`)
      .then(async (res) => {
        if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
        return (await res.json()) as { entries: SkillEntry[] };
      })
      .then((body) => {
        if (cancelled) return;
        const byName = new Map<string, SlashCommand>();
        for (const command of SLASH_COMMANDS) byName.set(command.name, command);
        for (const skill of body.entries ?? []) {
          const normalizedName = skill.name.startsWith("/") ? skill.name : `/${skill.name}`;
          byName.set(normalizedName, {
            name: normalizedName,
            desc: skill.description || skill.body_preview || `${skill.source} skill`,
          });
        }
        setSlashCommands([...byName.values()]);
      })
      .catch(() => {
        if (!cancelled) setSlashCommands(SLASH_COMMANDS);
      });
    return () => {
      cancelled = true;
    };
  }, [readOnly, session.id, session.status]);

  function openFileEntry(name: string, type: FileEntry["type"]) {
    const next = joinFilesPath(filesPath, name);
    if (type === "dir") {
      setFilesPath(next);
      setSelectedFile(null);
      setSelectedFileLine(null);
      setFileDraft(null);
      setFileSaveError(null);
      return;
    }
    // Trigger content fetch by setting a placeholder.
    setSelectedFile({ path: next, size: 0, truncated: false, text: "", binary: false });
    setSelectedFileLine(null);
    setFileDraft(null);
    setFileSaveError(null);
  }

  function openWorkspacePath(target: WorkspacePathTarget | string) {
    if (!filesAvailable) return;
    const normalized = typeof target === "string"
      ? normalizeWorkspacePathTarget(target)
      : target;
    if (!normalized) return;
    setActiveTab("files");
    setFilesPath(parentFilesPath(normalized.path));
    setSelectedFile({ path: normalized.path, size: 0, truncated: false, text: "", binary: false });
    setSelectedFileLine(normalized.line);
    setFileDraft(null);
    setFileSaveError(null);
  }

  async function uploadAttachment(file: File) {
    const id = `att-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const uploadName = file.name || "file";
    const previewUrl = file.type.startsWith("image/")
      ? URL.createObjectURL(file)
      : undefined;
    setAttachments((prev) => {
      const [{ label }] = labelAttachments([file], prev);
      return [
        ...prev,
        {
          id,
          name: uploadName,
          label,
          path: "",
          absPath: "",
          size: file.size,
          previewUrl,
          status: "uploading",
        },
      ];
    });
    try {
      // Raw-body upload (orchestrator image doesn't ship python-multipart).
      const res = await authedFetch(
        `/api/sessions/${session.id}/files/upload?name=${encodeURIComponent(uploadName)}`,
        {
          method: "POST",
          headers: {
            "Content-Type": file.type || "application/octet-stream",
          },
          body: file,
        },
      );
      if (!res.ok) {
        throw new Error(`${res.status} ${await res.text()}`);
      }
      const body = (await res.json()) as {
        path: string;
        abs_path: string;
        name: string;
        size: number;
      };
      setAttachments((prev) =>
        prev.map((a) =>
          a.id === id
            ? {
                ...a,
                path: body.path,
                absPath: body.abs_path,
                name: body.name || a.name,
                status: "ready",
              }
            : a,
        ),
      );
    } catch (err) {
      setAttachments((prev) =>
        prev.map((a) =>
          a.id === id
            ? {
                ...a,
                status: "error",
                errorMsg: String((err as Error).message ?? err),
              }
            : a,
        ),
      );
    }
  }

  function removeAttachment(id: string) {
    setAttachments((prev) => {
      const att = prev.find((a) => a.id === id);
      if (att?.previewUrl) URL.revokeObjectURL(att.previewUrl);
      return prev.filter((a) => a.id !== id);
    });
  }

  function handleAttachmentFiles(files: FileList | null) {
    if (!supportsFileAttachments) return;
    if (!files) return;
    for (const f of Array.from(files)) {
      // Be permissive on file type; the agent runtime can load many formats.
      void uploadAttachment(f);
    }
  }

  async function saveFileDraft() {
    if (!selectedFile || fileDraft == null) return;
    setFileSaving(true);
    setFileSaveError(null);
    try {
      const res = await authedFetch(
        `/api/sessions/${session.id}/files/content?path=${encodeURIComponent(selectedFile.path)}`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ text: fileDraft }),
        },
      );
      if (!res.ok) {
        throw new Error(`${res.status} ${await res.text()}`);
      }
      const body = (await res.json()) as SelectedFile;
      setSelectedFile(body);
      setFileDraft(null);
      // Re-fetch the listing in case size changed.
      void authedFetch(
        `/api/sessions/${session.id}/files?path=${encodeURIComponent(filesPath)}`,
      )
        .then(async (r) => {
          if (!r.ok) return;
          const listing = (await r.json()) as { entries: FileEntry[] };
          setFilesEntries(listing.entries);
        })
        .catch(() => undefined);
    } catch (err) {
      setFileSaveError(String((err as Error).message ?? err));
    } finally {
      setFileSaving(false);
    }
  }

  // When the session id changes, reset transcript state and allow the
  // history sync to run again. The replay paths repopulate from backend.
  useLayoutEffect(() => {
    sessionIdRef.current = session.id;
    wasVisibleRef.current = visible;
    resetSdkTimelineBootstrapState("session-change", {
      source: "history",
      clearRealtime: false,
      scrollToLatestOnReady: !Boolean(pendingScrollMessageId?.trim()),
    });
    sdkAssistantDurationsRef.current = new Map();
    currentRunRef.current = null;
    activeInterruptTargetRef.current = null;
    setQueuedMessages([]);
    setRunStatus("idle");
    setRunning(false);
  // resetSdkTimelineBootstrapState intentionally closes over current session
  // state; this layout reset must run exactly once for each session id before
  // passive timeline bootstrap effects can start.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session.id]);

  // sendByCtrlEnter — when on, plain Enter inserts a newline and only
  // Ctrl/⌘+Enter submits. Implemented by intercepting at capture phase
  // on the composer wrap so it runs before AI Elements' internal handler.
  useEffect(() => {
    const wrap = composerWrapRef.current;
    if (!wrap) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Enter") return;
      const ta = wrap.querySelector("textarea");
      if (!ta || e.target !== ta) return;
      if (slashOpen) return; // palette handler owns Enter
      if (e.shiftKey) return; // newline regardless of mode
      if (runPrefs.sendByCtrlEnter) {
        // Plain Enter must NOT submit; let the textarea insert a newline.
        if (!e.ctrlKey && !e.metaKey) {
          e.stopPropagation();
        }
        // Ctrl/⌘+Enter submits — fall through to AI Elements' form submit.
      } else {
        // Default: plain Enter submits (AI Elements handles it).
        // Ctrl/⌘+Enter also submits naturally.
      }
    };
    wrap.addEventListener("keydown", onKey, true);
    return () => wrap.removeEventListener("keydown", onKey, true);
  }, [runPrefs.sendByCtrlEnter, slashOpen]);

  // Tab toggles focus between the two primary chat surfaces: composer and
  // transcript. Leave native tab order alone for message buttons, header tabs,
  // Stop, and every other control.
  useEffect(() => {
    if (!visible || activeTab !== "chat") return;
    const onKey = (e: KeyboardEvent) => {
      if (
        e.key !== "Tab" ||
        e.altKey ||
        e.ctrlKey ||
        e.metaKey ||
        e.isComposing ||
        slashOpen ||
        mentionOpen ||
        mcpOpen
      ) {
        return;
      }
      const textarea = composerWrapRef.current?.querySelector("textarea") as HTMLTextAreaElement | null;
      if (!textarea) return;
      if (e.target === textarea) {
        if (!focusTranscriptSection()) return;
      } else if (transcriptScrollEl && e.target === transcriptScrollEl) {
        if (!focusComposerTextarea()) return;
      } else {
        return;
      }
      e.preventDefault();
      e.stopImmediatePropagation();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [
    activeTab,
    focusComposerTextarea,
    focusTranscriptSection,
    mcpOpen,
    mentionOpen,
    slashOpen,
    transcriptScrollEl,
    visible,
  ]);

  // When the transcript region has focus, Home/End should target the
  // conversation ledger edges, not just the current virtualized window.
  useEffect(() => {
    if (!visible || activeTab !== "chat" || !transcriptScrollEl) return;
    const onKey = (e: KeyboardEvent) => {
      if (
        e.isComposing ||
        e.altKey ||
        e.ctrlKey ||
        e.metaKey ||
        e.shiftKey ||
        e.target !== transcriptScrollEl ||
        slashOpen ||
        mentionOpen ||
        mcpOpen
      ) {
        return;
      }
      if (e.key === "Home") {
        e.preventDefault();
        e.stopImmediatePropagation();
        logChatScrollEvent("keyboard-edge-navigation", {
          surface: "session",
          sessionId: session.id,
          sessionMode: session.mode,
          key: "Home",
          targetEdge: "oldest",
          navInFlight: sdkTranscriptKeyboardNavInFlightRef.current ?? "",
          foundOldest: sdkFoundOldestRef.current,
          foundNewest: sdkFoundNewestRef.current,
          ...chatScrollElementSnapshot(transcriptScrollEl),
        });
        void scrollTranscriptToConversationStart();
      } else if (e.key === "End") {
        e.preventDefault();
        e.stopImmediatePropagation();
        logChatScrollEvent("keyboard-edge-navigation", {
          surface: "session",
          sessionId: session.id,
          sessionMode: session.mode,
          key: "End",
          targetEdge: "newest",
          navInFlight: sdkTranscriptKeyboardNavInFlightRef.current ?? "",
          foundOldest: sdkFoundOldestRef.current,
          foundNewest: sdkFoundNewestRef.current,
          ...chatScrollElementSnapshot(transcriptScrollEl),
        });
        void scrollTranscriptToConversationEnd();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [
    activeTab,
    mcpOpen,
    mentionOpen,
    session.id,
    session.mode,
    sessionScope,
    slashOpen,
    transcriptScrollEl,
    visible,
  ]);

  // User-initiated scroll-up gestures transition the navigation mode
  // from live-tail to historical-anchor. This is the only DOM-side
  // input to mode transitions; intentionally NO continuous DOM-distance
  // measurement of the scroll container is consulted (the retired bug
  // class read scrollHeight/scrollTop mid-followOutput-smooth-scroll
  // and latched the mode wrongly). Listening on input events is what
  // captures *user intent* without confusing it with programmatic
  // scroll catch-up.
  //
  // We gate on the current mode being live-tail before dispatching;
  // gestures while already in historical-anchor are no-ops (the user
  // is already where they're trying to go). That gate keeps the
  // emitted telemetry stream meaningful — every fire represents a
  // real leaving-live-tail intent.
  useEffect(() => {
    if (!visible || activeTab !== "chat" || !transcriptScrollEl) return;
    const target = transcriptScrollEl;
    const dispatchScrollUp = () => {
      if (navigationModeRef.current !== "live-tail") return;
      dispatchNavigationMode("user-scroll-up");
    };
    const onWheel = (e: WheelEvent) => {
      if (e.deltaY < 0) dispatchScrollUp();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.altKey || e.ctrlKey || e.metaKey) return;
      if (e.key === "ArrowUp" || e.key === "PageUp") dispatchScrollUp();
    };
    let touchStartY: number | null = null;
    const onTouchStart = (e: TouchEvent) => {
      touchStartY = e.touches[0]?.clientY ?? null;
    };
    const onTouchMove = (e: TouchEvent) => {
      if (touchStartY === null) return;
      const currentY = e.touches[0]?.clientY;
      if (currentY === undefined) return;
      // 4 px keeps tap-and-release out of the scroll-intent signal
      // while still capturing the start of any real swipe-down (which
      // scrolls the content up). This is a one-shot intent detector
      // per touch sequence, not a continuous-position threshold.
      if (currentY - touchStartY > 4) dispatchScrollUp();
    };
    target.addEventListener("wheel", onWheel, { passive: true });
    target.addEventListener("keydown", onKey);
    target.addEventListener("touchstart", onTouchStart, { passive: true });
    target.addEventListener("touchmove", onTouchMove, { passive: true });
    return () => {
      target.removeEventListener("wheel", onWheel);
      target.removeEventListener("keydown", onKey);
      target.removeEventListener("touchstart", onTouchStart);
      target.removeEventListener("touchmove", onTouchMove);
    };
  }, [activeTab, transcriptScrollEl, visible]);

  // Esc-to-abort while streaming. Mirrors cloudcli's "ESC" kbd hint on the
  // Stop pill. Capture phase so it fires even if focus is in the textarea.
  // Skips when a palette is open — Esc closes the palette in those cases
  // (handled below).
  useEffect(() => {
    if (!visible || !running) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !slashOpen && !mentionOpen && !mcpOpen) {
        if (e.target instanceof Element && e.target.closest(".run-header-name-input")) return;
        e.preventDefault();
        cancelRun();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [mcpOpen, mentionOpen, running, slashOpen, visible]);

  // Slash- + mention-palette typing detection + composer-text mirror.
  // Listens at the composer wrap; reads the textarea's value + cursor on
  // every event to recompute the active trigger context AND keep
  // `composerText` in sync. The mirror drives hint fade + clear-X
  // visibility without making the textarea controlled (which would
  // conflict with PromptInput's form).
  useEffect(() => {
    const wrap = composerWrapRef.current;
    if (!wrap) return;
    const recompute = () => {
      const ta = wrap.querySelector("textarea") as HTMLTextAreaElement | null;
      if (!ta) {
        setSlashOpen(false);
        setMentionOpen(false);
        setMcpOpen(false);
        setComposerText("");
        return;
      }
      setComposerText(ta.value);
      const slash = findSlashContext(ta);
      const mention = findMentionContext(ta);
      if (slash) {
        slashManualOpenRef.current = false;
        setSlashOpen(true);
        setMcpOpen(false);
        setSlashQuery((prev) => {
          if (prev !== slash.query) setSlashIndex(0);
          return slash.query;
        });
      } else if (!slashManualOpenRef.current) {
        setSlashOpen(false);
      }
      if (mention) {
        setMentionOpen(true);
        setMcpOpen(false);
        setMentionQuery((prev) => {
          if (prev !== mention.query) setMentionIndex(0);
          return mention.query;
        });
        // Lazy-load the workspace walk on first `@` keystroke.
        if (!mentionLoadedRef.current) {
          mentionLoadedRef.current = true;
          void authedFetch(`/api/sessions/${session.id}/files/walk`)
            .then(async (res) => {
              if (!res.ok) throw new Error(`${res.status}`);
              return (await res.json()) as { paths: string[] };
            })
            .then((b) => setMentionPaths(b.paths))
            .catch(() => {
              mentionLoadedRef.current = false; // allow retry
            });
        }
      } else {
        setMentionOpen(false);
      }
    };
    wrap.addEventListener("input", recompute);
    wrap.addEventListener("click", recompute);
    // keyup is the trigger for cursor-position changes via arrow keys
    // (e.g. left/right out of slash context). Filter to ignore the
    // arrow keys we use for palette nav so they don't trigger a
    // recompute mid-navigation.
    const onKeyUp = (e: Event) => {
      const ke = e as KeyboardEvent;
      if (
        ke.key === "ArrowDown" ||
        ke.key === "ArrowUp" ||
        ke.key === "Tab" ||
        ke.key === "Enter"
      ) {
        return;
      }
      recompute();
    };
    wrap.addEventListener("keyup", onKeyUp);
    return () => {
      wrap.removeEventListener("input", recompute);
      wrap.removeEventListener("click", recompute);
      wrap.removeEventListener("keyup", onKeyUp);
    };
  }, [activeTab]);

  // Slash-palette keyboard nav. Up/Down moves selection, Enter/Tab applies,
  // Esc closes. Capture phase + stopPropagation so the textarea's own
  // Enter-to-submit doesn't fire while the palette is open.
  useEffect(() => {
    if (!slashOpen) return;
    const onKey = (e: KeyboardEvent) => {
      const filtered = filterSlashCommands(slashCommands, slashQuery);
      if (e.key === "ArrowDown" && !e.shiftKey) {
        e.preventDefault();
        e.stopPropagation();
        setSlashIndex((i) => (filtered.length ? (i + 1) % filtered.length : 0));
      } else if (e.key === "ArrowUp" && !e.shiftKey) {
        e.preventDefault();
        e.stopPropagation();
        setSlashIndex((i) =>
          filtered.length ? (i - 1 + filtered.length) % filtered.length : 0,
        );
      } else if (e.key === "Enter" || e.key === "Tab") {
        if (!filtered.length) return;
        e.preventDefault();
        e.stopPropagation();
        applySlashCommand(filtered[Math.min(slashIndex, filtered.length - 1)].name);
        slashManualOpenRef.current = false;
        setSlashOpen(false);
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        slashManualOpenRef.current = false;
        setSlashOpen(false);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [slashOpen, slashQuery, slashIndex, slashCommands]);

  // Mention-palette keyboard nav (mirror of slash).
  useEffect(() => {
    if (!mentionOpen) return;
    const onKey = (e: KeyboardEvent) => {
      const filtered = mentionPaths
        ? filterMentionPaths(mentionPaths, mentionQuery)
        : [];
      if (e.key === "ArrowDown" && !e.shiftKey) {
        e.preventDefault();
        e.stopPropagation();
        setMentionIndex((i) => (filtered.length ? (i + 1) % filtered.length : 0));
      } else if (e.key === "ArrowUp" && !e.shiftKey) {
        e.preventDefault();
        e.stopPropagation();
        setMentionIndex((i) =>
          filtered.length ? (i - 1 + filtered.length) % filtered.length : 0,
        );
      } else if (e.key === "Enter" || e.key === "Tab") {
        if (!filtered.length) return;
        e.preventDefault();
        e.stopPropagation();
        applyMentionPath(filtered[Math.min(mentionIndex, filtered.length - 1)]);
        setMentionOpen(false);
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        setMentionOpen(false);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [mentionOpen, mentionQuery, mentionIndex, mentionPaths]);

  useEffect(() => {
    if (!mcpOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        setMcpOpen(false);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [mcpOpen]);

  function applyMentionPath(relPath: string) {
    const wrap = composerWrapRef.current;
    const ta = wrap?.querySelector("textarea") as HTMLTextAreaElement | null;
    if (!ta) return;
    const ctx = findMentionContext(ta);
    const start = ctx?.start ?? ta.selectionStart ?? 0;
    const cursor = ta.selectionStart ?? start;
    const before = ta.value.slice(0, start);
    const after = ta.value.slice(cursor);
    // Insert the absolute /workspace path so the agent can load it without
    // re-resolving.
    const insert = `/workspace/${relPath} `;
    const newValue = before + insert + after;
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLTextAreaElement.prototype,
      "value",
    )?.set;
    setter?.call(ta, newValue);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    const newPos = start + insert.length;
    ta.setSelectionRange(newPos, newPos);
    ta.focus();
  }

  function getComposerValue(): string {
    const wrap = composerWrapRef.current;
    const ta = wrap?.querySelector("textarea") as HTMLTextAreaElement | null;
    return ta?.value ?? composerText;
  }

  function setComposerValue(value: string) {
    const wrap = composerWrapRef.current;
    const ta = wrap?.querySelector("textarea") as HTMLTextAreaElement | null;
    if (!ta) return;
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLTextAreaElement.prototype,
      "value",
    )?.set;
    setter?.call(ta, value);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    ta.focus();
    const cursor = value.length;
    ta.setSelectionRange(cursor, cursor);
  }

  function appendQuotedMessage(text: string, style: QuoteStyle) {
    const quoted = quoteMessageText(text, style);
    const next = composerText.trim().length > 0 ? `${composerText}\n\n${quoted}` : quoted;
    setComposerValue(next);
  }

  function applySlashCommand(name: string) {
    const wrap = composerWrapRef.current;
    const ta = wrap?.querySelector("textarea") as HTMLTextAreaElement | null;
    if (!ta) return;
    const ctx = findSlashContext(ta);
    const start = ctx?.start ?? ta.selectionStart ?? 0;
    const cursor = ta.selectionStart ?? start;
    const before = ta.value.slice(0, start);
    const after = ta.value.slice(cursor);
    const insert = name + " ";
    const newValue = before + insert + after;
    // Native setter so React-controlled / form-managed textareas pick up
    // the change instead of dropping it on the next render.
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLTextAreaElement.prototype,
      "value",
    )?.set;
    setter?.call(ta, newValue);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    const newPos = start + insert.length;
    ta.setSelectionRange(newPos, newPos);
    ta.focus();
  }

  function setActiveTool(toolName: string | null, toolUseId: string | null = null) {
    if (isScheduleWakeupToolName(toolName ?? undefined)) {
      scheduledWakeupRef.current = true;
    }
    activeToolUseIdRef.current = toolName ? toolUseId : null;
  }

  function openSlashCommandMenu() {
    if (slashOpen && slashManualOpenRef.current) {
      slashManualOpenRef.current = false;
      setSlashOpen(false);
      return;
    }
    slashManualOpenRef.current = true;
    setMcpOpen(false);
    setMentionOpen(false);
    setSlashQuery("");
    setSlashIndex(0);
    setSlashOpen(true);
    const ta = composerWrapRef.current?.querySelector("textarea") as HTMLTextAreaElement | null;
    ta?.focus();
  }

  function newRunId() {
    const cryptoObj = window.crypto;
    if (cryptoObj?.randomUUID) return cryptoObj.randomUUID();
    return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
  }

  function cancelRun() {
    // Stop is a durable boundary: cancelRun POSTs and the transcript row /
    // activity streams reflect the resulting stopping or terminal state.
    if (runStatus === "stopping" || runStatus === "done") return;
    const interruptTarget = activeInterruptTargetRef.current;
    if (!interruptTarget) {
      const id = nextEntryId("sdk-interrupt-error");
      appendSdkRealtimeEntries(
        markLocalEntries(
          appendMeta([], id, "Stop failed", "No active turn is available to stop.", "error"),
          id,
        ),
      );
      return;
    }
    void requestSdkInterrupt(interruptTarget);
  }

  async function requestSdkInterrupt(turnID: string): Promise<void> {
    try {
      await interruptSdkTurn(turnID);
    } catch (err) {
      const id = nextEntryId("sdk-interrupt-error");
      appendSdkRealtimeEntries(
        markLocalEntries(
          appendMeta([], id, "Stop failed", err instanceof Error ? err.message : String(err), "error"),
          id,
        ),
      );
    }
  }

  async function interruptSdkTurn(turnID: string): Promise<void> {
    const res = await authedFetch(
      `/api/sessions/${encodeURIComponent(session.id)}/turns/${encodeURIComponent(turnID)}/interrupt`,
      { method: "POST" },
    );
    if (!res.ok) {
      let detail = `interrupt failed: ${res.status}`;
      try {
        const body = await res.json();
        if (typeof body?.detail === "string") detail = body.detail;
      } catch {
        // Keep the status-only detail when the response is not JSON.
      }
      throw new Error(detail);
    }
  }

  async function stopBackgroundTask(entry: TranscriptEntry): Promise<void> {
    const taskID = entry.taskId?.trim();
    const turnID = entry.turnId?.trim();
    if (!taskID || !turnID) {
      throw new Error("background task stop target is not available");
    }
    const body = {
      turn_id: turnID,
      timeline_id: entry.id,
      provider_item_id: entry.providerItemId,
      process_id: entry.taskProcessId ?? entry.taskId,
    };
    const res = await authedFetch(
      `/api/sessions/${encodeURIComponent(session.id)}/background-tasks/${encodeURIComponent(taskID)}/stop`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      },
    );
    if (!res.ok) {
      let detail = `background task stop failed: ${res.status}`;
      try {
        const data = await res.json();
        if (typeof data?.detail === "string") detail = data.detail;
      } catch {
        // Keep the status-only detail when the response is not JSON.
      }
      throw new Error(detail);
    }
  }

  async function requestBackgroundTaskStop(entry: TranscriptEntry): Promise<void> {
    try {
      await stopBackgroundTask(entry);
    } catch (err) {
      const id = nextEntryId("background-stop-error");
      appendSdkRealtimeEntries(
        markLocalEntries(
          appendMeta([], id, "Stop failed", err instanceof Error ? err.message : String(err), "error"),
          id,
        ),
      );
    }
  }

  function stopBackgroundActivity(entry: TranscriptEntry) {
    if (isDetachedShellCandidateEntry(entry)) return;
    if (isRunningShellInvocationEntry(entry)) {
      const turnID = entry.turnId?.trim();
      if (!turnID) {
        const id = nextEntryId("background-stop-error");
        appendSdkRealtimeEntries(
          markLocalEntries(
            appendMeta([], id, "Stop failed", "No active turn is available to stop.", "error"),
            id,
          ),
        );
        return;
      }
      void requestSdkInterrupt(turnID);
      return;
    }
    if (isBackgroundTaskEntry(entry)) {
      void requestBackgroundTaskStop(entry);
    }
  }

  function handleSubmit(message: PromptInputMessage) {
    const trimmed = message.text.trim();
    if (!trimmed || session.status !== "Active") return;
    // Wait until all attachments have finished uploading. If any errored
    // out, surface it but still let the run go ahead with what's ready.
    const composed = composePromptWithAttachments(trimmed);
    if (composed == null) return;
    const run = currentRunRef.current;
    const terminal = run
      ? terminalResultForTurn(sdkServerProjectedEntriesRef.current, run.turnId)
      : null;
    const submitDecision = decideFollowupSubmit({
      running,
      durableRunStatus:
        runStatus === "stopping"
          ? "stopping"
          : running
            ? "streaming"
            : "ready",
      hasLocalRun: run !== null,
      localRunHasDurableTerminal: terminal !== null,
    });
    if (submitDecision.action === "queue") {
      // Queue message to send once the current run finishes. PromptInput
      // clears the textarea before this callback returns, so the visible
      // queued-message list is the user's confirmation that the submit stuck.
      setQueuedMessages((prev) => [
        ...prev,
        {
          id: nextQueuedMessageId(),
          text: composed.prompt,
          displayText: composed.displayText,
          displayAttachments: composed.displayAttachments,
        },
      ]);
      return;
    }
    if (submitDecision.staleReason) {
      logSessionEventStreamEvent("stale_running_blocked_submit", {
        sessionMode: session.mode,
      });
      if (run && terminal) {
        finalizeSdkRun(run, terminal, { refreshHistory: false });
      } else {
        currentRunRef.current = null;
        setRunning(false);
        setRunStatus((prev) => (prev === "running" || prev === "stopping" ? "done" : prev));
        setSdkConnectionState("idle");
      }
    }
    startRun(composed.prompt, composed.displayText, undefined, composed.displayAttachments);
  }

  function composePromptWithAttachments(trimmed: string): ComposedAttachmentPrompt | null {
    const ready = attachments.filter((a) => a.status === "ready");
    const stillUploading = attachments.some((a) => a.status === "uploading");
    if (stillUploading) return null;
    const prompt = composeAttachmentPathText(trimmed, ready.map((a) => a.absPath));
    const displayText = composeAttachmentDisplayText(trimmed, ready);
    const displayAttachments = messageAttachmentDisplays(ready);
    // Clear attachment state once they've been baked into the run (or queue).
    setAttachments((prev) => {
      for (const a of prev) {
        if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
      }
      return [];
    });
    return { prompt, displayText, displayAttachments };
  }

  function submitSkillInvocation(skillName: string, promptText = "") {
    const displayText = skillInvocationTitle(skillName);
    const trigger = skillTrigger(isClaude, skillName);
    const trimmedPrompt = promptText.trim();
    const skillPrompt = trimmedPrompt ? `${trigger}\n\n${trimmedPrompt}` : trigger;
    if (running) {
      setQueuedMessages((prev) => [
        ...prev,
        { id: nextQueuedMessageId(), text: skillPrompt, displayText, skillName },
      ]);
      return;
    }
    startRun(skillPrompt, displayText, skillName);
  }

  async function enqueueSdkTurn(run: NonNullable<typeof currentRunRef.current>): Promise<void> {
    const res = await authedFetch(`/api/sessions/${session.id}/turns`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        client_nonce: run.id,
        prompt: run.prompt,
        ...(!run.skillName && run.displayText && run.displayText !== run.prompt
          ? { display_text: run.displayText }
          : {}),
        ...(!run.skillName && run.displayAttachments.length > 0
          ? { display_attachments: attachmentPayloadsForApi(run.displayAttachments) }
          : {}),
        model: run.model,
        // effort is forwarded only when set — the backend's
        // validateEffort treats empty string as "use the runner's
        // baked-in default" rather than rejecting. Sending "" on
        // every turn would still work but obscures the intent in
        // request logs; the omit-when-empty keeps the wire shape
        // clean for non-Claude sessions.
        ...(run.effort ? { effort: run.effort } : {}),
        permission_mode: run.permissionMode,
        skill_name: run.skillName,
        follow_up: run.followUp,
      }),
    });
    if (!res.ok) {
      let detail = `submit failed: ${res.status}`;
      try {
        const body = await res.json();
        if (typeof body?.detail === "string") detail = body.detail;
      } catch {
        // Keep the status-only detail when the response is not JSON.
      }
      throw new Error(detail);
    }
    const body = (await res.json().catch(() => null)) as { turn_id?: unknown } | null;
    if (typeof body?.turn_id === "string" && body.turn_id.trim()) {
      run.turnId = body.turn_id.trim();
    }
  }

  async function markTestState(state: TestState) {
    setTestState(state);
    setRolloutState(null);
    onSessionPatch(session.id, { test_state: state, rollout_state: null });
    const res = await authedFetch(`/api/sessions/${session.id}/test-state`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(state),
    });
    if (!res.ok) {
      throw new Error(`test state update failed: ${res.status}`);
    }
    const updated: Session = normalizeSession(await res.json());
    const nextState = updated.test_state ?? null;
    setTestState(nextState);
    setRolloutState(null);
    onSessionPatch(session.id, { test_state: nextState, rollout_state: null });
  }

  async function markRolloutState(state: RolloutState) {
    setRolloutState(state);
    setTestState(null);
    onSessionPatch(session.id, { test_state: null, rollout_state: state });
    const res = await authedFetch(`/api/sessions/${session.id}/rollout-state`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(state),
    });
    if (!res.ok) {
      throw new Error(`rollout state update failed: ${res.status}`);
    }
    const updated: Session = normalizeSession(await res.json());
    const nextState = updated.rollout_state ?? null;
    setTestState(null);
    setRolloutState(nextState);
    onSessionPatch(session.id, { test_state: null, rollout_state: nextState });
  }

  function startTestSkill() {
    if (session.status !== "Active") return;
    const promptText = getComposerValue().trim();
    const composed = composePromptWithAttachments(promptText);
    if (composed == null) return;
    setComposerValue("");
    void markTestState({ active: true }).catch((e) => {
      setEntries((prev) =>
        appendMeta(prev, nextEntryId("test-state-error"), "test state update failed", String(e), "error"),
      );
    });
    submitSkillInvocation("test", composed.prompt);
  }

  function startGuiRollout() {
    if (session.status !== "Active") return;
    void markRolloutState({ active: true }).catch((e) => {
      setEntries((prev) =>
        appendMeta(prev, nextEntryId("rollout-state-error"), "rollout state update failed", String(e), "error"),
      );
    });
    submitSkillInvocation("rollout");
  }

  function startRun(
    trimmed: string,
    displayText = trimmed,
    skillName?: string,
    displayAttachments: MessageAttachmentDisplay[] = [],
  ) {
    primeTurnCompleteSound();
    const followUp = entries.length > 0;
    const turnStart = Date.now();
    const id = newRunId();
    const run = {
      id,
      turnId: turnIdForBrowserClientNonce(id),
      prompt: trimmed,
      displayText,
      displayAttachments,
      skillName,
      followUp,
      model: isClaude || isCodex
        ? (selectedModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID ? "" : selectedModelId)
        : "",
      effort: isClaude || isCodex ? selectedEffortId : "",
      permissionMode: composerMode,
      turnStart,
      submitAccepted: false,
    };
    currentRunRef.current = run;
    activeInterruptTargetRef.current = run.turnId;
    terminalCorrelationReportedRef.current = new Set();
    queuedFollowupBlockedReportedRef.current = null;
    const optimisticTime = nowIso();
    if (skillName) {
      appendSdkRealtimeEntries(
        markLocalEntries(
          appendSkillInvocation([], skillName, stripSkillTrigger(skillName, trimmed), optimisticTime),
          run.id,
        ),
      );
    } else {
      const userEntry = {
        id: nextEntryId("user"),
        kind: "message",
        role: "user",
        text: displayText,
        attachments: displayAttachments,
        time: optimisticTime,
      } as TranscriptEntry;
      appendSdkRealtimeEntries(markLocalEntries([userEntry], run.id));
    }
    if (visible) {
      dispatchNavigationMode("submit");
      requestScrollToLatest("auto", "submit");
    }
    setRunStatus("running");
    setRunning(true);
    setActiveTool(null);
    // The form clears the textarea internally on submit but doesn't
    // always fire an input event in time, so my mirror lingers and the
    // X-clear button stays visible. Force the mirror clean.
    setComposerText("");
    void enqueueSdkTurn(run)
      .then(() => {
        if (currentRunRef.current?.id !== run.id) return;
        run.submitAccepted = true;
        setSdkConnectionState("connected");
      })
      .catch((err) => {
        if (currentRunRef.current?.id !== run.id) return;
        currentRunRef.current = null;
        setRunning(false);
        setRunStatus("error");
        setSdkConnectionState("idle");
        const id = nextEntryId("sdk-submit-error");
        appendSdkRealtimeEntries(
          markLocalEntries(
            appendMeta([], id, "Submit failed", err instanceof Error ? err.message : String(err), "error"),
            id,
          ),
        );
      });
  }

  // Terminal rows close the local run state only after they arrive from the
  // server-owned transcript projection.
  function finalizeSdkRun(
    run: NonNullable<typeof currentRunRef.current>,
    terminal: SdkTerminalResult,
    options: {
      refreshHistory?: boolean;
      clearRealtime?: boolean;
    } = {},
  ): void {
    if (currentRunRef.current?.id !== run.id) return;
    currentRunRef.current = null;
    const durationMs = Date.now() - run.turnStart;
    updateSdkLastAssistantDuration(durationMs);
    if (terminal.status === "done") {
      setRunStatus("done");
      // Turn-complete sound is fired by the App-level SSE consumer
      // on the always-on /api/sessions/events stream, NOT here. Per
      // docs/migration-policy.md, no parallel path: leaving a ring
      // call here would split the "your turn" signal across two
      // listeners — the bug that produced the "only rings when I
      // return to the session" regression. The activity-stream
      // listener covers this same transition for both visible and
      // background sessions.
    } else if (terminal.status === "stopped") {
      setRunStatus("done");
    } else {
      setRunStatus("error");
    }
    scheduledWakeupRef.current = false;
    setActiveTool(null);
    setRunning(false);
    setSdkConnectionState("idle");
    if (options.refreshHistory ?? false) {
      void refreshSdkRunHistory(options.clearRealtime ?? false, "terminal-refresh");
    }
  }

  function scheduleSdkEventStreamReconnect(): void {
    if (sdkEventReconnectTimerRef.current !== null) {
      window.clearTimeout(sdkEventReconnectTimerRef.current);
    }
    sdkEventReconnectTimerRef.current = window.setTimeout(() => {
      sdkEventReconnectTimerRef.current = null;
      if (!visibleRef.current) return;
      sdkEventSourceRef.current?.close();
      sdkEventSourceRef.current = null;
      void openSdkEventStream();
    }, 1000);
  }

  async function openSdkEventStream(): Promise<EventSource | null> {
    setSdkConnectionState("connecting");
    const params = new URLSearchParams();
    if (sdkTimelineCursorRef.current) {
      params.set("last_order_key", sdkTimelineCursorRef.current);
    }
    const query = params.toString();
    let source: EventSource;
    try {
      source = await authedEventSource(
        scopedSessionPathForPane(`/api/sessions/${encodeURIComponent(session.id)}/events${query ? `?${query}` : ""}`),
        { stream: "session-events", sessionId: session.id, sessionScope },
      );
    } catch {
      setSdkConnectionState("connection_lost");
      logSessionEventStreamEvent("reconnect_scheduled", { sessionMode: session.mode });
      scheduleSdkEventStreamReconnect();
      return null;
    }
    if (sessionIdRef.current !== session.id || !visibleRef.current) {
      source.close();
      return null;
    }
    sdkEventSourceRef.current = source;
    logSessionEventStreamEvent("opened", { sessionMode: session.mode });
    // Build a fresh silence watchdog for this stream. Per-open
    // lifecycle so a reconnect produces independent histogram
    // observations.
    silenceWatchdogRef.current?.stop();
    silenceWatchdogRef.current = createSilenceWatchdog({
      sessionMode: session.mode,
      isRunning: () => runningRef.current,
    });
    silenceWatchdogRef.current.reset();
    source.addEventListener("ready", () => {
      setSdkConnectionState("connected");
      silenceWatchdogRef.current?.reset();
      logSessionEventStreamEvent("ready", { sessionMode: session.mode });
    });
    source.addEventListener("transcript-rows", (event) => {
      const message = event as MessageEvent;
      let parsed: unknown;
      try {
        parsed = JSON.parse(String(message.data));
      } catch {
        return;
      }
      if (!isJsonObject(parsed)) return;
      const projectedRows = normalizeProjectedTranscriptEntries(
        Array.isArray(parsed.rows) ? parsed.rows : [],
      );
      if (projectedRows.length === 0) return;
      logSessionEventStreamEvent("transcript_rows_received", {
        sessionMode: session.mode,
        eventType: "transcript_rows",
      });
      noteTankEvent();
      silenceWatchdogRef.current?.reset();
      applySdkTranscriptRows(
        projectedRows,
        typeof parsed.order_key === "string" ? parsed.order_key : "",
      );
    });
    source.addEventListener("resync_required", () => {
      source.close();
      if (sdkEventSourceRef.current === source) sdkEventSourceRef.current = null;
      setSdkConnectionState("resyncing");
      sdkTimelineCursorRef.current = null;
      logSessionEventStreamEvent("resync_required", { sessionMode: session.mode });
      void refreshSdkRunHistoryResult(true, "resync").finally(() => {
        if (sessionIdRef.current !== session.id) return;
        sdkEventSourceRef.current?.close();
        sdkEventSourceRef.current = null;
        void openSdkEventStream();
      });
    });
    source.addEventListener("stream-error", () => {
      source.close();
      if (sdkEventSourceRef.current === source) sdkEventSourceRef.current = null;
      setSdkConnectionState("connection_lost");
      logSessionEventStreamEvent("stream_error", { sessionMode: session.mode });
      scheduleSdkEventStreamReconnect();
    });
    source.onerror = () => {
      source.close();
      if (sdkEventSourceRef.current === source) sdkEventSourceRef.current = null;
      setSdkConnectionState("connection_lost");
      logSessionEventStreamEvent("closed_error", { sessionMode: session.mode });
      scheduleSdkEventStreamReconnect();
    };
    return source;
  }

  useEffect(() => {
    if (!visible || !CHAT_MODES.has(session.mode) || !historyBootstrapped) return;
    // Long-task correlation: switching active sessions triggers a
    // reducer reset + transcript bootstrap + scroll rewire. Attribute
    // any block in that window to the switch via sinceSessionSwitchMs,
    // and label any block that fires now by the session's mode.
    setActiveSessionMode(session.mode);
    noteSessionSwitch();
    sdkEventSourceRef.current?.close();
    sdkEventSourceRef.current = null;
    void openSdkEventStream();
    return () => {
      if (sdkEventReconnectTimerRef.current !== null) {
        window.clearTimeout(sdkEventReconnectTimerRef.current);
        sdkEventReconnectTimerRef.current = null;
      }
      sdkEventSourceRef.current?.close();
      sdkEventSourceRef.current = null;
      // Telemetry: closed by effect cleanup (session change, pane
      // hidden, history reset). Pairs with closed_error from
      // source.onerror so an operator can distinguish "we tore
      // down" from "the socket died."
      if (silenceWatchdogRef.current) {
        silenceWatchdogRef.current.stop();
        silenceWatchdogRef.current = null;
        logSessionEventStreamEvent("closed_unmount", { sessionMode: session.mode });
      }
    };
  // openSdkEventStream closes over the current session cursor and reducer state.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [historyBootstrapped, visible, session.id, session.mode, sessionScope]);

  const submitStatus =
    runStatus === "running" || runStatus === "stopping"
      ? "streaming"
      : runStatus === "error"
        ? "error"
        : undefined;

  const sessionAvatar = useMemo(
    () => getSessionAvatarByID(session.agent_avatar_id),
    [avatarCatalogVersion, session.agent_avatar_id],
  );
  const systemAvatar = useMemo(
    () => getSystemAvatarByID(session.system_avatar_id),
    [avatarCatalogVersion, session.system_avatar_id],
  );
  useEffect(() => {
    if (!visible) return;
    recordSessionListDebugEvent({
      kind: "active-avatar-render",
      source: "ChatPane",
      session_id: session.id,
      row: {
        ...sessionListDebugRow(session),
        rendered_avatar_id: sessionAvatar?.id ?? null,
        rendered_avatar_src: sessionAvatar?.src ?? null,
        rendered_avatar_custom: sessionAvatar?.custom === true,
      },
      detail: {
        avatar_catalog_version: avatarCatalogVersion,
        session_avatar_id: sessionAvatar?.id ?? null,
        session_avatar_src: sessionAvatar?.src ?? null,
        session_avatar_custom: sessionAvatar?.custom === true,
        system_avatar_id: systemAvatar?.id ?? null,
        system_avatar_src: systemAvatar?.src ?? null,
        system_avatar_custom: systemAvatar?.custom === true,
      },
    });
  }, [
    visible,
    avatarCatalogVersion,
    session,
    sessionAvatar?.id,
    sessionAvatar?.src,
    sessionAvatar?.custom,
    systemAvatar?.id,
    systemAvatar?.src,
    systemAvatar?.custom,
  ]);
  const renderedEntries = entries;
  const backgroundTaskEntries = useMemo(
    () => renderedEntries.filter(isBackgroundTaskEntry),
    [renderedEntries],
  );
  const runningShellInvocationEntries = useMemo(
    () => renderedEntries.filter(isRunningShellInvocationEntry),
    [renderedEntries],
  );
  const detachedShellEntries = useMemo(
    () => renderedEntries.filter(isDetachedShellCandidateEntry),
    [renderedEntries],
  );
  const activeBackgroundEntries = useMemo(
    () => [
      ...backgroundTaskEntries.filter(isBackgroundTaskRunning),
      ...runningShellInvocationEntries,
    ],
    [backgroundTaskEntries, runningShellInvocationEntries],
  );
  const backgroundLedgerEntries = useMemo(
    () => [
      ...activeBackgroundEntries,
      ...detachedShellEntries,
    ],
    [activeBackgroundEntries, detachedShellEntries],
  );
  const turnViewItems = useMemo(
    () => buildTurnViewItems(renderedEntries, renderedActiveTurnId, activityEntriesByTurn),
    [activityEntriesByTurn, renderedActiveTurnId, renderedEntries],
  );
  const turnsAvailable = turnViewItems.length > 0;
  const activeTurnViewId = turnViewItems.find((turn) => turn.active)?.turnId ?? null;
  const latestTurnId = turnViewItems[turnViewItems.length - 1]?.turnId ?? null;
  const selectedTurnExists =
    selectedTurnId != null && turnViewItems.some((turn) => turn.turnId === selectedTurnId);
  const effectiveSelectedTurnId = selectedTurnExists ? selectedTurnId : latestTurnId;
  const routedSelectedTurnId =
    activeTab === "turns" ? (pendingRouteTurnId ?? effectiveSelectedTurnId) : null;
  const ensureTurnActivityLoaded = useCallback((turnId: string) => {
    const trimmedTurnId = turnId.trim();
    if (!trimmedTurnId) return;
    if (activityEntriesByTurn[trimmedTurnId]) return;
    if (loadingActivityTurns[trimmedTurnId]) return;
    setLoadingActivityTurns((prev) => ({ ...prev, [trimmedTurnId]: true }));
    void authedFetch(
      scopedSessionPathForPane(
        `/api/sessions/${encodeURIComponent(session.id)}/turns/${encodeURIComponent(trimmedTurnId)}/activity`,
      ),
    )
      .then(async (res) => {
        if (!res.ok) throw new Error(`activity request failed: ${res.status}`);
        const body = (await res.json()) as { entries?: unknown[] };
        const loaded = normalizeProjectedTranscriptEntries(
          Array.isArray(body.entries) ? body.entries : [],
        );
        setActivityEntriesByTurn((prev) => ({ ...prev, [trimmedTurnId]: loaded }));
      })
      .catch(() => {
        setActivityEntriesByTurn((prev) => ({ ...prev, [trimmedTurnId]: [] }));
      })
      .finally(() => {
        setLoadingActivityTurns((prev) => ({ ...prev, [trimmedTurnId]: false }));
      });
  }, [activityEntriesByTurn, loadingActivityTurns, scopedSessionPathForPane, session.id]);
  useEffect(() => {
    if (turnViewItems.length === 0) {
      if (historyBootstrapped && selectedTurnId !== null) setSelectedTurnId(null);
      return;
    }
    if (pendingRouteTurnId) {
      const routeTurnExists = turnViewItems.some((turn) => turn.turnId === pendingRouteTurnId);
      if (routeTurnExists) {
        if (selectedTurnId !== pendingRouteTurnId) setSelectedTurnId(pendingRouteTurnId);
        setPendingRouteTurnId(null);
        return;
      }
      if (!historyBootstrapped) return;
      setPendingRouteTurnId(null);
    }
    if (!selectedTurnExists && latestTurnId) setSelectedTurnId(latestTurnId);
  }, [
    historyBootstrapped,
    latestTurnId,
    pendingRouteTurnId,
    selectedTurnExists,
    selectedTurnId,
    turnViewItems,
  ]);
  useEffect(() => {
    if (activeTab !== "turns" || turnsAvailable) return;
    if (!historyBootstrapped || pendingRouteTurnId) return;
    setActiveTab("chat");
  }, [activeTab, historyBootstrapped, pendingRouteTurnId, turnsAvailable]);
  useEffect(() => {
    if (!visible || pendingScrollMessageId) return;
    if (activeTab === "turns") {
      replaceSessionRoute(session.id, "turns", routedSelectedTurnId);
    } else {
      replaceSessionRoute(session.id, "chat");
    }
  }, [activeTab, pendingScrollMessageId, routedSelectedTurnId, session.id, visible]);
  useEffect(() => {
    if (activeTab !== "turns") return;
    if (!effectiveSelectedTurnId) return;
    ensureTurnActivityLoaded(effectiveSelectedTurnId);
  }, [activeTab, effectiveSelectedTurnId, ensureTurnActivityLoaded]);
  const openTurnPage = useCallback((turnId?: string) => {
    const target = turnId?.trim() || activeTurnViewId || effectiveSelectedTurnId || latestTurnId;
    if (target) {
      setPendingRouteTurnId(null);
      setSelectedTurnId(target);
      ensureTurnActivityLoaded(target);
    }
    setActiveTab("turns");
  }, [activeTurnViewId, effectiveSelectedTurnId, ensureTurnActivityLoaded, latestTurnId]);
  const codexBackgroundStopAvailable = isCodexRunMode(session.mode);
  const canStopBackgroundEntry = useCallback(
    (entry: TranscriptEntry) =>
      !readOnly && canStopBackgroundActivity(entry, codexBackgroundStopAvailable),
    [codexBackgroundStopAvailable, readOnly],
  );
  const openBackgroundPage = useCallback((entry?: TranscriptEntry) => {
    if (entry?.id) setSelectedBackgroundId(entry.id);
    setBackgroundView(
      entry && isDetachedShellCandidateEntry(entry)
        ? "detached"
        : activeBackgroundEntries.length === 0 && detachedShellEntries.length > 0
          ? "detached"
          : "shells",
    );
    setActiveTab("background");
  }, [activeBackgroundEntries.length, detachedShellEntries.length]);
  const currentSkillState = currentSessionSkillState(testState, rolloutState);
  const testActionActive = currentSkillState === "test";
  const rolloutActionActive = currentSkillState === "rollout";
  const appliedModelId = (session.runtime_model ?? "").trim();
  const appliedEffortId = (session.runtime_effort ?? "").trim();
  const hasAppliedRuntimeConfig = Boolean(session.runtime_configured_at);
  const configuredDisplayModelId =
    selectedModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID ? "" : selectedModelId;
  const configuredModelLabel =
    modelDisplayLabel(session.mode, configuredDisplayModelId) || "default model";
  const configuredEffortLabel = effortDisplayLabel(session.mode, selectedEffortId);
  const modelChipLabel = hasAppliedRuntimeConfig
    ? (modelDisplayLabel(session.mode, appliedModelId) || "Default model")
    : "Model pending";
  const effortChipLabel = hasAppliedRuntimeConfig
    ? effortDisplayLabel(session.mode, appliedEffortId)
    : "";
  const modelChipTitle = hasAppliedRuntimeConfig
    ? `Runtime applied: ${modelChipLabel}${effortChipLabel ? ` / ${effortChipLabel}` : ""}`
    : `Waiting for runner report. Intended: ${configuredModelLabel}${configuredEffortLabel ? ` / ${configuredEffortLabel}` : ""}`;
  const modelForContext = selectedModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID
    ? DEFAULT_CODEX_MODEL_ID
    : selectedModelId;
  const contextWindow = getContextWindow(modelForContext);

  useEffect(() => {
    if (!autoFocusComposer || !visible || activeTab !== "chat" || !ready) return;
    const activeElement = document.activeElement;
    if (
      activeElement &&
      activeElement !== document.body &&
      activeElement !== document.documentElement &&
      isTextEntryShortcutTarget(activeElement)
    ) {
      onAutoFocusComposerConsumed();
      return;
    }
    requestAnimationFrame(() => {
      if (focusComposerTextarea()) onAutoFocusComposerConsumed();
    });
  }, [
    activeTab,
    autoFocusComposer,
    focusComposerTextarea,
    onAutoFocusComposerConsumed,
    ready,
    visible,
  ]);

  useEffect(() => {
    if (!visible || activeTab !== "chat" || !pendingComposerFocusRef.current) return;
    pendingComposerFocusRef.current = false;
    requestAnimationFrame(() => {
      focusComposerTextarea();
    });
  }, [activeTab, focusComposerTextarea, visible]);

  useEffect(() => {
    if (activeTab !== "background") return;
    if (backgroundLedgerEntries.length === 0) {
      if (selectedBackgroundId !== null) setSelectedBackgroundId(null);
      return;
    }
    if (
      !selectedBackgroundId ||
      !backgroundLedgerEntries.some((entry) => entry.id === selectedBackgroundId)
    ) {
      setSelectedBackgroundId(
        backgroundView === "detached"
          ? detachedShellEntries[0]?.id ?? activeBackgroundEntries[0]?.id ?? null
          : activeBackgroundEntries[0]?.id ?? detachedShellEntries[0]?.id ?? null,
      );
    }
  }, [
    activeTab,
    activeBackgroundEntries,
    backgroundLedgerEntries,
    backgroundView,
    detachedShellEntries,
    selectedBackgroundId,
  ]);

  // `/` is a "return to prompt" shortcut when focus is anywhere except the
  // composer textarea. Once the textarea is focused, `/` keeps its normal
  // typing behavior and opens the slash-command palette through input events.
  useEffect(() => {
    if (!visible) return;
    const onKey = (e: KeyboardEvent) => {
      if (
        e.key !== "/" ||
        e.altKey ||
        e.ctrlKey ||
        e.metaKey ||
        e.shiftKey ||
        e.isComposing
      ) {
        return;
      }
      const textarea = composerWrapRef.current?.querySelector("textarea") as HTMLTextAreaElement | null;
      if (textarea && e.target === textarea) return;

      e.preventDefault();
      e.stopPropagation();
      pendingComposerFocusRef.current = true;
      if (activeTab !== "chat") {
        setActiveTab("chat");
        return;
      }
      pendingComposerFocusRef.current = false;
      requestAnimationFrame(() => {
        focusComposerTextarea();
      });
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [activeTab, focusComposerTextarea, visible]);

  const connectionLabel = sdkConnectionLabel(sdkConnectionState);
  const visibleConnectionLabel =
    visible && activeTab === "chat" ? connectionLabel : null;

  useEffect(() => {
    onConnectionLabelChange(session.id, visibleConnectionLabel);
    return () => onConnectionLabelChange(session.id, null);
  }, [onConnectionLabelChange, session.id, visibleConnectionLabel]);

  async function sendInputReply(
    entry: TranscriptEntry,
    payload: InputReplyPayload,
  ): Promise<void> {
    const turnID = entry.turnId?.trim();
    const providerItemID = entry.providerItemId?.trim();
    if (!turnID || !providerItemID) {
      throw new Error("input reply target is not available");
    }
    if (!payload.answers || Object.keys(payload.answers).length === 0) {
      throw new Error("input reply requires at least one answer");
    }
    const body: Record<string, unknown> = {
      provider_item_id: providerItemID,
      timeline_id: entry.id,
      answers: payload.answers,
    };
    if (payload.annotations && Object.keys(payload.annotations).length > 0) {
      body.annotations = payload.annotations;
    }
    const res = await authedFetch(
      `/api/sessions/${encodeURIComponent(session.id)}/turns/${encodeURIComponent(turnID)}/input-reply`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      },
    );
    if (!res.ok) {
      let detail = `input reply failed: ${res.status}`;
      try {
        const data = await res.json();
        if (typeof data?.detail === "string") detail = data.detail;
      } catch {
        // Keep the status-only detail when the response is not JSON.
      }
      throw new Error(detail);
    }
    // Patch the Turns-tab activity cache with the answer the user just
    // submitted. The Turns view's activityEntriesByTurn is a one-shot
    // fetch (loadActivityForTurn), so without this update the cached
    // AskUserQuestion entry stays at toolStatus="started" with no
    // askUserAnswers — the durable tool.approval_resolved that the
    // backend persists never makes it into the Turns render. Chat tab
    // is fine because it consumes the SSE stream and keeps refreshing
    // sdkServerProjectedEntriesRef; Turns is the regression surface.
    // We mirror the shape projectionAskUserAnswers emits, so when the
    // durable update eventually lands the entry is byte-identical.
    const projected: Record<string, AskUserQuestionAnswer> = {};
    for (const [question, labels] of Object.entries(payload.answers)) {
      if (!Array.isArray(labels) || labels.length === 0) continue;
      const ann = payload.annotations?.[question];
      const answer: AskUserQuestionAnswer = { labels };
      if (typeof ann?.preview === "string" && ann.preview) answer.preview = ann.preview;
      if (typeof ann?.notes === "string" && ann.notes) answer.notes = ann.notes;
      projected[question] = answer;
    }
    if (Object.keys(projected).length > 0) {
      setActivityEntriesByTurn((prev) => {
        const cached = prev[turnID];
        if (!cached) return prev;
        let mutated = false;
        const next = cached.map((cachedEntry) => {
          if (cachedEntry.id !== entry.id) return cachedEntry;
          mutated = true;
          return {
            ...cachedEntry,
            toolStatus: "completed",
            askUserAnswers: projected,
          } as TranscriptEntry;
        });
        return mutated ? { ...prev, [turnID]: next } : prev;
      });
    }
  }

  const toggleRunTab = (tab: Exclude<RunTab, "chat">) => {
    if (tab === "files" && !filesAvailable) return;
    setActiveTab((current) => (current === tab ? "chat" : tab));
  };
  const retryTimelineBootstrap = () => {
    historyRefreshRef.current = null;
    timelineBootstrapSourceRef.current = "history";
    timelineBootstrapClearRealtimeRef.current = false;
    timelineBootstrapScrollToLatestRef.current = !Boolean(pendingScrollMessageId?.trim());
    clearScrollToLatestRequest();
    dispatchTimelineBootstrap({
      type: "reset",
      sessionId: session.id,
      epoch: sdkWindowEpochRef.current,
    });
  };

  return (
    <RunContext.Provider
      value={{
        openWorkspacePath,
        sendInputReply: readOnly
          ? async () => {
              throw new Error("session is read-only");
            }
          : sendInputReply,
        user,
      }}
    >
    <WorkspaceShell
      style={chatFontScaleStyle}
      bodyClassName={`run-main-${runStatus}`}
      bodyRef={transcriptScrollCallbackRef}
      bodyAriaLabel={activeTab === "chat" ? "Transcript" : "Workspace panel"}
      composerVisible={activeTab === "chat"}
      composerWrapRef={composerWrapRef}
      composerWrapStyle={chatFontScaleStyle}
      composerWrapClassName={dragActive ? "run-composer-wrap-drag" : ""}
      onComposerWrapDragOver={(e) => {
        if (!supportsFileAttachments) return;
        e.preventDefault();
        if (!dragActive) setDragActive(true);
      }}
      onComposerWrapDragLeave={(e) => {
        if (e.currentTarget === e.target) setDragActive(false);
      }}
      onComposerWrapDrop={(e) => {
        if (!supportsFileAttachments) return;
        e.preventDefault();
        setDragActive(false);
        handleAttachmentFiles(e.dataTransfer?.files ?? null);
      }}
      onComposerWrapPaste={(e) => {
        if (!supportsFileAttachments) return;
        const items = e.clipboardData?.items;
        if (!items) return;
        const fs: File[] = [];
        for (const it of Array.from(items)) {
          if (it.kind === "file") {
            const f = it.getAsFile();
            if (f) fs.push(f);
          }
        }
        if (fs.length > 0) {
          e.preventDefault();
          for (const f of fs) void uploadAttachment(f);
        }
      }}
      title={<WorkspaceTitleSpacer />}
      tabs={(<>
          {activeTab !== "chat" && (
            <button
              type="button"
              className="run-tab run-tab-back"
              onClick={() => setActiveTab("chat")}
              aria-label="Back to chat"
              title="Back to chat"
            >
              <ArrowLeftIcon
                className="run-tab-icon"
                strokeWidth={2.2}
                aria-hidden="true"
              />
              <span>Back</span>
            </button>
          )}
          <TurnsTab
            active={activeTab === "turns"}
            count={turnViewItems.length}
            hasActiveTurn={turnViewItems.some((turn) => turn.active)}
            disabled={!turnsAvailable}
            title={turnsAvailable ? "Turns" : "Turns are available once the agent has turn activity"}
            onOpen={() => {
              if (activeTab === "turns") setActiveTab("chat");
              else openTurnPage();
            }}
          />
          <BackgroundLedger
            entries={backgroundLedgerEntries}
            active={activeTab === "background"}
            onOpen={() => {
              if (activeTab === "background") setActiveTab("chat");
              else openBackgroundPage();
            }}
          />
          <button
            type="button"
            className={`run-tab${activeTab === "files" ? " run-tab-active" : ""}`}
            onClick={() => toggleRunTab("files")}
            aria-pressed={activeTab === "files"}
            disabled={!filesAvailable}
            title={filesTabTitle}
          >
            <FolderIcon
              className="run-tab-icon"
              strokeWidth={1.8}
              aria-hidden="true"
            />
            <span>Files</span>
          </button>
          <button
            type="button"
            className={`run-tab${activeTab === "settings" ? " run-tab-active" : ""}`}
            onClick={() => toggleRunTab("settings")}
            aria-pressed={activeTab === "settings"}
            title="Settings"
          >
            <SettingsIcon className="run-tab-icon" aria-hidden="true" />
            <span>Settings</span>
          </button>
          <button
            type="button"
            className={`run-tab${activeTab === "help" ? " run-tab-active" : ""}`}
            onClick={() => toggleRunTab("help")}
            aria-pressed={activeTab === "help"}
            title="Help"
          >
            <InfoIcon className="run-tab-icon" aria-hidden="true" />
            <span>Help</span>
          </button>
      </>)}
      body={(<>
        {activeTab === "files" ? (
          <div className="run-files">
            <div className="run-files-breadcrumb">
              <button
                type="button"
                className="run-files-crumb"
                onClick={() => {
                  setFilesPath("");
                  setSelectedFile(null);
                  setSelectedFileLine(null);
                }}
              >
                /workspace
              </button>
              {filesPath
                .split("/")
                .filter(Boolean)
                .map((seg, i, arr) => {
                  const target = arr.slice(0, i + 1).join("/");
                  return (
                    <span key={target} className="run-files-crumb-wrap">
                      <span className="run-files-crumb-sep">/</span>
                      <button
                        type="button"
                        className="run-files-crumb"
                        onClick={() => {
                          setFilesPath(target);
                          setSelectedFile(null);
                          setSelectedFileLine(null);
                        }}
                      >
                        {seg}
                      </button>
                    </span>
                  );
                })}
            </div>
            <div className="run-files-body">
              <div className="run-files-list">
                {filesPath && (
                  <button
                    type="button"
                    className="run-files-row"
                    onClick={() => {
                      setFilesPath(parentFilesPath(filesPath));
                      setSelectedFile(null);
                      setSelectedFileLine(null);
                    }}
                  >
                    <ArrowUpFromLineIcon size={14} className="run-files-row-icon" aria-hidden="true" />
                    <span className="run-files-row-name">..</span>
                  </button>
                )}
                {filesLoading && (
                  <div className="run-files-status">
                    <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                    <span>Loading…</span>
                  </div>
                )}
                {filesError && (
                  <div className="run-files-status run-files-error">
                    {filesError}
                  </div>
                )}
                {!filesLoading &&
                  !filesError &&
                  filesEntries &&
                  filesEntries.map((e) => {
                    const Icon =
                      e.type === "dir" ? FolderIcon : FileIcon;
                    return (
                      <div
                        key={e.name}
                        className={`run-files-row${
                          selectedFile?.path === joinFilesPath(filesPath, e.name)
                            ? " run-files-row-active"
                            : ""
                        }`}
                      >
                        <button
                          type="button"
                          className="run-files-row-main"
                          onClick={() => openFileEntry(e.name, e.type)}
                        >
                          <Icon
                            size={14}
                            className={`run-files-row-icon run-files-row-${e.type}`}
                            aria-hidden="true"
                          />
                          <span className="run-files-row-name">{e.name}</span>
                        </button>
                        {e.github_url ? (
                          <a
                            className="run-files-row-link"
                            href={e.github_url}
                            target="_blank"
                            rel="noreferrer"
                            title={`Open ${e.name} on GitHub`}
                            aria-label={`Open ${e.name} on GitHub`}
                          >
                            <IconGithub />
                            <ExternalLinkIcon size={11} aria-hidden="true" />
                          </a>
                        ) : e.type === "file" ? (
                          <span className="run-files-row-size">
                            {humanFileSize(e.size)}
                          </span>
                        ) : (
                          <span aria-hidden="true" />
                        )}
                      </div>
                    );
                  })}
                {!filesLoading &&
                  !filesError &&
                  filesEntries &&
                  filesEntries.length === 0 &&
                  !filesPath && (
                    <div className="run-files-status">empty workspace</div>
                  )}
              </div>
              <div className="run-files-viewer">
                {!selectedFile ? (
                  <div className="run-files-empty">
                    <FolderOpenIcon size={28} aria-hidden="true" />
                    <span>Select a file to preview</span>
                  </div>
                ) : fileContentLoading ? (
                  <div className="run-files-status">
                    <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                    <span>Loading…</span>
                  </div>
                ) : selectedFile.binary && isImagePath(selectedFile.path) ? (
                  fileRawImageLoading ? (
                    <div className="run-files-viewer-image-wrap">
                      <div className="run-files-status">
                        <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                        <span>Loading image...</span>
                      </div>
                    </div>
                  ) : fileRawImageError ? (
                    <div className="run-files-viewer-image-wrap">
                      <div className="run-files-status">
                        <AlertCircleIcon size={14} aria-hidden="true" />
                        <span>Image preview failed.</span>
                      </div>
                    </div>
                  ) : fileRawImageUrl ? (
                    <Suspense fallback={(
                      <div className="run-files-viewer-image-wrap">
                        <div className="run-files-status">
                          <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                          <span>Loading image...</span>
                        </div>
                      </div>
                    )}>
                      <FileImageViewer
                        src={fileRawImageUrl}
                        alt={selectedFile.path}
                      />
                    </Suspense>
                  ) : (
                    <div className="run-files-viewer-image-wrap" />
                  )
                ) : selectedFile.binary ? (
                  <div className="run-files-status">
                    <FileIcon size={14} aria-hidden="true" />
                    <span>
                      Binary file ({humanFileSize(selectedFile.size)}) — preview
                      not available.
                    </span>
                  </div>
                ) : (
                  <>
                    <div className="run-files-viewer-header">
                      <span className="run-files-viewer-path">
                        {selectedFile.path}
                        {selectedFileLine ? `:${selectedFileLine}` : ""}
                      </span>
                      <div className="run-files-viewer-actions">
                        {fileDraft != null && fileDraft !== selectedFile.text && (
                          <>
                            <button
                              type="button"
                              className="run-files-viewer-btn"
                              disabled={fileSaving}
                              onClick={() => {
                                setFileDraft(null);
                                setFileSaveError(null);
                              }}
                            >
                              Reset
                            </button>
                            <button
                              type="button"
                              className="run-files-viewer-btn run-files-viewer-btn-primary"
                              disabled={fileSaving}
                              onClick={() => void saveFileDraft()}
                            >
                              {fileSaving ? "Saving…" : "Save"}
                            </button>
                          </>
                        )}
                        <span className="run-files-viewer-meta">
                          {humanFileSize(selectedFile.size)}
                          {selectedFile.truncated && " · truncated"}
                        </span>
                      </div>
                    </div>
                    {fileSaveError && (
                      <div className="run-files-status run-files-error">
                        {fileSaveError}
                      </div>
                    )}
                    {/* Editable when not truncated. Truncated reads aren't
                        safe to overwrite — would silently destroy the
                        unread tail. Read-only CodeMirror view when the
                        user hasn't started editing yet (fileDraft==null);
                        switches to editable CodeMirror on first focus. */}
                    {selectedFile.truncated ? (
                      <div className="run-files-viewer-content run-files-viewer-codemirror">
                        <Suspense fallback={(
                          <div className="run-files-status">
                            <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                            <span>Loading…</span>
                          </div>
                        )}>
                          <FileCodeViewer
                            editable={false}
                            path={selectedFile.path}
                            targetLine={selectedFileLine}
                            value={selectedFile.text}
                          />
                        </Suspense>
                      </div>
                    ) : fileDraft == null ? (
                      <div
                        className="run-files-viewer-content run-files-viewer-codemirror run-files-viewer-readonly"
                        onClick={() => setFileDraft(selectedFile.text)}
                        role="button"
                        tabIndex={0}
                        onKeyDown={(e) => {
                          if (e.key === "Enter" || e.key === " ") {
                            e.preventDefault();
                            setFileDraft(selectedFile.text);
                          }
                        }}
                        title="Click to edit"
                      >
                        <Suspense fallback={(
                          <div className="run-files-status">
                            <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                            <span>Loading…</span>
                          </div>
                        )}>
                          <FileCodeViewer
                            editable={false}
                            path={selectedFile.path}
                            targetLine={selectedFileLine}
                            value={selectedFile.text}
                          />
                        </Suspense>
                      </div>
                    ) : (
                      <div className="run-files-viewer-content run-files-viewer-codemirror">
                        <Suspense fallback={(
                          <div className="run-files-status">
                            <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                            <span>Loading…</span>
                          </div>
                        )}>
                          <FileCodeViewer
                            editable
                            onChange={setFileDraft}
                            path={selectedFile.path}
                            targetLine={selectedFileLine}
                            value={fileDraft}
                          />
                        </Suspense>
                      </div>
                    )}
                  </>
                )}
              </div>
            </div>
          </div>
        ) : activeTab === "turns" ? (
          <RunTurnActivityScreen
            turns={turnViewItems}
            selectedTurnId={effectiveSelectedTurnId}
            onSelectTurn={(turnId) => {
              setPendingRouteTurnId(null);
              setSelectedTurnId(turnId);
              ensureTurnActivityLoaded(turnId);
            }}
            avatar={sessionAvatar}
            systemAvatar={systemAvatar}
            sessionId={session.id}
            showThinking={runPrefs.showThinking}
            autoExpandTools={runPrefs.autoExpandTools}
            showTimestamps={runPrefs.showTimestamps}
            showDuration={runPrefs.showDuration}
            userKey={user?.sub ?? user?.email ?? "anon"}
            loadingActivityTurns={loadingActivityTurns}
            onOpenBackgroundTask={openBackgroundPage}
          />
        ) : activeTab === "background" ? (
          <BackgroundScreen
            shellEntries={activeBackgroundEntries}
            detachedEntries={detachedShellEntries}
            view={backgroundView}
            onViewChange={setBackgroundView}
            selectedId={selectedBackgroundId}
            onSelect={setSelectedBackgroundId}
            canStopEntry={canStopBackgroundEntry}
            onStop={stopBackgroundActivity}
          />
        ) : activeTab === "settings" ? (
          <RunSettingsPanel
            runPrefs={runPrefs}
            setRunPref={setRunPref}
            soundControlId={`turn-sound-volume-${session.id}`}
            turnCompleteSoundVolumePct={turnCompleteSoundVolumePct}
            setTurnCompleteSoundVolume={setTurnCompleteSoundVolume}
            playTurnCompleteSound={playTurnCompleteSound}
            paneFontScale={paneFontScale}
            paneFontScalePct={paneFontScalePct}
            setPaneFontScale={setPaneFontScale}
            adminControls={adminControls}
          />
        ) : activeTab === "help" ? (
          <RunHelpScreen />
        ) : timelineBootstrap.status === "error" ? (
          <div className="run-empty run-transcript-state" role="alert">
            <strong>Conversation failed to load</strong>
            <span>{timelineBootstrap.error ?? "Timeline bootstrap failed."}</span>
            <button
              type="button"
              className="btn-secondary"
              onClick={retryTimelineBootstrap}
            >
              Retry
            </button>
          </div>
        ) : renderedEntries.length === 0 && timelineBootstrap.status !== "ready" ? (
          <div className="run-shell-loading" role="status" aria-live="polite">
            <Loader2Icon size={18} className="run-spin" aria-hidden="true" />
            <span>Loading conversation...</span>
          </div>
        ) : renderedEntries.length === 0 ? (
          <div className="run-empty run-transcript-state" role="status">
            <span>No messages yet.</span>
          </div>
        ) : (
          <>
            {/* Top-of-transcript pagination surface. Auto-load still fires via
                Virtuoso's startReached, and the explicit button keeps older
                history reachable if the virtualized edge callback misses.
                Keep the load button mounted while loading: replacing the
                focused button with status text makes browsers drop focus and
                can yank the transcript toward the live tail. */}
            {sdkOlderError ? (
              <div className="run-transcript-load-error" role="alert">
                <span>{sdkOlderError}</span>
                <button
                  type="button"
                  className="run-transcript-load-older"
                  onClick={() => {
                    if (!sdkLoadingOlder) void loadSdkOlderEvents();
                  }}
                  aria-disabled={sdkLoadingOlder || undefined}
                  aria-busy={sdkLoadingOlder || undefined}
                >
                  {sdkLoadingOlder ? "Loading earlier messages…" : "Retry"}
                </button>
              </div>
            ) : !sdkFoundOldest && renderedEntries.length > 0 ? (
              <button
                type="button"
                className="run-transcript-load-older"
                onClick={() => {
                  if (!sdkLoadingOlder) void loadSdkOlderEvents();
                }}
                aria-disabled={sdkLoadingOlder || undefined}
                aria-busy={sdkLoadingOlder || undefined}
                aria-live="polite"
              >
                {sdkLoadingOlder ? "Loading earlier messages…" : "Load earlier messages"}
              </button>
            ) : null}
            <RunMessages
              entries={renderedEntries}
              avatar={sessionAvatar}
              systemAvatar={systemAvatar}
              sessionId={session.id}
              sessionMode={session.mode}
              pendingScrollMessageId={pendingScrollMessageId}
              onScrollConsumed={onScrollConsumed}
              showThinking={runPrefs.showThinking}
              autoExpandTools={runPrefs.autoExpandTools}
              condenseCompletedTurns={runPrefs.condenseCompletedTurns}
              activeTurnId={renderedActiveTurnId}
              showTimestamps={runPrefs.showTimestamps}
              showDuration={runPrefs.showDuration}
              userKey={user?.sub ?? user?.email ?? "anon"}
              onQuote={readOnly ? undefined : appendQuotedMessage}
              onFork={
                readOnly
                  ? undefined
                  : (forkedEntry) =>
                      onForkMessage({
                        sourceSession: session,
                        forkedEntry,
                        model:
                          isClaude || isCodex
                            ? (selectedModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID
                                ? ""
                                : selectedModelId)
                            : "",
                        // Fork inherits the source pane's effort pick so the
                        // forked pod boots with the same reasoning depth the
                        // user had been working at.
                        effort: isClaude || isCodex ? selectedEffortId : "",
                        permissionMode: composerMode,
                      })
              }
              onOpenBackgroundTask={openBackgroundPage}
              onOpenTurn={openTurnPage}
              scrollParent={transcriptScrollEl}
              onStartReached={() => {
                void loadSdkOlderEvents();
              }}
              onAtBottomChange={handleSdkAtBottomChange}
              followLiveTail={navigationMode === "live-tail"}
              scrollToLatestSignal={
                scrollToLatestRequest.enabled ? scrollToLatestRequest.signal : 0
              }
              scrollToLatestBehavior={scrollToLatestRequest.behavior}
              scrollToLatestReason={scrollToLatestRequest.reason}
              onScrollToLatestConsumed={clearScrollToLatestRequest}
              scrollToOldestSignal={scrollToOldestSignal}
              activityEntriesByTurn={activityEntriesByTurn}
              loadingActivityTurns={loadingActivityTurns}
              onActivityOpen={ensureTurnActivityLoaded}
            />
          </>
        )}
      </>)}
      floatingBetweenBodyAndComposer={activeTab === "chat" ? (<>
      {/* Non-intrusive edge glows for transcript overflow. These keep the
          explicit jump actions while avoiding floating arrow buttons over
          avatars or message content while the navigation-mode signal is
          still being hardened. */}
      {activeTab === "chat" && renderedEntries.length > 0 && !sdkFoundOldest && navigationMode === "historical-anchor" && (
        <button
          type="button"
          className="run-transcript-edge-cue run-transcript-edge-cue-top"
          onClick={() => {
            const reachOldest = async () => {
              dispatchNavigationMode("up-button");
              await jumpSdkToOldest("button");
              setScrollToOldestSignal((value) => value + 1);
            };
            void reachOldest();
          }}
          aria-label="Scroll to beginning of conversation"
          title="Scroll to beginning of conversation"
        >
          <span className="run-transcript-edge-cue-rail" aria-hidden="true" />
        </button>
      )}

      {/* The tail cue is deliberately just an edge glow. The aria-label
          carries the exact pending count, but the visible UI stays quiet. */}
      {activeTab === "chat" && renderedEntries.length > 0 && (
        <button
          type="button"
          className={`run-transcript-edge-cue run-transcript-edge-cue-bottom${
            navigationMode === "historical-anchor" || !sdkFoundNewest ? "" : " run-transcript-edge-cue-hidden"
          }${sdkPendingTailCount > 0 ? " run-transcript-edge-cue-pending" : ""}`}
          onClick={() => {
            const reachNewest = async () => {
              dispatchNavigationMode("down-button");
              await jumpSdkToLatest("button");
              requestScrollToLatest("smooth", "manual");
            };
            void reachNewest();
          }}
          aria-label={
            sdkPendingTailCount > 0
              ? `${sdkPendingTailCount} new messages below`
              : "Scroll to latest"
          }
          title={
            sdkPendingTailCount > 0
              ? `${sdkPendingTailCount} new messages below`
              : "Scroll to latest"
          }
        >
          <span className="run-transcript-edge-cue-rail" aria-hidden="true" />
        </button>
      )}

      </>) : null}
      composerAbove={(<>
          {dragActive && (
            <div className="run-composer-drop-overlay" aria-hidden="true">
              Drop to attach
            </div>
          )}
          {attachments.length > 0 && (
            <div className="run-composer-attachments">
              {attachments.map((a) => (
                <div
                  key={a.id}
                  className={`run-composer-chip run-composer-chip-${a.status}`}
                  title={attachmentDisplayTitle(a)}
                >
                  {a.previewUrl ? (
                    <img
                      className="run-composer-chip-thumb"
                      src={a.previewUrl}
                      alt=""
                      aria-hidden="true"
                    />
                  ) : (
                    <FileIcon size={14} aria-hidden="true" />
                  )}
                  <span className="run-composer-chip-name">{a.label}</span>
                  {a.status === "uploading" && (
                    <Loader2Icon size={12} className="run-spin" aria-hidden="true" />
                  )}
                  <button
                    type="button"
                    className="run-composer-chip-remove"
                    onMouseDown={(e) => {
                      e.preventDefault();
                      removeAttachment(a.id);
                    }}
                    aria-label={`Remove ${a.label}`}
                  >
                    <XIcon size={11} aria-hidden="true" />
                  </button>
                </div>
              ))}
            </div>
          )}
          <input
            ref={fileInputRef}
            type="file"
            multiple
            style={{ display: "none" }}
            onChange={(e) => {
              handleAttachmentFiles(e.target.files);
              // Reset so the same file can be reselected later.
              e.target.value = "";
            }}
          />
          {/* Slash-command palette — opens above the composer when the
              user types `/` at a word boundary. Includes built-ins and
              SKILL.md entries discovered from the active session pod. */}
          {slashOpen && slashFiltered.length > 0 && (
            <div className="run-slash-palette" role="listbox" aria-label="Slash commands">
              <div className="run-slash-palette-label">Slash commands</div>
              {slashFiltered.map((cmd, i) => (
                <button
                  key={cmd.name}
                  type="button"
                  role="option"
                  aria-selected={i === slashIndex}
                  className={`run-slash-item${i === slashIndex ? " run-slash-item-active" : ""}`}
                  onMouseDown={(e) => {
                    // mousedown not click — click would fire after blur of
                    // the textarea, which closes the palette via the
                    // input handler.
                    e.preventDefault();
                    applySlashCommand(cmd.name);
                    slashManualOpenRef.current = false;
                    setSlashOpen(false);
                  }}
                  onMouseEnter={() => setSlashIndex(i)}
                >
                  <span className="run-slash-name">{cmd.name}</span>
                  <span className="run-slash-desc">{cmd.desc}</span>
                </button>
              ))}
            </div>
          )}
          {mentionOpen && (
            <div className="run-slash-palette" role="listbox" aria-label="File mentions">
              <div className="run-slash-palette-label">
                {mentionPaths == null ? "Loading files…" : "Files (@)"}
              </div>
              {mentionPaths != null && mentionFiltered.length === 0 && (
                <div className="run-slash-empty">no matches</div>
              )}
              {mentionFiltered.map((p, i) => (
                <button
                  key={p}
                  type="button"
                  role="option"
                  aria-selected={i === mentionIndex}
                  className={`run-slash-item${i === mentionIndex ? " run-slash-item-active" : ""}`}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    applyMentionPath(p);
                    setMentionOpen(false);
                  }}
                  onMouseEnter={() => setMentionIndex(i)}
                >
                  <span className="run-slash-name run-mention-path">
                    {p.split("/").pop()}
                  </span>
                  <span className="run-slash-desc run-mention-dir">{p}</span>
                </button>
              ))}
            </div>
          )}
          {mcpOpen && (
            <div className="run-slash-palette run-mcp-palette" role="listbox" aria-label="MCP servers">
              <div className="run-slash-palette-label">MCP servers</div>
              {mcpLoading && (
                <div className="run-slash-empty">
                  <Loader2Icon className="run-spin" size={13} aria-hidden="true" />
                  loading
                </div>
              )}
              {mcpError && (
                <div className="run-slash-empty run-mcp-error">{mcpError}</div>
              )}
              {!mcpLoading && !mcpError && mcpServers && mcpServers.length === 0 && (
                <div className="run-slash-empty">no MCP servers</div>
              )}
              {!mcpLoading && !mcpError && mcpServers?.map((server) => (
                <div className="run-mcp-menu-item" role="option" aria-selected={false} key={server.name}>
                  <span className="run-mcp-menu-top">
                    <span className="run-slash-name">{server.name}</span>
                    <span className="run-mcp-menu-transport">{server.transport}</span>
                  </span>
                  <span className="run-slash-desc run-mcp-menu-target">
                    {server.target || server.source}
                  </span>
                </div>
              ))}
            </div>
          )}
          {queuedMessages.length > 0 && (
            <div className="run-queued-followups" aria-live="polite">
              <div className="run-queued-followups-head">
                <span>Queued follow-up inputs</span>
                <span>{queuedMessages.length}</span>
              </div>
              <div className="run-queued-followups-list">
                {queuedMessages.map((message, index) => (
                  <div className="run-queued-followup" key={message.id}>
                    <div className="run-queued-followup-index">
                      {index + 1}
                    </div>
                    <div
                      className="run-queued-followup-text"
                      title={message.displayText ?? message.text}
                    >
                      {message.displayText ?? message.text}
                    </div>
                    <button
                      type="button"
                      className="run-queued-followup-action"
                      aria-label="Edit queued follow-up"
                      title="Edit queued follow-up"
                      onMouseDown={(e) => {
                        e.preventDefault();
                        setQueuedMessages((prev) =>
                          prev.filter((item) => item.id !== message.id),
                        );
                        setComposerValue(message.text);
                      }}
                    >
                      <SquarePenIcon size={13} aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className="run-queued-followup-action"
                      aria-label="Remove queued follow-up"
                      title="Remove queued follow-up"
                      onMouseDown={(e) => {
                        e.preventDefault();
                        setQueuedMessages((prev) =>
                          prev.filter((item) => item.id !== message.id),
                        );
                      }}
                    >
                      <XIcon size={13} aria-hidden="true" />
                    </button>
                  </div>
                ))}
              </div>
            </div>
          )}
      </>)}
      composer={(
        <ChatComposer
          className={`run-composer-runpane run-composer-interactive${readOnly ? " run-composer-readonly" : ""}`}
          placeholder={
            readOnly
              ? "Production sessions are read-only in this test slot"
              : RUN_COMPOSER_PLACEHOLDER
          }
          onSubmit={(args) => {
            if (readOnly) return;
            handleSubmit({ text: args.text, files: [] });
          }}
          permissionMode={composerMode}
          onPermissionModeChange={setComposerMode}
          sendByCtrlEnter={runPrefs.sendByCtrlEnter}
          hintSuffix={RUN_COMPOSER_HINT_SUFFIX}
          hintOverride={
            readOnly
              ? "Read-only production view. Switch back to this slot's sessions in Settings to send messages."
              : undefined
          }
          disabled={readOnly}
          canSubmit={!readOnly && ready}
          controlsDisabled={readOnly || !ready}
          submitStatus={submitStatus}
          onStop={cancelRun}
          isStopping={runStatus === "stopping"}
          onTextChange={setComposerText}
          toolButtons={
            <>
              {/* Image-attach — opens the hidden file input. Drag-and-drop
                    and clipboard paste are wired on the composer wrap. */}
              <button
                type="button"
                className="run-composer-icon-btn"
                aria-label="Attach files"
                title={
                  supportsFileAttachments
                    ? "Attach files"
                    : "File attachments require a session workspace"
                }
                onClick={() => fileInputRef.current?.click()}
                disabled={!supportsFileAttachments}
              >
                <ImageIcon className="run-composer-icon" aria-hidden="true" />
              </button>
              <ComposerUsageRing
                tokensUsed={tokensUsed}
                contextWindow={contextWindow}
              />
              {GUI_ROLLOUT_MODES.has(session.mode) && (
                <button
                  type="button"
                  className={`run-composer-icon-btn run-composer-action-btn run-rollout-action-btn${rolloutActionActive ? " is-active" : ""}`}
                  onClick={startGuiRollout}
                  disabled={!ready}
                  aria-label="Start rollout"
                  title={isClaude ? "Use /rollout in this run" : "Use $rollout in this run"}
                >
                  <TankIcon className="run-composer-icon" />
                </button>
              )}
              {testState?.active && testState.url ? (
                <a
                  className={`run-composer-icon-btn run-composer-action-btn run-test-action-btn is-ready${testActionActive ? " is-active" : ""}`}
                  href={testState.url}
                  target="_blank"
                  rel="noreferrer"
                  onClick={() => {
                    void markTestState({ ...testState, active: true });
                  }}
                  aria-label="Open test environment in new tab"
                  title="Open test environment in new tab"
                >
                  <FlaskConicalIcon className="run-composer-icon" aria-hidden="true" />
                  <ExternalLinkIcon className="run-test-ready-icon" aria-hidden="true" />
                </a>
              ) : (
                <button
                  type="button"
                  className={`run-composer-icon-btn run-composer-action-btn run-test-action-btn${testActionActive ? " is-active" : ""}`}
                  onClick={startTestSkill}
                  disabled={!ready}
                  aria-label="Start test skill"
                  title={testState?.active ? "Test skill is active" : "Use the test skill"}
                >
                  <FlaskConicalIcon className="run-composer-icon" aria-hidden="true" />
                </button>
              )}
              <button
                type="button"
                className="run-composer-icon-btn run-command-menu-btn"
                aria-label="Show slash commands"
                title="Show slash commands"
                onMouseDown={(e) => {
                  e.preventDefault();
                  openSlashCommandMenu();
                }}
              >
                <MessageSquareIcon className="run-composer-icon" aria-hidden="true" />
                {slashCommands.length > 0 && (
                  <span className="run-command-menu-count">
                    {slashCommands.length}
                  </span>
                )}
              </button>
              <button
                type="button"
                className="run-composer-icon-btn run-command-menu-btn"
                aria-label="Show MCP servers"
                title="Show MCP servers"
                onMouseDown={(e) => {
                  e.preventDefault();
                  setSlashOpen(false);
                  setMentionOpen(false);
                  setMcpOpen((open) => !open);
                }}
              >
                <McpIcon className="run-composer-icon" aria-hidden="true" />
                {mcpServers && mcpServers.length > 0 && (
                  <span className="run-command-menu-count">
                    {mcpServers.length}
                  </span>
                )}
              </button>
              {(isClaude || isCodex) && (
                <span
                  className={`run-model-chip${hasAppliedRuntimeConfig ? "" : " is-pending"}`}
                  title={modelChipTitle}
                  aria-label={modelChipTitle}
                >
                  <BrainIcon className="run-model-chip-icon" aria-hidden="true" />
                  <span className="run-model-chip-label">{modelChipLabel}</span>
                  {effortChipLabel && (
                    <span className="run-model-chip-effort">{effortChipLabel}</span>
                  )}
                </span>
              )}
            </>
          }
        />
      )}
    />
    </RunContext.Provider>
  );
}

function CliProcessTerminal({
  session,
  visible,
}: {
  session: Session;
  visible: boolean;
}) {
  const [processId, setProcessId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [clientToken, setClientToken] = useState<string | undefined>(() => getStoredToken() ?? undefined);
  const client = useMemo(
    () =>
      new SandboxAgent({
        baseUrl: `${location.origin}/api/sessions/${session.id}/sandbox-agent`,
        token: clientToken,
        skipHealthCheck: true,
      }),
    [clientToken, session.id],
  );

  useEffect(() => {
    const syncToken = () => setClientToken(getStoredToken() ?? undefined);
    syncToken();
    window.addEventListener(AUTH_TOKEN_UPDATED_EVENT, syncToken);
    return () => window.removeEventListener(AUTH_TOKEN_UPDATED_EVENT, syncToken);
  }, [session.id]);

  useEffect(() => {
    if (!visible) return;
    let cancelled = false;
    setError(null);
    authedFetch(`/api/sessions/${session.id}/cli-process`, { method: "POST" })
      .then(async (res) => {
        if (!res.ok) throw new Error(`CLI process create failed: ${res.status}`);
        return (await res.json()) as { process_id: string };
      })
      .then((body) => {
        if (!cancelled) {
          setClientToken(getStoredToken() ?? undefined);
          setProcessId(body.process_id);
        }
      })
      .catch((err) => {
        if (!cancelled) setError(String((err as Error).message ?? err));
      });
    return () => {
      cancelled = true;
    };
  }, [session.id, visible]);

  if (error) {
    return <div className="run-shell-error">{error}</div>;
  }
  if (!processId) {
    return (
      <div className="run-shell-loading">
        <Loader2Icon size={18} className="run-spin" aria-hidden="true" />
        <span>starting CLI...</span>
      </div>
    );
  }
  return (
    <ProcessTerminal
      client={client}
      processId={processId}
      className="run-process-terminal"
      height="100%"
      showStatusBar={false}
    />
  );
}

function CliSession({ session, visible }: { session: Session; visible: boolean }) {
  // The sidebar already shows the session title, mode badge, and status —
  // a duplicate header inside the pane is wasted vertical space and made
  // the un-sized provider icon render at intrinsic SVG dimensions
  // (the giant cloud). Let the terminal fill the pane.
  return (
    <section className="run-panel">
      <main className="run-main">
        <div className="run-shell">
          <CliProcessTerminal session={session} visible={visible} />
        </div>
      </main>
    </section>
  );
}

export function App() {
  const [user, setUser] = useState<SessionUser | null>(null);
  const [booted, setBooted] = useState(false);
  const [authError, setAuthError] = useState<string | null>(null);
  const [appConfig, setAppConfig] = useState<AppPublicConfig>({});
  const [appConfigLoaded, setAppConfigLoaded] = useState(false);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [active, setActive] = useState<string | null>(null);
  const [avatarCatalogVersion, setAvatarCatalogVersion] = useState(0);
  // Per-user run-pane prefs live at app scope so mounted GUI sessions stay in
  // sync immediately, while localStorage keeps them across reloads/windows.
  // Phase E: also persisted to the Postgres profiles row so prefs ride across
  // devices. The localStorage write stays as the offline fallback.
  const [runPrefs, setRunPrefs] = useState<RunPrefs>(() => loadRunPrefs());
  const prefsWriteTimer = useRef<number | null>(null);
  const persistRunPrefs = useCallback((prefs: RunPrefs) => {
    // 500ms debounce — sliders fire setRunPref dozens of times per drag,
    // and a single PUT at the end is enough for cross-device sync.
    if (prefsWriteTimer.current) {
      window.clearTimeout(prefsWriteTimer.current);
    }
    prefsWriteTimer.current = window.setTimeout(() => {
      void authedFetch("/api/auth/prefs", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ run_prefs: prefs }),
      }).catch(() => {
        // Best-effort — localStorage is the source of truth on this
        // device; failed sync just means other devices won't see the
        // change until the next successful write.
      });
    }, 500);
  }, []);
  function setRunPref<K extends keyof RunPrefs>(key: K, value: RunPrefs[K]) {
    setRunPrefs((p) => {
      const next = { ...p, [key]: value };
      persistRunPrefs(next);
      return next;
    });
    try {
      localStorage.setItem(RUN_PREF_PREFIX + String(key), String(value));
    } catch {
      /* ignore */
    }
  }
  const setTurnCompleteSoundVolume = useCallback((value: number) => {
    setRunPref(
      "turnCompleteSoundVolume",
      Number(clampTurnCompleteSoundVolume(value).toFixed(2)),
    );
  }, []);
  const setPaneFontScale = useCallback((value: number) => {
    setRunPref("chatFontScale", Number(clampChatFontScale(value).toFixed(2)));
  }, []);

  // Turn-complete sound lives at App scope so the always-on
  // /api/sessions/events SSE handler can ring for any session — including
  // ones whose ChatPane isn't visible. The prior per-pane setup was the
  // reason the sound only fired on returning to a session: ChatPane's
  // /api/sessions/{id}/events SSE closes when the pane is hidden, so
  // background turn-complete events never reached the per-pane listener
  // until you came back and it replayed from cursor. See
  // docs/product-inspirations.md → "Durable conversation history must be
  // replayable from the server without an open browser connection." The
  // global activity stream is the canonical "your turn" transport; chat
  // panes do not own this signal.
  const turnCompleteAudioRef = useRef<HTMLAudioElement | null>(null);
  const runPrefsRef = useRef<RunPrefs>(runPrefs);
  useEffect(() => {
    runPrefsRef.current = runPrefs;
  }, [runPrefs]);
  const getTurnCompleteAudio = useCallback((): HTMLAudioElement | null => {
    if (typeof Audio === "undefined") return null;
    if (!turnCompleteAudioRef.current) {
      const audio = new Audio(TURN_COMPLETE_SOUND_SRC);
      audio.preload = "auto";
      turnCompleteAudioRef.current = audio;
    }
    return turnCompleteAudioRef.current;
  }, []);
  // primeTurnCompleteSound MUST be called from inside a user-gesture event
  // handler (click, keypress, etc.). Browsers' autoplay policies refuse
  // audio.play() until the page has received at least one gesture; calling
  // audio.load() during a gesture marks the element as "unlocked" for the
  // rest of the page lifetime. We prime on activate() (sidebar session
  // click) and on startRun (Send button) so realistic interaction paths
  // unlock audio before any background turn completes.
  const primeTurnCompleteSound = useCallback(() => {
    const audio = getTurnCompleteAudio();
    if (!audio) return;
    audio.load();
  }, [getTurnCompleteAudio]);
  const playTurnCompleteSound = useCallback(() => {
    const prefs = runPrefsRef.current;
    if (!prefs.turnCompleteSound) return;
    const audio = getTurnCompleteAudio();
    if (!audio) return;
    audio.volume = clampTurnCompleteSoundVolume(prefs.turnCompleteSoundVolume);
    audio.currentTime = 0;
    void audio.play().catch(() => undefined);
  }, [getTurnCompleteAudio]);
  const refreshRuntimeAvatarCatalog = useCallback(async () => {
    await loadRuntimeAvatarCatalog();
    setAvatarCatalogVersion((version) => version + 1);
  }, []);
  // Cluster-health polling lives inside ClusterHealthWidget so the
  // 30s setInterval + its setState calls don't cascade re-renders
  // through the App-root tree. See SessionStats above for the same
  // pattern applied to the per-row time labels.

  // Reflect the active session in the URL so reloads land back on it.
  // Mirrors cloudcli's URL-tracking behaviour. Done as an effect rather
  // than wrapping setActive so all call sites benefit.
  useEffect(() => {
    const url = new URL(window.location.href);
    if (active) {
      const route = readSessionRouteFromPath();
      if (route?.sessionId === active && !routeHasMessageTarget()) {
        url.searchParams.delete("session");
      } else if (!routeHasMessageTarget()) {
        window.history.replaceState({}, "", sessionRouteUrl(active));
        return;
      } else {
        url.searchParams.set("session", active);
      }
    } else {
      url.searchParams.delete("session");
    }
    window.history.replaceState({}, "", url.toString());
  }, [active]);

  useEffect(() => {
    const syncRunPrefs = (event: StorageEvent) => {
      if (event.storageArea !== localStorage) return;
      if (!event.key?.startsWith(RUN_PREF_PREFIX)) return;
      setRunPrefs(loadRunPrefs());
    };
    window.addEventListener("storage", syncRunPrefs);
    return () => window.removeEventListener("storage", syncRunPrefs);
  }, []);

  // Phase E: merge server-canonical prefs in once the user resolves. If
  // the server has prefs they win (this device may be stale); if it
  // doesn't, push our local prefs up so cross-device sync stops at the
  // first-write boundary instead of perpetually empty.
  useEffect(() => {
    if (!user) return;
    const server = user.run_prefs;
    if (server && Object.keys(server).length > 0) {
      setRunPrefs((prev) => mergeServerRunPrefs(prev, server));
    } else {
      persistRunPrefs(runPrefs);
    }
    // We deliberately don't re-run when runPrefs changes — this is a
    // one-shot reconciliation per user identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user, persistRunPrefs]);
  const [closingIds, setClosingIds] = useState<Set<string>>(() => new Set());
  // Sessions stay mounted after first activation so chat state and websocket
  // runs survive switching. Unopened sessions do not initialize their panel.
  const [mounted, setMounted] = useState<Set<string>>(() => new Set());
  const [sessionActivities, setSessionActivities] = useState<Record<string, SessionActivitySummary>>({});
  // Refs mirror the latest sessions + activities state so the SSE event
  // reducer (which closes over the user-keyed useEffect) can read the
  // current value without forcing a resubscribe on every render. The
  // reducer mutates the canonical state via setState; the refs are
  // updated by the effects below.
  const sessionsRef = useRef<Session[]>([]);
  const sessionActivitiesRef = useRef<Record<string, SessionActivitySummary>>({});
  // sessionStoreRef holds the client-side reconciler for the sidebar
  // (docs/session-list-redesign.md Phase 3). It owns the row cache,
  // the tombstone set, and the cursor. React state (sessions /
  // sessionActivities) is derived from store.list() on every store
  // event; the optimistic-delete handshake goes through
  // store.optimisticDelete BEFORE the DELETE API call.
  const sessionStoreRef = useRef<SessionStore>(new SessionStore());
  // Bootstrap-suppression for the turn-complete sound. With lifecycleTipRef
  // seeding the SSE cursor, the server no longer replays the full ledger
  // on cold open — only events past the tip. Still, refresh() races with
  // the SSE useEffect, so events delivered before activitySnapshotAppliedRef
  // flips true are silenced as a defense-in-depth. lastSoundedOrderKeyRef
  // dedups per-session last_order_key replays after a reconnect or resync.
  const activitySnapshotAppliedRef = useRef(false);
  const lastSoundedOrderKeyRef = useRef<Map<string, string>>(new Map());
  const activeRef = useRef<string | null>(null);
  const [profileMenuOpen, setProfileMenuOpen] = useState(false);
  const [defaultInteraction, setDefaultInteraction] =
    useState<SessionInteraction>(readDefaultInteraction);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [draggingSessionId, setDraggingSessionId] = useState<string | null>(null);
  const [dragOverSessionId, setDragOverSessionId] = useState<string | null>(null);
  const [defaultSessionMode, setDefaultSessionMode] =
    useState<DefaultSessionMode>(readDefaultSessionMode);
  const [homeActiveTab, setHomeActiveTab] = useState<HomeTab>("chat");
  const [sessionViewScopeOverride, setSessionViewScopeOverride] = useState(
    readInitialSessionViewScopeOverride,
  );
  // The home composer's permission-mode pick. Carries into the first turn
  // when the user types a prompt and presses Enter from the home screen,
  // so the choice they made on the launch surface persists into the live
  // session's run pane (which uses its own per-session composerMode state).
  const [homeComposerMode, setHomeComposerMode] =
    useState<RunComposerMode>("default");
  const [homeComposerText, setHomeComposerText] = useState("");
  const [homeSessionName, setHomeSessionName] = useState("");
  const [homeEditingTitle, setHomeEditingTitle] = useState(false);
  const homeSessionNameRef = useRef("");
  const homeEditingTitleRef = useRef(false);
  const homeEditingDefaultTitleRef = useRef(false);
  const homeTitleClosingRef = useRef(false);
  const [pendingCreateTitleSessionId, setPendingCreateTitleSessionId] = useState<string | null>(null);
  const pendingCreateTitleSessionIdRef = useRef<string | null>(null);
  const [editingSessionTitleId, setEditingSessionTitleId] = useState<string | null>(null);
  const [editingSessionTitleValue, setEditingSessionTitleValue] = useState("");
  const editingSessionTitleValueRef = useRef("");
  const editingSessionTitleFromFallbackRef = useRef(false);
  const editingSessionTitleClosingRef = useRef(false);
  const homeBodyRef = useRef<HTMLElement | null>(null);
  const homeComposerWrapRef = useRef<HTMLElement | null>(null);
  const pendingHomeComposerFocusRef = useRef(false);
  // Files picked / dropped / pasted onto the home composer before the
  // session pod exists. They are uploaded to `/api/sessions/{id}/files/upload`
  // after the pod becomes Ready, then their absolute paths are appended to
  // the seed turn the same way the in-chat composer appends them via
  // `composePromptWithAttachments`. Cleared once the seed turn is submitted
  // (or on the next createSession call).
  const [homeAttachments, setHomeAttachments] = useState<HomePendingAttachment[]>([]);
  const homeFileInputRef = useRef<HTMLInputElement | null>(null);
  const addHomeAttachments = useCallback((files: FileList | File[] | null) => {
    if (!files) return;
    const list = Array.from(files);
    if (list.length === 0) return;
    setHomeAttachments((prev) => [
      ...prev,
      ...labelAttachments(list, prev).map(({ file, label }) => ({
        id: `home-att-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
        file,
        name: file.name || "file",
        label,
        size: file.size,
        previewUrl: file.type.startsWith("image/")
          ? URL.createObjectURL(file)
          : undefined,
      })),
    ]);
  }, []);
  const removeHomeAttachment = useCallback((id: string) => {
    setHomeAttachments((prev) => {
      const att = prev.find((a) => a.id === id);
      if (att?.previewUrl) URL.revokeObjectURL(att.previewUrl);
      return prev.filter((a) => a.id !== id);
    });
  }, []);
  const focusHomeComposerTextarea = useCallback((): boolean => {
    const textarea = homeComposerWrapRef.current?.querySelector("textarea") as
      | HTMLTextAreaElement
      | null;
    if (!textarea || textarea.disabled) return false;
    textarea.focus();
    const cursor = textarea.value.length;
    textarea.setSelectionRange(cursor, cursor);
    return true;
  }, []);
  const focusHomeSetupSection = useCallback((): boolean => {
    if (!homeBodyRef.current) return false;
    homeBodyRef.current.focus({ preventScroll: true });
    return document.activeElement === homeBodyRef.current;
  }, []);
  const [homeDragActive, setHomeDragActive] = useState(false);
  useEffect(() => {
    if (sessionModeSupportsWorkspaceFiles(defaultSessionMode)) return;
    setHomeDragActive(false);
    setHomeAttachments((prev) => {
      for (const att of prev) {
        if (att.previewUrl) URL.revokeObjectURL(att.previewUrl);
      }
      return [];
    });
  }, [defaultSessionMode]);
  // Splash-page repo picker state:
  //
  //   - selectedRepos: the chips the user has staged for the
  //     about-to-be-created session. Posted to /api/sessions on
  //     create; persisted as the next splash default so the last
  //     picked repo set reappears when the user comes back home.
  //   - recentRepos: GET /api/github/recent-repos result for this
  //     user. The picker's "Recent" section reads from here. Stays
  //     empty (and the section hidden) when the user has never
  //     selected a repo before — no error state, just absence.
  //   - repoPickerOpen: tri-state — closed by default; opens on
  //     "+ Add repo" click; closes on outside-click / Esc / explicit
  //     close.
  //   - repoInput: the manual-entry text field's controlled value.
  //
  // "All repos" is sourced from /api/github/repos; the manual text
  // input remains the escape hatch when enumeration fails or lags.
  const [selectedRepos, setSelectedRepos] = useState<string[]>(readHomeSelectedRepos);
  const [recentRepos, setRecentRepos] = useState<string[]>([]);
  const [repoPickerOpen, setRepoPickerOpen] = useState(false);
  const [repoInput, setRepoInput] = useState("");
  const [repoError, setRepoError] = useState<string | null>(null);
  const recentRepoShortcuts = useMemo(
    () => recentRepoShortcutSlugs(recentRepos),
    [recentRepos],
  );
  // All-repos lazy-load state. Sourced from /api/github/repos,
  // which proxies to mcp-github via an on-behalf-of token mint. The
  // picker calls onLoadAllRepos on first open; this state is the
  // result. Refreshed after a successful session create so just-used
  // repos float in the list next time the splash opens.
  const [allRepos, setAllRepos] = useState<{
    status: "idle" | "loading" | "ready" | "error";
    repos: string[];
    error?: string | null;
  }>({ status: "idle", repos: [] });
  // Freshly-created chat sessions should land in the composer once the
  // session is ready. The title can still be renamed explicitly via F2.
  const [autoFocusComposerSessionId, setAutoFocusComposerSessionId] = useState<string | null>(
    null,
  );
  const initialSessionId = useRef<string | null>(readInitialSessionId());
  // ?message=<entry.id> deep link, captured once at boot. We keep it in
  // state (not a ref) so React re-renders the matching ChatPane with
  // the prop populated; that pane consumes it via onScrollConsumed,
  // which clears state + URL param so back/forward navigation doesn't
  // re-scroll.
  const [pendingScrollMessageId, setPendingScrollMessageId] = useState<string | null>(
    readInitialMessageId,
  );
  const consumePendingScroll = useCallback(() => {
    setPendingScrollMessageId(null);
    clearInitialMessageId();
  }, []);
  const [sessionConnectionLabels, setSessionConnectionLabels] = useState<Record<string, string | undefined>>({});
  const updateSessionConnectionLabel = useCallback((id: string, label: string | null) => {
    setSessionConnectionLabels((prev) => {
      if (prev[id] === (label ?? undefined)) return prev;
      const next = { ...prev };
      if (label) next[id] = label;
      else delete next[id];
      return next;
    });
  }, []);
  const glimmungLaunchContext = useRef<GlimmungLaunchContext | null>(
    readGlimmungLaunchContext()
  );
  const currentSessionScope = normalizeSessionScopeValue(appConfig.session_scope);
  const hasAdminAccess = userIsAdmin(user);
  const canViewProdSessions =
    hasAdminAccess && currentSessionScope !== PROD_SESSION_SCOPE;
  const effectiveSessionScope =
    canViewProdSessions && sessionViewScopeOverride === PROD_SESSION_SCOPE
      ? PROD_SESSION_SCOPE
      : currentSessionScope;
  const viewingProdSessions =
    canViewProdSessions && effectiveSessionScope === PROD_SESSION_SCOPE;
  const readOnlySessionView = effectiveSessionScope !== currentSessionScope;
  const scopedSessionPath = useCallback(
    (path: string) => appendQueryParam(path, "session_scope", effectiveSessionScope),
    [effectiveSessionScope],
  );
  const adminSettingsControls =
    hasAdminAccess
      ? {
          visible: true,
          canViewProdSessions,
          viewingProdSessions,
          currentScope: currentSessionScope,
          prodScope: PROD_SESSION_SCOPE,
          onAvatarCatalogChanged: refreshRuntimeAvatarCatalog,
          onViewingProdSessionsChange: (value: boolean) => {
            const next = value ? PROD_SESSION_SCOPE : "";
            setSessionViewScopeOverride(next);
            writeSessionViewScopePreference(next);
          },
        }
      : undefined;

  useEffect(() => {
    let cancelled = false;
    void fetchAppPublicConfig().then((config) => {
      if (!cancelled) {
        setAppConfig(config);
        setAppConfigLoaded(true);
      }
    });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    // Only reconcile the persisted prod-view preference once we actually
    // know the caller's admin capability and the live session scope. During
    // the auth/config bootstrap window canViewProdSessions is transiently
    // false (user === null, appConfig === {}), so clearing here would destroy
    // an admin's persisted "view prod sessions" choice on every reload or
    // deep link — the toggle would never survive a refresh. Gate on both the
    // auth bootstrap (booted) and the public-config fetch (appConfigLoaded).
    if (!booted || !appConfigLoaded) return;
    if (canViewProdSessions || !sessionViewScopeOverride) return;
    setSessionViewScopeOverride("");
    writeSessionViewScopePreference("");
  }, [booted, appConfigLoaded, canViewProdSessions, sessionViewScopeOverride]);

  useEffect(() => {
    bootstrapAuth()
      .then(async (u) => {
        const pendingInstallState = readPendingGitHubInstallState();
        if (u && pendingInstallState) {
          try {
            const installedUser = await completeGitHubInstall(pendingInstallState);
            clearPendingGitHubInstallState();
            setUser(installedUser);
          } catch (e) {
            const reason = errorMessage(e);
            setInstallErrorParam(reason);
            setUser(u);
          }
        } else {
          setUser(u);
        }
        setBooted(true);
      })
      .catch((e) => {
        setAuthError(errorMessage(e));
        setBooted(true);
      });
  }, []);

  useEffect(() => {
    if (!user) return;
    let cancelled = false;
    void loadRuntimeAvatarCatalog()
      .then(() => {
        if (!cancelled) setAvatarCatalogVersion((version) => version + 1);
      })
      .catch(() => {
        // Avatar uploads are an enhancement over the static built-in pool.
        // A failed catalog read should not block the session shell.
      });
    return () => {
      cancelled = true;
    };
  }, [user]);

  // refreshRecentRepos pulls /api/github/recent-repos and seeds the
  // splash picker's "Recent" section. Best-effort: a network blip or
  // an older backend (pre-stage-1) without the endpoint just leaves
  // the section empty, which is a fine product state.
  const refreshRecentRepos = useCallback(async () => {
    try {
      const res = await authedFetch("/api/github/recent-repos");
      if (!res.ok) return;
      const body = (await res.json()) as Partial<RecentReposResponse>;
      if (Array.isArray(body.repos)) {
        setRecentRepos(body.repos.map(String).filter(isValidRepoSlug));
      }
    } catch {
      // Picker still works without the Recent section.
    }
  }, []);

  // Fetch the full installation repo list. Lazy-loaded on first picker
  // open and refreshed after a successful session create so just-installed
  // repos appear in the list next time.
  //
  // The endpoint mints an on-behalf-of service JWT (auth.romaine.life
  // PR #43) and proxies to mcp-github; failures here become a
  // user-visible "Couldn't load your repos" line in the picker rather
  // than a silent empty list — the user can still type owner/name and
  // click Add to use an exact slug.
  const loadAllRepos = useCallback(async (): Promise<void> => {
    setAllRepos((prev) =>
      prev.status === "ready" ? prev : { status: "loading", repos: [] },
    );
    try {
      const res = await authedFetch("/api/github/repos");
      if (!res.ok) {
        let detail = `HTTP ${res.status}`;
        try {
          const body = await res.json();
          if (typeof body?.detail === "string") detail = body.detail;
        } catch {
          // Keep the status-only detail when response isn't JSON.
        }
        setAllRepos({ status: "error", repos: [], error: detail });
        return;
      }
      const body = (await res.json()) as {
        repos?: Array<{ full_name?: string } & Record<string, unknown>>;
      };
      const slugs = Array.isArray(body.repos)
        ? body.repos
            .map((r) => String(r.full_name ?? ""))
            .filter((slug) => slug !== "" && isValidRepoSlug(slug))
        : [];
      setAllRepos({ status: "ready", repos: slugs });
    } catch (e) {
      setAllRepos({
        status: "error",
        repos: [],
        error: String(e instanceof Error ? e.message : e),
      });
    }
  }, []);

  useEffect(() => {
    if (!user) return;
    void refreshRecentRepos();
  }, [user, refreshRecentRepos]);

  // Close the picker when the user switches default mode to one
  // that doesn't support repos. The staged repos stay intact so the
  // splash can restore the last-picked set if the user switches back
  // to a repo-capable mode.
  useEffect(() => {
    if (!REPO_SUPPORTED_MODES.has(defaultSessionMode)) {
      if (repoPickerOpen) setRepoPickerOpen(false);
      if (repoError) setRepoError(null);
    }
  }, [defaultSessionMode, repoPickerOpen, repoError]);

  useEffect(() => {
    writeHomeSelectedRepos(selectedRepos);
  }, [selectedRepos]);

  const selectExclusiveRepo = useCallback((rawSlug: string) => {
    const result = addRepoSlug([], rawSlug);
    if (result.ok) {
      setSelectedRepos(result.next);
      setRepoInput("");
      setRepoError(null);
    } else {
      setRepoError(result.error);
    }
  }, []);

  useEffect(() => {
    if (
      active !== null ||
      homeActiveTab !== "chat" ||
      !REPO_SUPPORTED_MODES.has(defaultSessionMode)
    ) {
      return;
    }
    const selectRecentRepoByNumber = (event: KeyboardEvent) => {
      if (
        event.altKey ||
        event.ctrlKey ||
        event.metaKey ||
        event.shiftKey ||
        event.isComposing ||
        event.target !== homeBodyRef.current ||
        !/^[1-9]$/.test(event.key)
      ) {
        return;
      }
      const slug = recentRepoShortcuts[Number(event.key) - 1];
      if (!slug) return;
      event.preventDefault();
      event.stopPropagation();
      selectExclusiveRepo(slug);
    };
    window.addEventListener("keydown", selectRecentRepoByNumber, { capture: true });
    return () => {
      window.removeEventListener("keydown", selectRecentRepoByNumber, { capture: true });
    };
  }, [
    active,
    defaultSessionMode,
    homeActiveTab,
    recentRepoShortcuts,
    selectExclusiveRepo,
  ]);

  // Close the profile menu on an outside click. Menus use a `data-menu`
  // attribute so a single listener can route by which menu is open.
  useEffect(() => {
    if (!profileMenuOpen) return;
    const close = (e: MouseEvent) => {
      const target = e.target as HTMLElement | null;
      const root = target?.closest("[data-menu]") as HTMLElement | null;
      if (root?.dataset.menu === "profile") return;
      setProfileMenuOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [profileMenuOpen]);

  // refresh re-fetches the per-owner session snapshot from
  // /api/sessions and hydrates BOTH the sessions list and the
  // sessionActivities map from the response. The activity block lives on
  // each Session row (server-side join against the latest
  // sessions.activity_summary row column; the prior activity-poll
  // endpoint is gone (tank-operator#83). Steady-state updates after the
  // initial snapshot flow through the row-update SSE stream; refresh() is
  // only used for first-load and post-resync reseeding.
  // rowToSession projects one SessionStore row back into the
  // SPA's Session shape for React-state consumption. The wire row's
  // fields align one-for-one with Session's so this is mostly a
  // field-copy with type coercions for the optional fields.
  function rowToSession(row: SessionRow): Session {
    return {
      id: row.id,
      session_scope: normalizeSessionScopeValue(row.session_scope),
      pod_name: row.pod_name ?? null,
      owner: row.owner,
      status: row.status,
      mode: row.mode as SessionMode,
      requested_at: row.requested_at ?? null,
      created_at: row.created_at ?? null,
      ready_at: row.ready_at ?? null,
      name: row.name ?? null,
      test_state: (row.test_state as TestState | undefined) ?? null,
      rollout_state: (row.rollout_state as RolloutState | undefined) ?? null,
      sidebar_position: row.sidebar_position,
      activity: undefined, // activities live in the parallel sessionActivities map
      repos: Array.isArray(row.repos) ? row.repos : [],
      clone_state: (row.clone_state as Record<string, unknown> | undefined) ?? null,
      model: row.model ?? "",
      effort: row.effort ?? "",
      runtime_model: row.runtime_model ?? "",
      runtime_effort: row.runtime_effort ?? "",
      runtime_configured_at: row.runtime_configured_at ?? null,
      agent_avatar_id: row.agent_avatar_id ?? null,
      system_avatar_id: row.system_avatar_id ?? null,
      row_version: row.row_version,
    };
  }

  // infoJSONToSessionRow converts one item from GET /api/sessions
  // into the wire-shape row the SessionStore caches. Field names align
  // one-for-one with the backend rowWireShape (Phase 3) so this is a
  // copy with snapshot-only defaults (visible=true).
  function infoJSONToSessionRow(raw: any): SessionRow {
    const sidebarPosition = Number(raw.sidebar_position);
    const rowVersion = Number(raw.row_version);
    if (!Number.isFinite(sidebarPosition) || !Number.isFinite(rowVersion)) {
      throw new Error("session snapshot missing row order cursor");
    }
    return {
      id: String(raw.id ?? ""),
      owner: String(raw.owner ?? ""),
      mode: String(raw.mode ?? "claude_gui"),
      session_scope: normalizeSessionScopeValue(raw.session_scope),
      pod_name: raw.pod_name ?? undefined,
      name: raw.name ?? null,
      visible: true,
      status: String(raw.status ?? "Pending"),
      requested_at: raw.requested_at ?? undefined,
      created_at: raw.created_at ?? undefined,
      ready_at: raw.ready_at ?? undefined,
      activity_summary: raw.activity ?? undefined,
      test_state: raw.test_state ?? undefined,
      rollout_state: raw.rollout_state ?? undefined,
      repos: Array.isArray(raw.repos) ? raw.repos.map(String) : [],
      clone_state: raw.clone_state ?? undefined,
      model: typeof raw.model === "string" ? raw.model : undefined,
      effort: typeof raw.effort === "string" ? raw.effort : undefined,
      runtime_model: typeof raw.runtime_model === "string" ? raw.runtime_model : undefined,
      runtime_effort: typeof raw.runtime_effort === "string" ? raw.runtime_effort : undefined,
      runtime_configured_at:
        typeof raw.runtime_configured_at === "string" ? raw.runtime_configured_at : undefined,
      agent_avatar_id:
        typeof raw.agent_avatar_id === "string" ? raw.agent_avatar_id : undefined,
      system_avatar_id:
        typeof raw.system_avatar_id === "string" ? raw.system_avatar_id : undefined,
      sidebar_position: sidebarPosition,
      row_version: rowVersion,
    };
  }

  async function refresh() {
    try {
      const res = await authedFetch(scopedSessionPath("/api/sessions"));
      if (!res.ok) throw new Error(`list failed: ${res.status}`);
      // Tank-Sessions-Snapshot-Cursor carries MAX(row_version) at
      // snapshot time. Seeding the SessionStore with it closes the
      // race between the snapshot query and the SSE open — the
      // row-update catch-up only emits changes after the snapshot's
      // cursor. See docs/product-inspirations.md: "Reconnect resumes
      // from a cursor over persisted events."
      const snapshotCursor = res.headers.get("Tank-Sessions-Snapshot-Cursor");
      const rawList = await res.json();
      const listed: Session[] = rawList.map(normalizeSession);
      // Feed the SessionStore from the same Info[] payload so the
      // row cache, tombstones, and cursor stay in lockstep with what
      // the SPA renders. The infoJSONToSessionRow helper maps Info
      // JSON onto the wire-shape SessionRow.
      sessionStoreRef.current.applySnapshot(
        rawList.map(infoJSONToSessionRow),
        snapshotCursor,
        "refresh",
      );
      logSessionListSnapshot({
        tip: snapshotCursor,
        sessionCount: listed.length,
        source: "initial",
      });
      const sessionsFromStore = sessionStoreRef.current.list().map(rowToSession);
      setSessions(sessionsFromStore);
      const nextActivities: Record<string, SessionActivitySummary> = {};
      for (const row of sessionStoreRef.current.list()) {
        const activity = sessionStoreRef.current.activityForRender(row.id);
        if (activity) nextActivities[row.id] = activity;
      }
      setSessionActivities(nextActivities);
      // Synchronously mirror into the ref so the SSE handler reading
      // sessionActivitiesRef.current immediately after refresh sees the
      // fresh snapshot, not whatever was in there before. The useEffect
      // ref-sync below also catches this on the next render but lags by
      // a render cycle, which the back-to-back-activity-events race
      // (submitted → streaming → ready in microseconds) cannot tolerate.
      sessionActivitiesRef.current = nextActivities;
      // Seed the turn-complete-sound dedup map from the snapshot's
      // per-session last_order_key. Any SSE-replayed activity_changed
      // event with last_order_key <= the seeded value will be silenced
      // (it represents a chat-ledger order we've already accounted for
      // in the snapshot). Sessions absent from the snapshot are not
      // seeded — the predicate's "no prior, no ring" rule plus the
      // snapshot-applied flag below cover them.
      lastSoundedOrderKeyRef.current = new Map(
        listed
          .map((session): [string, string | null] => [
            session.id,
            sessionStoreRef.current.activityForRender(session.id)?.last_order_key ?? null,
          ])
          .filter((entry): entry is [string, string] => entry[1] !== null),
      );
      activitySnapshotAppliedRef.current = true;
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }

  useEffect(() => {
    recordSessionListDebugEvent({
      kind: "scope-reset",
      source: "App",
      reason: "effective_session_scope_changed",
      active_id: activeRef.current,
      rows: sessionListDebugRows(sessionsRef.current),
      detail: { effective_session_scope: effectiveSessionScope },
    });
    sessionStoreRef.current = new SessionStore();
    activitySnapshotAppliedRef.current = false;
    lastSoundedOrderKeyRef.current = new Map();
    setSessions([]);
    setSessionActivities({});
    sessionActivitiesRef.current = {};
    setMounted(new Set());
    setClosingIds(new Set());
    setActive(null);
  }, [effectiveSessionScope]);

  useEffect(() => {
    if (!user) return;
    void refresh();
  }, [user, effectiveSessionScope]);

  // Keep the refs that the SSE reducer reads in sync with the latest
  // committed state. This lets the SSE useEffect close over a stable
  // user identity instead of every render's state, avoiding constant
  // resubscribe.
  useEffect(() => {
    sessionsRef.current = sessions;
  }, [sessions]);
  useEffect(() => {
    sessionActivitiesRef.current = sessionActivities;
  }, [sessionActivities]);
  useEffect(() => {
    activeRef.current = active;
  }, [active]);
  useEffect(() => {
    homeSessionNameRef.current = homeSessionName;
  }, [homeSessionName]);
  useEffect(() => {
    homeEditingTitleRef.current = homeEditingTitle;
  }, [homeEditingTitle]);
  useEffect(() => {
    pendingCreateTitleSessionIdRef.current = pendingCreateTitleSessionId;
  }, [pendingCreateTitleSessionId]);
  useEffect(() => {
    editingSessionTitleValueRef.current = editingSessionTitleValue;
  }, [editingSessionTitleValue]);

  useEffect(() => {
    updateSessionListDebugRender({
      active_id: active,
      avatar_catalog_version: avatarCatalogVersion,
      sessions: sessionListDebugRows(sessions),
    });
  }, [sessions, active, avatarCatalogVersion]);

  useEffect(() => {
    const context = glimmungLaunchContext.current;
    if (!user || requiresGitHubOnboarding(user) || !context) return;
    glimmungLaunchContext.current = null;

    async function launch() {
      setBusy(true);
      setError(null);
      try {
        const res = await authedFetch("/api/sessions/with-context", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            ...context,
            caller_email: user!.email,
            mode: defaultSessionMode,
          }),
        });
        if (!res.ok) throw new Error(`glimmung launch failed: ${res.status}`);
        const created = await res.json();
        clearGlimmungLaunchContext();
        const session: Session = created.session;
        await refresh();
        activate(session.id);
      } catch (e) {
        glimmungLaunchContext.current = context;
        setError(String(e));
      } finally {
        setBusy(false);
      }
    }

    void launch();
  }, [user, defaultSessionMode]);

  // Typed session-list SSE stream. Replaces the prior wake-and-refetch
  // SSE + 1.5s pending-session polling loop + visibility/focus refetch
  // handlers per tank-operator#83 — the durable ledger backing this
  // stream means cursor-resume after disconnect catches up
  // automatically; no polling is needed for any failure mode the prior
  // path covered. Mirrors the chat-window event stream shape: tracks a
  // ref'd cursor for Last-Event-ID reconnect, handles resync_required
  // by re-fetching the snapshot, and reopens on connection-loss.
  useEffect(() => {
    if (!user) return;
    const cursorRef = { current: null as string | null };
    let source: EventSource | null = null;
    let cancelled = false;
    let reopenTimer: number | null = null;

    const open = async () => {
      if (cancelled) return;
      // Cold open: seed the cursor from the SessionStore (which was
      // primed by the snapshot's Tank-Sessions-Snapshot-Cursor
      // header). The server's catch-up will then emit only row
      // updates strictly after that cursor — no replay-from-zero,
      // no resurrection of deleted sessions. If the store has no
      // cursor (first request after backend roll, fresh owner),
      // the server fast-forwards an empty cursor itself.
      if (!cursorRef.current) {
        cursorRef.current = sessionStoreRef.current.getCursor();
      }
      const params = new URLSearchParams();
      if (cursorRef.current) params.set("after_row_version", cursorRef.current);
      logSessionListSseOpen(cursorRef.current);
      const query = params.toString();
      let nextSource: EventSource;
      try {
        nextSource = await authedEventSource(
          scopedSessionPath(`/api/sessions/events${query ? `?${query}` : ""}`),
          { stream: "session-list", sessionScope: effectiveSessionScope },
        );
      } catch {
        if (cancelled) return;
        reopenTimer = window.setTimeout(() => void open(), 1000);
        return;
      }
      if (cancelled) {
        nextSource.close();
        return;
      }
      source = nextSource;
      nextSource.addEventListener("session-row", (event) => {
        const message = event as MessageEvent;
        let parsed: unknown;
        try {
          parsed = JSON.parse(String(message.data));
        } catch {
          return;
        }
        const payload = normalizeSessionRowUpdate(parsed);
        if (!payload) return;
        logSessionListEvent({
          type: "session.row_update",
          session_id: payload.row.id,
          session_scope: payload.row.session_scope,
          order_key: payload.cursor,
        });
        cursorRef.current = payload.cursor;

        // Capture pre-apply state so the turn-complete sound logic
        // can compare prior vs next activity. Once store.applyRowUpdate
        // runs, the prior row is gone.
        const store = sessionStoreRef.current;
        const priorActivity = store.activityForRender(payload.row.id);

        const changed = store.applyRowUpdate(payload);
        if (!changed) {
          return; // tombstoned drop or unchanged — no React state churn
        }

        // Derive sessions[] + activities from the store. The
        // store's durable sidebar_position sort mirrors what
        // refresh() does on snapshot.
        const rows = store.list();
        const sessionsFromStore: Session[] = rows.map(rowToSession);
        setSessions(sessionsFromStore);
        sessionsRef.current = sessionsFromStore;

        const nextActivities: Record<string, SessionActivitySummary> = {};
        for (const id of rows.map((r) => r.id)) {
          const act = store.activityForRender(id);
          if (act) nextActivities[id] = act;
        }
        setSessionActivities(nextActivities);
        sessionActivitiesRef.current = nextActivities;

        // Turn-complete sound. Adapted to the row-update wire: the
        // row's activity_summary field carries the same activity
        // shape sessionActivities renders against. Compare prior
        // activity (snapshot before applyRowUpdate) against next.
        // Gates unchanged: snapshot-applied, per-session
        // last_order_key dedup, shouldRingForActivityTransition,
        // turnCompleteSoundOnVisible.
        const nextActivity = store.activityForRender(payload.row.id);
        if (
          activitySnapshotAppliedRef.current &&
          nextActivity &&
          nextActivity.last_order_key
        ) {
          const priorRing = lastSoundedOrderKeyRef.current.get(payload.row.id);
          const alreadyRepresented =
            priorRing != null && !orderKeyAfter(nextActivity.last_order_key, priorRing);
          if (!alreadyRepresented) {
            lastSoundedOrderKeyRef.current.set(payload.row.id, nextActivity.last_order_key);
            if (shouldRingForActivityTransition(priorActivity ?? undefined, nextActivity)) {
              const isVisible = activeRef.current === payload.row.id;
              const suppressForVisible =
                isVisible && !runPrefsRef.current.turnCompleteSoundOnVisible;
              if (!suppressForVisible) {
                playTurnCompleteSound();
              }
            }
          }
        }
      });
      nextSource.addEventListener("ready", (event) => {
        const message = event as MessageEvent;
        let parsed: Record<string, unknown> | undefined;
        try {
          parsed = JSON.parse(String(message.data));
        } catch {
          parsed = undefined;
        }
        logSessionListStreamSignal({ signal: "ready", detail: parsed });
      });
      nextSource.addEventListener("resync_required", (event) => {
        const message = event as MessageEvent;
        let parsed: Record<string, unknown> | undefined;
        try {
          parsed = JSON.parse(String(message.data));
        } catch {
          parsed = undefined;
        }
        logSessionListStreamSignal({ signal: "resync_required", detail: parsed });
        cursorRef.current = null;
        // Drop the snapshot-applied flag so the SSE replay that
        // follows refresh() can't ring for transitions that happened
        // during the disconnect window. refresh() re-seeds the dedup
        // map + flips the flag back on once the fresh snapshot lands.
        activitySnapshotAppliedRef.current = false;
        nextSource.close();
        if (source === nextSource) source = null;
        void refresh();
        // Refresh hydrates the snapshot; open() resumes the stream
        // from a fresh cursor on the next tick.
        reopenTimer = window.setTimeout(() => void open(), 250);
      });
      nextSource.addEventListener("stream-error", (event) => {
        const message = event as MessageEvent;
        let parsed: Record<string, unknown> | undefined;
        try {
          parsed = JSON.parse(String(message.data));
        } catch {
          parsed = undefined;
        }
        logSessionListStreamSignal({ signal: "stream-error", detail: parsed });
        nextSource.close();
        if (source === nextSource) source = null;
        if (cancelled) return;
        reopenTimer = window.setTimeout(() => void open(), 1000);
      });
      nextSource.onerror = () => {
        nextSource.close();
        if (source === nextSource) source = null;
        if (cancelled) return;
        reopenTimer = window.setTimeout(() => void open(), 1000);
      };
    };

    void open();
    return () => {
      cancelled = true;
      if (reopenTimer != null) window.clearTimeout(reopenTimer);
      source?.close();
    };
    // refresh + the ref-backed sessions/activities snapshots are
    // intentionally stable; closing over them would resubscribe on
    // every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user, effectiveSessionScope, scopedSessionPath]);

  useEffect(() => {
    if (active && (!sessions.some((s) => s.id === active) || closingIds.has(active))) {
      const selectable = sessions.filter((s) => !closingIds.has(s.id));
      const nextActive = selectable[selectable.length - 1]?.id ?? null;
      recordSessionListDebugEvent({
        kind: "active-fallback",
        source: "App",
        reason: closingIds.has(active) ? "active_closing" : "active_missing",
        previous_active_id: active,
        active_id: nextActive,
        rows: sessionListDebugRows(sessions),
      });
      setActive(nextActive);
    }
    // Drop any mounted ids that no longer exist or are being deleted.
    setMounted((prev) => {
      let changed = false;
      const next = new Set<string>();
      prev.forEach((id) => {
        if (sessions.some((s) => s.id === id) && !closingIds.has(id)) next.add(id);
        else changed = true;
      });
      return changed ? next : prev;
    });
    setClosingIds((prev) => {
      const existing = new Set(sessions.map((s) => s.id));
      let changed = false;
      const next = new Set<string>();
      prev.forEach((id) => {
        if (existing.has(id)) next.add(id);
        else changed = true;
      });
      return changed ? next : prev;
    });
    setSessionActivities((prev) => {
      const existing = new Set(sessions.map((s) => s.id));
      let changed = false;
      const next: Record<string, SessionActivitySummary> = {};
      for (const [id, activity] of Object.entries(prev)) {
        if (existing.has(id) && !closingIds.has(id)) {
          next[id] = activity;
        } else {
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [sessions, active, closingIds]);

  useEffect(() => {
    const target = initialSessionId.current;
    if (!target) return;
    if (!sessions.some((s) => s.id === target)) return;
    activate(target);
    initialSessionId.current = null;
    clearInitialSessionId();
  }, [sessions]);

  useEffect(() => {
    if (active !== null) {
      pendingHomeComposerFocusRef.current = false;
      return;
    }
    if (homeActiveTab !== "chat" || !pendingHomeComposerFocusRef.current) return;
    pendingHomeComposerFocusRef.current = false;
    requestAnimationFrame(() => {
      focusHomeComposerTextarea();
    });
  }, [active, focusHomeComposerTextarea, homeActiveTab]);

  // Match the run pane's two-surface Tab toggle on the pre-session splash:
  // textarea ⇄ setup body. Other controls keep the browser's native tab order.
  useEffect(() => {
    if (active !== null || homeActiveTab !== "chat") return;
    const toggleHomeFocus = (event: KeyboardEvent) => {
      if (
        event.key !== "Tab" ||
        event.altKey ||
        event.ctrlKey ||
        event.metaKey ||
        event.isComposing
      ) {
        return;
      }
      const textarea = homeComposerWrapRef.current?.querySelector("textarea") as
        | HTMLTextAreaElement
        | null;
      const body = homeBodyRef.current;
      if (!textarea || !body) return;
      if (event.target === textarea) {
        if (!focusHomeSetupSection()) return;
      } else if (event.target === body) {
        if (!focusHomeComposerTextarea()) return;
      } else {
        return;
      }
      event.preventDefault();
      event.stopImmediatePropagation();
    };
    window.addEventListener("keydown", toggleHomeFocus, { capture: true });
    return () => window.removeEventListener("keydown", toggleHomeFocus, { capture: true });
  }, [active, focusHomeComposerTextarea, focusHomeSetupSection, homeActiveTab]);

  useEffect(() => {
    if (active !== null) return;
    const focusHomeComposer = (event: KeyboardEvent) => {
      if (
        event.key !== "/" ||
        event.altKey ||
        event.ctrlKey ||
        event.metaKey ||
        event.shiftKey ||
        event.isComposing
      ) {
        return;
      }
      const textarea = homeComposerWrapRef.current?.querySelector("textarea") as
        | HTMLTextAreaElement
        | null;
      if (textarea && event.target === textarea) return;
      if (isTextEntryShortcutTarget(event.target)) return;

      event.preventDefault();
      event.stopPropagation();
      pendingHomeComposerFocusRef.current = true;
      if (homeActiveTab !== "chat") {
        setHomeActiveTab("chat");
        return;
      }
      pendingHomeComposerFocusRef.current = false;
      requestAnimationFrame(() => {
        focusHomeComposerTextarea();
      });
    };
    window.addEventListener("keydown", focusHomeComposer, { capture: true });
    return () => window.removeEventListener("keydown", focusHomeComposer, { capture: true });
  }, [active, focusHomeComposerTextarea, homeActiveTab]);

  useEffect(() => {
    const openNewSessionPage = (event: KeyboardEvent) => {
      if (
        event.key !== "+" ||
        event.altKey ||
        event.ctrlKey ||
        event.metaKey ||
        event.isComposing ||
        isTextEntryShortcutTarget(event.target)
      ) {
        return;
      }
      if (active === null && homeActiveTab === "chat") return;

      event.preventDefault();
      event.stopPropagation();
      goHome();
    };
    window.addEventListener("keydown", openNewSessionPage, { capture: true });
    return () => window.removeEventListener("keydown", openNewSessionPage, { capture: true });
  }, [active, homeActiveTab]);

  useEffect(() => {
    const cycleTabs = (event: KeyboardEvent) => {
      const direction = altArrowSessionDirection(event);
      if (direction == null || isSessionShortcutEditableTarget(event.target)) return;
      const nextId = adjacentSessionId(sessions, active, direction, closingIds);
      if (nextId == null) return;
      event.preventDefault();
      event.stopPropagation();
      setActive(nextId);
      setMounted((prev) => (prev.has(nextId) ? prev : new Set(prev).add(nextId)));
    };
    window.addEventListener("keydown", cycleTabs, { capture: true });
    return () => window.removeEventListener("keydown", cycleTabs, { capture: true });
  }, [sessions, active, closingIds]);

  useEffect(() => {
    const renameHighlightedSession = (event: KeyboardEvent) => {
      if (event.key !== "F2" || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      const targetId = shortcutSessionId(event.target) ?? active;
      if (!targetId) {
        if (active == null) {
          event.preventDefault();
          event.stopPropagation();
          event.stopImmediatePropagation();
          beginHomeTitleEdit();
        }
        return;
      }
      if (closingIds.has(targetId)) return;
      const session = sessions.find((s) => s.id === targetId);
      if (!session) return;
      event.preventDefault();
      event.stopPropagation();
      event.stopImmediatePropagation();
      activate(session.id);
      setAutoFocusComposerSessionId(null);
      beginSessionTitleEdit(session);
    };
    window.addEventListener("keydown", renameHighlightedSession, { capture: true });
    return () => window.removeEventListener("keydown", renameHighlightedSession, { capture: true });
  }, [sessions, active, closingIds, readOnlySessionView]);

  function activate(id: string) {
    // Treat the sidebar click as the user gesture that unlocks audio
    // for the page. Browsers refuse audio.play() until the page has
    // received at least one gesture; calling audio.load() here marks
    // the element as "unlocked" for the rest of the page lifetime,
    // so a background turn that completes a minute later can ring
    // without further interaction.
    primeTurnCompleteSound();
    setActive(id);
    setMounted((prev) => (prev.has(id) ? prev : new Set(prev).add(id)));
  }

  function goHome() {
    const url = new URL(window.location.href);
    url.pathname = "/";
    url.search = "";
    url.hash = "";
    window.history.replaceState({}, "", url.toString());
    setActive(null);
    setHomeActiveTab("chat");
  }

  function openSession(id: string, e: ReactMouseEvent) {
    if (e.ctrlKey || e.metaKey) {
      window.open(sessionUrl(id), "_blank", "noopener,noreferrer");
      return;
    }
    activate(id);
  }

  function dragSessionStart(id: string, event: ReactDragEvent<HTMLLIElement>) {
    if (readOnlySessionView) return;
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("text/plain", id);
    setDraggingSessionId(id);
    setDragOverSessionId(id);
  }

  function dragSessionOver(id: string, event: ReactDragEvent<HTMLLIElement>) {
    if (readOnlySessionView) return;
    if (!draggingSessionId || draggingSessionId === id) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    setDragOverSessionId(id);
  }

  async function persistSessionOrder(sessionIds: string[]): Promise<void> {
    try {
      const res = await authedFetch("/api/sessions/order", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ session_ids: sessionIds }),
      });
      if (!res.ok) {
        throw new Error(`session order update failed: ${res.status}`);
      }
    } catch (e) {
      setError(String(e));
      void refresh();
    }
  }

  function dropSession(id: string, event: ReactDragEvent<HTMLLIElement>) {
    event.preventDefault();
    if (readOnlySessionView) return;
    const movedId = event.dataTransfer.getData("text/plain") || draggingSessionId;
    setDraggingSessionId(null);
    setDragOverSessionId(null);
    if (!movedId || movedId === id || !user) return;

    const currentOrder = sessions.map((session) => session.id);
    const next = moveSessionId(currentOrder, movedId, id);
    if (next === currentOrder) return;
    sessionStoreRef.current.applyLocalOrder(next);
    const ordered = orderSessionsByIds(sessions, next);
    setSessions(ordered);
    sessionsRef.current = ordered;
    void persistSessionOrder(next);
  }

  function dragSessionEnd() {
    setDraggingSessionId(null);
    setDragOverSessionId(null);
  }

  async function markCreatedSessionTestState(id: string): Promise<void> {
    const state: TestState = { active: true };
    recordSessionListDebugEvent({
      kind: "test-state-optimistic",
      source: "markCreatedSessionTestState",
      session_id: id,
      rows: sessionListDebugRows(sessionsRef.current),
      detail: { state },
    });
    setSessions((prev) =>
      prev.map((s) => (s.id === id ? { ...s, test_state: state, rollout_state: null } : s)),
    );
    const res = await authedFetch(`/api/sessions/${id}/test-state`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(state),
    });
    if (!res.ok) {
      throw new Error(`test state update failed: ${res.status}`);
    }
    const updated: Session = normalizeSession(await res.json());
    recordSessionListDebugEvent({
      kind: "test-state-response",
      source: "markCreatedSessionTestState",
      session_id: id,
      row: sessionListDebugRow(updated),
      rows: sessionListDebugRows(sessionsRef.current),
    });
    setSessions((prev) => prev.map((s) => (s.id === id ? updated : s)));
  }

  async function createSession(
    mode: SessionMode = defaultSessionMode,
    initialPrompt?: string,
    initialPermissionMode: RunComposerMode = "default",
    initialSkillName?: SkillStateName,
    initialMessageMode: InitialMessageMode = DEFAULT_INITIAL_MESSAGE_MODE,
  ) {
    if (isDefaultSessionMode(mode)) {
      setDefaultSessionMode(mode);
      writeDefaultSessionMode(mode);
    }
    setBusy(true);
    setSidebarCollapsed(false);
    setError(null);
    // Only forward the chip selection for modes that support it. The
    // mode-flip effect above keeps selectedRepos empty for
    // unsupported modes, so this is a belt-and-braces guard: a
    // mode-override createSession() call could otherwise send repos for a
    // CLI session and get a 400.
    const repos = REPO_SUPPORTED_MODES.has(mode) ? selectedRepos : [];
    const requestedName = normalizedHomeTitleNameFrom(
      homeSessionNameRef.current,
      homeEditingDefaultTitleRef.current,
    );
    const requestedInitialSkillName = initialSkillName ?? initialMessageModeSkillName(initialMessageMode);
    const seedPrompt = composeInitialMessageModePrompt(initialMessageMode, initialPrompt?.trim() ?? "");
    const pendingHomeAttachments = sessionModeSupportsWorkspaceFiles(mode) ? [...homeAttachments] : [];
    const seedTurnRequested =
      (seedPrompt || pendingHomeAttachments.length > 0 || requestedInitialSkillName) &&
      CHAT_MODES.has(mode);
    const seedClientNonce = seedTurnRequested ? newForkTurnId() : "";
    const seedModel =
      selectedProvider === "anthropic" || selectedProvider === "codex"
        ? (selectedHomeModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID ? "" : selectedHomeModelId)
        : "";
    const seedEffort =
      selectedProvider === "anthropic" || selectedProvider === "codex"
        ? selectedHomeEffortId
        : "";
    const sessionModel = SDK_CHAT_MODES.has(mode) ? seedModel : "";
    const sessionEffort = SDK_CHAT_MODES.has(mode) ? seedEffort : "";
    const seedInitialTurnAtCreate =
      seedTurnRequested && CREATE_TIME_INITIAL_TURN_MODES.has(mode);
    const seedTurnDeferredAtCreate =
      seedInitialTurnAtCreate && SDK_CHAT_MODES.has(mode) && pendingHomeAttachments.length > 0;
    const seedTurnSubmittedAtCreate = seedInitialTurnAtCreate && !seedTurnDeferredAtCreate;
    const launchDisplayAttachments = messageAttachmentDisplays(pendingHomeAttachments);
    const launchUserPrompt = composeLaunchUserPrompt(seedPrompt, pendingHomeAttachments);
    const createTimeTurnPrompt = requestedInitialSkillName
      ? composeSkillPrompt(mode, requestedInitialSkillName, launchUserPrompt)
      : launchUserPrompt;
    const initialTurnPayload = seedInitialTurnAtCreate
      ? {
          client_nonce: seedClientNonce,
          prompt: createTimeTurnPrompt,
          ...(launchDisplayAttachments.length > 0
            ? { display_attachments: attachmentPayloadsForApi(launchDisplayAttachments) }
            : {}),
          ...(seedTurnDeferredAtCreate ? { deferred: true } : {}),
          permission_mode: initialPermissionMode,
          ...(requestedInitialSkillName ? { skill_name: requestedInitialSkillName } : {}),
        }
      : undefined;
    try {
      const res = await authedFetch("/api/sessions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          mode,
          repos,
          ...(requestedName ? { name: requestedName } : {}),
          ...(sessionModel || sessionEffort ? { model: sessionModel, effort: sessionEffort } : {}),
          ...(initialTurnPayload ? { initial_turn: initialTurnPayload } : {}),
        }),
      });
      if (!res.ok) throw new Error(`create failed: ${res.status}`);
      let created: Session = normalizeSession(await res.json());
      const titleEditorOpen = homeEditingTitleRef.current;
      const latestHomeName = normalizedHomeTitleNameFrom(
        homeSessionNameRef.current,
        homeEditingDefaultTitleRef.current,
      );
      let responseTitleUpdate = false;
      if (!titleEditorOpen && latestHomeName !== requestedName) {
        try {
          created = await patchSessionName(created.id, latestHomeName);
          responseTitleUpdate = true;
        } catch (e) {
          setError(String(e));
        }
      }
      if (CHAT_MODES.has(mode)) {
        writeSessionInteraction(created.id, defaultInteraction);
      }
      recordSessionListDebugEvent({
        kind: "create-response",
        source: "createSession",
        session_id: created.id,
        row: sessionListDebugRow(created),
        rows: sessionListDebugRows(sessionsRef.current),
        detail: {
          requested_name: requestedName || null,
          latest_home_name: latestHomeName,
          response_title_update: responseTitleUpdate,
          initial_skill_name: requestedInitialSkillName ?? null,
        },
      });
      // Insert the freshly-created session into the local list and open the
      // chat pane immediately, without waiting on /api/sessions to re-list or
      // on the pod becoming Ready. The backend returned the full session row
      // synchronously (status: "Pending"), so the sidebar entry and the chat
      // pane header can render against it right now; the session_lifecycle
      // SSE will reconcile status, runtimeLabel, etc. as they arrive. The
      // prior shape awaited a list refresh before activating, which made the
      // new pane appear "on the side" — sidebar entry showing up while the
      // main pane stayed on whatever was already open.
      setSessions((prev) => {
        if (prev.some((s) => s.id === created.id)) return prev;
        return [created, ...prev];
      });
      recordSessionListDebugEvent({
        kind: "react-sessions-direct-write",
        source: "createSession",
        session_id: created.id,
        row: sessionListDebugRow(created),
        rows: sessionListDebugRows([created, ...sessionsRef.current]),
        detail: { operation: "prepend_created_session" },
      });
      activate(created.id);
      if (titleEditorOpen) {
        pendingCreateTitleSessionIdRef.current = created.id;
        setPendingCreateTitleSessionId(created.id);
      } else {
        pendingCreateTitleSessionIdRef.current = null;
        setPendingCreateTitleSessionId(null);
        homeSessionNameRef.current = "";
        setHomeSessionName("");
        homeEditingTitleRef.current = false;
        setHomeEditingTitle(false);
      }
      if (CHAT_MODES.has(created.mode) && !titleEditorOpen) {
        setAutoFocusComposerSessionId(created.id);
      }
      if (seedInitialTurnAtCreate && requestedInitialSkillName === "test") {
        void markCreatedSessionTestState(created.id).catch((e) => {
          setError(String(e));
        });
      }
      // Belt-and-braces reconcile in the background — the lifecycle SSE
      // wake from session.created should beat this in practice. Does not
      // gate the UI.
      void refresh();
      // Create-time submitted launch turns are already durable. Deferred
      // launch turns already have their user row; after readiness this path
      // uploads files and writes only the turn.submitted boundary.
      if (seedTurnRequested && !seedTurnSubmittedAtCreate) {
        try {
          const readySession = await waitForSessionReady(created.id);
          recordSessionListDebugEvent({
            kind: "ready-session-response",
            source: "createSession",
            session_id: created.id,
            row: sessionListDebugRow(readySession),
            rows: sessionListDebugRows(sessionsRef.current),
          });
          setSessions((prev) => prev.map((s) => (s.id === created.id ? readySession : s)));
          if (requestedInitialSkillName === "test") {
            void markCreatedSessionTestState(created.id).catch((e) => {
              setError(String(e));
            });
          }
          // Upload home-buffered attachments to the new session pod before
          // submitting the seed turn — same endpoint and shape the in-chat
          // composer's uploadAttachment() uses, so the agent runner sees an
          // identical attachments payload regardless of where the user
          // started typing.
          const uploadedPaths: { name: string; absPath: string }[] = [];
          for (const att of pendingHomeAttachments) {
            const upRes = await authedFetch(
              `/api/sessions/${created.id}/files/upload?name=${encodeURIComponent(att.name)}`,
              {
                method: "POST",
                headers: { "Content-Type": att.file.type || "application/octet-stream" },
                body: att.file,
              },
            );
            if (!upRes.ok) {
              throw new Error(`attachment upload failed: ${upRes.status}`);
            }
            const body = (await upRes.json()) as { abs_path: string; name: string };
            uploadedPaths.push({ name: body.name, absPath: body.abs_path });
          }
          // Clear the home buffer once the files are persisted on the pod.
          setHomeAttachments((prev) => {
            for (const a of prev) {
              if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
            }
            return [];
          });
          const composedPrompt =
            uploadedPaths.length > 0
              ? composeAttachmentPathText(seedPrompt, uploadedPaths.map((p) => p.absPath))
              : seedPrompt;
          const turnPrompt = requestedInitialSkillName
            ? composeSkillPrompt(mode, requestedInitialSkillName, composedPrompt)
            : composedPrompt;
          const turnRes = await authedFetch(`/api/sessions/${created.id}/turns`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              client_nonce: seedClientNonce,
              prompt: turnPrompt,
              permission_mode: initialPermissionMode,
              ...(requestedInitialSkillName ? { skill_name: requestedInitialSkillName } : {}),
              ...(seedTurnDeferredAtCreate ? { existing_user_message: true } : {}),
              follow_up: false,
            }),
          });
          if (!turnRes.ok) {
            let detail = `seed turn failed: ${turnRes.status}`;
            try {
              const body = await turnRes.json();
              if (typeof body?.detail === "string") detail = body.detail;
            } catch {
              // Status-only detail is fine when the body isn't JSON.
            }
            throw new Error(detail);
          }
        } catch (e) {
          setError(String(e));
        }
      }
      // Keep the last-picked repos staged so the next splash can
      // default to the same repo set. Close the picker and reset the
      // inline editor state; if repos were used, refresh "Recent" so
      // the just-used repos float to the top next time the splash opens.
      setRepoPickerOpen(false);
      setRepoInput("");
      setRepoError(null);
      if (repos.length > 0) {
        void refreshRecentRepos();
        // Invalidate the All-repos cache too: a user who just
        // installed our App on a new account expects the new repos
        // to show up in the picker without a full SPA reload.
        setAllRepos({ status: "idle", repos: [] });
      }
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function forkSessionFromMessage(request: ForkSessionRequest) {
    const mode = request.sourceSession.mode;
    const prompt = await buildForkSessionPrompt(request);
    const name = `fork: ${sessionDisplayName(request.sourceSession)}`.slice(0, 80);
    setBusy(true);
    setSidebarCollapsed(false);
    setError(null);
    try {
      const res = await authedFetch("/api/sessions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          mode,
          ...(SDK_CHAT_MODES.has(mode) && (request.model || request.effort)
            ? { model: request.model, effort: request.effort }
            : {}),
        }),
      });
      if (!res.ok) {
        let detail = `fork failed: ${res.status}`;
        try {
          const body = await res.json();
          if (typeof body?.detail === "string") detail = body.detail;
        } catch {
          // Keep the status-only detail when the response is not JSON.
        }
        throw new Error(detail);
      }
      const created: Session = normalizeSession(await res.json());
      void authedFetch(`/api/sessions/${created.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name }),
      }).catch(() => undefined);
      if (CHAT_MODES.has(created.mode)) {
        writeSessionInteraction(created.id, "gui");
      }
      await refresh();
      activate(created.id);
      await waitForSessionReady(created.id);
      const turnRes = await authedFetch(`/api/sessions/${created.id}/turns`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          client_nonce: newForkTurnId(),
          prompt,
          permission_mode: request.permissionMode,
          follow_up: false,
          origin_session_id: request.sourceSession.id,
        }),
      });
      if (!turnRes.ok) {
        let detail = `fork turn failed: ${turnRes.status}`;
        try {
          const body = await turnRes.json();
          if (typeof body?.detail === "string") detail = body.detail;
        } catch {
          // Keep the status-only detail when the response is not JSON.
        }
        throw new Error(detail);
      }
      // The SSE stream on /api/sessions/events delivers the
      // session.activity_changed delta for the new turn; no manual
      // refresh needed.
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  function newForkTurnId(): string {
    const cryptoObj = window.crypto;
    if (cryptoObj?.randomUUID) return cryptoObj.randomUUID();
    return `fork-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
  }

  async function waitForSessionReady(id: string): Promise<Session> {
    for (let attempt = 0; attempt < 60; attempt += 1) {
      const res = await authedFetch(`/api/sessions/${id}`);
      if (res.ok) {
        const session: Session = normalizeSession(await res.json());
        if (session.status === "Active" || session.ready_at) return session;
      }
      await new Promise((resolve) => window.setTimeout(resolve, 1000));
    }
    throw new Error("new session did not become ready");
  }

  function setDefaultProvider(provider: Provider) {
    const interaction = availableInteractionFor(provider, defaultInteraction);
    const mode = defaultModeFor(provider, interaction);
    if (interaction !== defaultInteraction) {
      setDefaultInteraction(interaction);
      writeDefaultInteraction(interaction);
    }
    setDefaultSessionMode(mode);
    writeDefaultSessionMode(mode);
  }

  function selectDefaultInteraction(interaction: SessionInteraction) {
    const provider = MODE_MENU_ICONS[defaultSessionMode];
    const nextInteraction = availableInteractionFor(provider, interaction);
    const mode = defaultModeFor(provider, nextInteraction);
    setDefaultInteraction(nextInteraction);
    writeDefaultInteraction(nextInteraction);
    setDefaultSessionMode(mode);
    writeDefaultSessionMode(mode);
  }

  function beginHomeTitleEdit() {
    const usingDefaultTitle = homeSessionNameRef.current.trim() === "";
    homeEditingDefaultTitleRef.current = usingDefaultTitle;
    homeTitleClosingRef.current = false;
    if (usingDefaultTitle) {
      homeSessionNameRef.current = HOME_DEFAULT_SESSION_TITLE;
      setHomeSessionName(HOME_DEFAULT_SESSION_TITLE);
    }
    homeEditingTitleRef.current = true;
    setHomeEditingTitle(true);
  }

  async function finishHomeTitleEdit() {
    if (homeTitleClosingRef.current) return;
    homeTitleClosingRef.current = true;
    const fromDefault = homeEditingDefaultTitleRef.current;
    const nextName = normalizedHomeTitleNameFrom(homeSessionNameRef.current, fromDefault);
    const pendingSessionId = pendingCreateTitleSessionIdRef.current;
    homeEditingDefaultTitleRef.current = false;
    homeEditingTitleRef.current = false;
    setHomeEditingTitle(false);

    if (pendingSessionId) {
      pendingCreateTitleSessionIdRef.current = null;
      setPendingCreateTitleSessionId(null);
      await renameSession(pendingSessionId, nextName);
      homeSessionNameRef.current = "";
      setHomeSessionName("");
      return;
    }

    if (nextName == null && fromDefault) {
      homeSessionNameRef.current = "";
      setHomeSessionName("");
    }
  }

  function beginSessionTitleEdit(session: Session) {
    if (readOnlySessionView) return;
    editingSessionTitleFromFallbackRef.current = session.name == null;
    editingSessionTitleClosingRef.current = false;
    const value = sessionDisplayName(session);
    editingSessionTitleValueRef.current = value;
    setEditingSessionTitleValue(value);
    setEditingSessionTitleId(session.id);
  }

  function cancelSessionTitleEdit() {
    editingSessionTitleClosingRef.current = true;
    editingSessionTitleFromFallbackRef.current = false;
    editingSessionTitleValueRef.current = "";
    setEditingSessionTitleId(null);
    setEditingSessionTitleValue("");
  }

  function commitSessionTitleEdit() {
    if (editingSessionTitleClosingRef.current) return;
    const sessionID = editingSessionTitleId;
    if (!sessionID) return;
    editingSessionTitleClosingRef.current = true;
    const session = sessionsRef.current.find((s) => s.id === sessionID);
    const trimmed = editingSessionTitleValueRef.current.trim();
    const fallbackName = session ? defaultSessionName(session) : "";
    const nextName =
      trimmed === "" ||
      (editingSessionTitleFromFallbackRef.current && trimmed === fallbackName)
        ? null
        : trimmed;
    editingSessionTitleFromFallbackRef.current = false;
    editingSessionTitleValueRef.current = "";
    setEditingSessionTitleId(null);
    setEditingSessionTitleValue("");
    void renameSession(sessionID, nextName);
  }

  async function patchSessionName(id: string, nextName: string | null): Promise<Session> {
    const res = await authedFetch(`/api/sessions/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: nextName }),
    });
    if (!res.ok) throw new Error(`rename failed: ${res.status}`);
    return normalizeSession(await res.json());
  }

  async function renameSession(id: string, nextName: string | null) {
    try {
      const updated = await patchSessionName(id, nextName);
      recordSessionListDebugEvent({
        kind: "rename-response",
        source: "renameSession",
        session_id: id,
        row: sessionListDebugRow(updated),
        rows: sessionListDebugRows(sessionsRef.current),
        detail: { next_name: nextName },
      });
      setSessions((prev) =>
        prev.map((s) => (s.id === id ? { ...s, name: updated.name ?? null } : s))
      );
    } catch (e) {
      setError(String(e));
    }
  }

  function patchSession(id: string, patch: Partial<Session>) {
    recordSessionListDebugEvent({
      kind: "react-sessions-direct-write",
      source: "patchSession",
      session_id: id,
      rows: sessionListDebugRows(sessionsRef.current),
      detail: { patch_keys: Object.keys(patch) },
    });
    setSessions((prev) =>
      prev.map((s) => (s.id === id ? { ...s, ...patch } : s))
    );
  }

  async function deleteSession(id: string) {
    if (closingIds.has(id)) return;
    setError(null);
    // Optimistic delete in the SessionStore (Phase 3): tombstones the
    // id immediately, so any subsequent server-side wire payload for
    // this id (post-delete pod-informer events, etc.) is dropped at
    // the store boundary. If the DELETE API call fails, refresh()
    // will rehydrate from the snapshot and clear the tombstone for
    // ids the server still considers visible — recovery for free.
    sessionStoreRef.current.optimisticDelete(id);
    setClosingIds((prev) => new Set(prev).add(id));
    setMounted((prev) => {
      if (!prev.has(id)) return prev;
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
    setEditingSessionTitleId((prev) => (prev === id ? null : prev));
    setActive((prev) => {
      if (prev !== id) return prev;
      const idx = sessions.findIndex((s) => s.id === id);
      const selectable = sessions.filter((s) => s.id !== id && !closingIds.has(s.id));
      if (selectable.length === 0) return null;
      return sessions[idx + 1]?.id && !closingIds.has(sessions[idx + 1].id)
        ? sessions[idx + 1].id
        : selectable[selectable.length - 1].id;
    });
    try {
      const res = await authedFetch(`/api/sessions/${id}`, { method: "DELETE" });
      if (!res.ok) throw new Error(`delete failed: ${res.status}`);
      await refresh();
    } catch (e) {
      setClosingIds((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
      setError(String(e));
    }
  }

  async function saveCredentials(id: string) {
    setBusy(true);
    setError(null);
    try {
      const res = await authedFetch(`/api/sessions/${id}/save-credentials`, {
        method: "POST",
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error(body.detail || `save failed: ${res.status}`);
      }
      await deleteSession(id);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  if (authError) {
    return (
      <div className="boot-state">
        <pre className="error">auth error: {authError}</pre>
        <button className="btn-secondary" onClick={() => location.reload()}>retry</button>
      </div>
    );
  }

  if (!booted) {
    return <div className="boot-state"><span className="boot-text">loading…</span></div>;
  }

  if (!user) {
    return <DemoLanding />;
  }

  // Admins and service principals bypass the wall: admins use the host
  // installation for MCP-github, and service principals are platform-internal
  // callers used by test automation and session-pod handoffs.
  if (requiresGitHubOnboarding(user)) {
    return <OnboardingWall user={user} onLogout={logout} />;
  }

  const selectedProvider = MODE_MENU_ICONS[defaultSessionMode];
  const configMode = PROVIDER_CONFIG_MODES[selectedProvider];
  const homeModelOptions =
    selectedProvider === "anthropic"
      ? CLAUDE_MODELS
      : selectedProvider === "codex"
        ? CODEX_MODELS
        : [];
  const homeModelApplies = defaultInteraction === "gui" && homeModelOptions.length > 0;
  const selectedHomeModelId =
    selectedProvider === "anthropic"
      ? runPrefs.claudeModelId
      : selectedProvider === "codex"
        ? runPrefs.codexModelId
        : CODEX_ACCOUNT_DEFAULT_MODEL_ID;
  const selectedHomeEffortId =
    selectedProvider === "anthropic"
      ? runPrefs.claudeEffort
      : selectedProvider === "codex"
        ? runPrefs.codexEffort
        : DEFAULT_CLAUDE_EFFORT_ID;
  const selectedInitialMessageMode =
    INITIAL_MESSAGE_MODE_OPTIONS.find((option) => option.id === runPrefs.initialMessageMode) ??
    INITIAL_MESSAGE_MODE_OPTIONS[0];
  const homeSessionTitle = homeSessionName.trim() || HOME_DEFAULT_SESSION_TITLE;
  const activeWorkspaceSession = active == null
    ? null
    : sessions.find((session) => session.id === active) ?? null;
  const activeConnectionLabel =
    activeWorkspaceSession == null
      ? null
      : sessionConnectionLabels[activeWorkspaceSession.id] ?? null;
  const useHomeTitleChrome =
    active == null || homeEditingTitle || pendingCreateTitleSessionId != null;
  const showWorkspaceTitleChrome =
    useHomeTitleChrome ||
    (activeWorkspaceSession != null && CHAT_MODES.has(activeWorkspaceSession.mode));
  const workspaceTitleChrome = showWorkspaceTitleChrome ? (
    <div className="workspace-title-overlay">
      {useHomeTitleChrome ? (
        homeEditingTitle ? (
          <input
            className="run-header-name-input"
            aria-label="Session name"
            value={homeSessionName}
            autoFocus
            onFocus={selectTitleInputText}
            onChange={(e) => {
              homeSessionNameRef.current = e.target.value;
              setHomeSessionName(e.target.value);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                e.stopPropagation();
                void finishHomeTitleEdit();
              } else if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                void finishHomeTitleEdit();
              }
            }}
            onBlur={() => {
              void finishHomeTitleEdit();
            }}
            maxLength={80}
          />
        ) : (
          <button
            type="button"
            className="run-header-name-btn"
            title="Name this session before creating it"
            onClick={beginHomeTitleEdit}
          >
            {homeSessionTitle}
          </button>
        )
      ) : activeWorkspaceSession != null ? (
        editingSessionTitleId === activeWorkspaceSession.id ? (
          <input
            className="run-header-name-input"
            aria-label="Session name"
            value={editingSessionTitleValue}
            autoFocus
            onFocus={selectTitleInputText}
            onChange={(e) => {
              editingSessionTitleValueRef.current = e.target.value;
              setEditingSessionTitleValue(e.target.value);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                e.stopPropagation();
                commitSessionTitleEdit();
              } else if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                cancelSessionTitleEdit();
              }
            }}
            onBlur={commitSessionTitleEdit}
            maxLength={80}
          />
        ) : (
          <button
            type="button"
            className="run-header-name-btn"
            title={
              readOnlySessionView
                ? sessionDisplayName(activeWorkspaceSession)
                : activeWorkspaceSession.name
                  ? `${defaultSessionName(activeWorkspaceSession)} - click to rename`
                  : "click to rename"
            }
            disabled={readOnlySessionView}
            onClick={() => beginSessionTitleEdit(activeWorkspaceSession)}
          >
            {sessionDisplayName(activeWorkspaceSession)}
          </button>
        )
      ) : null}
      {!useHomeTitleChrome && activeConnectionLabel && (
        <span
          className="run-connection-pill"
          role="status"
          aria-live="polite"
        >
          <span className="run-connection-label">{activeConnectionLabel}</span>
        </span>
      )}
    </div>
  ) : null;
  const paneFontScale = runPrefs.chatFontScale;
  const paneFontScalePct = Math.round(paneFontScale * 100);
  const turnCompleteSoundVolumePct = Math.round(runPrefs.turnCompleteSoundVolume * 100);

  return (
    <div className={`shell${sidebarCollapsed ? " sidebar-collapsed" : ""}`}>
      <aside className={`sidebar${sidebarCollapsed ? " is-collapsed" : ""}`}>
        <div className="sidebar-brand">
          <button
            className={`sidebar-home${active == null ? " is-active" : ""}`}
            onClick={goHome}
            title="Home"
            aria-label="Home"
            aria-current={active == null ? "page" : undefined}
          >
            <span className="sidebar-home-label">tank-operator</span>
          </button>
          <div className="sidebar-brand-actions">
            <button
              className="sidebar-collapse"
              onClick={() => setSidebarCollapsed((v) => !v)}
              title={sidebarCollapsed ? "expand sidebar" : "collapse sidebar"}
              aria-label={sidebarCollapsed ? "expand sidebar" : "collapse sidebar"}
              aria-pressed={sidebarCollapsed}
            >
              <IconPanelToggle collapsed={sidebarCollapsed} />
            </button>
          </div>
        </div>

        {error && <pre className="error">{error}</pre>}

        <div className="sidebar-list">
          <div className="sidebar-list-head">
            <div className="sidebar-section-label">Sessions</div>
            <button
              className="sidebar-new-session"
              onClick={goHome}
              aria-label="New session"
              title="new session"
            >
              <span className="row-icon"><IconPlus /></span>
            </button>
          </div>
          <ul className="sessions">
            {sessions.length === 0 && <li className="sessions-empty">no sessions</li>}
            {sessions.map((s) => {
              const isLive = s.status === "Active";
              const isClosing = closingIds.has(s.id);
              const isActive = active === s.id && !isClosing;
              const avatar = getSessionAvatarByID(s.agent_avatar_id);
              const statusDotClass = sessionStatusDotClass(s, sessionActivities[s.id]);
              const statusLabel = sessionStatusLabel(s, sessionActivities[s.id]);
              const skillStateClass = sessionSkillStateClass(s);
              return (
                <li
                  key={s.id}
                  data-session-id={s.id}
                  className={`${isActive ? "is-open" : ""}${isClosing ? " is-closing" : ""}${skillStateClass}${draggingSessionId === s.id ? " is-dragging" : ""}${dragOverSessionId === s.id && draggingSessionId !== s.id ? " is-drag-over" : ""}`}
                  draggable={!isClosing && !readOnlySessionView}
                  onDragStart={(e) => dragSessionStart(s.id, e)}
                  onDragOver={(e) => dragSessionOver(s.id, e)}
                  onDrop={(e) => dropSession(s.id, e)}
                  onDragEnd={dragSessionEnd}
                  onClick={isClosing ? undefined : (e) => openSession(s.id, e)}
                  title={sidebarCollapsed ? `${sessionDisplayName(s)} (${statusLabel})` : undefined}
                >
                  <SessionAvatarIcon avatar={avatar} className="session-avatar" />
                  <div className="session-row-top">
                    {/* Session name is now a read-only label here; rename
                        lives in the chat-pane header (see ChatPane's
                        run-header-title). This avoids the prior
                        sidebar-inline-edit input that opened on a row click
                        and lost typed characters whenever the pod-state
                        re-render or refresh fired underneath it. */}
                    <span
                      className="session-open"
                      title={isClosing ? "session is closing" : defaultSessionName(s)}
                    >
                      <span className="session-id">{sessionDisplayName(s)}</span>
                    </span>
                    <button
                      className="session-delete"
                      onClick={(e) => { e.stopPropagation(); deleteSession(s.id); }}
                      disabled={isClosing || readOnlySessionView}
                      title={isClosing ? "closing session" : "delete session"}
                      aria-label={isClosing ? "closing session" : "delete session"}
                    >
                      {isClosing ? <span className="session-delete-spinner" /> : <IconClose />}
                    </button>
                  </div>
                  <div className="session-row-bottom">
                    <span
                      className={statusDotClass}
                      title={statusLabel}
                      aria-label={`status: ${statusLabel}`}
                    />
                    <ModeChip mode={s.mode} interaction={sessionInteractionForSession(s)} />
                    <SessionStats session={s} />
                    {isClosing && <span className="session-closing-chip">closing</span>}
                    {CONFIG_MODES.has(s.mode) && (
                      <button
                        className="session-action"
                        onClick={(e) => { e.stopPropagation(); saveCredentials(s.id); }}
                        disabled={busy || !isLive || isClosing || readOnlySessionView}
                        title={
                          s.mode === "codex_config"
                            ? "capture ~/.codex/auth.json from this pod and write it to KV"
                            : "capture ~/.claude/.credentials.json from this pod and write it to KV"
                        }
                      >
                        save
                      </button>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        </div>

        <ClusterHealthWidget enabled={Boolean(user)} />

        <div className="sidebar-footer" data-menu="profile">
          <button
            className="profile"
            onClick={() => setProfileMenuOpen((v) => !v)}
            title={user.email}
          >
            <Avatar user={user} />
            <span className="profile-text">
              <span className="profile-name">{user.name || user.email}</span>
            </span>
            <span className="profile-kebab"><IconKebab /></span>
          </button>
          {profileMenuOpen && (
            <ul className="dropdown dropdown-profile" role="menu">
              <li className="dropdown-meta">
                <span className="dropdown-meta-label">Signed in as</span>
                <span className="dropdown-meta-value">{user.email}</span>
              </li>
              <li className="dropdown-divider" role="separator" />
              <li>
                <button onClick={logout}>Sign out</button>
              </li>
            </ul>
          )}
        </div>
      </aside>

      <main className="workspace">
        {workspaceTitleChrome}
        {active == null ? (
          // Pre-session "home" state. Same workspace scaffold as an active
          // session — same body/composer column and same composer footer at
          // the same y-coordinate — with the header strip restored so the
          // about-to-be-created session can be named before launch.
          <WorkspaceShell
            className="run-panel-home"
            bodyClassName={homeActiveTab === "chat" ? "run-main-home" : undefined}
            bodyRef={homeBodyRef}
            bodyAriaLabel={homeActiveTab === "chat" ? "New session setup" : "Workspace panel"}
            title={<WorkspaceTitleSpacer />}
            tabs={(<>
              {homeActiveTab !== "chat" && (
                <button
                  type="button"
                  className="run-tab run-tab-back"
                  onClick={() => setHomeActiveTab("chat")}
                  aria-label="Back to chat"
                  title="Back to chat"
                >
                  <ArrowLeftIcon
                    className="run-tab-icon"
                    strokeWidth={2.2}
                    aria-hidden="true"
                  />
                  <span>Back</span>
                </button>
              )}
              <TurnsTab
                active={false}
                disabled
                onOpen={() => undefined}
              />
              <BackgroundLedger
                entries={[]}
                active={false}
                onOpen={() => undefined}
                disabled
                title="Background activity is available once the session starts"
              />
              <button
                type="button"
                className="run-tab"
                disabled
                title="Files are available once the session starts"
              >
                <FolderIcon
                  className="run-tab-icon"
                  strokeWidth={1.8}
                  aria-hidden="true"
                />
                <span>Files</span>
              </button>
              <button
                type="button"
                className={`run-tab${homeActiveTab === "settings" ? " run-tab-active" : ""}`}
                onClick={() => setHomeActiveTab((current) => current === "settings" ? "chat" : "settings")}
                aria-pressed={homeActiveTab === "settings"}
                title="Settings"
              >
                <SettingsIcon className="run-tab-icon" aria-hidden="true" />
                <span>Settings</span>
              </button>
              <button
                type="button"
                className={`run-tab${homeActiveTab === "help" ? " run-tab-active" : ""}`}
                onClick={() => setHomeActiveTab((current) => current === "help" ? "chat" : "help")}
                aria-pressed={homeActiveTab === "help"}
                title="Help"
              >
                <InfoIcon className="run-tab-icon" aria-hidden="true" />
                <span>Help</span>
              </button>
            </>)}
            composerVisible={homeActiveTab === "chat"}
            composerWrapRef={homeComposerWrapRef}
            composerWrapClassName={homeDragActive ? "run-composer-wrap-drag" : ""}
            onComposerWrapDragOver={(e) => {
              if (!sessionModeSupportsWorkspaceFiles(defaultSessionMode)) return;
              e.preventDefault();
              if (!homeDragActive) setHomeDragActive(true);
            }}
            onComposerWrapDragLeave={(e) => {
              if (e.currentTarget === e.target) setHomeDragActive(false);
            }}
            onComposerWrapDrop={(e) => {
              if (!sessionModeSupportsWorkspaceFiles(defaultSessionMode)) return;
              e.preventDefault();
              setHomeDragActive(false);
              addHomeAttachments(e.dataTransfer?.files ?? null);
            }}
            onComposerWrapPaste={(e) => {
              if (!sessionModeSupportsWorkspaceFiles(defaultSessionMode)) return;
              const items = e.clipboardData?.items;
              if (!items) return;
              const fs: File[] = [];
              for (const it of Array.from(items)) {
                if (it.kind === "file") {
                  const f = it.getAsFile();
                  if (f) fs.push(f);
                }
              }
              if (fs.length > 0) {
                e.preventDefault();
                addHomeAttachments(fs);
              }
            }}
            body={
              homeActiveTab === "settings" ? (
                <RunSettingsPanel
                  runPrefs={runPrefs}
                  setRunPref={setRunPref}
                  soundControlId="home-turn-sound-volume"
                  turnCompleteSoundVolumePct={turnCompleteSoundVolumePct}
                  setTurnCompleteSoundVolume={setTurnCompleteSoundVolume}
                  playTurnCompleteSound={playTurnCompleteSound}
                  paneFontScale={paneFontScale}
                  paneFontScalePct={paneFontScalePct}
                  setPaneFontScale={setPaneFontScale}
                  adminControls={adminSettingsControls}
                />
              ) : homeActiveTab === "help" ? (
                <RunHelpScreen />
              ) : (
              <>
                <div className="home-inner">
                <section className="home-hero" aria-labelledby="home-title">
                  <div>
                    <h2 id="home-title" className="home-title">What do you want to build?</h2>
                    <p className="home-sub">
                      Type below to start a session with the selected runtime.
                    </p>
                  </div>
                  <span className="home-count">{sessions.length} session{sessions.length === 1 ? "" : "s"}</span>
                </section>

                <div className="home-grid">
                <section className="home-panel" aria-labelledby="home-start-title">
                  <div className="home-panel-head">
                    <h3 id="home-start-title">Configuration</h3>
                    <span className="home-panel-meta">{MODE_LABELS[defaultSessionMode]}</span>
                  </div>
                  <div className="home-choice-grid" role="group" aria-label="provider">
                    {PROVIDERS.map((provider) => {
                      const mode = defaultModeFor(provider, defaultInteraction);
                      const selected = provider === selectedProvider;
                      return (
                        <button
                          key={provider}
                          className={`home-choice${selected ? " is-selected" : ""}`}
                          onClick={() => setDefaultProvider(provider)}
                          disabled={busy}
                          aria-pressed={selected}
                          title={MODE_LABELS[mode]}
                        >
                          <ProviderIcon provider={provider} className="home-choice-icon" />
                          <span>{PROVIDER_LABELS[provider]}</span>
                        </button>
                      );
                    })}
                  </div>
                  <div className="home-choice-grid" role="group" aria-label="interaction">
                    {INTERACTION_OPTIONS.map((interaction) => {
                      const unavailable =
                        PROVIDER_INTERACTION_MODES[selectedProvider][interaction] == null;
                      const selected = defaultInteraction === interaction && !unavailable;
                      return (
                        <button
                          key={interaction}
                          className={`home-choice${selected ? " is-selected" : ""}`}
                          onClick={() => selectDefaultInteraction(interaction)}
                          disabled={busy || unavailable}
                          aria-pressed={selected}
                          title={unavailable ? "not available for this provider" : INTERACTION_LABELS[interaction]}
                        >
                          <InteractionIcon interaction={interaction} className="home-choice-icon" />
                          <span>{INTERACTION_LABELS[interaction]}</span>
                        </button>
                      );
                    })}
                  </div>
                  <div className="home-panel-head home-panel-subhead">
                    <h3>Initial message</h3>
                    <span className="home-panel-meta">{selectedInitialMessageMode.label}</span>
                  </div>
                  <div className="home-initial-grid" role="group" aria-label="initial message type">
                    {INITIAL_MESSAGE_MODE_OPTIONS.map((option) => {
                      const selected = option.id === runPrefs.initialMessageMode;
                      const InitialIcon = option.icon;
                      return (
                        <button
                          key={option.id}
                          className={`home-model home-initial-option${selected ? " is-selected" : ""}`}
                          onClick={() => setRunPref("initialMessageMode", option.id)}
                          disabled={busy}
                          aria-pressed={selected}
                          title={option.hint}
                        >
                          <InitialIcon className="home-initial-icon" aria-hidden="true" />
                          <span className="home-initial-main">
                            <span className="home-model-title">{option.label}</span>
                            <span className="home-model-sub">{option.hint}</span>
                          </span>
                        </button>
                      );
                    })}
                  </div>
                  {homeModelApplies && (
                    <>
                      <div className="home-panel-head home-panel-subhead">
                        <h3>Model</h3>
                        <span className="home-panel-meta">
                          {selectedProvider === "anthropic"
                            ? CLAUDE_EFFORTS.find((effort) => effort.id === selectedHomeEffortId)?.label
                            : selectedProvider === "codex"
                              ? CODEX_EFFORTS.find((effort) => effort.id === selectedHomeEffortId)?.label
                              : ""}
                        </span>
                      </div>
                      <div className="home-model-list" role="group" aria-label="model">
                        {homeModelOptions.map((model) => {
                          const selected = model.id === selectedHomeModelId;
                          return (
                            <button
                              key={model.id}
                              className={`home-model${selected ? " is-selected" : ""}`}
                              onClick={() => {
                                if (selectedProvider === "anthropic") {
                                  setRunPref("claudeModelId", model.id);
                                } else if (selectedProvider === "codex") {
                                  setRunPref("codexModelId", model.id);
                                }
                              }}
                              aria-pressed={selected}
                            >
                              <span className="home-model-title">{model.label}</span>
                            </button>
                          );
                        })}
                      </div>
                      {(selectedProvider === "anthropic" || selectedProvider === "codex") && (
                        <div className="home-effort-grid" role="group" aria-label="effort">
                          {(selectedProvider === "anthropic" ? CLAUDE_EFFORTS : CODEX_EFFORTS).map((effort) => {
                            const selected = effort.id === selectedHomeEffortId;
                            return (
                              <button
                                key={effort.id}
                                className={`home-model home-effort${selected ? " is-selected" : ""}`}
                                onClick={() => {
                                  if (selectedProvider === "anthropic") {
                                    setRunPref("claudeEffort", effort.id);
                                  } else if (selectedProvider === "codex") {
                                    setRunPref("codexEffort", effort.id);
                                  }
                                }}
                                aria-pressed={selected}
                                title={effort.hint}
                              >
                                <span className="home-model-title">{effort.label}</span>
                                {effort.hint && <span className="home-model-sub">{effort.hint}</span>}
                              </button>
                            );
                          })}
                        </div>
                      )}
                    </>
                  )}
                </section>

                <section className="home-panel home-panel-actions" aria-labelledby="home-actions-title">
                  <div className="home-panel-head">
                    <h3 id="home-actions-title">Setup</h3>
                  </div>
                  <div className="home-quick-actions">
                    <button
                      className="home-quick-action"
                      onClick={() => createSession("api_key")}
                      disabled={busy}
                    >
                      <IconKey className="home-quick-icon" />
                      <span className="home-quick-main">
                        <span className="home-quick-title">API key</span>
                        <span className="home-quick-sub">{MODE_HINTS["api_key"]}</span>
                      </span>
                    </button>
                    {configMode && (
                      <button
                        className="home-quick-action"
                        onClick={() => createSession(configMode)}
                        disabled={busy}
                      >
                        <IconWrench className="home-quick-icon" />
                        <span className="home-quick-main">
                          <span className="home-quick-title">{MODE_LABELS[configMode]}</span>
                          <span className="home-quick-sub">{MODE_HINTS[configMode]}</span>
                        </span>
                      </button>
                    )}
                  </div>
                  {REPO_SUPPORTED_MODES.has(defaultSessionMode) && (
                    <RepoPicker
                      selected={selectedRepos}
                      recent={recentRepos}
                      allRepos={allRepos}
                      onLoadAllRepos={loadAllRepos}
                      open={repoPickerOpen}
                      input={repoInput}
                      error={repoError}
                      busy={busy}
                      onToggleOpen={() => {
                        setRepoPickerOpen((prev) => !prev);
                        setRepoError(null);
                      }}
                      onClose={() => {
                        setRepoPickerOpen(false);
                        setRepoError(null);
                      }}
                      onInputChange={(v) => {
                        setRepoInput(v);
                        setRepoError(null);
                      }}
                      onAdd={(rawSlug) => {
                        const result = addRepoSlug(selectedRepos, rawSlug);
                        if (result.ok) {
                          setSelectedRepos(result.next);
                          setRepoInput("");
                          setRepoError(null);
                        } else {
                          setRepoError(result.error);
                        }
                      }}
                      onSelectExclusive={selectExclusiveRepo}
                      onRemove={(slug) => {
                        setSelectedRepos((prev) => removeRepoSlug(prev, slug));
                        setRepoError(null);
                      }}
                    />
                  )}
                </section>
              </div>
                </div>
              </>
              )
            }
            composerAbove={(<>
              {homeDragActive && (
                <div className="run-composer-drop-overlay" aria-hidden="true">
                  Drop to attach
                </div>
              )}
              {homeAttachments.length > 0 && (
                <div className="run-composer-attachments">
                  {homeAttachments.map((a) => (
                    <div
                      key={a.id}
                      className="run-composer-chip run-composer-chip-ready"
                      title={attachmentDisplayTitle(a)}
                    >
                      {a.previewUrl ? (
                        <img
                          className="run-composer-chip-thumb"
                          src={a.previewUrl}
                          alt=""
                          aria-hidden="true"
                        />
                      ) : (
                        <FileIcon size={14} aria-hidden="true" />
                      )}
                      <span className="run-composer-chip-name">{a.label}</span>
                      <button
                        type="button"
                        className="run-composer-chip-remove"
                        onMouseDown={(e) => {
                          e.preventDefault();
                          removeHomeAttachment(a.id);
                        }}
                        aria-label={`Remove ${a.label}`}
                      >
                        <XIcon size={11} aria-hidden="true" />
                      </button>
                    </div>
                  ))}
                </div>
              )}
              <input
                ref={homeFileInputRef}
                type="file"
                multiple
                style={{ display: "none" }}
                onChange={(e) => {
                  addHomeAttachments(e.target.files);
                  e.target.value = "";
                }}
              />
            </>)}
            composer={(
              <ChatComposer
                className="run-composer-home run-composer-interactive"
                placeholder={RUN_COMPOSER_PLACEHOLDER}
                onSubmit={({ text, permissionMode }) => {
                  const trimmed = text.trim();
                  const initialMode = runPrefs.initialMessageMode;
                  const mode =
                    trimmed || homeAttachments.length > 0 || initialMode !== "direct"
                      ? chatModeForHomePrompt(defaultSessionMode)
                      : defaultSessionMode;
                  void createSession(
                    mode,
                    trimmed || undefined,
                    permissionMode,
                    undefined,
                    initialMode,
                  );
                }}
                permissionMode={homeComposerMode}
                onPermissionModeChange={setHomeComposerMode}
                sendByCtrlEnter={runPrefs.sendByCtrlEnter}
                hintSuffix={RUN_COMPOSER_HINT_SUFFIX}
                disabled={busy}
                onTextChange={setHomeComposerText}
                toolButtons={
                  <>
                    <button
                      type="button"
                      className="run-composer-icon-btn"
                      aria-label="Attach files"
                      title={
                        sessionModeSupportsWorkspaceFiles(defaultSessionMode)
                          ? "Attach files for the first turn"
                          : "File attachments require a session workspace"
                      }
                      onClick={() => homeFileInputRef.current?.click()}
                      disabled={busy || !sessionModeSupportsWorkspaceFiles(defaultSessionMode)}
                    >
                      <ImageIcon className="run-composer-icon" aria-hidden="true" />
                    </button>
                    <ComposerUsageRing
                      tokensUsed={0}
                      contextWindow={getContextWindow(selectedHomeModelId)}
                      placeholder
                      ariaLabel="Context usage preview"
                      title="Context usage appears after the session starts"
                    />
                    {GUI_ROLLOUT_MODES.has(defaultSessionMode) && (
                      <button
                        type="button"
                        className="run-composer-icon-btn run-composer-action-btn run-rollout-action-btn"
                        disabled
                        aria-label="Start rollout"
                        title="Use /rollout once your session starts"
                      >
                        <TankIcon className="run-composer-icon" />
                      </button>
                    )}
                    <button
                      type="button"
                      className="run-composer-icon-btn run-composer-action-btn run-test-action-btn"
                      onClick={() => {
                        void createSession(
                          defaultSessionMode,
                          homeComposerText.trim() || undefined,
                          homeComposerMode,
                          "test",
                        );
                      }}
                      disabled
                      aria-label="Start test skill"
                      title="Available in an active chat session"
                    >
                      <FlaskConicalIcon className="run-composer-icon" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className="run-composer-icon-btn run-command-menu-btn"
                      disabled
                      aria-label="Show slash commands"
                      title="Slash commands appear once your session has skills"
                    >
                      <MessageSquareIcon className="run-composer-icon" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className="run-composer-icon-btn run-command-menu-btn"
                      disabled
                      aria-label="Show MCP servers"
                      title="MCP servers appear once your session is connected"
                    >
                      <McpIcon className="run-composer-icon" aria-hidden="true" />
                    </button>
                  </>
                }
              />
            )}
          />
        ) : (
          <div className="terminals">
            {sessions
              .filter((s) => mounted.has(s.id))
              .map((s) =>
                CHAT_MODES.has(s.mode) ? (
                  <div
                    key={s.id}
                    className="run-body"
                    hidden={active !== s.id}
                  >
                    <ChatPane
                      session={s}
                      visible={active === s.id}
                      onSessionPatch={patchSession}
                      onConnectionLabelChange={updateSessionConnectionLabel}
                      onForkMessage={forkSessionFromMessage}
                      pendingScrollMessageId={
                        pendingScrollMessageId && active === s.id
                          ? pendingScrollMessageId
                          : null
                      }
                      onScrollConsumed={consumePendingScroll}
                      runPrefs={runPrefs}
                      setRunPref={setRunPref}
                      user={user!}
                      autoFocusComposer={autoFocusComposerSessionId === s.id}
                      onAutoFocusComposerConsumed={() => setAutoFocusComposerSessionId(null)}
                      primeTurnCompleteSound={primeTurnCompleteSound}
                      playTurnCompleteSound={playTurnCompleteSound}
                      adminControls={adminSettingsControls}
                      readOnly={readOnlySessionView}
                      sessionScope={effectiveSessionScope}
                      avatarCatalogVersion={avatarCatalogVersion}
                    />
                  </div>
                ) : (
                  <div
                    key={s.id}
                    className="run-body"
                    hidden={active !== s.id}
                  >
                    {readOnlySessionView ? (
                      <section className="run-panel">
                        <main className="run-main">
                          <div className="run-empty run-transcript-state" role="status">
                            <strong>Read-only session</strong>
                            <span>Terminal attach is unavailable from this scope.</span>
                          </div>
                        </main>
                      </section>
                    ) : (
                      <CliSession session={s} visible={active === s.id} />
                    )}
                  </div>
                )
              )}
          </div>
        )}
      </main>
    </div>
  );
}
