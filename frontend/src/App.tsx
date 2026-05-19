import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import type {
  AnchorHTMLAttributes,
  ComponentProps,
  CSSProperties,
  DragEvent as ReactDragEvent,
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
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import type { PromptInputMessage } from "@/components/ai-elements/prompt-input";
import { ChatComposer, type RunComposerMode } from "./ChatComposer";
import { WorkspaceShell } from "./WorkspaceShell";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  AlertCircleIcon,
  ArrowDownIcon,
  ArrowLeftIcon,
  ArrowUpIcon,
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
import { authedFetch, bootstrapAuth, getStoredToken, logout, startLogin } from "./auth";
import { requiresGitHubOnboarding, type SessionRole } from "./authPolicy";
import {
  initialConversationState,
  reduceConversationEvents,
  type ConversationReducerState,
} from "./conversationReducer";
import {
  projectConversationState,
  type ConversationViewEntry,
} from "./conversationProjection";
import { McpIcon } from "./McpIcon";
import { RepoPicker } from "./components/RepoPicker";
import {
  REPO_SUPPORTED_MODES,
  addRepoSlug,
  isValidRepoSlug,
  removeRepoSlug,
} from "./repos";
import { ProviderIcon } from "./providerIcons";
import {
  normalizeSessionActivity,
  orderKeyAfter,
  sessionActivityChips,
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
import {
  logSessionListEvent,
  logSessionListSnapshot,
  logSessionListSseOpen,
  logSessionListStreamSignal,
} from "./sessionListTelemetry";
import {
  isDurableTankConversationEvent,
  isTankConversationEvent,
  type TankConversationEvent,
} from "../../runner-shared/conversation.js";
import { ANSI_256_OVERRIDES, ANSI_STANDARD_OVERRIDES } from "./terminalTheme";
import { AgentAvatarIcon, getSessionAvatar, type AgentAvatar } from "./sessionAvatars";

type SessionMode =
  | "api_key"
  | "claude_cli"
  | "claude_gui"
  | "config"
  | "codex_cli"
  | "codex_gui"
  | "codex_app_server"
  | "codex_config"
  | "pi_cli"
  | "pi_config";
type DefaultSessionMode = Extract<
  SessionMode,
  | "claude_cli"
  | "claude_gui"
  | "codex_cli"
  | "codex_gui"
  | "codex_app_server"
  | "pi_cli"
>;
type Provider = "anthropic" | "codex" | "pi";
type SessionInteraction = "gui" | "cli";
type ToolKind = "mcp" | "shell";
type AskUserQuestionAnswer = {
  labels: string[];
  notes?: string;
  preview?: string;
};
type TranscriptEntry = SandboxTranscriptEntry & {
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
  // For user-role messages authored by a sibling tank-operator session
  // via the mcp-tank-operator handoff path: the originating session id.
  // RunMessageBubble swaps in that session's deterministic avatar in
  // place of the human's Gravatar so cross-session handoffs read as
  // agent-authored, not user-authored.
  originSessionId?: string;
  // Durable AskUserQuestion answers + annotations, sourced from the
  // `tool.approval_resolved` event payload via conversationProjection.
  // ToolAskUserBody reads this for the answered state so the UI matches
  // the durable ledger and not local React optimism.
  askUserAnswers?: Record<string, AskUserQuestionAnswer>;
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
};
type SkillStateName = "test" | "rollout";

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

interface Session {
  id: string;
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
  // event in GET /api/sessions; the durable SSE stream then keeps the
  // separately-tracked sessionActivities map up to date. The field is
  // here for the initial-state extraction in normalizeSession; runtime
  // reads go through sessionActivities[id], not session.activity.
  activity?: SessionActivitySummary | null;
  // repos is the durable owner/name slug list the user picked at
  // session creation. Always an array on the wire (empty when none
  // picked). The splash chips for existing sessions read from here
  // — never from localStorage — so the chip list never contradicts
  // the server's view. Stage 1 of the auto-clone feature.
  repos: string[];
  // clone_state is the per-repo init-container outcome (stage 3).
  // Optional until stage 3 ships and the cloner writes back.
  clone_state?: Record<string, unknown> | null;
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
  codex_app_server: "Codex App Server",
  codex_config: "Codex config",
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
  codex_app_server: "codex-app",
  codex_config: "codex-cfg",
  pi_cli: "pi-cli",
  pi_config: "pi-cfg",
};

const MODE_CHIP_ICONS: Partial<Record<SessionMode, Provider>> = {
  claude_cli: "anthropic",
  claude_gui: "anthropic",
  codex_cli: "codex",
  codex_gui: "codex",
  codex_app_server: "codex",
  pi_cli: "pi",
};

const MODE_MENU_ICONS: Record<SessionMode, Provider> = {
  api_key: "anthropic",
  claude_cli: "anthropic",
  claude_gui: "anthropic",
  config: "anthropic",
  codex_cli: "codex",
  codex_gui: "codex",
  codex_app_server: "codex",
  codex_config: "codex",
  pi_cli: "pi",
  pi_config: "pi",
};

const PROVIDER_INTERACTION_MODES: Record<
  Provider,
  Partial<Record<SessionInteraction, DefaultSessionMode | null>>
> = {
  anthropic: { gui: "claude_gui", cli: "claude_cli" },
  codex: { gui: "codex_gui", cli: "codex_cli" },
  pi: { gui: null, cli: "pi_cli" },
};

const INTERACTION_LABELS: Record<SessionInteraction, string> = {
  gui: "gui",
  cli: "cli",
};

const INTERACTION_OPTIONS: SessionInteraction[] = ["gui", "cli"];

const PROVIDER_CONFIG_MODES: Record<Provider, SessionMode> = {
  anthropic: "config",
  codex: "codex_config",
  pi: "pi_config",
};

const MODE_HINTS: Record<SessionMode, string> = {
  claude_cli: "Uses claude.ai login",
  claude_gui: "GUI chat pane for claude -p output",
  api_key: "Specify an API key fallback",
  config: "Log in once · seeds KV for future sessions",
  codex_cli: "Uses ChatGPT login from KV",
  codex_gui: "GUI chat pane for codex exec output",
  codex_app_server: "GUI chat pane for codex app-server transport",
  codex_config: "codex login --device-auth · seeds KV for Codex",
  pi_cli: "Uses Tank Claude/Codex subscriptions",
  pi_config: "Pi /login sandbox",
};

const MODE_ORDER: SessionMode[] = [
  "claude_gui",
  "api_key",
  "config",
  "codex_gui",
  "codex_app_server",
  "codex_config",
  "pi_cli",
  "pi_config",
];

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
  "\x1b[31m▝▜\x1b[40m█████\x1b[49m▛▘\x1b[39m  \x1b[37mOpus 4.7 (1M context) · Claude Max\x1b[39m",
  "\x1b[31m  ▘▘ ▝▝  \x1b[39m  \x1b[37m/workspace\x1b[39m",
  "",
  "  \x1b[1m\x1b[91mWelcome to Opus 4.7 xhigh!\x1b[22m\x1b[37m · /effort to tune speed vs. intelligence\x1b[39m",
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

const DEMO_LOGIN_MESSAGE = "You aren't logged in. Click the log in button on the bottom left.";

function demoTerminalLines(session: Session, promptText?: string): string[] {
  const template = session.mode === "codex_cli" || session.mode === "codex_gui" || session.mode === "codex_app_server"
    ? DEMO_CODEX_LINES
    : session.mode === "pi_cli"
      ? DEMO_PI_LINES
      : DEMO_CLAUDE_LINES;
  const lines = [...template];
  if (promptText) {
    if (session.mode === "codex_cli" || session.mode === "codex_gui" || session.mode === "codex_app_server") {
      lines[lines.length - 1] = `\x1b[1m›\x1b[0m ${promptText}`;
    } else if (session.mode === "pi_cli") {
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
  const label = mode === "codex_cli" || mode === "codex_gui" || mode === "codex_app_server"
    ? "Codex"
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
  };
}

const DEMO_LANDING_LINES = [
  "$ tank-operator preview",
  "Welcome. This is the real app shell with demo sessions.",
  "",
  "Click the provider icon to switch between Claude, Codex, and Pi.",
  "Click + to add a local preview session.",
  "The key and wrench buttons are present but disabled in preview mode.",
  "",
  "Sign in from the lower-left profile area when you want real pods.",
];

const DEFAULT_SESSION_MODE_KEY = "tank.defaultSessionMode";
const DEFAULT_INTERACTION_KEY = "tank.defaultInteraction";
const SESSION_INTERACTION_KEY_PREFIX = "tank.sessionInteraction:";
const SESSION_ORDER_KEY_PREFIX = "tank.sessionOrder";

function normalizeSessionMode(value: string | null): string | null {
  return value;
}

function isDefaultSessionMode(value: string | null): value is DefaultSessionMode {
  return (
    value === "claude_cli" ||
    value === "claude_gui" ||
    value === "codex_cli" ||
    value === "codex_gui" ||
    value === "codex_app_server" ||
    value === "pi_cli"
  );
}

function readDefaultSessionMode(): DefaultSessionMode {
  try {
    const stored = normalizeSessionMode(localStorage.getItem(DEFAULT_SESSION_MODE_KEY));
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

// Per-session transcript-anchor persistence (Stage 2 chat windowing). When
// the user returns to a long session we want them to land on the last
// event they were viewing instead of either the head of the ledger (the
// old 50-page-walk behavior) or always-newest. The order_key is a small
// string — one entry per session — so localStorage is the right fit.
//
// Read/write paths defensive against quota errors and private-mode storage
// blocks; the fallback chain (saved → first_unread → newest) means losing
// a key never breaks the load, it just degrades the UX one notch.
const SDK_TRANSCRIPT_POSITION_KEY_PREFIX = "tank.transcript.position.";

function readSdkTranscriptPosition(sessionId: string): string | null {
  try {
    const stored = localStorage.getItem(
      SDK_TRANSCRIPT_POSITION_KEY_PREFIX + sessionId,
    );
    if (typeof stored === "string" && stored) return stored;
  } catch {}
  return null;
}

function writeSdkTranscriptPosition(sessionId: string, orderKey: string): void {
  if (!orderKey) return;
  try {
    localStorage.setItem(
      SDK_TRANSCRIPT_POSITION_KEY_PREFIX + sessionId,
      orderKey,
    );
  } catch {}
}

function clearSdkTranscriptPosition(sessionId: string): void {
  try {
    localStorage.removeItem(SDK_TRANSCRIPT_POSITION_KEY_PREFIX + sessionId);
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
  next.activity = activity;
  // Defend against degraded snapshots (older server, infoFromPod
  // fallback, hand-rolled JSON in tests): repos must always be an
  // array so downstream renderers can `.map` without a guard.
  next.repos = Array.isArray(session.repos) ? session.repos : [];
  next.clone_state = session.clone_state ?? null;
  return next;
}

function sessionOrderStorageKey(user: SessionUser): string {
  return `${SESSION_ORDER_KEY_PREFIX}.${user.sub}`;
}

function readSessionOrder(key: string): string[] {
  try {
    const stored = localStorage.getItem(key);
    const parsed: unknown = stored ? JSON.parse(stored) : [];
    if (Array.isArray(parsed)) {
      return parsed.filter((id): id is string => typeof id === "string");
    }
  } catch {
    // Ordering persistence is best-effort; server order is still usable.
  }
  return [];
}

function writeSessionOrder(key: string, order: string[]): void {
  try {
    localStorage.setItem(key, JSON.stringify(order));
  } catch {
    // Ordering persistence is best-effort.
  }
}

function orderSessions(sessions: Session[], order: string[]): Session[] {
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
const CHAT_MODES = new Set<SessionMode>(["claude_gui", "codex_gui", "codex_app_server"]);
const CLAUDE_ROLLOUT_MODES = new Set<SessionMode>(["claude_cli", "api_key"]);
const CODEX_ROLLOUT_MODES = new Set<SessionMode>(["codex_cli"]);
const GUI_ROLLOUT_MODES = new Set<SessionMode>(["claude_gui", "codex_gui", "codex_app_server"]);
const ROLLOUT_MODES = new Set<SessionMode>([
  ...CLAUDE_ROLLOUT_MODES,
  ...CODEX_ROLLOUT_MODES,
]);
const PROVIDERS: Provider[] = ["anthropic", "codex", "pi"];


function defaultModeFor(provider: Provider, interaction: SessionInteraction): DefaultSessionMode {
  return (
    PROVIDER_INTERACTION_MODES[provider][interaction] ??
    PROVIDER_INTERACTION_MODES[provider].cli!
  );
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

interface SessionUser {
  sub: string;
  email: string;
  name: string;
  // Platform role carried in the tank-operator session JWT. `admin` and
  // `service` bypass the OnboardingWall; `user` is the standard signed-in
  // caller. auth.romaine.life mints `pending` by default but tank-operator's
  // exchange rejects that before a session JWT is ever issued.
  role: SessionRole;
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
  invalid_state: "Install link signature didn't validate. Try again.",
  missing_installation_id: "GitHub didn't send an installation id. Re-run the install.",
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

function readInitialSessionId(): string | null {
  const params = new URLSearchParams(window.location.search);
  return params.get("session");
}

function clearInitialSessionId(): void {
  const url = new URL(window.location.href);
  url.searchParams.delete("session");
  window.history.replaceState({}, "", url.toString());
}

function sessionUrl(id: string): string {
  const url = new URL(window.location.href);
  url.search = "";
  url.hash = "";
  url.searchParams.set("session", id);
  return url.toString();
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

function sessionDisplayName(session: Session): string {
  return session.name ?? defaultSessionName(session);
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

function sessionRuntimeLabel(session: Session, nowMs: number): string | null {
  if (!session.created_at) return null;
  const startedMs = Date.parse(session.created_at);
  if (!Number.isFinite(startedMs)) return null;
  return formatRuntime(nowMs - startedMs);
}

function sessionRuntimeTitle(session: Session, nowMs: number): string {
  const startedMs = session.created_at ? Date.parse(session.created_at) : NaN;
  const runtime = Number.isFinite(startedMs) ? formatRuntime(nowMs - startedMs) : "unknown";
  return `running ${runtime}`;
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

function sessionSkillStateClass(session: Session): string {
  const currentSkill = currentSessionSkillState(session.test_state, session.rollout_state);
  if (currentSkill === "test") return " is-skill-test";
  if (currentSkill === "rollout") return " is-skill-rollout";
  return "";
}

function mergeMutualSessionSkillState(incoming: Session, existing?: Session): Session {
  if (!existing) return incoming;
  if (!incoming.test_state?.active || !incoming.rollout_state?.active) return incoming;

  const existingSkill = currentSessionSkillState(existing.test_state, existing.rollout_state);
  if (existingSkill === "test") return { ...incoming, rollout_state: null };
  if (existingSkill === "rollout") return { ...incoming, test_state: null };
  return incoming;
}

function sessionBootStartMs(session: Session): number {
  const requestedMs = session.requested_at ? Date.parse(session.requested_at) : NaN;
  if (Number.isFinite(requestedMs)) return requestedMs;
  const createdMs = session.created_at ? Date.parse(session.created_at) : NaN;
  return Number.isFinite(createdMs) ? createdMs : NaN;
}

function sessionBootLabel(session: Session, nowMs: number): string | null {
  const startMs = sessionBootStartMs(session);
  if (!Number.isFinite(startMs)) return null;
  const readyMs = session.ready_at ? Date.parse(session.ready_at) : NaN;
  if (Number.isFinite(readyMs)) return formatBootTime(readyMs - startMs);
  if (session.status === "Pending") return formatBootTime(nowMs - startMs);
  return null;
}

function sessionBootTitle(session: Session, nowMs: number): string {
  const startMs = sessionBootStartMs(session);
  if (!Number.isFinite(startMs)) return "startup time unknown";
  const readyMs = session.ready_at ? Date.parse(session.ready_at) : NaN;
  if (Number.isFinite(readyMs)) {
    return `ready ${formatBootTime(readyMs - startMs)} after request`;
  }
  if (session.status === "Pending") return `starting for ${formatBootTime(nowMs - startMs)} since request`;
  return "startup time unknown";
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
// Two cadences for the shared `nowMs` clock:
//   - BOOT: 1s while any session is Pending, so the sidebar ↓ boot label
//     (second-resolution via formatBootTime) visibly counts up second by
//     second during pod launch. A coarser interval here makes the counter
//     look frozen and only "post an update when it's done loading."
//   - IDLE: 30s once nothing is booting, since the ↑ runtime label is
//     minute-resolution (formatRuntime returns "<1m" / "Nm" / "Nh" / "Nd")
//     and doesn't need second-grain ticks — pod uptime lasts minutes-to-
//     hours, so a slow tick is enough to catch minute boundaries.
const SESSION_BOOT_TICK_MS = 1_000;
const SESSION_RUNTIME_TICK_MS = 30_000;

function shiftArrowSessionDirection(event: KeyboardEvent): -1 | 1 | null {
  if (!event.shiftKey || event.altKey || event.ctrlKey || event.metaKey) return null;
  if (event.key === "ArrowUp") return -1;
  if (event.key === "ArrowDown") return 1;
  return null;
}

function isSessionShortcutEditableTarget(_target: EventTarget | null): boolean {
  return false;
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

function Avatar({ user }: { user: SessionUser }) {
  const [failed, setFailed] = useState(false);
  if (failed || !user.avatar_url) {
    return <span className="avatar" aria-hidden="true">{initials(user)}</span>;
  }
  return (
    <span className="avatar avatar-image" aria-hidden="true">
      <img src={user.avatar_url} alt="" onError={() => setFailed(true)} />
    </span>
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
      const direction = shiftArrowSessionDirection(event);
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

  function setDemoProvider(provider: Provider) {
    const interaction =
      PROVIDER_INTERACTION_MODES[provider][demoInteraction] == null
        ? "cli"
        : demoInteraction;
    setDemoInteraction(interaction);
    setSelectedProvider(provider);
  }

  function setPreviewMode(mode: SessionMode) {
    if (isDefaultSessionMode(mode)) {
      setSelectedProvider(MODE_MENU_ICONS[mode]);
      setDemoInteraction(sessionInteractionForSession({ ...DEMO_BASE_SESSIONS[0], mode }) ?? "cli");
    }
    createPreviewSession(mode);
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
              const bootLabel = sessionBootLabel(s, Date.now());
              const runtimeLabel = sessionRuntimeLabel(s, Date.now());
              const avatar = getSessionAvatar(s.id);
              return (
                <li
                  key={s.id}
                  className={isActive ? "is-open" : ""}
                  onClick={() => setActiveDemoSession(s.id)}
                >
                  <AgentAvatarIcon avatar={avatar} className="session-avatar" />
                  <div className="session-row-top">
                    <span
                      className={statusDotClass}
                      title={s.status}
                      aria-label={`status: ${s.status}`}
                    />
                    <button className="session-open" onClick={() => setActiveDemoSession(s.id)}>
                      <span className="session-id">{sessionDisplayName(s)}</span>
                    </button>
                    {(bootLabel || runtimeLabel) && (
                      <span className="session-stats">
                        {bootLabel && (
                          <span className="session-stat" title={sessionBootTitle(s, Date.now())}>
                            <span aria-hidden="true">↓</span>
                            <span>{bootLabel}</span>
                          </span>
                        )}
                        {runtimeLabel && (
                          <span className="session-stat" title={sessionRuntimeTitle(s, Date.now())}>
                            <span aria-hidden="true">↑</span>
                            <span>{runtimeLabel}</span>
                          </span>
                        )}
                      </span>
                    )}
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
                    <ModeChip mode={s.mode} interaction={sessionInteractionForSession(s)} />
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

      <main className="workspace demo-workspace">
        {activeDemoSession == null ? (
          <div className="home">
            <div className="home-inner">
              <section className="home-hero" aria-labelledby="demo-home-title">
                <div>
                  <h2 id="demo-home-title" className="home-title">What do you want to build?</h2>
                  <p className="home-sub">
                    Type below to start a session — or pick a runtime and launcher first.
                  </p>
                </div>
                <span className="home-count">{demoSessions.length} preview session{demoSessions.length === 1 ? "" : "s"}</span>
              </section>

              <div className="home-grid">
                <section className="home-panel home-panel-start" aria-labelledby="demo-home-start-title">
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
                          <span>{provider === "anthropic" ? "Claude" : provider === "codex" ? "Codex" : "Pi"}</span>
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
                  <button
                    className="home-primary-action"
                    onClick={() => createPreviewSession()}
                  >
                    <span className="home-action-icons">
                      <ProviderIcon provider={selectedProvider} className="home-provider-icon" />
                      <InteractionIcon interaction={demoInteraction} className="home-interaction-icon" />
                    </span>
                    <span>
                      <span className="home-action-title">{MODE_LABELS[selectedMode]}</span>
                      <span className="home-action-sub">{MODE_HINTS[selectedMode]}</span>
                    </span>
                  </button>
                  <div className="home-quick-actions">
                    <button className="home-quick-action" onClick={() => createPreviewSession("api_key")}>
                      <IconKey className="home-quick-icon" />
                      <span>API key</span>
                    </button>
                    <button className="home-quick-action" onClick={() => createPreviewSession(configMode)}>
                      <IconWrench className="home-quick-icon" />
                      <span>{MODE_LABELS[configMode]}</span>
                    </button>
                  </div>
                </section>

                <section className="home-panel" aria-labelledby="demo-home-modes-title">
                  <div className="home-panel-head">
                    <h3 id="demo-home-modes-title">Launchers</h3>
                  </div>
                  <div className="home-mode-list" role="list">
                    {MODE_ORDER.map((m) => (
                      <button
                        key={m}
                        className="home-mode"
                        onClick={() => setPreviewMode(m)}
                        role="listitem"
                      >
                        <ProviderIcon provider={MODE_MENU_ICONS[m]} className="home-mode-icon" />
                        <span>
                          <span className="home-mode-title">{MODE_LABELS[m]}</span>
                          <span className="home-mode-sub">{MODE_HINTS[m]}</span>
                        </span>
                      </button>
                    ))}
                  </div>
                </section>

                <section className="home-panel" aria-labelledby="demo-home-sessions-title">
                  <div className="home-panel-head">
                    <h3 id="demo-home-sessions-title">Sessions</h3>
                    <span className="home-panel-meta">{demoSessions.length} available</span>
                  </div>
                  <div className="home-session-list">
                    {demoSessions.slice(0, 6).map((s) => (
                      <button
                        key={s.id}
                        className="home-session"
                        onClick={() => setActiveDemoSession(s.id)}
                      >
                        <span className={sessionStatusDotClass(s)} />
                        <ProviderIcon provider={MODE_MENU_ICONS[s.mode]} className="home-session-icon" />
                        <span className="home-session-main">
                          <span className="home-session-title">{sessionDisplayName(s)}</span>
                          <span className="home-session-sub">{MODE_LABELS[s.mode]}</span>
                        </span>
                      </button>
                    ))}
                  </div>
                </section>
              </div>

              {/* Demo preview of the chat composer. Same `ChatComposer`
                  component the authenticated home and the run pane use;
                  submitting redirects to sign-in instead of creating a
                  session. The icon row mirrors the authenticated home so
                  the demo accurately previews the chat surface. */}
              <ChatComposer
                className="run-composer-home"
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
  const userAction = {
    id: `skill-action-${name}-${suffix}`,
    kind: "message" as const,
    role: "user" as const,
    text: skillActionText(name),
    time,
    messageKind: "skill-action",
    skillName: name,
    skillSupplementalText: supplementalText.trim(),
  } as TranscriptEntry;
  return appendMeta(
    [...entries, userAction],
    `skill-invocation-${name}-${suffix}`,
    skillInvocationTitle(name),
    undefined,
    "info",
    time,
  );
}

function eventTimelineCursor(event: JsonObject): string | null {
  return typeof event.order_key === "string" && event.order_key ? event.order_key : null;
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

function isSdkTimelineEvent(event: unknown): event is TankConversationEvent {
  return isDurableTankConversationEvent(event) && typeof event.order_key === "string" && event.order_key.length > 0;
}

function isScheduleWakeupToolName(name: string | undefined): boolean {
  return (name ?? "").toLowerCase() === "schedulewakeup";
}

function isProviderAbortMessage(message: unknown): boolean {
  return typeof message === "string" && /operation was aborted/i.test(message);
}

// eventCountsAsTailOutput mirrors store/session_events.go's
// UnreadOutputItemTypes / UnreadOutputTurnTypes — content events that
// render as new bubbles in the transcript. Lifecycle markers
// (turn.submitted / turn.started / turn.completed, the user's own
// user_message.created) are excluded so the pending-tail pill counter
// only ticks on something the user would actually want to scroll down
// to see.
function eventCountsAsTailOutput(event: unknown): boolean {
  if (!isTankConversationEvent(event)) return false;
  if (event.actor === "user") return false;
  const type = event.type;
  return (
    type === "item.started" ||
    type === "item.completed" ||
    type === "item.failed" ||
    type === "tool.approval_requested" ||
    type === "tool.approval_resolved" ||
    type === "turn.failed" ||
    type === "turn.command_failed" ||
    type === "turn.interrupted"
  );
}

function sdkTerminalResult(event: unknown): SdkTerminalResult | null {
  if (!isTankConversationEvent(event)) return null;
  const type = event.type;
  if (type === "turn.completed") {
    return { status: "done" };
  }
  if (type === "turn.interrupted") {
    return { status: "stopped" };
  }
  if (type === "turn.failed") {
    const error = event.payload?.error;
    if (isProviderAbortMessage(error)) return { status: "stopped" };
    return {
      status: "error",
      detail: typeof error === "string" ? error : shortJson(event.payload ?? event),
    };
  }
  return null;
}

function sdkHistoryTerminalForRun(
  events: unknown[],
  clientNonce: string,
): SdkTerminalResult | undefined {
  let afterRunUserMessage = false;
  for (const event of events) {
    if (!isTankConversationEvent(event)) continue;
    if (event.type === "user_message.created") {
      if (afterRunUserMessage) return undefined;
      if (event.client_nonce === clientNonce) afterRunUserMessage = true;
      continue;
    }
    if (!afterRunUserMessage) continue;
    const terminal = sdkTerminalResult(event);
    if (terminal) return terminal;
  }
  return undefined;
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
  return normalized;
}

// (formerly: transcriptClassNames slot map for AgentTranscript — gone
// now that the inline RunMessages renderer owns class names directly.)

type RunTab = "chat" | "files" | "settings" | "help";

/** A file the user picked / dropped / pasted on the home composer before
 *  a session pod exists. The `file` is kept on the object so it can be
 *  uploaded to `/api/sessions/{id}/files/upload` after the pod is Ready;
 *  `previewUrl` is a `URL.createObjectURL` blob for image thumbnails and
 *  is revoked on remove. */
interface HomePendingAttachment {
  id: string;
  file: File;
  name: string;
  size: number;
  previewUrl?: string;
}

interface ComposerAttachment {
  id: string; // local-only id for keying
  name: string;
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
  skillName?: string;
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

const INTERNAL_ABSOLUTE_HREF_PREFIXES = [
  "/api/",
  "/assets/",
  "/_",
  "/manifest.webmanifest",
];

function normalizeWorkspacePath(rawPath: string): string | null {
  let path = rawPath.trim();
  if (!path) return null;
  path = path.split(/[?#]/, 1)[0] ?? "";
  try {
    path = decodeURI(path);
  } catch {
    // Keep the raw path if it is not valid percent-encoded text.
  }
  path = path.replace(/\\/g, "/");
  if (path === "/workspace" || path === "workspace") return "";
  path = path.replace(/^\/workspace\/?/, "");
  path = path.replace(/^workspace\/+/, "");
  path = path.replace(/^\/+/, "");
  path = path.replace(/^\.\//, "");
  if (!path || path === ".") return null;
  if (path.split("/").some((seg) => seg === "..")) return null;
  return path;
}

function workspacePathFromHref(href: string | undefined): string | null {
  if (!href) return null;
  const trimmed = href.trim();
  if (!trimmed || trimmed.startsWith("#")) return null;

  if (trimmed.startsWith("file://")) {
    try {
      const url = new URL(trimmed);
      return normalizeWorkspacePath(url.pathname);
    } catch {
      return null;
    }
  }

  if (/^[a-z][a-z0-9+.-]*:/i.test(trimmed) || trimmed.startsWith("//")) {
    return null;
  }

  if (trimmed.startsWith("/")) {
    if (INTERNAL_ABSOLUTE_HREF_PREFIXES.some((prefix) => trimmed.startsWith(prefix))) {
      return null;
    }
    return normalizeWorkspacePath(trimmed);
  }

  if (trimmed.startsWith("workspace/") || trimmed.startsWith("./")) {
    return normalizeWorkspacePath(trimmed);
  }

  return null;
}

function humanFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} kB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/** Map a filename to a syntax-highlighter language hint. Streamdown
 *  pipes through Prism so common short identifiers work out of the box. */
function syntaxLangForPath(path: string): string {
  const lower = path.toLowerCase();
  const ext = lower.includes(".") ? lower.slice(lower.lastIndexOf(".") + 1) : "";
  const name = lower.slice(lower.lastIndexOf("/") + 1);
  // Special-cased filenames first.
  if (name === "dockerfile" || name.startsWith("dockerfile.")) return "dockerfile";
  if (name === "makefile") return "makefile";
  if (name === ".gitignore" || name.endsWith(".gitignore")) return "ini";
  if (name.endsWith(".env") || name === ".env") return "ini";
  // Then by extension. Limited to common cases — Prism falls back to plain
  // text on unknown lang, which is fine.
  return (
    {
      ts: "ts",
      tsx: "tsx",
      js: "js",
      jsx: "jsx",
      mjs: "js",
      cjs: "js",
      py: "python",
      rb: "ruby",
      go: "go",
      rs: "rust",
      java: "java",
      kt: "kotlin",
      cs: "csharp",
      cpp: "cpp",
      cc: "cpp",
      c: "c",
      h: "c",
      hpp: "cpp",
      sh: "bash",
      bash: "bash",
      zsh: "bash",
      fish: "bash",
      yml: "yaml",
      yaml: "yaml",
      json: "json",
      jsonc: "json",
      md: "markdown",
      mdx: "markdown",
      sql: "sql",
      html: "html",
      htm: "html",
      xml: "xml",
      svg: "xml",
      css: "css",
      scss: "scss",
      sass: "sass",
      less: "less",
      tf: "hcl",
      hcl: "hcl",
      toml: "toml",
      lua: "lua",
      php: "php",
      swift: "swift",
      dart: "dart",
      ex: "elixir",
      exs: "elixir",
      erl: "erlang",
      hs: "haskell",
      r: "r",
      scala: "scala",
      vue: "html",
      svelte: "html",
      proto: "protobuf",
      graphql: "graphql",
      gql: "graphql",
    }[ext] ?? "text"
  );
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
const STREAM_VERBS = [
  "Thinking",
  "Processing",
  "Analyzing",
  "Working",
  "Computing",
  "Reasoning",
] as const;

// Format a Claude tool name for display in the status pill.
// "Bash" → "Bash"
// "mcp__github__create_pull_request" → "github · create pull request"
function formatToolLabel(toolName: string): string {
  const stripped = toolName.replace(/^mcp__/, "");
  const [server, ...rest] = stripped.split("__");
  if (rest.length === 0) return server;
  const action = rest.join("__").replace(/_/g, " ");
  return `${server} · ${action}`;
}

// Context-window sizes per model. Used for the usage % ring. The 1M
// variant of Opus is a separate id; for everything else, 200k is the
// shipping default.
const CONTEXT_WINDOW_BY_MODEL: Record<string, number> = {
  "claude-opus-4-7-1m": 1_000_000,
  "claude-opus-4-7": 200_000,
  "claude-sonnet-4-6": 200_000,
  "claude-haiku-4-5": 200_000,
  "gpt-5": 128_000,
};

function getContextWindow(modelId: string): number {
  return CONTEXT_WINDOW_BY_MODEL[modelId] ?? 200_000;
}

interface ClaudeUsage {
  input_tokens?: number;
  output_tokens?: number;
  cache_creation_input_tokens?: number;
  cache_read_input_tokens?: number;
}

/** Current-turn context size = the input that produced this response.
 *  cache_read counts as in-context tokens that were sent for free; we
 *  include it so the gauge reflects "how full is the window" not "how
 *  many tokens were billed". */
function totalContextTokens(u: ClaudeUsage | undefined): number {
  if (!u) return 0;
  return (
    (u.input_tokens ?? 0) +
    (u.cache_creation_input_tokens ?? 0) +
    (u.cache_read_input_tokens ?? 0)
  );
}

function formatStreamElapsed(ms: number): string {
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}m ${s}s`;
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
// (claude-opus-4-7) so a fresh session lands on the strongest model by
// default. The id strings are forwarded straight to the SDK's
// options.model via the bus; tightening to an allowlist lives in the
// agent-runner's pinning code, not here, because adding a model should
// be a one-line UI change.
const CLAUDE_MODELS: ModelOption[] = [
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
  { id: "xhigh", label: "Extra High", hint: "Opus 4.7 only; ~2× tokens" },
  { id: "max", label: "Max", hint: "Opus 4.6/4.7, Sonnet 4.6 only" },
];
const DEFAULT_CLAUDE_MODEL_ID = "claude-opus-4-7";
const DEFAULT_CLAUDE_EFFORT_ID = "high";
const CODEX_EFFORTS: EffortOption[] = [
  { id: "low", label: "Low", hint: "Fast responses with lighter reasoning" },
  { id: "medium", label: "Medium", hint: "Balanced reasoning" },
  { id: "high", label: "High", hint: "Greater reasoning depth" },
  { id: "xhigh", label: "Extra High", hint: "Strongest reasoning" },
];
const DEFAULT_CODEX_MODEL_ID = "gpt-5.5";
const DEFAULT_CODEX_EFFORT_ID = "xhigh";

// Per-user run-pane preferences. localStorage-backed, shared across all
// sessions in this browser. Keys mirror cloudcli's QuickSettings.
const RUN_PREF_PREFIX = "tank-run-pref-";
const TURN_COMPLETE_SOUND_SRC = "/assets/upgrade-complete.mp3";

interface RunPrefs {
  sendByCtrlEnter: boolean;
  showThinking: boolean;
  autoExpandTools: boolean;
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
}

const DEFAULT_RUN_PREFS: RunPrefs = {
  sendByCtrlEnter: false,
  showThinking: true,
  autoExpandTools: false,
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
};

const CHAT_FONT_SCALE_MIN = 0.8;
const CHAT_FONT_SCALE_MAX = 2.0;
const CHAT_FONT_SCALE_STEP = 0.1;
const TURN_COMPLETE_SOUND_VOLUME_MIN = 0;
const TURN_COMPLETE_SOUND_VOLUME_MAX = 1;
const TURN_COMPLETE_SOUND_VOLUME_STEP = 0.05;

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
    } else if (typeof raw === "boolean") {
      (out as unknown as Record<string, unknown>)[key] = raw;
    }
  }
  return out;
}

// transcriptComparable returns a stable JSON of the transcript's load-bearing
// fields, used to short-circuit no-op replay updates. Chat sessions rebuild
// from canonical /timeline events; there is no client-side cache.
function transcriptComparable(entries: TranscriptEntry[]): string {
  return JSON.stringify(
    entries.map((entry) => {
      if (entry.kind === "message") {
        return {
          kind: entry.kind,
          id: entry.id,
          role: entry.role,
          text: entry.text,
          durationMs: (entry as Record<string, unknown>).durationMs,
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
        };
      }
      if (entry.kind === "reasoning") {
        return {
          kind: entry.kind,
          id: entry.id,
          reasoning: entry.reasoning,
        };
      }
      return {
        kind: entry.kind,
        meta: entry.meta,
      };
    }),
  );
}

function entryMessageFingerprint(entry: TranscriptEntry): string | null {
  if (entry.kind !== "message" || !entry.role || !entry.text) return null;
  const text = entry.text.trim();
  return text ? `${entry.role}:${text}` : null;
}

function entryMetaFingerprint(entry: TranscriptEntry): string | null {
  if (entry.kind !== "meta") return null;
  return [
    entry.meta?.title ?? "",
    entry.meta?.detail ?? "",
    entry.meta?.severity ?? "",
  ].join("\u0000");
}

function shouldDropRealtimeEntry(
  entry: TranscriptEntry,
  serverIds: Set<string>,
  serverEventIds: Set<string>,
  serverMetaFingerprints: Set<string>,
  serverMessageFingerprints: Set<string>,
): boolean {
  if (serverIds.has(entry.id)) return true;
  if (entry.sourceEventId && serverEventIds.has(entry.sourceEventId)) return true;
  const messageFingerprint = entryMessageFingerprint(entry);
  if (
    entry.localOnly &&
    messageFingerprint &&
    serverMessageFingerprints.has(messageFingerprint)
  ) {
    return true;
  }
  const metaFingerprint = entryMetaFingerprint(entry);
  return Boolean(
    metaFingerprint &&
      entry.localOnly &&
      serverMetaFingerprints.has(metaFingerprint),
  );
}

function pruneRealtimeEntries(
  server: TranscriptEntry[],
  realtime: TranscriptEntry[],
): TranscriptEntry[] {
  if (server.length === 0) return realtime;
  const serverIds = new Set(server.map((entry) => entry.id));
  const serverEventIds = new Set(
    server.map((entry) => entry.sourceEventId).filter((id): id is string => Boolean(id)),
  );
  const serverMetaFingerprints = new Set(
    server
      .map(entryMetaFingerprint)
      .filter((fingerprint): fingerprint is string => fingerprint !== null),
  );
  const serverMessageFingerprints = new Set(
    server
      .map(entryMessageFingerprint)
      .filter((fingerprint): fingerprint is string => fingerprint !== null),
  );
  return realtime.filter(
    (entry) =>
      !shouldDropRealtimeEntry(
        entry,
        serverIds,
        serverEventIds,
        serverMetaFingerprints,
        serverMessageFingerprints,
      ),
  );
}

function pruneLocalRealtimeEchoes(realtime: TranscriptEntry[]): TranscriptEntry[] {
  const nonLocalMessageFingerprints = new Set(
    realtime
      .filter((entry) => !entry.localOnly)
      .map(entryMessageFingerprint)
      .filter((fingerprint): fingerprint is string => fingerprint !== null),
  );
  if (nonLocalMessageFingerprints.size === 0) return realtime;
  return realtime.filter((entry) => {
    if (!entry.localOnly) return true;
    const fingerprint = entryMessageFingerprint(entry);
    return !fingerprint || !nonLocalMessageFingerprints.has(fingerprint);
  });
}

function dedupeAdjacentAssistantEchoes(entries: TranscriptEntry[]): TranscriptEntry[] {
  const out: TranscriptEntry[] = [];
  for (const entry of entries) {
    const prev = out[out.length - 1];
    if (
      prev?.kind === "message" &&
      entry.kind === "message" &&
      prev.role === "assistant" &&
      entry.role === "assistant" &&
      prev.text?.trim() &&
      prev.text.trim() === entry.text?.trim()
    ) {
      out[out.length - 1] = entry.transcriptSource === "server" ? entry : prev;
      continue;
    }
    out.push(entry);
  }
  return out;
}

function mergeSdkTranscript(
  server: TranscriptEntry[],
  realtime: TranscriptEntry[],
): TranscriptEntry[] {
  if (realtime.length === 0) return server;
  const extra = pruneRealtimeEntries(server, realtime);
  if (server.length === 0) return dedupeAdjacentAssistantEchoes(extra);
  if (extra.length === 0) return server;
  return dedupeAdjacentAssistantEchoes([...server, ...extra]);
}

function countTranscriptMessages(entries: TranscriptEntry[]): number {
  return entries.filter((entry) => entry.kind === "message").length;
}

function orderedConversationEvents(events: TankConversationEvent[]): TankConversationEvent[] {
  return events
    .map((event, index) => ({ event, index }))
    .sort((a, b) => {
      const keyCompare = conversationEventSortKey(a.event).localeCompare(
        conversationEventSortKey(b.event),
      );
      return keyCompare !== 0 ? keyCompare : a.index - b.index;
    })
    .map((item) => item.event);
}

function conversationEventSortKey(event: TankConversationEvent): string {
  return [
    event.order_key ?? "",
    event.created_at,
    event.sequence == null ? "" : String(event.sequence).padStart(12, "0"),
    event.event_id,
  ].join("\u001f");
}

function conversationEntriesToTranscript(
  entries: ConversationViewEntry[],
): TranscriptEntry[] {
  return entries.flatMap((entry) => {
    if (entry.kind !== "message" || entry.role !== "user") {
      return [entry as TranscriptEntry];
    }
    const display = entry.display;
    if (!display || display.kind !== "skill_invocation") return [entry as TranscriptEntry];

    return appendSkillInvocation(
      [],
      display.skill_name,
      display.supplemental_text ?? "",
      entry.time,
    ).map((skillEntry, index) => ({
      ...skillEntry,
      transcriptSource: "server",
      sourceEventId: entry.sourceEventId,
      orderKey: entry.orderKey ? `${entry.orderKey}:skill:${index}` : undefined,
    }));
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
  | { kind: "message" | "reasoning" | "meta"; entry: TranscriptEntry }
  | { kind: "tools"; entries: TranscriptEntry[] };

function groupTranscriptEntries(entries: TranscriptEntry[]): EntryGroup[] {
  const groups: EntryGroup[] = [];
  let bucket: TranscriptEntry[] = [];
  const flush = () => {
    if (bucket.length) {
      groups.push({ kind: "tools", entries: bucket });
      bucket = [];
    }
  };
  for (const e of entries) {
    if (e.kind === "tool") {
      bucket.push(e);
      continue;
    }
    flush();
    if (e.kind === "message") groups.push({ kind: "message", entry: e });
    else if (e.kind === "reasoning") groups.push({ kind: "reasoning", entry: e });
    else groups.push({ kind: "meta", entry: e });
  }
  flush();
  return groups;
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
  const workspacePath = typeof props.href === "string"
    ? workspacePathFromHref(props.href)
    : null;
  return (
    <a
      {...props}
      rel={workspacePath ? undefined : "noreferrer"}
      target={workspacePath ? undefined : "_blank"}
      onClick={(e) => {
        props.onClick?.(e);
        if (e.defaultPrevented || !workspacePath) return;
        e.preventDefault();
        openWorkspacePath(workspacePath);
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
  return (
    <Streamdown
      components={RUN_MARKDOWN_COMPONENTS}
      linkSafety={{ enabled: false }}
      shikiTheme={STREAMDOWN_DARK_THEME}
    >
      {children}
    </Streamdown>
  );
}

interface InputReplyPayload {
  answers: Record<string, string[]>;
  annotations?: Record<string, { preview?: string; notes?: string }>;
}

const RunContext = createContext<{
  openWorkspacePath: (path: string) => void;
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
  sessionId,
  highlighted,
  showTimestamps,
  showDuration,
  onQuote,
  onFork,
}: {
  entry: TranscriptEntry;
  avatar: AgentAvatar;
  sessionId: string;
  highlighted: boolean;
  showTimestamps: boolean;
  showDuration: boolean;
  onQuote: (text: string, style: QuoteStyle) => void;
  onFork?: (entry: TranscriptEntry) => Promise<void>;
}) {
  const variant = entry.role === "user" ? "user" : "assistant";
  const { user } = useContext(RunContext);
  const text = entry.text ?? "";
  const messageKind = (entry as Record<string, unknown>).messageKind;
  const isSkillAction = messageKind === "skill-action";
  const skillName = (entry as Record<string, unknown>).skillName;
  const skillSupplementalText =
    typeof (entry as Record<string, unknown>).skillSupplementalText === "string"
      ? ((entry as Record<string, unknown>).skillSupplementalText as string)
      : "";
  const skillActionIcon =
    skillName === "test"
      ? FlaskConicalIcon
      : skillName === "rollout"
        ? TankIcon
        : ListChecksIcon;
  const SkillActionIcon = skillActionIcon;
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
      data-message-id={entry.id}
      data-highlight={highlighted ? "true" : undefined}
    >
      {variant === "assistant" && (
        <span className="run-msg-ai-avatar" aria-hidden="true">
          <AgentAvatarIcon avatar={avatar} className="run-msg-ai-icon" />
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
            <RunMarkdown>{text}</RunMarkdown>
          )}
        </div>
        <div
          className="run-msg-footer"
          data-always-visible={alwaysVisible ? "" : undefined}
        >
          {variant === "assistant" && onFork && (
            <ForkButton entry={entry} onFork={onFork} />
          )}
          <QuoteButton text={text} style="fence" onQuote={onQuote} />
          <QuoteButton text={text} style="blockquote" onQuote={onQuote} />
          <CopyButton text={text} />
          <LinkButton sessionId={sessionId} entryId={entry.id} />
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
      {variant === "user" && (() => {
        // Cross-session handoff: a sibling tank-operator session posted
        // this turn via mcp-tank-operator. Render the parent session's
        // deterministic avatar in place of the human owner's Gravatar
        // so the bubble reads as agent-authored. The user message is
        // still owned by the same human — only the visual identity
        // changes, mirroring how the assistant bubble already uses
        // a session-derived avatar.
        const originId = entry.originSessionId;
        if (originId) {
          return (
            <span
              className="run-msg-avatar"
              data-origin-session-id={originId}
            >
              <AgentAvatarIcon
                avatar={getSessionAvatar(originId)}
                className="run-msg-ai-icon"
              />
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
}

function parseAskUserQuestions(input: Record<string, unknown> | null): AskUserQuestion[] {
  if (!Array.isArray(input?.questions)) return [];
  return (input.questions as Array<Record<string, unknown>>).map((q) => {
    const options = Array.isArray(q.options)
      ? (q.options as Array<Record<string, unknown>>).map((opt) => ({
          label: String(opt.label ?? ""),
          description: typeof opt.description === "string" ? opt.description : undefined,
          preview: typeof opt.preview === "string" ? opt.preview : undefined,
        }))
      : [];
    return {
      question: String(q.question ?? ""),
      header: typeof q.header === "string" && q.header ? q.header : undefined,
      multiSelect: q.multiSelect === true,
      options,
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

  const questions = parseAskUserQuestions(input);

  // durableAnswers is the canonical source of truth for the answered
  // state — it comes from the `tool.approval_resolved` event's payload
  // via projection, so a fresh tab opened after the user answered (in
  // this or any other tab) still renders the selections. Local
  // `selections` state only powers the in-flight click-to-submit UX.
  const durableAnswers = entry.askUserAnswers;
  const answered =
    (durableAnswers && Object.keys(durableAnswers).length > 0) ||
    entry.toolStatus === "completed";

  if (answered) {
    return (
      <div className="run-tool-body run-tool-ask">
        {durableAnswers && Object.entries(durableAnswers).length > 0 ? (
          <ul className="run-tool-ask-answered-list">
            {Object.entries(durableAnswers).map(([question, answer]) => (
              <li key={question} className="run-tool-ask-answered-item">
                <span className="run-tool-ask-answered-question">{question}</span>
                <span className="run-tool-ask-answered-arrow"> → </span>
                <span className="run-tool-ask-answered-labels">{answer.labels.join(", ")}</span>
                {answer.notes && (
                  <span className="run-tool-ask-answered-notes"> ({answer.notes})</span>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <span className="run-tool-ask-answered">answered</span>
        )}
      </div>
    );
  }

  const isReady =
    questions.length > 0 &&
    questions.every((q) => (selections[q.question]?.length ?? 0) > 0);

  function toggleSelection(q: AskUserQuestion, label: string): void {
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
    if (submitting || !isReady) return;
    setSubmitting(true);
    setReplyError(null);
    const answers: Record<string, string[]> = {};
    const annotations: Record<string, { preview?: string; notes?: string }> = {};
    for (const q of questions) {
      const labels = selections[q.question];
      if (!labels || labels.length === 0) continue;
      answers[q.question] = labels;
      const notesText = notes[q.question]?.trim();
      const preview = q.options.find((opt) => labels.includes(opt.label))?.preview;
      const ann: { preview?: string; notes?: string } = {};
      if (preview) ann.preview = preview;
      if (notesText) ann.notes = notesText;
      if (ann.preview || ann.notes) annotations[q.question] = ann;
    }
    try {
      await sendInputReply(entry, { answers, annotations });
    } catch (err) {
      setReplyError(err instanceof Error ? err.message : String(err));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="run-tool-body run-tool-ask">
      {questions.map((q, qi) => {
        const selectedLabels = selections[q.question] ?? [];
        return (
          <div key={qi} className="run-tool-ask-question">
            {q.header && <span className="run-tool-ask-chip">{q.header}</span>}
            {q.question && <p className="run-tool-ask-text">{q.question}</p>}
            <div className="run-tool-ask-options">
              {q.options.map((opt, oi) => {
                const selected = selectedLabels.includes(opt.label);
                return (
                  <button
                    key={oi}
                    type="button"
                    className={`run-tool-ask-option${selected ? " run-tool-ask-option-selected" : ""}`}
                    aria-pressed={selected}
                    disabled={submitting}
                    onClick={() => toggleSelection(q, opt.label)}
                  >
                    <span className="run-tool-ask-option-label">
                      {q.multiSelect ? (selected ? "☑ " : "☐ ") : ""}
                      {opt.label}
                    </span>
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
                  </button>
                );
              })}
            </div>
            {selectedLabels.length > 0 && q.options.some((opt) => opt.preview) && (
              <label className="run-tool-ask-notes-label">
                <span>Notes (optional)</span>
                <textarea
                  className="run-tool-ask-notes"
                  rows={2}
                  value={notes[q.question] ?? ""}
                  disabled={submitting}
                  onChange={(e) => setNoteFor(q.question, e.target.value)}
                  placeholder="Add any context Claude should consider…"
                />
              </label>
            )}
          </div>
        );
      })}
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
  autoExpand,
}: {
  entry: TranscriptEntry;
  autoExpand: boolean;
}) {
  const [expanded, setExpanded] = useState(autoExpand || entry.toolName === "AskUserQuestion");
  const cfg = getToolVisualConfig(entry);
  const state = normalizeToolState(entry.toolStatus);
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
          onClick={() => setExpanded((e) => !e)}
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
          {state === "running" && (
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
}: {
  entries: TranscriptEntry[];
  autoExpand: boolean;
}) {
  if (entries.length === 1) {
    return (
      <div className="run-transcript-tool-single" data-slot="tool-group-single">
        <RunToolItem entry={entries[0]} autoExpand={autoExpand} />
      </div>
    );
  }
  const [open, setOpen] = useState(autoExpand);
  const runningCount = entries.filter(
    (e) => normalizeToolState(e.toolStatus) === "running",
  ).length;
  const errorCount = entries.filter(
    (e) => (e.toolStatus ?? "") === "failed" || (e.toolStatus ?? "") === "error",
  ).length;
  const summaryParts = [`${entries.length} tool calls`];
  if (runningCount > 0) {
    summaryParts.push(`${runningCount} running`);
  }
  if (errorCount > 0) {
    summaryParts.push(`${errorCount} error${errorCount === 1 ? "" : "s"}`);
  }
  const summary = summaryParts.join(" · ");
  return (
    <div
      className="run-transcript-tools"
      data-slot="tool-group"
      data-state={runningCount > 0 ? "running" : undefined}
    >
      <button
        type="button"
        className="run-transcript-tools-header"
        onClick={() => setOpen((o) => !o)}
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
        {runningCount > 0 && (
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
            <RunToolItem key={e.id} entry={e} autoExpand={autoExpand} />
          ))}
        </div>
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
function RunMessages({
  entries,
  avatar,
  sessionId,
  pendingScrollMessageId,
  onScrollConsumed,
  showThinking,
  autoExpandTools,
  showTimestamps,
  showDuration,
  onQuote,
  onFork,
  scrollParent,
  onStartReached,
  onAtBottomChange,
}: {
  entries: TranscriptEntry[];
  avatar: AgentAvatar;
  sessionId: string;
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
  showTimestamps: boolean;
  showDuration: boolean;
  onQuote: (text: string, style: QuoteStyle) => void;
  onFork?: (entry: TranscriptEntry) => Promise<void>;
  scrollParent: HTMLElement | null;
  onStartReached?: () => void;
  onAtBottomChange?: (atBottom: boolean) => void;
}) {
  const groups = useMemo(() => groupTranscriptEntries(entries), [entries]);
  const virtuosoRef = useRef<VirtuosoHandle | null>(null);
  // Highlighted entry is the bubble that should pulse after a deep-link
  // scroll. We clear it on a timer so re-renders during streaming don't
  // re-trigger the animation on entries the user is just reading.
  const [highlightedEntryId, setHighlightedEntryId] = useState<string | null>(null);
  // Track which message id we've already handled so we don't re-scroll
  // every time entries change during streaming.
  const consumedScrollIdRef = useRef<string | null>(null);
  useEffect(() => {
    const target = pendingScrollMessageId;
    if (!target) return;
    if (consumedScrollIdRef.current === target) return;
    const groupIndex = groups.findIndex((g) => {
      if (g.kind === "tools") return g.entries.some((e) => e.id === target);
      return g.entry.id === target;
    });
    if (groupIndex < 0) return; // entry not yet loaded; try again on next entries change
    consumedScrollIdRef.current = target;
    setHighlightedEntryId(target);
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
  }, [pendingScrollMessageId, groups, onScrollConsumed]);
  // computeItemKey stabilizes Virtuoso's per-item identity across renders.
  // Tool groups have no single id, so we composite the first/last child
  // ids — same group instance stays the same key as it grows during a
  // streaming turn.
  const computeKey = useCallback(
    (_index: number, g: EntryGroup) => {
      if (g.kind === "tools") {
        const head = g.entries[0]?.id ?? "tools";
        const tail = g.entries[g.entries.length - 1]?.id ?? head;
        return `tools-${head}-${tail}`;
      }
      return g.entry.id;
    },
    [],
  );
  const renderItem = useCallback(
    (_index: number, g: EntryGroup) => {
      if (g.kind === "tools") {
        return <RunToolGroup entries={g.entries} autoExpand={autoExpandTools} />;
      }
      if (g.kind === "reasoning") {
        return <RunReasoningBlock entry={g.entry} showThinking={showThinking} />;
      }
      if (g.kind === "meta") {
        return <RunMetaBlock entry={g.entry} />;
      }
      return (
        <RunMessageBubble
          entry={g.entry}
          avatar={avatar}
          sessionId={sessionId}
          highlighted={highlightedEntryId === g.entry.id}
          showTimestamps={showTimestamps}
          showDuration={showDuration}
          onQuote={onQuote}
          onFork={onFork}
        />
      );
    },
    [
      autoExpandTools,
      avatar,
      highlightedEntryId,
      onFork,
      onQuote,
      sessionId,
      showDuration,
      showThinking,
      showTimestamps,
    ],
  );
  // followOutput="smooth" keeps the user stuck to the live tail when they
  // ARE at the bottom; releases when they scroll up. Returning false from
  // followOutput's callback would let us suppress auto-scroll mid-render,
  // but the default behavior matches what ChatPane wants today (sticky
  // when at bottom; the buffered pill in ChatPane handles back-read).
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
      followOutput="smooth"
      startReached={onStartReached}
      atBottomStateChange={onAtBottomChange}
      // Render two extra screens worth above and below the viewport so
      // tool-group expansion and markdown reflow don't expose unrendered
      // gaps mid-scroll.
      overscan={{ main: 800, reverse: 800 }}
      // Default initialTopMostItemIndex is the first item; we want the
      // bottom so caught-up sessions land at the live tail. The minus-1
      // safeguards against empty arrays (Virtuoso accepts negative indices
      // as "no anchor").
      initialTopMostItemIndex={Math.max(groups.length - 1, 0)}
    />
  );
}

function ChatPane({
  session,
  visible,
  onRename,
  onSessionPatch,
  onForkMessage,
  pendingScrollMessageId,
  onScrollConsumed,
  runPrefs,
  setRunPref,
  user,
  autoRename,
  onAutoRenameConsumed,
  primeTurnCompleteSound,
  playTurnCompleteSound,
}: {
  session: Session;
  visible: boolean;
  onRename: (id: string, name: string | null) => void;
  onSessionPatch: (id: string, patch: Partial<Session>) => void;
  onForkMessage: (request: ForkSessionRequest) => Promise<void>;
  // Deep-link target the parent extracted from ?message=<id>. Only set
  // for the ChatPane whose session matches ?session=<id>; other panes
  // receive null and skip the scroll logic.
  pendingScrollMessageId?: string | null;
  onScrollConsumed?: () => void;
  runPrefs: RunPrefs;
  setRunPref: SetRunPref;
  user: SessionUser;
  autoRename: boolean;
  onAutoRenameConsumed: () => void;
  // App-owned audio: the SSE consumer in App.tsx rings on the
  // always-on /api/sessions/events stream's activity_changed events.
  // ChatPane gets these props for two narrower uses: primeTurnCompleteSound
  // on the Send-button gesture (audio-unlock backup beyond the sidebar
  // click primer in activate()), and playTurnCompleteSound for the
  // Test button in the settings panel. ChatPane no longer fires the
  // sound off chat events — see App.tsx commentary at the audio refs.
  primeTurnCompleteSound: () => void;
  playTurnCompleteSound: () => void;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[]>([]);
  const sdkServerEntriesRef = useRef<TranscriptEntry[]>([]);
  const sdkRealtimeEntriesRef = useRef<TranscriptEntry[]>([]);
  const sdkServerEventsRef = useRef<TankConversationEvent[]>([]);
  const sdkRealtimeEventsRef = useRef<TankConversationEvent[]>([]);
  const sdkConversationStateRef = useRef<ConversationReducerState>(initialConversationState);
  const sdkAssistantDurationsRef = useRef<Map<string, number>>(new Map());
  const [running, setRunning] = useState(false);
  const [editingTitle, setEditingTitle] = useState(false);
  const [editingTitleValue, setEditingTitleValue] = useState("");

  // Parent-driven auto-rename. When App sets autoRenameSessionId to this
  // session's id (freshly created, or F2 pressed), the chat-pane title
  // input opens with the current name pre-loaded. We ack via
  // onAutoRenameConsumed so the signal is single-shot and re-runs cleanly
  // on a subsequent F2.
  useEffect(() => {
    if (!autoRename) return;
    setEditingTitleValue(session.name ?? "");
    setEditingTitle(true);
    onAutoRenameConsumed();
  }, [autoRename, session.id, session.name, onAutoRenameConsumed]);
  const [runStatus, setRunStatus] = useState<LocalRunStatus>("idle");
  const [activeToolName, setActiveToolName] = useState<string | null>(null);
  const activeToolNameRef = useRef<string | null>(null);
  const activeToolUseIdRef = useRef<string | null>(null);
  const scheduledWakeupRef = useRef(false);
  // Mirrors cloudcli's ClaudeStatus idle state: persists last status text
  // after the run ends (amber/static pill) instead of vanishing.
  const [lastStatusText, setLastStatusText] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<RunTab>("chat");
  const [testState, setTestState] = useState<TestState | null>(session.test_state ?? null);
  const [rolloutState, setRolloutState] = useState<RolloutState | null>(session.rollout_state ?? null);
  const [composerMode, setComposerMode] = useState<RunComposerMode>("default");
  const isClaude = isClaudeRunMode(session.mode);
  const isCodex = isCodexRunMode(session.mode);
  const modelOptions = isClaude ? CLAUDE_MODELS : CODEX_MODELS;
  const effortOptions = isClaude ? CLAUDE_EFFORTS : CODEX_EFFORTS;
  // Seed model + effort from RunPrefs (browser-persisted). State is local
  // because the runners seal model + effort from the first submit_turn —
  // switching the dropdown after a turn has been submitted would silently
  // no-op at the pod, and the UI hides the launchpad once entries.length > 0
  // so the user can't try.
  const initialModelId = isClaude
    ? (CLAUDE_MODELS.some((opt) => opt.id === runPrefs.claudeModelId)
        ? runPrefs.claudeModelId
        : DEFAULT_CLAUDE_MODEL_ID)
    : (CODEX_MODELS.some((opt) => opt.id === runPrefs.codexModelId)
        ? runPrefs.codexModelId
        : DEFAULT_CODEX_MODEL_ID);
  const initialEffortId = isClaude
    ? (CLAUDE_EFFORTS.some((opt) => opt.id === runPrefs.claudeEffort)
        ? runPrefs.claudeEffort
        : DEFAULT_CLAUDE_EFFORT_ID)
    : (CODEX_EFFORTS.some((opt) => opt.id === runPrefs.codexEffort)
        ? runPrefs.codexEffort
        : DEFAULT_CODEX_EFFORT_ID);
  const [selectedModelId, setSelectedModelIdState] = useState<string>(initialModelId);
  const [selectedEffortId, setSelectedEffortIdState] = useState<string>(initialEffortId);
  // Persist-then-set wrappers so the dropdown's onValueChange both
  // updates local state for the active session and writes the new pick
  // into RunPrefs for the *next* session this browser opens.
  const setSelectedModelId = useCallback(
    (id: string) => {
      setSelectedModelIdState(id);
      if (isClaude && CLAUDE_MODELS.some((opt) => opt.id === id)) {
        setRunPref("claudeModelId", id);
      } else if (isCodex && CODEX_MODELS.some((opt) => opt.id === id)) {
        setRunPref("codexModelId", id);
      }
    },
    [isClaude, isCodex, setRunPref],
  );
  const setSelectedEffortId = useCallback(
    (id: string) => {
      setSelectedEffortIdState(id);
      if (isClaude && CLAUDE_EFFORTS.some((opt) => opt.id === id)) {
        setRunPref("claudeEffort", id);
      } else if (isCodex && CODEX_EFFORTS.some((opt) => opt.id === id)) {
        setRunPref("codexEffort", id);
      }
    },
    [isClaude, isCodex, setRunPref],
  );
  // Run timing — drives the streaming status pill's elapsed counter and the
  // rotating action verb / animated dots. Both refresh on a single 250ms
  // interval while running so the bar updates without a per-element timer.
  const [runStartedAt, setRunStartedAt] = useState<number | null>(null);
  const [now, setNow] = useState<number>(() => Date.now());
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
  const [fileContentLoading, setFileContentLoading] = useState(false);
  // Edit-mode bookkeeping for the file viewer.
  const [fileDraft, setFileDraft] = useState<string | null>(null);
  const [fileSaving, setFileSaving] = useState(false);
  const [fileSaveError, setFileSaveError] = useState<string | null>(null);
  const [mcpServers, setMcpServers] = useState<McpServerEntry[] | null>(null);
  const [mcpLoading, setMcpLoading] = useState(false);
  const [mcpError, setMcpError] = useState<string | null>(null);
  // Auto-scroll bookkeeping — track whether the user has scrolled away from
  // the bottom; if so, suppress auto-scroll on new entries and offer the
  // floating "scroll to bottom" button.
  const [userScrolledUp, setUserScrolledUp] = useState(false);
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
  const transcriptScrollRef = useRef<HTMLElement | null>(null);
  const transcriptScrollCallbackRef = useCallback((node: HTMLElement | null) => {
    transcriptScrollRef.current = node;
    setTranscriptScrollEl(node);
  }, []);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const sdkEventSourceRef = useRef<EventSource | null>(null);
  const historyRefreshRef = useRef<Promise<boolean> | null>(null);
  const sdkTimelineCursorRef = useRef<string | null>(null);
  const sdkLastReadSentRef = useRef<string | null>(null);
  // Windowed-transcript bookkeeping introduced when /timeline switched from
  // a 50-page forward walk to anchored reads. `sdkOldestLoadedOrderKeyRef`
  // tracks the prev-cursor of the loaded window so "Load earlier" can
  // back-paginate. `sdkFoundOldestRef` / `sdkFoundNewestRef` mirror the
  // server's found_oldest / found_newest so the UI can hide back- and
  // forward-paginate affordances at the edges of the ledger. Empty cursor
  // / unknown booleans default to "we don't know yet" — UI treats that
  // conservatively (paginate buttons remain visible until proven needed).
  const sdkOldestLoadedOrderKeyRef = useRef<string | null>(null);
  const sdkFoundOldestRef = useRef(false);
  const sdkFoundNewestRef = useRef(false);
  const [sdkFoundOldest, setSdkFoundOldest] = useState(false);
  const [sdkLoadingOlder, setSdkLoadingOlder] = useState(false);
  // Streaming-while-back-reading bookkeeping. While the user is reading
  // older context (atBottom=false), incoming SSE events get appended to
  // the window — Virtuoso correctly renders them at the bottom of the
  // virtualized list without moving the user's scroll position. The pill
  // counter surfaces those events as a clickable "N new messages below ↓"
  // affordance so the user knows the conversation moved. Cleared when
  // atBottomStateChange(true) fires. Slack/Discord ship the same pattern.
  const sdkAtBottomRef = useRef(true);
  const [sdkPendingTailCount, setSdkPendingTailCount] = useState(0);
  const sdkReadStateInFlightRef = useRef<string | null>(null);
  const sdkReadStateTimerRef = useRef<number | null>(null);
  const sessionIdRef = useRef(session.id);
  const visibleRef = useRef(visible);
  visibleRef.current = visible;
  const [sdkConnectionState, setSdkConnectionState] =
    useState<SdkConnectionState>("idle");
  const currentRunRef = useRef<{
    id: string;
    prompt: string;
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
  // Mirror of the durable projection's active turn — read, not written.
  // Run status itself comes from the projection (see applySdkProjectionToUi
  // and the turn.interrupt_requested handling in conversationReducer), not
  // from a local flag.
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
  function reduceSdkConversationState(): ConversationReducerState {
    return reduceConversationEvents(
      orderedConversationEvents([
        ...sdkServerEventsRef.current,
        ...sdkRealtimeEventsRef.current,
      ]),
      initialConversationState,
    );
  }
  function syncSdkRenderedEntries(): void {
    const state = reduceSdkConversationState();
    sdkConversationStateRef.current = state;
    const projection = projectConversationState(state);
    sdkServerEntriesRef.current = applySdkAssistantDurations(
      conversationEntriesToTranscript(projection.entries),
    );
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
    applySdkProjectionToUi(projection);
    scheduleSdkReadStateUpdate();
  }
  function applySdkProjectionToUi(
    projection: ReturnType<typeof projectConversationState>,
  ): void {
    const total = totalContextTokens(projection.lastUsage as ClaudeUsage | undefined);
    if (total > 0) setTokensUsed(total);

    const sdkActive =
      projection.runStatus === "submitted" ||
      projection.runStatus === "streaming" ||
      projection.runStatus === "needs_input" ||
      projection.runStatus === "stopping";
    activeInterruptTargetRef.current = sdkActive
      ? projection.activeClientNonce ?? projection.activeTurnId
      : null;
    if (projection.activeToolName) {
      setActiveTool(projection.activeToolName, projection.activeItemId);
    } else if (!sdkActive) {
      setActiveTool(null);
    }

    if (currentRunRef.current) {
      if (sdkActive) {
        setRunStatus(projection.runStatus === "stopping" ? "stopping" : "running");
        setRunning(true);
      }
      return;
    }

    if (sdkActive) {
      setRunStatus(projection.runStatus === "stopping" ? "stopping" : "running");
      setRunning(true);
      setRunStartedAt((startedAt) => startedAt ?? Date.now());
      setNow(Date.now());
      return;
    }

    setRunning(false);
    if (projection.runStatus === "error") {
      setRunStatus("error");
      setLastStatusText("Error");
    } else if (projection.runStatus === "stopped") {
      setRunStatus("done");
      setLastStatusText("Stopped");
    } else {
      setRunStatus((prev) => (prev === "running" ? "done" : prev));
    }
  }
  function replaceSdkServerEvents(
    serverEvents: TankConversationEvent[],
    clearRealtime = false,
  ): void {
    sdkServerEventsRef.current = serverEvents;
    if (clearRealtime) {
      sdkRealtimeEventsRef.current = [];
      sdkRealtimeEntriesRef.current = [];
    }
    syncSdkRenderedEntries();
  }
  function canClearSdkRealtime(
    serverEvents: TankConversationEvent[],
    expectedCursor: string | null,
  ): boolean {
    // Postgres writes are the durable source, but a replay query can still
    // return behind the tab's SSE cursor. Keep local entries until replay can
    // replace them without reducing the visible message transcript.
    const serverCursor = serverEvents.reduce<string | null>(
      (cursor, event) =>
        advanceTimelineCursor(
          cursor,
          eventTimelineCursor(event as unknown as JsonObject),
        ),
      null,
    );
    if (expectedCursor && (!serverCursor || serverCursor < expectedCursor)) {
      return false;
    }
    const serverProjection = projectConversationState(
      reduceConversationEvents(orderedConversationEvents(serverEvents)),
    );
    const serverEntries = conversationEntriesToTranscript(serverProjection.entries);
    const currentEntries = mergeSdkTranscript(
      sdkServerEntriesRef.current,
      sdkRealtimeEntriesRef.current,
    );
    return countTranscriptMessages(serverEntries) >= countTranscriptMessages(currentEntries);
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
  function advanceSdkTimelineCursor(event: unknown): void {
    if (!isSdkTimelineEvent(event)) return;
    sdkTimelineCursorRef.current = advanceTimelineCursor(
      sdkTimelineCursorRef.current,
      eventTimelineCursor(event as unknown as JsonObject),
    );
  }
  function scheduleSdkReadStateUpdate(): void {
    if (!visibleRef.current) return;
    if (document.visibilityState !== "visible") return;
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
    const cursor = sdkTimelineCursorRef.current;
    if (!cursor) return;
    if (sdkLastReadSentRef.current && cursor <= sdkLastReadSentRef.current) return;
    sdkReadStateInFlightRef.current = cursor;
    try {
      const res = await authedFetch(
        `/api/sessions/${encodeURIComponent(session.id)}/read-state`,
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
  function applySdkDurableEvent(event: JsonObject): void {
    if (!isTankConversationEvent(event)) return;
    advanceSdkTimelineCursor(event);
    const alreadySeen = sdkServerEventsRef.current.some(
      (candidate) => candidate.event_id === event.event_id,
    );
    if (!alreadySeen) {
      sdkServerEventsRef.current = orderedConversationEvents([
        ...sdkServerEventsRef.current,
        event,
      ]);
      // Streaming-back-read pill: count visible-output events that
      // arrive while the user isn't viewing the live tail. Lifecycle
      // markers (turn.*, tool.approval_*) shouldn't count — they don't
      // produce new bubbles, just status transitions. Match the same
      // policy the server-side UnreadOutputItemTypes uses for badging.
      if (
        !sdkAtBottomRef.current &&
        eventCountsAsTailOutput(event as unknown as JsonObject)
      ) {
        setSdkPendingTailCount((count) => count + 1);
      }
    }
    syncSdkRenderedEntries();

    const run = currentRunRef.current;
    const terminal = sdkTerminalResult(event);
    if (run && terminal && event.client_nonce === run.id) {
      finalizeSdkRun(run, terminal, { refreshHistory: false });
    }
  }
  // handleSdkAtBottomChange is the durable boolean source from Virtuoso
  // for "is the user viewing the live tail." Replaces the prior 24px
  // scrollTop hysteresis listener. Two side effects:
  //   - Mirror to userScrolledUp so the existing scroll-to-bottom button
  //     visibility CSS still works.
  //   - When transitioning to atBottom=true, clear the pending-tail
  //     pill counter — the user has now seen those events.
  function handleSdkAtBottomChange(atBottom: boolean): void {
    sdkAtBottomRef.current = atBottom;
    setUserScrolledUp(!atBottom);
    if (atBottom) setSdkPendingTailCount(0);
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
      if (sdkReadStateTimerRef.current !== null) {
        window.clearTimeout(sdkReadStateTimerRef.current);
        sdkReadStateTimerRef.current = null;
      }
    };
  }, []);

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
    if (!visible || session.status !== "Active") return;
    const touch = () => {
      void authedFetch(`/api/sessions/${session.id}/touch`, {
        method: "POST",
      }).catch(() => undefined);
    };
    touch();
    const interval = window.setInterval(touch, 30_000);
    return () => window.clearInterval(interval);
  }, [session.id, session.status, visible]);

  // Tick `now` every 250ms while running. 250 is the LCM-ish for the dot
  // animation (500ms cycle) and the elapsed counter (1s display step) —
  // one timer drives both without per-element setIntervals.
  useEffect(() => {
    if (!running) return;
    const id = window.setInterval(() => setNow(Date.now()), 250);
    return () => window.clearInterval(id);
  }, [running]);

  // Auto-send the next queued message once the current run finishes.
  useEffect(() => {
    if (!running && queuedMessages.length > 0) {
      const [nextMessage, ...remaining] = queuedMessages;
      setQueuedMessages(remaining);
      startRun(nextMessage.text, nextMessage.displayText, nextMessage.skillName);
    }
  // startRun is intentionally omitted — it's redefined each render, and
  // useEffect's closure gives us the fresh version when deps actually change.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running, queuedMessages]);

  // Scroll behavior is owned by react-virtuoso now: `followOutput="smooth"`
  // keeps the user pinned to the live tail when at-bottom, and
  // `atBottomStateChange` is the durable boolean source for
  // `setUserScrolledUp` (no more 24px hysteresis listener, no more manual
  // scrollTop=scrollHeight effect). See RunMessages for the wiring.

  // History replay is intentionally not limited to empty transcript state: a
  // run can finish while the tab is closed, leaving a stale partial transcript.
  const [historyAttempted, setHistoryAttempted] = useState(false);
  // Toggled briefly when entries are restored from backend history so we can
  // show a "Continuing previous conversation" hint.
  const [continueHintVisible, setContinueHintVisible] = useState(false);
  function refreshRunHistory(showHint: boolean) {
    if (session.status !== "Active") return;
    if (historyRefreshRef.current) return;
    const refreshSessionId = session.id;
    const refresh = refreshSdkRunHistory(showHint)
      .finally(() => {
        if (sessionIdRef.current !== refreshSessionId) return;
        if (historyRefreshRef.current === refresh) {
          historyRefreshRef.current = null;
        }
      });
    historyRefreshRef.current = refresh;
  }

  // History replay hits the canonical event log written by the pod-side
  // runner, then renders through the same reducer/projection path used for
  // live SDK frames.
  function refreshSdkRunHistory(showHint: boolean, clearRealtime = false): Promise<boolean> {
    return refreshSdkRunHistoryResult(showHint, clearRealtime).then(
      (result) => result.replayed,
    );
  }

  // refreshSdkRunHistoryResult loads the durable transcript through a
  // single anchored read instead of the prior 50-page forward walk from
  // order_key=0. The old loop was the root cause of the mid-load scroll
  // "dance": rendering as each 1000-event page arrived extended the DOM
  // under the user's eyes, so any user scroll-down latched off the
  // auto-scroll effect and the next page rendered with stale scroll
  // position. Per docs/migration-policy.md the old path is deleted, not
  // kept as a fallback.
  //
  // Anchor resolution: prefer the saved per-session position from
  // localStorage (so reopening a session lands where the user left off),
  // fall back to anchor=first_unread (server resolves to
  // last_read_order_key), final fallback to anchor=newest.
  function refreshSdkRunHistoryResult(
    showHint: boolean,
    clearRealtime = false,
    clientNonce?: string,
  ): Promise<SdkHistoryRefreshResult> {
    const refreshSessionId = session.id;
    const clearRealtimeCursor = clearRealtime ? sdkTimelineCursorRef.current : null;
    const load = async (): Promise<SdkHistoryRefreshResult> => {
      const savedAnchor = readSdkTranscriptPosition(refreshSessionId);
      const params = new URLSearchParams();
      if (savedAnchor) {
        params.set("anchor", savedAnchor);
        params.set("num_before", "100");
        params.set("num_after", "100");
      } else {
        params.set("anchor", "first_unread");
        params.set("num_before", "100");
        params.set("num_after", "100");
      }
      let res = await authedFetch(
        `/api/sessions/${encodeURIComponent(refreshSessionId)}/timeline?${params.toString()}`,
      );
      // If the saved anchor was pruned/never existed, server returns 409;
      // fall through to first_unread which never 409s (the server degrades
      // to tail when read state is empty or fully caught up).
      if (res.status === 409 && savedAnchor) {
        clearSdkTranscriptPosition(refreshSessionId);
        const fallback = new URLSearchParams({
          anchor: "first_unread",
          num_before: "100",
          num_after: "100",
        });
        res = await authedFetch(
          `/api/sessions/${encodeURIComponent(refreshSessionId)}/timeline?${fallback.toString()}`,
        );
      }
      if (!res.ok) return { replayed: false };
      const body = (await res.json()) as {
        session_id?: string;
        events?: unknown[];
        next_order_key?: string;
        prev_order_key?: string;
        has_more?: boolean;
        found_oldest?: boolean;
        found_newest?: boolean;
        read_state?: { last_read_order_key?: unknown } | null;
      };
      if (sessionIdRef.current !== refreshSessionId) return { replayed: false };
      const lastReadOrderKey = body.read_state?.last_read_order_key;
      if (typeof lastReadOrderKey === "string" && lastReadOrderKey) {
        sdkLastReadSentRef.current = advanceTimelineCursor(
          sdkLastReadSentRef.current,
          lastReadOrderKey,
        );
      }
      if (!Array.isArray(body.events)) return { replayed: false };
      const canonicalEvents: TankConversationEvent[] = [];
      for (const ev of body.events) {
        if (isTankConversationEvent(ev)) {
          advanceSdkTimelineCursor(ev);
          canonicalEvents.push(ev);
        }
      }
      // Forward cursor advances to next_order_key — the SSE stream resumes
      // from this point so we don't re-emit events already in the window.
      const nextAfter =
        typeof body.next_order_key === "string" ? body.next_order_key : "";
      if (nextAfter) {
        sdkTimelineCursorRef.current = advanceTimelineCursor(
          sdkTimelineCursorRef.current,
          nextAfter,
        );
      }
      const prevAfter =
        typeof body.prev_order_key === "string" ? body.prev_order_key : "";
      if (prevAfter) {
        sdkOldestLoadedOrderKeyRef.current = prevAfter;
      }
      const foundOldest = body.found_oldest === true;
      const foundNewest = body.found_newest === true;
      sdkFoundOldestRef.current = foundOldest;
      sdkFoundNewestRef.current = foundNewest;
      setSdkFoundOldest(foundOldest);
      const terminal = clientNonce
        ? sdkHistoryTerminalForRun(body.events, clientNonce)
        : undefined;
      if (canonicalEvents.length === 0) return { replayed: false, terminal };
      replaceSdkServerEvents(
        canonicalEvents,
        clearRealtime && canClearSdkRealtime(canonicalEvents, clearRealtimeCursor),
      );
      if (showHint) {
        setContinueHintVisible(true);
        window.setTimeout(() => setContinueHintVisible(false), 3000);
      }
      return { replayed: true, terminal };
    };
    return load().catch(() => ({ replayed: false }));
  }

  // loadSdkOlderEvents fetches one bounded page of events strictly older
  // than the current window's oldest event. Surfaced through the "Earlier
  // messages" affordance at the top of the transcript. Each click brings
  // 100 more events into the window; the UI hides the button once
  // sdkFoundOldest flips true.
  async function loadSdkOlderEvents(): Promise<void> {
    const refreshSessionId = session.id;
    const oldest = sdkOldestLoadedOrderKeyRef.current;
    if (!oldest) return;
    if (sdkFoundOldestRef.current) return;
    if (sdkLoadingOlder) return;
    setSdkLoadingOlder(true);
    try {
      const params = new URLSearchParams({
        before_order_key: oldest,
        limit: "100",
      });
      const res = await authedFetch(
        `/api/sessions/${encodeURIComponent(refreshSessionId)}/timeline?${params.toString()}`,
      );
      if (!res.ok) return;
      const body = (await res.json()) as {
        events?: unknown[];
        prev_order_key?: string;
        found_oldest?: boolean;
      };
      if (sessionIdRef.current !== refreshSessionId) return;
      if (!Array.isArray(body.events)) return;
      const olderEvents: TankConversationEvent[] = [];
      for (const ev of body.events) {
        if (isTankConversationEvent(ev)) olderEvents.push(ev);
      }
      if (olderEvents.length > 0) {
        // Merge: existing server events stay at the tail of the array;
        // older page goes to the head. orderedConversationEvents
        // sorts ASC by order_key so insertion order doesn't matter, but
        // doing a head-merge keeps the conceptual model honest.
        sdkServerEventsRef.current = orderedConversationEvents([
          ...olderEvents,
          ...sdkServerEventsRef.current,
        ]);
        syncSdkRenderedEntries();
      }
      const prevAfter =
        typeof body.prev_order_key === "string" ? body.prev_order_key : "";
      if (prevAfter) {
        sdkOldestLoadedOrderKeyRef.current = prevAfter;
      }
      const foundOldest = body.found_oldest === true;
      if (foundOldest) {
        sdkFoundOldestRef.current = true;
        setSdkFoundOldest(true);
      }
    } finally {
      setSdkLoadingOlder(false);
    }
  }

  // jumpSdkToOldest resets the window to the head of the ledger. Symmetric
  // counterpart of jumpSdkToLatest — drives the "scroll to start" floating
  // button next to scroll-to-bottom. Always refetches (anchor=oldest)
  // rather than walking the entire ledger client-side, so the round-trip
  // cost is O(limit) regardless of session length. The handler is the
  // dedicated anchor=oldest path added in handlers_session_events.go,
  // which dispatches an indexed ASC scan from the head with
  // FoundOldest=true.
  async function jumpSdkToOldest(): Promise<void> {
    const refreshSessionId = session.id;
    const params = new URLSearchParams({ anchor: "oldest", limit: "200" });
    const res = await authedFetch(
      `/api/sessions/${encodeURIComponent(refreshSessionId)}/timeline?${params.toString()}`,
    );
    if (!res.ok) return;
    const body = (await res.json()) as {
      events?: unknown[];
      next_order_key?: string;
      prev_order_key?: string;
      found_oldest?: boolean;
      found_newest?: boolean;
    };
    if (sessionIdRef.current !== refreshSessionId) return;
    if (!Array.isArray(body.events)) return;
    const canonicalEvents: TankConversationEvent[] = [];
    for (const ev of body.events) {
      if (isTankConversationEvent(ev)) {
        advanceSdkTimelineCursor(ev);
        canonicalEvents.push(ev);
      }
    }
    // Forward cursor: events newer than this page exist only if found_newest
    // is false. The SSE stream resumes at next_order_key; we don't drop the
    // SSE cursor here because the user can scroll forward into territory
    // already covered by the live tail subscriber.
    const nextAfter =
      typeof body.next_order_key === "string" ? body.next_order_key : "";
    if (nextAfter) {
      sdkTimelineCursorRef.current = advanceTimelineCursor(
        sdkTimelineCursorRef.current,
        nextAfter,
      );
    }
    const prevAfter =
      typeof body.prev_order_key === "string" ? body.prev_order_key : "";
    if (prevAfter) sdkOldestLoadedOrderKeyRef.current = prevAfter;
    // anchor=oldest always sets found_oldest=true server-side; mirror it.
    sdkFoundOldestRef.current = body.found_oldest === true;
    sdkFoundNewestRef.current = body.found_newest === true;
    setSdkFoundOldest(body.found_oldest === true);
    replaceSdkServerEvents(canonicalEvents, false);
  }

  // jumpSdkToLatest resets the window to the live tail. If the SPA never
  // back-paginated (foundNewest=true), this is a no-op fast path: the
  // existing DOM bottom is the live tail, so the caller can just scroll.
  // If the user back-paginated past the live tail, we drop the window
  // and refetch — that's the "stale tail" path mentioned in the plan.
  async function jumpSdkToLatest(): Promise<void> {
    if (sdkFoundNewestRef.current) return;
    const refreshSessionId = session.id;
    const params = new URLSearchParams({ anchor: "newest", limit: "200" });
    const res = await authedFetch(
      `/api/sessions/${encodeURIComponent(refreshSessionId)}/timeline?${params.toString()}`,
    );
    if (!res.ok) return;
    const body = (await res.json()) as {
      events?: unknown[];
      next_order_key?: string;
      prev_order_key?: string;
      found_oldest?: boolean;
      found_newest?: boolean;
    };
    if (sessionIdRef.current !== refreshSessionId) return;
    if (!Array.isArray(body.events)) return;
    const canonicalEvents: TankConversationEvent[] = [];
    for (const ev of body.events) {
      if (isTankConversationEvent(ev)) {
        advanceSdkTimelineCursor(ev);
        canonicalEvents.push(ev);
      }
    }
    const nextAfter =
      typeof body.next_order_key === "string" ? body.next_order_key : "";
    if (nextAfter) {
      sdkTimelineCursorRef.current = advanceTimelineCursor(
        sdkTimelineCursorRef.current,
        nextAfter,
      );
    }
    const prevAfter =
      typeof body.prev_order_key === "string" ? body.prev_order_key : "";
    if (prevAfter) sdkOldestLoadedOrderKeyRef.current = prevAfter;
    sdkFoundOldestRef.current = body.found_oldest === true;
    sdkFoundNewestRef.current = body.found_newest === true;
    setSdkFoundOldest(body.found_oldest === true);
    replaceSdkServerEvents(canonicalEvents, false);
  }

  useEffect(() => {
    if (historyAttempted) return;
    if (entries.length > 0) {
      setHistoryAttempted(true);
      refreshRunHistory(false);
      return;
    }
    if (session.status !== "Active") return;
    setHistoryAttempted(true);
    refreshRunHistory(true);
  // refreshRunHistory is intentionally omitted; it closes over current
  // session state and should only run for the gates above.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session.id, session.status, historyAttempted, entries.length]);

  // Files tab — fetch directory listing whenever the path changes or the
  // user opens the tab on a ready session.
  useEffect(() => {
    if (activeTab !== "files" || session.status !== "Active") return;
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
  }, [activeTab, filesPath, session.id, session.status]);

  // Selected-file content fetch.
  useEffect(() => {
    if (!selectedFile || selectedFile.text || selectedFile.binary) return;
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
  }, [selectedFile, session.id]);

  useEffect(() => {
    if (session.status !== "Active") {
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
  }, [session.id, session.status]);

  useEffect(() => {
    if (session.status !== "Active") {
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
  }, [session.id, session.status]);

  function openFileEntry(name: string, type: FileEntry["type"]) {
    const next = joinFilesPath(filesPath, name);
    if (type === "dir") {
      setFilesPath(next);
      setSelectedFile(null);
      setFileDraft(null);
      setFileSaveError(null);
      return;
    }
    // Trigger content fetch by setting a placeholder.
    setSelectedFile({ path: next, size: 0, truncated: false, text: "", binary: false });
    setFileDraft(null);
    setFileSaveError(null);
  }

  function openWorkspacePath(path: string) {
    const normalized = normalizeWorkspacePath(path);
    if (!normalized) return;
    setActiveTab("files");
    setFilesPath(parentFilesPath(normalized));
    setSelectedFile({ path: normalized, size: 0, truncated: false, text: "", binary: false });
    setFileDraft(null);
    setFileSaveError(null);
  }

  async function uploadAttachment(file: File) {
    const id = `att-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const previewUrl = file.type.startsWith("image/")
      ? URL.createObjectURL(file)
      : undefined;
    setAttachments((prev) => [
      ...prev,
      {
        id,
        name: file.name || "file",
        path: "",
        absPath: "",
        size: file.size,
        previewUrl,
        status: "uploading",
      },
    ]);
    try {
      // Raw-body upload (orchestrator image doesn't ship python-multipart).
      const res = await authedFetch(
        `/api/sessions/${session.id}/files/upload?name=${encodeURIComponent(file.name)}`,
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
                name: body.name,
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
    if (!files) return;
    for (const f of Array.from(files)) {
      // Be permissive on file type — Claude's Read tool handles many.
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
  // The cleanup also persists the user's last-viewing cursor for the
  // departing session so reopening lands at the same spot (Stage 2 chat
  // windowing).
  useEffect(() => {
    sessionIdRef.current = session.id;
    sdkServerEntriesRef.current = [];
    sdkRealtimeEntriesRef.current = [];
    sdkServerEventsRef.current = [];
    sdkRealtimeEventsRef.current = [];
    sdkConversationStateRef.current = initialConversationState;
    sdkAssistantDurationsRef.current = new Map();
    sdkTimelineCursorRef.current = null;
    sdkOldestLoadedOrderKeyRef.current = null;
    sdkFoundOldestRef.current = false;
    sdkFoundNewestRef.current = false;
    setSdkFoundOldest(false);
    setSdkLoadingOlder(false);
    sdkAtBottomRef.current = true;
    setSdkPendingTailCount(0);
    currentRunRef.current = null;
    activeInterruptTargetRef.current = null;
    setEntries([]);
    setQueuedMessages([]);
    historyRefreshRef.current = null;
    setHistoryAttempted(false);
    setContinueHintVisible(false);
    setSdkConnectionState("idle");
    setRunStatus("idle");
    setRunning(false);
    const departingSessionId = session.id;
    return () => {
      // Persist transcript position only when the user has back-paginated
      // past the live tail; otherwise the default first_unread / newest
      // anchor on reopen is already the right place to land. Save the
      // last_order_key of the loaded window — the next open uses it as
      // the anchor and EventsAround re-centers around that point.
      const newest = sdkTimelineCursorRef.current;
      if (newest && sdkFoundNewestRef.current === false) {
        writeSdkTranscriptPosition(departingSessionId, newest);
      } else {
        clearSdkTranscriptPosition(departingSessionId);
      }
    };
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

  // Esc-to-abort while streaming. Mirrors cloudcli's "ESC" kbd hint on the
  // Stop pill. Capture phase so it fires even if focus is on the textarea.
  // Skips when the slash palette is open — Esc closes the palette in that
  // case (handled below).
  useEffect(() => {
    if (!visible || !running) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !slashOpen) {
        e.preventDefault();
        cancelRun();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [running, slashOpen, visible]);

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
    // Insert the absolute /workspace path so claude can pass it directly
    // to the Read tool without re-resolving.
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
    activeToolNameRef.current = toolName;
    activeToolUseIdRef.current = toolName ? toolUseId : null;
    setActiveToolName(toolName);
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
    // Stop is a durable boundary: cancelRun POSTs and the projection
    // transitions runStatus → "stopping" off the resulting durable
    // turn.interrupt_requested event. cancelRun does not set runStatus
    // imperatively — see frontend/src/migrationPolicy.test.ts for the
    // gate that pins this invariant.
    const currentStatus = sdkConversationStateRef.current.runStatus;
    if (currentStatus === "stopping" || currentStatus === "stopped") return;
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
      setLastStatusText("Stop failed");
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

  function handleSubmit(message: PromptInputMessage) {
    const trimmed = message.text.trim();
    if (!trimmed || session.status !== "Active") return;
    // Wait until all attachments have finished uploading. If any errored
    // out, surface it but still let the run go ahead with what's ready.
    const composed = composePromptWithAttachments(trimmed);
    if (composed == null) return;
    if (running) {
      // Queue message to send once the current run finishes. PromptInput
      // clears the textarea before this callback returns, so the visible
      // queued-message list is the user's confirmation that the submit stuck.
      setQueuedMessages((prev) => [
        ...prev,
        { id: nextQueuedMessageId(), text: composed },
      ]);
      return;
    }
    startRun(composed);
  }

  function composePromptWithAttachments(trimmed: string): string | null {
    const ready = attachments.filter((a) => a.status === "ready");
    const stillUploading = attachments.some((a) => a.status === "uploading");
    if (stillUploading) return null;
    let composed = trimmed;
    if (ready.length > 0) {
      const lines = ready
        .map((a) => `- ${a.absPath}`)
        .join("\n");
      composed = `${trimmed}\n\nAttachments (use the Read tool to load):\n${lines}`;
    }
    // Clear attachment state once they've been baked into the run (or queue).
    setAttachments((prev) => {
      for (const a of prev) {
        if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
      }
      return [];
    });
    return composed;
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
    submitSkillInvocation("test", composed);
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

  function startRun(trimmed: string, displayText = trimmed, skillName?: string) {
    primeTurnCompleteSound();
    const followUp = entries.length > 0;
    const turnStart = Date.now();
    const run = {
      id: newRunId(),
      prompt: trimmed,
      skillName,
      followUp,
      model: selectedModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID ? "" : selectedModelId,
      effort: isClaude || isCodex ? selectedEffortId : "",
      permissionMode: composerMode,
      turnStart,
      submitAccepted: false,
    };
    currentRunRef.current = run;
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
          time: optimisticTime,
        } as TranscriptEntry;
      appendSdkRealtimeEntries(markLocalEntries([userEntry], run.id));
    }
    setRunStatus("running");
    setRunning(true);
    setActiveTool(null);
    setLastStatusText(null);
    setRunStartedAt(Date.now());
    setNow(Date.now());
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
        setLastStatusText("Error");
        const id = nextEntryId("sdk-submit-error");
        appendSdkRealtimeEntries(
          markLocalEntries(
            appendMeta([], id, "Submit failed", err instanceof Error ? err.message : String(err), "error"),
            id,
          ),
        );
      });
  }

  // The canonical event log is the transcript transport. Terminal events close
  // the local run state only after they arrive from the durable SSE stream.
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
      setLastStatusText(
        scheduledWakeupRef.current
          ? "Wakeup scheduled"
          : activeToolNameRef.current
            ? `Used ${formatToolLabel(activeToolNameRef.current)}`
            : "Done",
      );
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
      setLastStatusText("Stopped");
      setRunStatus("done");
    } else {
      setLastStatusText(activeToolNameRef.current ? `Used ${formatToolLabel(activeToolNameRef.current)}` : "Error");
      setRunStatus("error");
    }
    scheduledWakeupRef.current = false;
    setActiveTool(null);
    setRunning(false);
    setSdkConnectionState("idle");
    if (options.refreshHistory ?? false) {
      void refreshSdkRunHistory(false, options.clearRealtime ?? false);
    }
  }

  function openSdkEventStream(): EventSource {
    setSdkConnectionState("connecting");
    const params = new URLSearchParams();
    if (sdkTimelineCursorRef.current) {
      params.set("last_order_key", sdkTimelineCursorRef.current);
    }
    const query = params.toString();
    const source = new EventSource(
      `/api/sessions/${encodeURIComponent(session.id)}/events${query ? `?${query}` : ""}`,
      { withCredentials: true },
    );
    sdkEventSourceRef.current = source;
    source.addEventListener("ready", () => {
      setSdkConnectionState("connected");
    });
    source.addEventListener("tank-event", (event) => {
      const message = event as MessageEvent;
      let parsed: unknown;
      try {
        parsed = JSON.parse(String(message.data));
      } catch {
        return;
      }
      if (!isJsonObject(parsed)) return;
      applySdkDurableEvent(parsed);
    });
    source.addEventListener("resync_required", () => {
      source.close();
      if (sdkEventSourceRef.current === source) sdkEventSourceRef.current = null;
      setSdkConnectionState("resyncing");
      sdkTimelineCursorRef.current = null;
      void refreshSdkRunHistoryResult(false, true).finally(() => {
        if (sessionIdRef.current !== session.id) return;
        sdkEventSourceRef.current?.close();
        sdkEventSourceRef.current = openSdkEventStream();
      });
    });
    source.addEventListener("stream-error", () => {
      setSdkConnectionState("connection_lost");
    });
    source.onerror = () => {
      setSdkConnectionState("connection_lost");
    };
    return source;
  }

  useEffect(() => {
    if (!visible || session.status !== "Active") return;
    sdkEventSourceRef.current?.close();
    sdkEventSourceRef.current = openSdkEventStream();
    return () => {
      sdkEventSourceRef.current?.close();
      sdkEventSourceRef.current = null;
    };
  // openSdkEventStream closes over the current session cursor and reducer state.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, session.id, session.status]);

  const submitStatus =
    runStatus === "running" || runStatus === "stopping"
      ? "streaming"
      : runStatus === "error"
        ? "error"
        : undefined;

  const provider: Provider = isClaude ? "anthropic" : "codex";
  const sessionAvatar = useMemo(() => getSessionAvatar(session.id), [session.id]);
  const modeLabel = MODE_LABELS[session.mode];
  const ready = session.status === "Active";
  const currentSkillState = currentSessionSkillState(testState, rolloutState);
  const testActionActive = currentSkillState === "test";
  const rolloutActionActive = currentSkillState === "rollout";
  const selectedModel =
    modelOptions.find((m) => m.id === selectedModelId) ?? modelOptions[0];
  // Derived label for the launchpad's "Ready to use" status line; falls
  // back to "High" rather than empty when the persisted value drifted
  // out of the allowlist so the status line never reads as "...
  // effort.". The provider default resolution mirrors the runner fallback
  // so the SPA and pod show the same thing.
  const selectedEffortLabel =
    (effortOptions.find((e) => e.id === selectedEffortId)?.label) ??
    (effortOptions.find((e) => e.id === (isClaude ? DEFAULT_CLAUDE_EFFORT_ID : DEFAULT_CODEX_EFFORT_ID))?.label ??
      "High");
  const contextWindow = getContextWindow(selectedModel.id);
  const usagePct = Math.min(100, (tokensUsed / contextWindow) * 100);
  const usageLevel = usagePct >= 75 ? "high" : usagePct >= 50 ? "mid" : "low";

  const focusComposerTextarea = useCallback((): boolean => {
    const textarea = composerWrapRef.current?.querySelector("textarea") as HTMLTextAreaElement | null;
    if (!textarea) return false;
    textarea.focus();
    const cursor = textarea.value.length;
    textarea.setSelectionRange(cursor, cursor);
    return true;
  }, []);

  useEffect(() => {
    if (!visible || activeTab !== "chat" || !pendingComposerFocusRef.current) return;
    pendingComposerFocusRef.current = false;
    requestAnimationFrame(() => {
      focusComposerTextarea();
    });
  }, [activeTab, focusComposerTextarea, visible]);

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

  // ⌘K / Ctrl+K opens the model picker on the empty state. Mirrors
  // cloudcli's keyboard shortcut hint.
  useEffect(() => {
    if (activeTab !== "chat") return;
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        // Open the trigger by clicking it — the dropdown manages its
        // own open state internally via Radix.
        const trigger = composerWrapRef.current?.parentElement?.querySelector(
          ".run-provider-card",
        ) as HTMLButtonElement | null;
        trigger?.click();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [activeTab]);

  // Streaming-pill computeds — only meaningful while running.
  const elapsedMs = runStartedAt != null ? Math.max(0, now - runStartedAt) : 0;
  const elapsedLabel = formatStreamElapsed(elapsedMs);
  const dotPhase = Math.floor(now / 500) % 3; // 0..2
  const dots = ".".repeat(dotPhase + 1);
  const isStopping = runStatus === "stopping";
  // When a tool call is in flight, show its name. Otherwise cycle the
  // generic verbs every 3s (matches cloudcli's ClaudeStatus pattern).
  const verbIndex = Math.floor(now / 3000) % STREAM_VERBS.length;
  const verb = isStopping
    ? "Stopping"
    : activeToolName
    ? `Using ${formatToolLabel(activeToolName)}`
    : STREAM_VERBS[verbIndex];
  const connectionLabel = sdkConnectionLabel(sdkConnectionState);

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
  }

  const toggleRunTab = (tab: Exclude<RunTab, "chat">) => {
    setActiveTab((current) => (current === tab ? "chat" : tab));
  };

  return (
    <RunContext.Provider value={{ openWorkspacePath, sendInputReply, user }}>
    <WorkspaceShell
      style={chatFontScaleStyle}
      bodyClassName={`run-main-${runStatus}`}
      bodyRef={transcriptScrollCallbackRef}
      composerVisible={activeTab === "chat"}
      composerWrapRef={composerWrapRef}
      composerWrapStyle={chatFontScaleStyle}
      composerWrapClassName={dragActive ? "run-composer-wrap-drag" : ""}
      onComposerWrapDragOver={(e) => {
        e.preventDefault();
        if (!dragActive) setDragActive(true);
      }}
      onComposerWrapDragLeave={(e) => {
        if (e.currentTarget === e.target) setDragActive(false);
      }}
      onComposerWrapDrop={(e) => {
        e.preventDefault();
        setDragActive(false);
        handleAttachmentFiles(e.dataTransfer?.files ?? null);
      }}
      onComposerWrapPaste={(e) => {
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
      title={(<>
          {editingTitle ? (
            <input
              className="run-header-name-input"
              value={editingTitleValue}
              autoFocus
              onChange={(e) => setEditingTitleValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  const trimmed = editingTitleValue.trim();
                  const nextName = trimmed === "" ? null : trimmed;
                  onRename(session.id, nextName);
                  setEditingTitle(false);
                } else if (e.key === "Escape") {
                  setEditingTitle(false);
                }
              }}
              onBlur={() => {
                const trimmed = editingTitleValue.trim();
                const nextName = trimmed === "" ? null : trimmed;
                onRename(session.id, nextName);
                setEditingTitle(false);
              }}
              placeholder={defaultSessionName(session)}
              maxLength={80}
            />
          ) : (
            <button
              className="run-header-name-btn"
              title={session.name ? `${defaultSessionName(session)} — click to rename` : "click to rename"}
              onClick={() => {
                setEditingTitleValue(session.name ?? "");
                setEditingTitle(true);
              }}
            >
              {sessionDisplayName(session)}
            </button>
          )}
      </>)}
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
          <button
            type="button"
            className={`run-tab${activeTab === "files" ? " run-tab-active" : ""}`}
            onClick={() => toggleRunTab("files")}
            aria-pressed={activeTab === "files"}
            title="Browse files in /workspace"
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
                  <div className="run-files-viewer-image-wrap">
                    <img
                      className="run-files-viewer-image"
                      alt={selectedFile.path}
                      src={`/api/sessions/${session.id}/files/raw?path=${encodeURIComponent(selectedFile.path)}`}
                    />
                  </div>
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
                        unread tail. Read-only highlighted view when the
                        user hasn't started editing yet (fileDraft==null);
                        switches to the textarea on first focus. */}
                    {selectedFile.truncated ? (
                      <div className="run-files-viewer-content">
                        <Streamdown linkSafety={{ enabled: false }} shikiTheme={STREAMDOWN_DARK_THEME}>
                          {`\`\`\`${syntaxLangForPath(selectedFile.path)}\n${selectedFile.text}\n\`\`\``}
                        </Streamdown>
                      </div>
                    ) : fileDraft == null ? (
                      <div
                        className="run-files-viewer-content run-files-viewer-readonly"
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
                        <Streamdown linkSafety={{ enabled: false }} shikiTheme={STREAMDOWN_DARK_THEME}>
                          {`\`\`\`${syntaxLangForPath(selectedFile.path)}\n${selectedFile.text}\n\`\`\``}
                        </Streamdown>
                      </div>
                    ) : (
                      <textarea
                        className="run-files-viewer-content run-files-viewer-editor"
                        value={fileDraft}
                        onChange={(e) => setFileDraft(e.target.value)}
                        spellCheck={false}
                        autoFocus
                      />
                    )}
                  </>
                )}
              </div>
            </div>
          </div>
        ) : !ready ? (
          <div className="run-empty">
            <Loader2Icon size={20} className="run-spin" aria-hidden="true" />
            <span className="run-muted">waiting for session pod…</span>
          </div>
        ) : activeTab === "settings" ? (
          <div className="run-settings-screen">
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
                <label className="run-settings-label" htmlFor={`turn-sound-volume-${session.id}`}>
                  Volume
                </label>
                <div className="run-settings-sound-controls">
                  <input
                    id={`turn-sound-volume-${session.id}`}
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
              {/* Chat-app convention: Mattermost/Element suppress the
                  ping while you're actively viewing the channel; Zulip
                  doesn't. Our default is on (ring regardless) because
                  the sound here is a state-transition chime, not a
                  message-arrival ping — but users who prefer the
                  chat-app convention can flip it off. */}
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
          </div>
        ) : activeTab === "help" ? (
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
          </div>
        ) : entries.length === 0 ? (
          <div className="run-empty run-empty-launchpad">
            <h3 className="run-empty-title">Choose Your AI Assistant</h3>
            <p className="run-empty-sub">Select a provider to start a new conversation</p>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button type="button" className="run-provider-card">
                  <span className="run-provider-icon">
                    <ProviderIcon provider={provider} />
                  </span>
                  <span className="run-provider-meta">
                    <span className="run-provider-name">{selectedModel.label}</span>
                    <span className="run-provider-sub">Click to change model</span>
                  </span>
                  <ChevronDownIcon className="run-provider-chevron" aria-hidden="true" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="center" className="run-model-menu">
                <DropdownMenuLabel>Model</DropdownMenuLabel>
                <DropdownMenuRadioGroup
                  value={selectedModelId}
                  onValueChange={setSelectedModelId}
                >
                  {modelOptions.map((opt) => (
                    <DropdownMenuRadioItem key={opt.id} value={opt.id}>
                      {opt.label}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
                {(isClaude || isCodex) && (
                  <>
                    <DropdownMenuLabel>Effort</DropdownMenuLabel>
                    <DropdownMenuRadioGroup
                      value={selectedEffortId}
                      onValueChange={setSelectedEffortId}
                    >
                      {effortOptions.map((opt) => (
                        <DropdownMenuRadioItem key={opt.id} value={opt.id}>
                          {opt.label}
                          {opt.hint ? (
                            <span className="run-model-menu-hint"> — {opt.hint}</span>
                          ) : null}
                        </DropdownMenuRadioItem>
                      ))}
                    </DropdownMenuRadioGroup>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
            <p className="run-empty-status">
              Ready to use {selectedModel.label}
              {isClaude || isCodex ? ` · ${selectedEffortLabel} effort` : ""}. Start typing your message below.
            </p>
            <p className="run-empty-kbd">
              Press <kbd>⌘K</kbd> to switch model
            </p>
            {(isClaude || isCodex) && (
              <p className="run-empty-lock-hint">
                Model and effort are fixed for this session once you send the first message.
              </p>
            )}
          </div>
        ) : (
          <>
            {/* Passive top-of-transcript indicator. Replaces the prior
                explicit "Load earlier messages" button: the auto-load on
                Virtuoso's startReached (wired below) is the affordance now,
                matching Slack and Discord. Three states:
                  - sdkLoadingOlder: spinner-ish "Loading earlier messages…"
                    surfaced while the back-paginate fetch is in flight so
                    the user has feedback that the silent scroll-up triggered
                    something.
                  - sdkFoundOldest: a "Beginning of conversation" divider so
                    the user can tell they've hit the head of the ledger
                    instead of just a scroll-stop with no explanation.
                  - otherwise: nothing — older content sits virtualized just
                    above the viewport and reveals itself as the user scrolls.
                Both states are status, not actions; the SDk loading marker
                gets role="status" so screenreaders announce it. */}
            {sdkLoadingOlder ? (
              <div
                className="run-transcript-load-older run-transcript-load-older-passive"
                role="status"
                aria-live="polite"
              >
                Loading earlier messages…
              </div>
            ) : sdkFoundOldest && entries.length > 0 ? (
              <div
                className="run-transcript-beginning"
                role="status"
                aria-label="Beginning of conversation"
              >
                <span className="run-transcript-beginning-rule" aria-hidden="true" />
                <span className="run-transcript-beginning-label">
                  Beginning of conversation
                </span>
                <span className="run-transcript-beginning-rule" aria-hidden="true" />
              </div>
            ) : null}
            {continueHintVisible && (
              <div className="run-continue-hint" role="status">
                Continuing previous conversation
              </div>
            )}
            <RunMessages
              entries={entries}
              avatar={sessionAvatar}
              sessionId={session.id}
              pendingScrollMessageId={pendingScrollMessageId}
              onScrollConsumed={onScrollConsumed}
              showThinking={runPrefs.showThinking}
              autoExpandTools={runPrefs.autoExpandTools}
              showTimestamps={runPrefs.showTimestamps}
              showDuration={runPrefs.showDuration}
              onQuote={appendQuotedMessage}
              onFork={(forkedEntry) =>
                onForkMessage({
                  sourceSession: session,
                  forkedEntry,
                  model:
                    selectedModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID
                      ? ""
                      : selectedModelId,
                  // Fork inherits the source pane's effort pick so the
                  // forked pod boots with the same reasoning depth the
                  // user had been working at.
                  effort: isClaude || isCodex ? selectedEffortId : "",
                  permissionMode: composerMode,
                })
              }
              scrollParent={transcriptScrollEl}
              onStartReached={() => {
                void loadSdkOlderEvents();
              }}
              onAtBottomChange={handleSdkAtBottomChange}
            />
          </>
        )}
      </>)}
      floatingBetweenBodyAndComposer={(<>
      {/* Streaming status pill — pinned between transcript and composer
          while the run is in flight. Provider icon, rotating verb +
          animated dots, elapsed counter, Stop button with ESC hint. */}
      {activeTab === "chat" && (running || lastStatusText !== null) && (
        <div
          className={`run-status-bar${!running ? " run-status-bar-idle" : ""}`}
          role="status"
          aria-live="polite"
        >
          <span className="run-status-icon">
            <AgentAvatarIcon avatar={sessionAvatar} className="run-status-avatar" />
          </span>
          <span className="run-status-text">
            <span className="run-status-verb">{running ? verb : lastStatusText}</span>
            {running && (
              <span className="run-status-dots" aria-hidden="true">
                {dots}
              </span>
            )}
          </span>
          {connectionLabel && (
            <span className="run-status-connection">{connectionLabel}</span>
          )}
          {running && (
            <>
              <span className="run-status-elapsed" title="elapsed">
                {elapsedLabel}
              </span>
              <button
                type="button"
                className="run-status-stop"
                onClick={cancelRun}
                disabled={isStopping}
                aria-label={isStopping ? "Stopping generation" : "Stop generating"}
              >
                <SquareIcon className="run-status-stop-icon" aria-hidden="true" />
                <span>{isStopping ? "Stopping" : "Stop"}</span>
                {!isStopping && <kbd className="run-status-kbd">ESC</kbd>}
              </button>
            </>
          )}
        </div>
      )}

      {/* Floating jump-to-start button — symmetric with jump-to-latest.
          Slack/Discord ship the pair: ↑ takes you to the very first
          message of the session (anchor=oldest), ↓ takes you back to the
          live tail. Visible when the user isn't already at the head AND
          the ledger isn't tiny enough that "start" is already on screen
          (loaded window doesn't include the oldest event yet). Hidden
          while scrolled-to-bottom on a fresh session so the at-tail UI
          isn't cluttered. Sits above the scroll-to-bottom button. */}
      {activeTab === "chat" && entries.length > 0 && !sdkFoundOldest && userScrolledUp && (
        <button
          type="button"
          className="run-scroll-to-top"
          onClick={() => {
            const reachOldest = async () => {
              await jumpSdkToOldest();
              // Don't clear sdkPendingTailCount — those events are still
              // unread, just no longer "below" the user; the pill on the
              // scroll-to-bottom button stays informative.
              requestAnimationFrame(() => {
                const main = transcriptScrollRef.current;
                if (main) main.scrollTop = 0;
              });
            };
            void reachOldest();
          }}
          aria-label="Scroll to beginning of conversation"
        >
          <ArrowUpIcon size={16} strokeWidth={2.2} aria-hidden="true" />
        </button>
      )}

      {/* Floating scroll-to-bottom button — fades in when the user has
          scrolled away from the live tail (atBottom=false). When new
          events have streamed in during a back-read the button shows
          "N new" so the user knows the conversation moved (Slack /
          Discord pattern). Click reaches the live tail in one round-trip
          — refetching the tail if the user back-paginated past it. */}
      {activeTab === "chat" && entries.length > 0 && (
        <button
          type="button"
          className={`run-scroll-to-bottom${
            userScrolledUp ? "" : " run-scroll-to-bottom-hidden"
          }${sdkPendingTailCount > 0 ? " run-scroll-to-bottom-pending" : ""}`}
          onClick={() => {
            const reachNewest = async () => {
              await jumpSdkToLatest();
              setUserScrolledUp(false);
              setSdkPendingTailCount(0);
              // Belt-and-suspenders: scroll on the next frame in case
              // Virtuoso's followOutput hasn't repositioned yet.
              requestAnimationFrame(() => {
                const main = transcriptScrollRef.current;
                if (main) main.scrollTop = main.scrollHeight;
              });
            };
            void reachNewest();
          }}
          aria-label={
            sdkPendingTailCount > 0
              ? `${sdkPendingTailCount} new messages below`
              : "Scroll to latest"
          }
        >
          <ArrowDownIcon size={16} strokeWidth={2.2} aria-hidden="true" />
          {sdkPendingTailCount > 0 && (
            <span className="run-scroll-to-bottom-count">
              {sdkPendingTailCount > 99 ? "99+" : sdkPendingTailCount}
            </span>
          )}
        </button>
      )}

      </>)}
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
                  title={a.errorMsg ?? a.name}
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
                  <span className="run-composer-chip-name">{a.name}</span>
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
                    aria-label={`Remove ${a.name}`}
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
            placeholder={`Type / for commands, @ for files, or ask ${modeLabel} anything...`}
            onSubmit={(args) => handleSubmit({ text: args.text, files: [] })}
            permissionMode={composerMode}
            onPermissionModeChange={setComposerMode}
            sendByCtrlEnter={runPrefs.sendByCtrlEnter}
            hintSuffix=" · / for slash commands"
            disabled={!ready}
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
                  onClick={() => fileInputRef.current?.click()}
                >
                  <ImageIcon className="run-composer-icon" aria-hidden="true" />
                </button>
                <span
                  className="run-usage-ring"
                  aria-label={`Context usage: ${usagePct.toFixed(1)}%`}
                  title={`${tokensUsed.toLocaleString()} / ${contextWindow.toLocaleString()} tokens`}
                  data-level={usageLevel}
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
                  <span className="run-usage-ring-text">
                    {usagePct.toFixed(usagePct < 10 ? 1 : 0)}%
                  </span>
                </span>
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
  const client = useMemo(
    () =>
      new SandboxAgent({
        baseUrl: `${location.origin}/api/sessions/${session.id}/sandbox-agent`,
        token: getStoredToken() ?? undefined,
        skipHealthCheck: true,
      }),
    [session.id],
  );

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
        if (!cancelled) setProcessId(body.process_id);
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
  return (
    <section className="run-panel">
      <header className="run-header">
        <div className="run-title-block">
          <div className="run-title-row">
            <span className="run-provider-mark">
              <ProviderIcon provider={MODE_MENU_ICONS[session.mode]} className="run-provider-icon" />
            </span>
            <h2 className="run-title">{sessionDisplayName(session)}</h2>
          </div>
          <p className="run-subtitle">{MODE_LABELS[session.mode]}</p>
        </div>
      </header>
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
  const [sessions, setSessions] = useState<Session[]>([]);
  const [nowMs, setNowMs] = useState(() => Date.now());
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [active, setActive] = useState<string | null>(null);
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

  // Reflect the active session in the URL so reloads land back on it.
  // Mirrors cloudcli's URL-tracking behaviour. Done as an effect rather
  // than wrapping setActive so all call sites benefit.
  useEffect(() => {
    const url = new URL(window.location.href);
    if (active) {
      url.searchParams.set("session", active);
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
  // The home composer's permission-mode pick. Carries into the first turn
  // when the user types a prompt and presses Enter from the home screen,
  // so the choice they made on the launch surface persists into the live
  // session's run pane (which uses its own per-session composerMode state).
  const [homeComposerMode, setHomeComposerMode] =
    useState<RunComposerMode>("default");
  const [homeSessionName, setHomeSessionName] = useState("");
  const [homeEditingTitle, setHomeEditingTitle] = useState(false);
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
      ...list.map((file) => ({
        id: `home-att-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
        file,
        name: file.name || "file",
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
  const [homeDragActive, setHomeDragActive] = useState(false);
  // Splash-page repo picker state. Stage 1 of the auto-clone feature:
  //
  //   - selectedRepos: the chips the user has staged for the
  //     about-to-be-created session. Posted to /api/sessions on
  //     create; cleared after a successful create so the next
  //     session starts empty.
  //   - recentRepos: GET /api/github/recent-repos result for this
  //     user. The picker's "Recent" section reads from here. Stays
  //     empty (and the section hidden) when the user has never
  //     selected a repo before — no error state, just absence.
  //   - repoPickerOpen: tri-state — closed by default; opens on
  //     "+ Add repo" click; closes on outside-click / Esc / explicit
  //     close.
  //   - repoInput: the manual-entry text field's controlled value.
  //
  // Stage 2 will widen the picker with an "All repos" section sourced
  // from /api/github/repos; until then the manual text input is the
  // escape hatch for first-use repos.
  const [selectedRepos, setSelectedRepos] = useState<string[]>([]);
  const [recentRepos, setRecentRepos] = useState<string[]>([]);
  const [repoPickerOpen, setRepoPickerOpen] = useState(false);
  const [repoInput, setRepoInput] = useState("");
  const [repoError, setRepoError] = useState<string | null>(null);
  // Stage 2: All-repos lazy-load state. Sourced from /api/github/repos,
  // which proxies to mcp-github via an on-behalf-of token mint. The
  // picker calls onLoadAllRepos on first open; this state is the
  // result. Refreshed after a successful session create so just-used
  // repos float in the list next time the splash opens.
  const [allRepos, setAllRepos] = useState<{
    status: "idle" | "loading" | "ready" | "error";
    repos: string[];
    error?: string | null;
  }>({ status: "idle", repos: [] });
  // When non-null, the chat pane for this session id auto-opens its title
  // rename input on its next render. Used to make freshly-created sessions
  // land directly in the chat pane with the title editor focused, and to
  // wire the F2 keyboard shortcut to the same surface. Cleared by ChatPane
  // via onAutoRenameConsumed once it has applied the signal.
  const [autoRenameSessionId, setAutoRenameSessionId] = useState<string | null>(null);
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
  const glimmungLaunchContext = useRef<GlimmungLaunchContext | null>(
    readGlimmungLaunchContext()
  );

  useEffect(() => {
    bootstrapAuth()
      .then((u) => {
        setUser(u);
        setBooted(true);
      })
      .catch((e) => {
        setAuthError(errorMessage(e));
        setBooted(true);
      });
  }, []);

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

  // Stage 2: fetch the full installation repo list. Lazy-loaded on
  // first picker open and refreshed after a successful session create
  // so just-installed repos appear in the list next time.
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
  // that doesn't support repos, AND clear any staged selection so a
  // mode flip on the splash can't leave behind chips that would 400
  // the create call.
  useEffect(() => {
    if (!REPO_SUPPORTED_MODES.has(defaultSessionMode)) {
      if (selectedRepos.length > 0) setSelectedRepos([]);
      if (repoPickerOpen) setRepoPickerOpen(false);
      if (repoError) setRepoError(null);
    }
  }, [defaultSessionMode, selectedRepos.length, repoPickerOpen, repoError]);

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
  // session.activity_changed lifecycle event); the prior activity-poll
  // endpoint is gone (tank-operator#83). Steady-state updates after the
  // initial snapshot flow through the typed SSE stream; refresh() is
  // only used for first-load and post-resync reseeding.
  // rowToSession projects one SessionStore row back into the
  // SPA's Session shape for React-state consumption. The wire row's
  // fields align one-for-one with Session's so this is mostly a
  // field-copy with type coercions for the optional fields.
  function rowToSession(row: SessionRow): Session {
    return {
      id: row.id,
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
      activity: undefined, // activities live in the parallel sessionActivities map
      // repos is always an array on the wire (see backend
      // sessioncontroller.MarshalRowUpdate). The fallback here is
      // for older snapshots that pre-date the column — they'll
      // get an empty array and render with no chips.
      repos: Array.isArray(row.repos) ? row.repos : [],
      clone_state: (row.clone_state as Record<string, unknown> | undefined) ?? null,
    };
  }

  // infoJSONToSessionRow converts one item from GET /api/sessions
  // into the wire-shape row the SessionStore caches. Field names align
  // one-for-one with the backend rowWireShape (Phase 3) so this is a
  // copy with snapshot-only defaults (visible=true; session_scope
  // unused at render).
  function infoJSONToSessionRow(raw: any): SessionRow {
    return {
      id: String(raw.id ?? ""),
      owner: String(raw.owner ?? ""),
      mode: String(raw.mode ?? "claude_gui"),
      session_scope: "default",
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
      row_version: typeof raw.row_version === "number" ? raw.row_version : 0,
    };
  }

  async function refresh() {
    try {
      const res = await authedFetch("/api/sessions");
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
      );
      logSessionListSnapshot({
        tip: snapshotCursor,
        sessionCount: listed.length,
        source: "initial",
      });
      setSessions((prev) => {
        const previousById = new Map(prev.map((session) => [session.id, session]));
        const merged = listed.map((session) =>
          mergeMutualSessionSkillState(session, previousById.get(session.id)),
        );
        return user ? orderSessions(merged, readSessionOrder(sessionOrderStorageKey(user))) : merged;
      });
      const nextActivities: Record<string, SessionActivitySummary> = {};
      for (const session of listed) {
        if (session.activity) nextActivities[session.id] = session.activity;
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
            session.activity?.last_order_key ?? null,
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
    if (!user) return;
    void refresh();
  }, [user]);

  // While a pod is booting we need a 1s tick so the ↓ boot label counts up
  // second by second. Once nothing's booting, fall back to the slow tick —
  // the ↑ runtime label is minute-resolution and re-rendering the sidebar
  // every second for hours of idle uptime would be wasted work.
  const hasPendingSession = useMemo(
    () => sessions.some((s) => s.status === "Pending"),
    [sessions],
  );

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
    if (!user) return;
    const tickMs = hasPendingSession ? SESSION_BOOT_TICK_MS : SESSION_RUNTIME_TICK_MS;
    const t = setInterval(() => setNowMs(Date.now()), tickMs);
    return () => clearInterval(t);
  }, [user, hasPendingSession]);

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

    const open = () => {
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
      source = new EventSource(`/api/sessions/events${query ? `?${query}` : ""}`, {
        withCredentials: true,
      });
      source.addEventListener("session-row", (event) => {
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
        // ordering pass mirrors what refresh() does on snapshot.
        const rows = store.list();
        const sessionsFromStore: Session[] = rows.map(rowToSession);
        const ordered = user
          ? orderSessions(sessionsFromStore, readSessionOrder(sessionOrderStorageKey(user)))
          : sessionsFromStore;
        setSessions(ordered);
        sessionsRef.current = ordered;

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
      source.addEventListener("ready", (event) => {
        const message = event as MessageEvent;
        let parsed: Record<string, unknown> | undefined;
        try {
          parsed = JSON.parse(String(message.data));
        } catch {
          parsed = undefined;
        }
        logSessionListStreamSignal({ signal: "ready", detail: parsed });
      });
      source.addEventListener("resync_required", (event) => {
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
        source?.close();
        source = null;
        void refresh();
        // Refresh hydrates the snapshot; open() resumes the stream
        // from a fresh cursor on the next tick.
        reopenTimer = window.setTimeout(open, 250);
      });
      source.addEventListener("stream-error", (event) => {
        const message = event as MessageEvent;
        let parsed: Record<string, unknown> | undefined;
        try {
          parsed = JSON.parse(String(message.data));
        } catch {
          parsed = undefined;
        }
        logSessionListStreamSignal({ signal: "stream-error", detail: parsed });
        // Server-acknowledged error; close and let onerror restart.
        source?.close();
        source = null;
      });
      source.onerror = () => {
        source?.close();
        source = null;
        if (cancelled) return;
        reopenTimer = window.setTimeout(open, 1000);
      };
    };

    open();
    return () => {
      cancelled = true;
      if (reopenTimer != null) window.clearTimeout(reopenTimer);
      source?.close();
    };
    // refresh + the ref-backed sessions/activities snapshots are
    // intentionally stable; closing over them would resubscribe on
    // every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user]);

  useEffect(() => {
    if (active && (!sessions.some((s) => s.id === active) || closingIds.has(active))) {
      const selectable = sessions.filter((s) => !closingIds.has(s.id));
      setActive(selectable[selectable.length - 1]?.id ?? null);
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
    const cycleTabs = (event: KeyboardEvent) => {
      const direction = shiftArrowSessionDirection(event);
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
      if (!targetId || closingIds.has(targetId)) return;
      const session = sessions.find((s) => s.id === targetId);
      if (!session) return;
      event.preventDefault();
      event.stopPropagation();
      // Rename now lives in the chat-pane header. Make sure the pane is
      // active (so the header is mounted) and ask it to enter edit mode.
      activate(session.id);
      setAutoRenameSessionId(session.id);
    };
    window.addEventListener("keydown", renameHighlightedSession, { capture: true });
    return () => window.removeEventListener("keydown", renameHighlightedSession, { capture: true });
  }, [sessions, active, closingIds]);

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
    setActive(null);
  }

  function openSession(id: string, e: ReactMouseEvent) {
    if (e.ctrlKey || e.metaKey) {
      window.open(sessionUrl(id), "_blank", "noopener,noreferrer");
      return;
    }
    activate(id);
  }

  function dragSessionStart(id: string, event: ReactDragEvent<HTMLLIElement>) {
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("text/plain", id);
    setDraggingSessionId(id);
    setDragOverSessionId(id);
  }

  function dragSessionOver(id: string, event: ReactDragEvent<HTMLLIElement>) {
    if (!draggingSessionId || draggingSessionId === id) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    setDragOverSessionId(id);
  }

  function dropSession(id: string, event: ReactDragEvent<HTMLLIElement>) {
    event.preventDefault();
    const movedId = event.dataTransfer.getData("text/plain") || draggingSessionId;
    setDraggingSessionId(null);
    setDragOverSessionId(null);
    if (!movedId || movedId === id || !user) return;

    setSessions((prev) => {
      const currentOrder = prev.map((session) => session.id);
      const next = moveSessionId(currentOrder, movedId, id);
      if (next === currentOrder) return prev;
      writeSessionOrder(sessionOrderStorageKey(user), next);
      return orderSessions(prev, next);
    });
  }

  function dragSessionEnd() {
    setDraggingSessionId(null);
    setDragOverSessionId(null);
  }

  async function createSession(
    mode: SessionMode = defaultSessionMode,
    initialPrompt?: string,
    initialPermissionMode: RunComposerMode = "default",
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
    // unsupported modes, so this is a belt-and-braces guard — a
    // mode-override createSession() call (the Launchers section)
    // could otherwise send repos for a CLI session and get a 400.
    const repos = REPO_SUPPORTED_MODES.has(mode) ? selectedRepos : [];
    const requestedName = homeSessionName.trim();
    try {
      const res = await authedFetch("/api/sessions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode, repos }),
      });
      if (!res.ok) throw new Error(`create failed: ${res.status}`);
      let created: Session = normalizeSession(await res.json());
      let requestedNameApplied = false;
      if (requestedName) {
        try {
          const renameRes = await authedFetch(`/api/sessions/${created.id}`, {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ name: requestedName }),
          });
          if (!renameRes.ok) throw new Error(`rename failed: ${renameRes.status}`);
          created = normalizeSession(await renameRes.json());
          requestedNameApplied = true;
        } catch (e) {
          setError(String(e));
        }
      }
      if (CHAT_MODES.has(mode)) {
        writeSessionInteraction(created.id, defaultInteraction);
      }
      // Insert the freshly-created session into the local list and focus the
      // chat pane immediately, without waiting on /api/sessions to re-list or
      // on the pod becoming Ready. The backend returned the full session row
      // synchronously (status: "Pending"), so the sidebar entry and the chat
      // pane header can render against it right now; the session_lifecycle
      // SSE will reconcile status, runtimeLabel, etc. as they arrive. The
      // prior shape awaited a list refresh before activating, which made the
      // new pane appear "on the side" — sidebar entry showing up while the
      // main pane stayed on whatever was already open — and also gated the
      // rename UI behind the same delay.
      setSessions((prev) => {
        if (prev.some((s) => s.id === created.id)) return prev;
        const merged = [created, ...prev];
        return user ? orderSessions(merged, readSessionOrder(sessionOrderStorageKey(user))) : merged;
      });
      activate(created.id);
      if (requestedNameApplied) {
        setHomeSessionName("");
        setHomeEditingTitle(false);
      } else {
        setAutoRenameSessionId(created.id);
      }
      // Belt-and-braces reconcile in the background — the lifecycle SSE
      // wake from session.created should beat this in practice. Does not
      // gate the UI.
      void refresh();
      // If the caller seeded an initial prompt from the home composer, wait
      // for the pod to become ready and submit it as the first turn. Only
      // chat modes have a turn endpoint; non-chat modes ignore the prompt
      // because the home composer would not have surfaced a sensible target.
      const seedPrompt = initialPrompt?.trim();
      const pendingHomeAttachments = homeAttachments;
      if ((seedPrompt || pendingHomeAttachments.length > 0) && CHAT_MODES.has(mode)) {
        const model =
          selectedHomeModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID ? "" : selectedHomeModelId;
        const effort =
          selectedProvider === "anthropic" || selectedProvider === "codex"
            ? selectedHomeEffortId
            : "";
        try {
          await waitForSessionReady(created.id);
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
              ? `${seedPrompt ?? ""}${seedPrompt ? "\n\n" : ""}Attachments (use the Read tool to load):\n${uploadedPaths
                  .map((p) => `- ${p.absPath}`)
                  .join("\n")}`
              : (seedPrompt ?? "");
          const turnRes = await authedFetch(`/api/sessions/${created.id}/turns`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              client_nonce: newForkTurnId(),
              prompt: composedPrompt,
              model,
              ...(effort ? { effort } : {}),
              permission_mode: initialPermissionMode,
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
      // Clear the chip selection and refresh "Recent" so the just-
      // used repos float to the top next time the splash opens.
      if (repos.length > 0) {
        setSelectedRepos([]);
        setRepoPickerOpen(false);
        setRepoInput("");
        setRepoError(null);
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
        body: JSON.stringify({ mode }),
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
          model: request.model,
          // Same omit-when-empty rule as enqueueSdkTurn: don't carry an
          // empty effort on the wire for Codex forks.
          ...(request.effort ? { effort: request.effort } : {}),
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
    throw new Error("fork failed: new session did not become ready");
  }

  function setDefaultProvider(provider: Provider) {
    const interaction =
      PROVIDER_INTERACTION_MODES[provider][defaultInteraction] == null
        ? "cli"
        : defaultInteraction;
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
    const mode = defaultModeFor(provider, interaction);
    setDefaultInteraction(interaction);
    writeDefaultInteraction(interaction);
    setDefaultSessionMode(mode);
    writeDefaultSessionMode(mode);
  }

  async function renameSession(id: string, nextName: string | null) {
    try {
      const res = await authedFetch(`/api/sessions/${id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: nextName }),
      });
      if (!res.ok) throw new Error(`rename failed: ${res.status}`);
      const updated: Session = normalizeSession(await res.json());
      setSessions((prev) =>
        prev.map((s) => (s.id === id ? { ...s, name: updated.name ?? null } : s))
      );
    } catch (e) {
      setError(String(e));
    }
  }

  function patchSession(id: string, patch: Partial<Session>) {
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
    setAutoRenameSessionId((prev) => (prev === id ? null : prev));
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
  const homeSessionTitle = homeSessionName.trim() || "New session";

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
              const avatar = getSessionAvatar(s.id);
              const statusDotClass = sessionStatusDotClass(s, sessionActivities[s.id]);
              const statusLabel = sessionStatusLabel(s, sessionActivities[s.id]);
              const activityChips = sessionActivityChips(sessionActivities[s.id]);
              const bootLabel = sessionBootLabel(s, nowMs);
              const runtimeLabel = sessionRuntimeLabel(s, nowMs);
              const skillStateClass = sessionSkillStateClass(s);
              return (
                <li
                  key={s.id}
                  data-session-id={s.id}
                  className={`${isActive ? "is-open" : ""}${isClosing ? " is-closing" : ""}${skillStateClass}${draggingSessionId === s.id ? " is-dragging" : ""}${dragOverSessionId === s.id && draggingSessionId !== s.id ? " is-drag-over" : ""}`}
                  draggable={!isClosing}
                  onDragStart={(e) => dragSessionStart(s.id, e)}
                  onDragOver={(e) => dragSessionOver(s.id, e)}
                  onDrop={(e) => dropSession(s.id, e)}
                  onDragEnd={dragSessionEnd}
                  onClick={isClosing ? undefined : (e) => openSession(s.id, e)}
                  title={sidebarCollapsed ? `${sessionDisplayName(s)} (${statusLabel})` : undefined}
                >
                  <AgentAvatarIcon avatar={avatar} className="session-avatar" />
                  <div className="session-row-top">
                    <span
                      className={statusDotClass}
                      title={statusLabel}
                      aria-label={`status: ${statusLabel}`}
                    />
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
                    {(bootLabel || runtimeLabel) && (
                      <span className="session-stats">
                        {bootLabel && (
                          <span
                            className="session-stat"
                            title={sessionBootTitle(s, nowMs)}
                            aria-label={sessionBootTitle(s, nowMs)}
                          >
                            <span aria-hidden="true">↓</span>
                            <span>{bootLabel}</span>
                          </span>
                        )}
                        {runtimeLabel && (
                          <span
                            className="session-stat"
                            title={sessionRuntimeTitle(s, nowMs)}
                            aria-label={sessionRuntimeTitle(s, nowMs)}
                          >
                            <span aria-hidden="true">↑</span>
                            <span>{runtimeLabel}</span>
                          </span>
                        )}
                      </span>
                    )}
                    <button
                      className="session-delete"
                      onClick={(e) => { e.stopPropagation(); deleteSession(s.id); }}
                      disabled={isClosing}
                      title={isClosing ? "closing session" : "delete session"}
                      aria-label={isClosing ? "closing session" : "delete session"}
                    >
                      {isClosing ? <span className="session-delete-spinner" /> : <IconClose />}
                    </button>
                  </div>
                  <div className="session-row-bottom">
                    <ModeChip mode={s.mode} interaction={sessionInteractionForSession(s)} />
                    {activityChips.map((chip) => (
                      <span
                        key={chip.key}
                        className={`session-activity-chip is-${chip.tone}`}
                        title={chip.title}
                        aria-label={chip.title}
                      >
                        {chip.label}
                      </span>
                    ))}
                    {isClosing && <span className="session-closing-chip">closing</span>}
                    {CONFIG_MODES.has(s.mode) && (
                      <button
                        className="session-action"
                        onClick={(e) => { e.stopPropagation(); saveCredentials(s.id); }}
                        disabled={busy || !isLive || isClosing}
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
        {active == null ? (
          // Pre-session "home" state. Same workspace scaffold as an active
          // session — same body/composer column and same composer footer at
          // the same y-coordinate — with the header strip restored so the
          // about-to-be-created session can be named before launch.
          <WorkspaceShell
            className="run-panel-home"
            bodyClassName="run-main-home"
            title={(<>
              {homeEditingTitle ? (
                <input
                  className="run-header-name-input"
                  aria-label="Session name"
                  value={homeSessionName}
                  autoFocus
                  onChange={(e) => setHomeSessionName(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      setHomeEditingTitle(false);
                    } else if (e.key === "Escape") {
                      setHomeEditingTitle(false);
                    }
                  }}
                  onBlur={() => setHomeEditingTitle(false)}
                  placeholder="New session"
                  maxLength={80}
                />
              ) : (
                <button
                  type="button"
                  className="run-header-name-btn"
                  title="Name this session before creating it"
                  onClick={() => setHomeEditingTitle(true)}
                >
                  {homeSessionTitle}
                </button>
              )}
            </>)}
            tabs={(<>
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
                className="run-tab"
                disabled
                title="Settings are available once the session starts"
              >
                <SettingsIcon className="run-tab-icon" aria-hidden="true" />
                <span>Settings</span>
              </button>
              <button
                type="button"
                className="run-tab"
                disabled
                title="Help is available once the session starts"
              >
                <InfoIcon className="run-tab-icon" aria-hidden="true" />
                <span>Help</span>
              </button>
            </>)}
            composerVisible={true}
            composerWrapClassName={homeDragActive ? "run-composer-wrap-drag" : ""}
            onComposerWrapDragOver={(e) => {
              if (!CHAT_MODES.has(defaultSessionMode)) return;
              e.preventDefault();
              if (!homeDragActive) setHomeDragActive(true);
            }}
            onComposerWrapDragLeave={(e) => {
              if (e.currentTarget === e.target) setHomeDragActive(false);
            }}
            onComposerWrapDrop={(e) => {
              if (!CHAT_MODES.has(defaultSessionMode)) return;
              e.preventDefault();
              setHomeDragActive(false);
              addHomeAttachments(e.dataTransfer?.files ?? null);
            }}
            onComposerWrapPaste={(e) => {
              if (!CHAT_MODES.has(defaultSessionMode)) return;
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
            body={(<>
              <div className="home-inner">
                <section className="home-hero" aria-labelledby="home-title">
                  <div>
                    <h2 id="home-title" className="home-title">What do you want to build?</h2>
                    <p className="home-sub">
                      Type below to start a session — or pick a runtime and launcher first.
                    </p>
                  </div>
                  <span className="home-count">{sessions.length} session{sessions.length === 1 ? "" : "s"}</span>
                </section>

                <div className="home-grid">
                <section className="home-panel home-panel-start" aria-labelledby="home-start-title">
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
                          <span>{provider === "anthropic" ? "Claude" : provider === "codex" ? "Codex" : "Pi"}</span>
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
                      onRemove={(slug) => {
                        setSelectedRepos((prev) => removeRepoSlug(prev, slug));
                        setRepoError(null);
                      }}
                    />
                  )}
                  <button
                    className="home-primary-action"
                    onClick={() => createSession(defaultSessionMode)}
                    disabled={busy}
                  >
                    <span className="home-action-icons">
                      <ProviderIcon provider={selectedProvider} className="home-provider-icon" />
                      <InteractionIcon interaction={defaultInteraction} className="home-interaction-icon" />
                    </span>
                    <span>
                      <span className="home-action-title">{MODE_LABELS[defaultSessionMode]}</span>
                      <span className="home-action-sub">{MODE_HINTS[defaultSessionMode]}</span>
                    </span>
                  </button>
                  <div className="home-quick-actions">
                    <button
                      className="home-quick-action"
                      onClick={() => createSession("api_key")}
                      disabled={busy}
                    >
                      <IconKey className="home-quick-icon" />
                      <span>API key</span>
                    </button>
                    <button
                      className="home-quick-action"
                      onClick={() => createSession(configMode)}
                      disabled={busy}
                    >
                      <IconWrench className="home-quick-icon" />
                      <span>{MODE_LABELS[configMode]}</span>
                    </button>
                  </div>
                </section>

                <section className="home-panel" aria-labelledby="home-modes-title">
                  <div className="home-panel-head">
                    <h3 id="home-modes-title">Launchers</h3>
                  </div>
                  <div className="home-mode-list" role="list">
                    {MODE_ORDER.map((m) => (
                      <button
                        key={m}
                        className="home-mode"
                        onClick={() => createSession(m)}
                        disabled={busy}
                        role="listitem"
                      >
                        <ProviderIcon provider={MODE_MENU_ICONS[m]} className="home-mode-icon" />
                        <span>
                          <span className="home-mode-title">{MODE_LABELS[m]}</span>
                          <span className="home-mode-sub">{MODE_HINTS[m]}</span>
                        </span>
                      </button>
                    ))}
                  </div>
                </section>

                <section className="home-panel" aria-labelledby="home-sessions-title">
                  <div className="home-panel-head">
                    <h3 id="home-sessions-title">Sessions</h3>
                    <span className="home-panel-meta">{sessions.filter((s) => !closingIds.has(s.id)).length} available</span>
                  </div>
                  <div className="home-session-list">
                    {sessions.length === 0 ? (
                      <div className="home-empty">No sessions</div>
                    ) : (
                      sessions.slice(0, 6).map((s) => (
                        <button
                          key={s.id}
                          data-session-id={s.id}
                          className="home-session"
                          onClick={() => activate(s.id)}
                          disabled={closingIds.has(s.id)}
                          title={
                            s.repos.length > 0
                              ? `Repos: ${s.repos.join(", ")}`
                              : undefined
                          }
                        >
                          <span className={sessionStatusDotClass(s, sessionActivities[s.id])} />
                          <ProviderIcon provider={MODE_MENU_ICONS[s.mode]} className="home-session-icon" />
                          <span className="home-session-main">
                            <span className="home-session-title">{sessionDisplayName(s)}</span>
                            <span className="home-session-sub">
                              {MODE_LABELS[s.mode]}
                              {s.repos.length > 0 && (
                                <span className="home-session-repos">
                                  {" · "}
                                  {s.repos.length === 1
                                    ? s.repos[0]
                                    : `${s.repos.length} repos`}
                                </span>
                              )}
                            </span>
                          </span>
                        </button>
                      ))
                    )}
                  </div>
                </section>
              </div>

              </div>
            </>)}
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
                      title={a.name}
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
                      <span className="run-composer-chip-name">{a.name}</span>
                      <button
                        type="button"
                        className="run-composer-chip-remove"
                        onMouseDown={(e) => {
                          e.preventDefault();
                          removeHomeAttachment(a.id);
                        }}
                        aria-label={`Remove ${a.name}`}
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
                placeholder={
                  CHAT_MODES.has(defaultSessionMode)
                    ? `Ask ${MODE_LABELS[defaultSessionMode]} anything to start a session...`
                    : `Press Enter to start ${MODE_LABELS[defaultSessionMode]}...`
                }
                onSubmit={({ text, permissionMode }) => {
                  const trimmed = text.trim();
                  void createSession(
                    defaultSessionMode,
                    trimmed || undefined,
                    permissionMode,
                  );
                }}
                permissionMode={homeComposerMode}
                onPermissionModeChange={setHomeComposerMode}
                sendByCtrlEnter={false}
                hintSuffix=" · / for slash commands"
                disabled={busy}
                toolButtons={
                    <>
                      <button
                        type="button"
                        className="run-composer-icon-btn"
                        aria-label="Attach files"
                        title={
                          CHAT_MODES.has(defaultSessionMode)
                            ? "Attach files for the first turn"
                            : "Attachments only apply to chat modes"
                        }
                        onClick={() => homeFileInputRef.current?.click()}
                        disabled={busy || !CHAT_MODES.has(defaultSessionMode)}
                      >
                        <ImageIcon className="run-composer-icon" aria-hidden="true" />
                      </button>
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
                        disabled
                        aria-label="Start test skill"
                        title="Available once your session starts"
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
                      onRename={renameSession}
                      onSessionPatch={patchSession}
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
                      autoRename={autoRenameSessionId === s.id}
                      onAutoRenameConsumed={() => setAutoRenameSessionId(null)}
                      primeTurnCompleteSound={primeTurnCompleteSound}
                      playTurnCompleteSound={playTurnCompleteSound}
                    />
                  </div>
                ) : (
                  <div
                    key={s.id}
                    className="run-body"
                    hidden={active !== s.id}
                  >
                    <CliSession session={s} visible={active === s.id} />
                  </div>
                )
              )}
          </div>
        )}
      </main>
    </div>
  );
}
