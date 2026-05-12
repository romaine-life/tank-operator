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
import {
  PromptInput,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
  type PromptInputMessage,
} from "@/components/ai-elements/prompt-input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  AlertCircleIcon,
  ArrowDownIcon,
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
  SendHorizontalIcon,
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
import { McpIcon } from "./McpIcon";
import { ProviderIcon } from "./providerIcons";
import { ANSI_256_OVERRIDES, ANSI_STANDARD_OVERRIDES } from "./terminalTheme";

type SessionMode =
  | "api_key"
  | "claude_cli"
  | "claude_gui"
  | "config"
  | "codex_cli"
  | "codex_gui"
  | "codex_config"
  | "pi_cli"
  | "pi_config";
type DefaultSessionMode = Extract<
  SessionMode,
  | "claude_cli"
  | "claude_gui"
  | "codex_cli"
  | "codex_gui"
  | "pi_cli"
>;
type Provider = "anthropic" | "codex" | "pi";
type SessionInteraction = "gui" | "cli";
type AgentSessionActivity = "waiting" | "working";
type ToolKind = "mcp" | "shell";
type TranscriptEntry = SandboxTranscriptEntry & {
  toolKind?: ToolKind;
  toolServer?: string;
  toolAction?: string;
};
type SkillStateName = "test" | "rollout";

interface Session {
  id: string;
  pod_name: string | null;
  owner: string;
  status: string;
  mode: SessionMode;
  // Which data-ingestion path the chat pane should use for this session.
  // "sdk" → pod has the agent-runner sidecar; chat pane opens /agent-ws
  // + /events. "legacy" → no agent-runner; chat pane uses /run +
  // /runs/latest/events.json + /run/history. Renderer is the same for
  // both; only the event source differs. Absent on older pods — treated
  // as "legacy".
  runtime?: "sdk" | "legacy";
  requested_at: string | null;
  created_at: string | null;
  ready_at: string | null;
  // User-set friendly name. Null when unset; UI falls back to the id slug.
  name: string | null;
  test_state?: TestState | null;
  rollout_state?: RolloutState | null;
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
  codex_config: "codex-cfg",
  pi_cli: "pi-cli",
  pi_config: "pi-cfg",
};

const MODE_CHIP_ICONS: Partial<Record<SessionMode, Provider>> = {
  claude_cli: "anthropic",
  claude_gui: "anthropic",
  codex_cli: "codex",
  codex_gui: "codex",
  pi_cli: "pi",
};

const MODE_MENU_ICONS: Record<SessionMode, Provider> = {
  api_key: "anthropic",
  claude_cli: "anthropic",
  claude_gui: "anthropic",
  config: "anthropic",
  codex_cli: "codex",
  codex_gui: "codex",
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
  claude_gui: "GUI run pane for claude -p output",
  api_key: "Specify an API key fallback",
  config: "Log in once · seeds KV for future sessions",
  codex_cli: "Uses ChatGPT login from KV",
  codex_gui: "GUI run pane for codex exec output",
  codex_config: "codex login --device-auth · seeds KV for Codex",
  pi_cli: "Uses Tank Claude/Codex subscriptions",
  pi_config: "Pi /login sandbox",
};

const MODE_ORDER: SessionMode[] = [
  "claude_gui",
  "api_key",
  "config",
  "codex_gui",
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
  const template = session.mode === "codex_cli" || session.mode === "codex_gui"
    ? DEMO_CODEX_LINES
    : session.mode === "pi_cli"
      ? DEMO_PI_LINES
      : DEMO_CLAUDE_LINES;
  const lines = [...template];
  if (promptText) {
    if (session.mode === "codex_cli" || session.mode === "codex_gui") {
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
  const label = mode === "codex_cli" || mode === "codex_gui"
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
  switch (value) {
    case "subscription":
      return "claude_cli";
    case "subscription_headless":
      return "claude_gui";
    case "codex_subscription":
      return "codex_cli";
    case "codex_headless":
      return "codex_gui";
    case "pi_subscription":
      return "pi_cli";
    default:
      return value;
  }
}

function isDefaultSessionMode(value: string | null): value is DefaultSessionMode {
  return (
    value === "claude_cli" ||
    value === "claude_gui" ||
    value === "codex_cli" ||
    value === "codex_gui" ||
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
    if (stored === "run") return "gui";
    if (stored === "newterm" || stored === "terminal") return "cli";
  } catch {}
  // Back-compat: derive from stored session mode.
  const mode = readDefaultSessionMode();
  return HEADLESS_MODES.has(mode) ? "gui" : "cli";
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

function normalizeSession(session: Session): Session {
  const mode = normalizeSessionMode(session.mode) as SessionMode;
  return mode === session.mode ? session : { ...session, mode };
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
const HEADLESS_MODES = new Set<SessionMode>(["claude_gui", "codex_gui"]);
const CLAUDE_ROLLOUT_MODES = new Set<SessionMode>(["claude_cli", "api_key"]);
const CODEX_ROLLOUT_MODES = new Set<SessionMode>(["codex_cli"]);
const GUI_ROLLOUT_MODES = new Set<SessionMode>(["claude_gui", "codex_gui"]);
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
  activity?: AgentSessionActivity,
): string {
  if (session.status === "Active" && HEADLESS_MODES.has(session.mode)) {
    return `status-dot status-agent-${activity === "working" ? "working" : "waiting"}`;
  }
  return `status-dot status-${session.status.toLowerCase()}`;
}

function sessionStatusLabel(
  session: Session,
  activity?: AgentSessionActivity,
): string {
  if (session.status === "Active" && HEADLESS_MODES.has(session.mode)) {
    return activity === "working" ? "Working" : "Waiting";
  }
  return session.status;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

interface SessionUser {
  sub: string;
  email: string;
  name: string;
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

const POLL_INTERVAL_MS = 1500;
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

function IconChevronDown({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 16 16"
      width="12"
      height="12"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      focusable="false"
      aria-hidden="true"
    >
      <polyline points="4,6 8,10 12,6" />
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
  if (HEADLESS_MODES.has(session.mode)) return "gui";
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
  const [activeDemoSession, setActiveDemoSession] = useState(DEMO_BASE_SESSIONS[0].id);
  const [selectedProvider, setSelectedProvider] = useState<Provider>("anthropic");
  const [modeMenuOpen, setModeMenuOpen] = useState(false);
  const [demoSessionOrdinal, setDemoSessionOrdinal] = useState(DEMO_BASE_SESSIONS.length);
  const [demoPromptMessages, setDemoPromptMessages] = useState<Record<string, string>>({});
  const selected = demoSessions.find((s) => s.id === activeDemoSession) ?? demoSessions[0];
  const selectedMode = defaultModeFor(selectedProvider, "cli");
  const terminalLines = selected
    ? demoTerminalLines(selected, demoPromptMessages[selected.id])
    : DEMO_LANDING_LINES;

  useEffect(() => {
    if (!modeMenuOpen) return;
    const close = (e: MouseEvent) => {
      const target = e.target as HTMLElement | null;
      const root = target?.closest("[data-menu]") as HTMLElement | null;
      if (root?.dataset.menu === "mode") return;
      setModeMenuOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [modeMenuOpen]);

  useEffect(() => {
    const cycleTabs = (event: KeyboardEvent) => {
      const direction = shiftArrowSessionDirection(event);
      if (direction == null || isSessionShortcutEditableTarget(event.target)) return;
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
    setSelectedProvider(provider);
    setModeMenuOpen(false);
  }

  function createPreviewSession() {
    const nextOrdinal = demoSessionOrdinal + 1;
    const next = { ...createDemoSession(selectedMode, nextOrdinal), name: null };
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
      setActiveDemoSession(next[0]?.id ?? "");
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
          <h1>tank-operator</h1>
        </div>

        <div className="sidebar-section">
          <div className="new-row new-row-launcher" data-menu="mode">
            <button
              className={`new-row-provider-toggle${modeMenuOpen ? " is-open" : ""}`}
              onClick={() => setModeMenuOpen((v) => !v)}
              aria-label="choose provider"
              aria-expanded={modeMenuOpen}
              title={`preview provider: ${MODE_LABELS[selectedMode]}`}
            >
              <span className="new-row-provider-slot">
                <ProviderIcon provider={selectedProvider} className="new-row-provider-icon" />
              </span>
              <IconChevronDown className="new-row-provider-chevron" />
            </button>
            <div className="new-row-action-group" role="group" aria-label="preview session actions">
              <button
                className="new-row-action"
                onClick={createPreviewSession}
                aria-label={`Start ${MODE_LABELS[selectedMode]} preview session`}
                title={`start ${MODE_LABELS[selectedMode]} preview session`}
              >
                <span className="row-icon"><IconPlus /></span>
              </button>
              <button
                className="new-row-action"
                onClick={() => {}}
                aria-label="Start API key session"
                title="API key sessions are not shown in preview"
              >
                <IconKey className="new-row-action-icon" />
              </button>
              <button
                className="new-row-action"
                onClick={() => {}}
                aria-label="Start config session"
                title="Config sessions are not shown in preview"
              >
                <IconWrench className="new-row-action-icon" />
              </button>
            </div>
            {modeMenuOpen && (
              <ul className="dropdown dropdown-provider" role="menu">
                {PROVIDERS.map((provider) => {
                  const mode = defaultModeFor(provider, "cli");
                  return (
                    <li key={provider}>
                      <button
                        onClick={() => setDemoProvider(provider)}
                        aria-label={MODE_LABELS[mode]}
                      >
                        <ProviderIcon
                          provider={provider}
                          className="dropdown-provider-icon"
                        />
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </div>

        <div className="sidebar-list">
          <div className="sidebar-section-label">Preview sessions</div>
          <ul className="sessions">
            {demoSessions.map((s) => {
              const isActive = s.id === selected?.id;
              const statusDotClass = sessionStatusDotClass(s, "waiting");
              const bootLabel = sessionBootLabel(s, Date.now());
              const runtimeLabel = sessionRuntimeLabel(s, Date.now());
              return (
                <li
                  key={s.id}
                  className={isActive ? "is-open" : ""}
                  onClick={() => setActiveDemoSession(s.id)}
                >
                  <div className="session-row-top">
                    <span
                      className={statusDotClass}
                      title={s.status}
                      aria-label={`status: ${s.status}`}
                    />
                    <ProviderIcon provider={MODE_MENU_ICONS[s.mode]} className="session-provider-icon" />
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
      </main>
    </div>
  );
}

type RunEvent = {
  stream?: "stdout" | "stderr";
  data?: string;
  status?: "attached" | "done" | "error";
  run_id?: string;
  detail?: string;
};

type RunLifecycleEvent = {
  run_id: string;
  session_id: string;
  event_id: number;
  type:
    | "run.started"
    | "run.completed"
    | "run.failed"
    | "run.stale"
    | "run.output.started"
    | "run.tool.started"
    | "run.tool.completed"
    | "run.message.created";
  payload?: JsonObject;
  created_at: string;
};

type RunReplayResponse = {
  session_id: string;
  run_id: string;
  events?: unknown[];
};

type JsonObject = Record<string, unknown>;

function isJsonObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parseRunLifecycleEvent(data: string): RunLifecycleEvent | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(data);
  } catch {
    return null;
  }
  if (!isJsonObject(parsed)) return null;
  const type = parsed.type;
  if (
    type !== "run.started" &&
    type !== "run.completed" &&
    type !== "run.failed" &&
    type !== "run.stale" &&
    type !== "run.output.started" &&
    type !== "run.tool.started" &&
    type !== "run.tool.completed" &&
    type !== "run.message.created"
  ) {
    return null;
  }
  if (
    typeof parsed.run_id !== "string" ||
    typeof parsed.session_id !== "string" ||
    typeof parsed.event_id !== "number" ||
    typeof parsed.created_at !== "string"
  ) {
    return null;
  }
  return {
    run_id: parsed.run_id,
    session_id: parsed.session_id,
    event_id: parsed.event_id,
    type,
    payload: isJsonObject(parsed.payload) ? parsed.payload : undefined,
    created_at: parsed.created_at,
  };
}

function isRunTerminalEvent(event: RunLifecycleEvent): boolean {
  return (
    event.type === "run.completed" ||
    event.type === "run.failed" ||
    event.type === "run.stale"
  );
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

function normalizeIsoTimestamp(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? new Date(parsed).toISOString() : null;
}

function eventTime(event: JsonObject): string {
  return (
    normalizeIsoTimestamp(event.timestamp) ??
    normalizeIsoTimestamp(event.time) ??
    normalizeIsoTimestamp(event.created_at) ??
    nowIso()
  );
}

function upsertEntry(entries: TranscriptEntry[], entry: TranscriptEntry): TranscriptEntry[] {
  const index = entries.findIndex((candidate) => candidate.id === entry.id);
  if (index === -1) return [...entries, entry];
  const next = [...entries];
  next[index] = { ...next[index], ...entry };
  return next;
}

function applyRunMessageEvent(entries: TranscriptEntry[], event: RunLifecycleEvent): TranscriptEntry[] {
  const payload = event.payload;
  const role = payload?.role;
  const text = typeof payload?.text === "string" ? payload.text.trim() : "";
  if ((role !== "user" && role !== "assistant") || !text) return entries;
  const messageId =
    typeof payload?.message_id === "string" && payload.message_id
      ? payload.message_id
      : `run-message-${event.event_id}`;
  const duplicate = entries.some(
    (entry) =>
      entry.kind === "message" &&
      entry.role === role &&
      (entry.id === messageId || entry.text === text),
  );
  if (duplicate) return entries;
  const id = entries.some((entry) => entry.id === messageId)
    ? `run-message-${messageId}`
    : messageId;
  const payloadTime = normalizeIsoTimestamp(payload?.time);
  const eventCreatedAt = normalizeIsoTimestamp(event.created_at);
  const entry: Record<string, unknown> = {
    id,
    kind: "message",
    role,
    text,
    time: payloadTime ?? eventCreatedAt ?? nowIso(),
  };
  // Preserve skill-action metadata stored by the backend at run start so the
  // event replay reconstructs the correct bubble without needing the JSONL.
  if (typeof payload?.messageKind === "string") entry.messageKind = payload.messageKind;
  if (typeof payload?.skillName === "string") entry.skillName = payload.skillName;
  if (typeof payload?.skillSupplementalText === "string") entry.skillSupplementalText = payload.skillSupplementalText;
  return upsertEntry(entries, entry as TranscriptEntry);
}

function applyRunToolStartedEvent(entries: TranscriptEntry[], event: RunLifecycleEvent): TranscriptEntry[] {
  const payload = event.payload;
  const toolUseId = typeof payload?.tool_use_id === "string" ? payload.tool_use_id : "";
  const toolName = typeof payload?.name === "string" && payload.name ? payload.name : "tool";
  if (!toolUseId) return entries;
  const existing = entries.find((entry) => entry.id === toolUseId);
  const terminal = existing?.toolStatus === "completed" || existing?.toolStatus === "failed";
  return upsertEntry(entries, {
    id: toolUseId,
    kind: "tool",
    toolName: existing?.toolName ?? toolName,
    toolInput: existing?.toolInput,
    toolOutput: existing?.toolOutput,
    toolStatus: terminal ? existing?.toolStatus : "started",
    time: existing?.time ?? normalizeIsoTimestamp(event.created_at) ?? nowIso(),
  });
}

function applyRunToolCompletedEvent(entries: TranscriptEntry[], event: RunLifecycleEvent): TranscriptEntry[] {
  const payload = event.payload;
  const toolUseId = typeof payload?.tool_use_id === "string" ? payload.tool_use_id : "";
  if (!toolUseId) return entries;
  const existing = entries.find((entry) => entry.id === toolUseId);
  const output = typeof payload?.output === "string" ? payload.output : existing?.toolOutput;
  return upsertEntry(entries, {
    id: toolUseId,
    kind: "tool",
    toolName: existing?.toolName ?? "tool result",
    toolInput: existing?.toolInput,
    toolOutput: output,
    toolStatus: payload?.is_error === true ? "failed" : "completed",
    time: existing?.time ?? normalizeIsoTimestamp(event.created_at) ?? nowIso(),
  });
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

function appendAssistantMessage(
  entries: TranscriptEntry[],
  id: string,
  text: string,
  time: string = nowIso(),
): TranscriptEntry[] {
  if (!text.trim()) return entries;
  return [
    ...entries,
    {
      id,
      kind: "message",
      role: "assistant",
      text,
      time,
    },
  ];
}

function skillInvocationTitle(name: string): string {
  return `You started ${name} skill`;
}

function skillActionText(name: string): string {
  return `${name.charAt(0).toUpperCase()}${name.slice(1)} skill`;
}

function skillSupplementalText(name: string, text: string): string {
  const trimmed = text.trim();
  const triggerPattern = new RegExp(`^[$/]${name}(?:\\s+|\\n+)?`, "i");
  return trimmed.replace(triggerPattern, "").trim();
}

function skillNameFromTrigger(text: string): string | null {
  const trimmed = text.trim();
  const match = trimmed.match(/^[$/]([A-Za-z0-9_-]{1,64})$/);
  return match ? match[1] : null;
}

function hasSkillInvocation(entries: TranscriptEntry[], name: string): boolean {
  return entries.some(
    (entry) =>
      entry.kind === "meta" &&
      entry.meta?.title === skillInvocationTitle(name),
  );
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

function describeUsage(usage: unknown): string {
  if (!isJsonObject(usage)) return "";
  const input = usage.input_tokens;
  const cached = usage.cached_input_tokens;
  const output = usage.output_tokens;
  const reasoning = usage.reasoning_output_tokens;
  return [
    typeof input === "number" ? `input ${input}` : null,
    typeof cached === "number" ? `cached ${cached}` : null,
    typeof output === "number" ? `output ${output}` : null,
    typeof reasoning === "number" ? `reasoning ${reasoning}` : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

function codexToolEntry(event: JsonObject): TranscriptEntry | null {
  const item = event.item;
  if (!isJsonObject(item)) return null;
  const id = typeof item.id === "string" ? item.id : `codex-tool-${Date.now()}`;
  const status = typeof item.status === "string" ? item.status : String(event.type ?? "");
  const itemType = item.type;
  const time = eventTime(event);

  if (itemType === "command_execution") {
    const command = typeof item.command === "string" ? item.command : "command";
    return {
      id,
      kind: "tool",
      toolKind: "shell",
      toolName: command,
      toolInput: command,
      toolOutput: shortJson(item.aggregated_output),
      toolStatus: status,
      time,
    };
  }

  if (itemType === "file_change") {
    return {
      id,
      kind: "tool",
      toolName: "file change",
      toolInput: shortJson(item.changes),
      toolStatus: status,
      time,
    };
  }

  if (itemType === "mcp_tool_call") {
    const server = typeof item.server === "string" ? item.server : "mcp";
    const tool = typeof item.tool === "string" ? item.tool : "tool";
    return {
      id,
      kind: "tool",
      toolKind: "mcp",
      toolServer: server,
      toolAction: tool,
      toolName: `${server}.${tool}`,
      toolInput: shortJson(item.arguments),
      toolOutput: shortJson(item.result ?? item.error),
      toolStatus: status,
      time,
    };
  }

  if (itemType === "web_search") {
    return {
      id,
      kind: "tool",
      toolName: "web search",
      toolInput: typeof item.query === "string" ? item.query : shortJson(item),
      toolStatus: status,
      time,
    };
  }

  return null;
}

function applyCodexEvent(entries: TranscriptEntry[], event: JsonObject): TranscriptEntry[] {
  const type = event.type;
  const time = eventTime(event);
  if (type === "tank.user_message") {
    const text = typeof event.message === "string" ? event.message.trim() : "";
    const skillName = skillNameFromTrigger(text);
    if (skillName && hasSkillInvocation(entries, skillName)) return entries;
    if (!text || entries.some((entry) => entry.kind === "message" && entry.role === "user" && entry.text === text)) {
      return entries;
    }
    return [
      ...entries,
      {
        id: `codex-user-message-${Date.now()}`,
        kind: "message",
        role: "user",
        text,
        time,
      },
    ];
  }
  if (type === "thread.started") {
    const threadId = typeof event.thread_id === "string" ? event.thread_id : "";
    return appendMeta(entries, `codex-thread-${threadId || Date.now()}`, "Codex thread started", threadId, "info", time);
  }
  if (type === "turn.started") {
    return appendMeta(entries, `codex-turn-started-${Date.now()}`, "Turn started", undefined, "info", time);
  }
  if (type === "turn.completed") {
    return appendMeta(entries, `codex-turn-completed-${Date.now()}`, "Turn completed", describeUsage(event.usage), "info", time);
  }
  if (type === "turn.failed" || type === "error") {
    const error = isJsonObject(event.error) ? event.error.message : event.message;
    return appendMeta(
      entries,
      `codex-error-${Date.now()}`,
      type === "turn.failed" ? "Turn failed" : "Codex error",
      typeof error === "string" ? error : shortJson(event),
      "error",
      time,
    );
  }
  if (type === "item.started" || type === "item.updated" || type === "item.completed") {
    const item = event.item;
    if (!isJsonObject(item)) return entries;
    if (item.type === "agent_message") {
      return appendAssistantMessage(
        entries,
        typeof item.id === "string" ? item.id : `codex-message-${Date.now()}`,
        typeof item.text === "string" ? item.text : "",
      );
    }
    if (item.type === "reasoning") {
      const id = typeof item.id === "string" ? item.id : `codex-reasoning-${Date.now()}`;
      return upsertEntry(entries, {
        id,
        kind: "reasoning",
        time,
        reasoning: { text: typeof item.text === "string" ? item.text : shortJson(item) },
      });
    }
    const toolEntry = codexToolEntry(event);
    return toolEntry ? upsertEntry(entries, toolEntry) : entries;
  }
  return appendMeta(entries, `codex-event-${Date.now()}`, String(type || "Codex event"), shortJson(event), "info", time);
}

function claudeTextFromContent(content: unknown, includeToolResults: boolean): string {
  if (!Array.isArray(content)) return "";
  return content
    .map((block) => {
      if (!isJsonObject(block)) return "";
      if (block.type === "text" && typeof block.text === "string") return block.text;
      if (includeToolResults && block.type === "tool_result") return shortJson(block.content);
      return "";
    })
    .filter(Boolean)
    .join("\n\n");
}

function claudeToolEntries(event: JsonObject): TranscriptEntry[] {
  const message = event.message;
  if (!isJsonObject(message) || !Array.isArray(message.content)) return [];
  const time = eventTime(event);
  return message.content.flatMap((block): TranscriptEntry[] => {
    if (!isJsonObject(block) || block.type !== "tool_use") return [];
    const id = typeof block.id === "string" ? block.id : `claude-tool-${Date.now()}`;
    const toolName = typeof block.name === "string" ? block.name : "tool";
    const mcpMatch = /^mcp__([^_]+)__(.+)$/.exec(toolName);
    const toolKind = mcpMatch ? "mcp" : toolName === "Bash" ? "shell" : undefined;
    return [
      {
        id,
        kind: "tool",
        toolName,
        ...(toolKind
          ? {
              toolKind,
              ...(mcpMatch
                ? {
                    toolServer: mcpMatch[1],
                    toolAction: mcpMatch[2],
                  }
                : {}),
            }
          : {}),
        toolInput: shortJson(block.input),
        toolStatus: "started",
        time,
      },
    ];
  });
}

function toolResultText(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    const text = content
      .map((b) => (isJsonObject(b) && b.type === "text" && typeof b.text === "string" ? b.text : ""))
      .filter(Boolean)
      .join("\n");
    return text || shortJson(content);
  }
  return shortJson(content);
}

function isScheduleWakeupToolName(name: string | undefined): boolean {
  return (name ?? "").toLowerCase() === "schedulewakeup";
}

function applyClaudeToolResults(entries: TranscriptEntry[], event: JsonObject): TranscriptEntry[] {
  const message = event.message;
  if (!isJsonObject(message) || !Array.isArray(message.content)) return entries;
  const time = eventTime(event);
  return message.content.reduce<TranscriptEntry[]>((nextEntries, block) => {
    if (!isJsonObject(block) || block.type !== "tool_result") return nextEntries;
    const toolUseId = typeof block.tool_use_id === "string" ? block.tool_use_id : "";
    if (!toolUseId) return nextEntries;
    const existing = nextEntries.find((entry) => entry.id === toolUseId);
    return upsertEntry(nextEntries, {
      id: toolUseId,
      kind: "tool",
      toolName: existing?.toolName ?? "tool result",
      toolInput: existing?.toolInput,
      toolOutput: toolResultText(block.content),
      toolStatus: block.is_error === true ? "failed" : "completed",
      time: existing?.time ?? time,
    });
  }, entries);
}

function applyClaudeEvent(entries: TranscriptEntry[], event: JsonObject): TranscriptEntry[] {
  const type = event.type;
  const time = eventTime(event);
  // Skip events that have no chat-visible meaning. stream_event is the
  // SDK's typewriter-partial channel (deltas inside an in-flight turn) —
  // the final `assistant` event carries the full content, so we silently
  // drop the partials. Before this filter they fell through to the
  // fallback appendMeta and briefly rendered as raw JSON until the
  // history-replay path clobbered them.
  if (
    type === "system" ||
    type === "stream_event" ||
    type === "rate_limit_event" ||
    type === "ai-title" ||
    type === "last-prompt" ||
    type === "attachment" ||
    type === "queue-operation"
  ) {
    return entries;
  }
  if (type === "assistant") {
    let nextEntries = entries;
    for (const toolEntry of claudeToolEntries(event)) {
      nextEntries = upsertEntry(nextEntries, toolEntry);
    }
    const message = event.message;
    const text = isJsonObject(message) ? claudeTextFromContent(message.content, false) : "";
    if (text) {
      nextEntries = appendAssistantMessage(
        nextEntries,
        typeof event.uuid === "string" ? event.uuid : `claude-message-${Date.now()}`,
        text,
        time,
      );
    }
    return nextEntries;
  }
  if (type === "user") {
    // Reconstruct tool results first.
    let nextEntries = applyClaudeToolResults(entries, event);
    // Also reconstruct user text messages (initial prompt and follow-ups).
    // These are stored in the JSONL but not re-emitted by the live stream
    // (the frontend adds the user bubble directly in startRun). Use text
    // deduplication so re-running this during live streaming is a no-op.
    const message = event.message;
    if (isJsonObject(message)) {
      const texts: string[] = [];
      if (typeof message.content === "string") {
        // Initial prompt stored as a plain string.
        const t = message.content.trim();
        if (t) texts.push(t);
      } else if (Array.isArray(message.content)) {
        // A user event with tool_result blocks is a tool-response turn; any
        // text blocks alongside them are echoed context (e.g. agent prompts),
        // not human input — skip text extraction entirely for those events.
        const hasToolResults = (message.content as unknown[]).some(
          (b) => isJsonObject(b) && b.type === "tool_result",
        );
        if (!hasToolResults) {
          for (const block of message.content as unknown[]) {
            if (!isJsonObject(block) || block.type !== "text") continue;
            const t = typeof block.text === "string" ? block.text.trim() : "";
            if (t) texts.push(t);
          }
        }
      }
      for (const text of texts) {
        const skillName = skillNameFromTrigger(text);
        if (skillName && hasSkillInvocation(nextEntries, skillName)) continue;
        if (!nextEntries.some((e) => e.kind === "message" && e.role === "user" && e.text === text)) {
          nextEntries = [
            ...nextEntries,
            {
              id: typeof event.uuid === "string" ? event.uuid : `user-msg-${Date.now()}`,
              kind: "message" as const,
              role: "user" as const,
              text,
              time,
            },
          ];
        }
      }
    }
    return nextEntries;
  }
  if (type === "result") {
    const isError = event.is_error === true || event.subtype === "error";
    const result = typeof event.result === "string" ? event.result : "";
    if (!isError) {
      if (result && !entries.some((entry) => entry.kind === "message" && entry.text === result)) {
        return appendAssistantMessage(entries, `claude-result-message-${Date.now()}`, result, time);
      }
      return entries;
    }
    let nextEntries = appendMeta(
      entries,
      `claude-result-${Date.now()}`,
      "Claude run failed",
      result,
      "error",
      time,
    );
    if (result && !entries.some((entry) => entry.kind === "message" && entry.text === result)) {
      nextEntries = appendAssistantMessage(nextEntries, `claude-result-message-${Date.now()}`, result, time);
    }
    return nextEntries;
  }
  return appendMeta(entries, `claude-event-${Date.now()}`, String(type || "Claude event"), shortJson(event), "info", time);
}

function applyProviderEvent(
  entries: TranscriptEntry[],
  mode: SessionMode,
  event: JsonObject,
): TranscriptEntry[] {
  if (event.type === "tank.skill_invocation") {
    const name = typeof event.name === "string" ? event.name.trim() : "";
    const trigger = typeof event.trigger === "string" ? event.trigger : "";
    return name
      ? appendSkillInvocation(entries, name, skillSupplementalText(name, trigger), eventTime(event))
      : entries;
  }
  if (mode === "codex_gui") return applyCodexEvent(entries, event);
  return applyClaudeEvent(entries, event);
}

function isClaudeRunMode(mode: SessionMode): boolean {
  return mode === "claude_gui";
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

interface ComposerAttachment {
  id: string; // local-only id for keying
  name: string;
  /** Path relative to /workspace, e.g. ".attachments/1715..-foo.png". */
  path: string;
  /** Full path inside the pod, e.g. "/workspace/.attachments/...". */
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
type RunComposerMode =
  | "default"
  | "acceptEdits"
  | "auto"
  | "bypassPermissions"
  | "plan";

interface PermissionModeInfo {
  label: string;
  desc: string;
  /** Color of the dot rendered next to the pill label. */
  dotColor: string;
}

const PERMISSION_MODE_INFO: Record<RunComposerMode, PermissionModeInfo> = {
  default: {
    label: "Default Mode",
    desc: "Ask before edits, agree to commands",
    dotColor: "#34d399",
  },
  acceptEdits: {
    label: "Accept Edits",
    desc: "Auto-approve file changes",
    dotColor: "#fbbf24",
  },
  auto: {
    label: "Auto",
    desc: "Auto-approve safe operations",
    dotColor: "#60a5fa",
  },
  bypassPermissions: {
    label: "Bypass Permissions",
    desc: "Run without permission prompts",
    dotColor: "#f87171",
  },
  plan: {
    label: "Plan Mode",
    desc: "Plan before execution",
    dotColor: "#a78bfa",
  },
};

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

const CLAUDE_MODELS: ModelOption[] = [
  { id: "claude-sonnet-4-6", label: "Claude · Sonnet 4.6" },
  { id: "claude-opus-4-7", label: "Claude · Opus 4.7" },
  { id: "claude-haiku-4-5", label: "Claude · Haiku 4.5" },
];
const CODEX_MODELS: ModelOption[] = [
  { id: CODEX_ACCOUNT_DEFAULT_MODEL_ID, label: "Codex · Account default" },
];

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
  chatFontScale: number;
}

const DEFAULT_RUN_PREFS: RunPrefs = {
  sendByCtrlEnter: false,
  showThinking: true,
  autoExpandTools: false,
  showTimestamps: true,
  showDuration: true,
  turnCompleteSound: true,
  turnCompleteSoundVolume: 0.8,
  chatFontScale: 1,
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

function loadRunPrefs(): RunPrefs {
  const out = { ...DEFAULT_RUN_PREFS };
  try {
    for (const key of Object.keys(out) as (keyof RunPrefs)[]) {
      const raw = localStorage.getItem(RUN_PREF_PREFIX + key);
      if (key === "chatFontScale") {
        if (raw != null) out[key] = clampChatFontScale(Number(raw));
      } else if (key === "turnCompleteSoundVolume") {
        if (raw != null) out[key] = clampTurnCompleteSoundVolume(Number(raw));
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
    } else if (typeof raw === "boolean") {
      (out as unknown as Record<string, unknown>)[key] = raw;
    }
  }
  return out;
}

// transcriptComparable returns a stable JSON of the transcript's load-bearing
// fields, used to short-circuit no-op replay updates. Backend replay paths
// (`/runs/latest/events.json` then `/run/history`) are the sole source of
// truth for transcript state across reloads; there is no client-side cache.
function transcriptComparable(entries: TranscriptEntry[]): string {
  return JSON.stringify(
    entries.map((entry) => {
      if (entry.kind === "message") {
        return {
          kind: entry.kind,
          role: entry.role,
          text: entry.text,
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

function parseRunHistory(text: string, mode: SessionMode): TranscriptEntry[] {
  let acc: TranscriptEntry[] = [];
  for (const line of text.split("\n")) {
    if (!line.trim()) continue;
    try {
      const ev = JSON.parse(line);
      if (isJsonObject(ev)) {
        acc = applyProviderEvent(acc, mode, ev);
      }
    } catch {
      /* skip unparseable history lines */
    }
  }
  return acc;
}

type ActiveRunData = {
  run_id: string;
  stream_offset: number;
  started_at?: string | null;
};

function activeRunStartedAtMs(data: ActiveRunData): number {
  if (!data.started_at) return Date.now();
  const parsed = Date.parse(data.started_at);
  return Number.isFinite(parsed) ? parsed : Date.now();
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
      <a
        key={`${start}-${href}`}
        className="run-markdown-code-link"
        href={href}
        rel="noreferrer"
        target="_blank"
      >
        {href}
      </a>,
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
  return <a {...props} rel="noreferrer" target="_blank" />;
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

const RunContext = createContext<{ sendStdin: (text: string) => void; user: SessionUser | null }>({
  sendStdin: () => {},
  user: null,
});

function RunMessageBubble({
  entry,
  provider,
  showTimestamps,
  showDuration,
  onQuote,
}: {
  entry: TranscriptEntry;
  provider: Provider;
  showTimestamps: boolean;
  showDuration: boolean;
  onQuote: (text: string, style: QuoteStyle) => void;
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
    >
      {variant === "assistant" && (
        <span className="run-msg-ai-avatar" aria-hidden="true">
          <ProviderIcon provider={provider} className="run-msg-ai-icon" />
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
          <QuoteButton text={text} style="fence" onQuote={onQuote} />
          <QuoteButton text={text} style="blockquote" onQuote={onQuote} />
          <CopyButton text={text} />
        </div>
      </div>
      {variant === "user" && user && (
        <span className="run-msg-avatar">
          <Avatar user={user} />
        </span>
      )}
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

function ToolAskUserBody({
  entry,
  input,
}: {
  entry: TranscriptEntry;
  input: Record<string, unknown> | null;
}) {
  const { sendStdin } = useContext(RunContext);
  const [selectedAnswer, setSelectedAnswer] = useState<string | null>(null);

  const questions = Array.isArray(input?.questions)
    ? (input.questions as Array<Record<string, unknown>>)
    : [];

  const answered = selectedAnswer !== null || entry.toolStatus === "completed";
  const displayAnswer = selectedAnswer ?? entry.toolOutput ?? null;

  if (answered) {
    return (
      <div className="run-tool-body run-tool-ask">
        <span className="run-tool-ask-answered">{displayAnswer ?? "answered"}</span>
      </div>
    );
  }

  return (
    <div className="run-tool-body run-tool-ask">
      {questions.map((q, qi) => {
        const questionText = String(q.question ?? "");
        const options = Array.isArray(q.options)
          ? (q.options as Array<Record<string, unknown>>)
          : [];
        return (
          <div key={qi} className="run-tool-ask-question">
            {questionText && <p className="run-tool-ask-text">{questionText}</p>}
            <div className="run-tool-ask-options">
              {options.map((opt, oi) => {
                const label = String(opt.label ?? "");
                return (
                  <button
                    key={oi}
                    type="button"
                    className="run-tool-ask-option"
                    onClick={() => {
                      sendStdin(label + "\n");
                      setSelectedAnswer(label);
                    }}
                  >
                    <span className="run-tool-ask-option-label">{label}</span>
                    {typeof opt.description === "string" && opt.description && (
                      <span className="run-tool-ask-option-desc">
                        {opt.description}
                      </span>
                    )}
                  </button>
                );
              })}
            </div>
          </div>
        );
      })}
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

function RunMessages({
  entries,
  provider,
  showThinking,
  autoExpandTools,
  showTimestamps,
  showDuration,
  onQuote,
}: {
  entries: TranscriptEntry[];
  provider: Provider;
  showThinking: boolean;
  autoExpandTools: boolean;
  showTimestamps: boolean;
  showDuration: boolean;
  onQuote: (text: string, style: QuoteStyle) => void;
}) {
  const groups = useMemo(() => groupTranscriptEntries(entries), [entries]);
  return (
    <div className="run-transcript run-transcript-claude" data-slot="root">
      {groups.map((g: EntryGroup, idx: number) => {
        if (g.kind === "tools") {
          return (
            <RunToolGroup
              key={`tools-${g.entries[0].id}-${idx}`}
              entries={g.entries}
              autoExpand={autoExpandTools}
            />
          );
        }
        if (g.kind === "reasoning") {
          return (
            <RunReasoningBlock
              key={g.entry.id}
              entry={g.entry}
              showThinking={showThinking}
            />
          );
        }
        if (g.kind === "meta") {
          return <RunMetaBlock key={g.entry.id} entry={g.entry} />;
        }
        return (
          <RunMessageBubble
            key={g.entry.id}
            entry={g.entry}
            provider={provider}
            showTimestamps={showTimestamps}
            showDuration={showDuration}
            onQuote={onQuote}
          />
        );
      })}
    </div>
  );
}

function HeadlessRun({
  session,
  visible,
  onRename,
  onSessionPatch,
  runPrefs,
  setRunPref,
  user,
  onActivityChange,
}: {
  session: Session;
  visible: boolean;
  onRename: (id: string, name: string | null) => void;
  onSessionPatch: (id: string, patch: Partial<Session>) => void;
  runPrefs: RunPrefs;
  setRunPref: SetRunPref;
  user: SessionUser;
  onActivityChange?: (id: string, activity: AgentSessionActivity) => void;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[]>([]);
  const [running, setRunning] = useState(false);
  const [editingTitle, setEditingTitle] = useState(false);
  const [editingTitleValue, setEditingTitleValue] = useState("");
  const [runStatus, setRunStatus] = useState<"idle" | "running" | "done" | "error">("idle");
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
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
  const modelOptions = isClaude ? CLAUDE_MODELS : CODEX_MODELS;
  const [selectedModelId, setSelectedModelId] = useState<string>(modelOptions[0].id);
  // Run timing — drives the streaming status pill's elapsed counter and the
  // rotating action verb / animated dots. Both refresh on a single 250ms
  // interval while running so the bar updates without a per-element timer.
  const [runStartedAt, setRunStartedAt] = useState<number | null>(null);
  const [now, setNow] = useState<number>(() => Date.now());
  // Context tokens used in the most recent assistant turn — updated via
  // applyStdoutLine when an `assistant` or `result` event with usage info
  // arrives. Drives the % ring in the composer footer.
  const [tokensUsed, setTokensUsed] = useState(0);
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([]);
  // Slash-command palette state. `slashOpen` gates rendering; `slashQuery`
  // and `slashIndex` drive filtering and keyboard selection.
  const [slashOpen, setSlashOpen] = useState(false);
  const [slashQuery, setSlashQuery] = useState("");
  const [slashIndex, setSlashIndex] = useState(0);

  useEffect(() => {
    onActivityChange?.(session.id, running ? "working" : "waiting");
  }, [onActivityChange, running, session.id]);
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
  const transcriptScrollRef = useRef<HTMLElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const runEventsRef = useRef<EventSource | null>(null);
  const historyRefreshRef = useRef<Promise<void> | null>(null);
  const sessionIdRef = useRef(session.id);
  const runLifecycleFinishTimerRef = useRef<number | null>(null);
  const turnCompleteAudioRef = useRef<HTMLAudioElement | null>(null);
  const runPrefsRef = useRef(runPrefs);
  const stdoutBufferRef = useRef("");
  const currentRunRef = useRef<{
    id: string;
    prompt: string;
    skillName?: string;
    followUp: boolean;
    model: string;
    permissionMode: string;
    turnStart: number;
    reconnects: number;
    offset: number;
    cancelled: boolean;
  } | null>(null);
  const slashManualOpenRef = useRef(false);
  // Monotonic counter for entry ids — Date.now() collides during fast
  // bursts (sub-ms) and React's key reconciler keeps a stable component
  // tree only as long as keys are stable across renders.
  const entryIdSeqRef = useRef(0);
  function nextEntryId(prefix: string): string {
    entryIdSeqRef.current += 1;
    return `${prefix}-${session.id}-${entryIdSeqRef.current}`;
  }
  const queuedMessageSeqRef = useRef(0);
  function nextQueuedMessageId(): string {
    queuedMessageSeqRef.current += 1;
    return `queued-${session.id}-${queuedMessageSeqRef.current}`;
  }

  useEffect(() => {
    runPrefsRef.current = runPrefs;
  }, [runPrefs]);

  function getTurnCompleteAudio(): HTMLAudioElement | null {
    if (typeof Audio === "undefined") return null;
    if (!turnCompleteAudioRef.current) {
      const audio = new Audio(TURN_COMPLETE_SOUND_SRC);
      audio.preload = "auto";
      turnCompleteAudioRef.current = audio;
    }
    return turnCompleteAudioRef.current;
  }

  function primeTurnCompleteSound() {
    const audio = getTurnCompleteAudio();
    if (!audio) return;
    audio.load();
  }

  function playTurnCompleteSound() {
    const prefs = runPrefsRef.current;
    if (!prefs.turnCompleteSound) return;
    const audio = getTurnCompleteAudio();
    if (!audio) return;
    audio.volume = clampTurnCompleteSoundVolume(prefs.turnCompleteSoundVolume);
    audio.currentTime = 0;
    void audio.play().catch(() => undefined);
  }

  const slashFiltered = slashOpen ? filterSlashCommands(slashCommands, slashQuery) : [];
  const mentionFiltered =
    mentionOpen && mentionPaths
      ? filterMentionPaths(mentionPaths, mentionQuery)
      : [];

  useEffect(() => {
    return () => {
      wsRef.current?.close();
      wsRef.current = null;
      runEventsRef.current?.close();
      runEventsRef.current = null;
      if (runLifecycleFinishTimerRef.current !== null) {
        window.clearTimeout(runLifecycleFinishTimerRef.current);
        runLifecycleFinishTimerRef.current = null;
      }
    };
  }, []);

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

  useEffect(() => {
    if (!activeRunId || session.status !== "Active") return;
    const source = new EventSource(
      `/api/sessions/${encodeURIComponent(session.id)}/runs/${encodeURIComponent(activeRunId)}/events`,
      { withCredentials: true },
    );
    runEventsRef.current = source;
    const onLifecycleEvent = (event: MessageEvent<string>) => {
      const lifecycleEvent = parseRunLifecycleEvent(event.data);
      if (lifecycleEvent) handleRunLifecycleEvent(lifecycleEvent);
    };
    source.addEventListener("run.started", onLifecycleEvent);
    source.addEventListener("run.completed", onLifecycleEvent);
    source.addEventListener("run.failed", onLifecycleEvent);
    source.addEventListener("run.stale", onLifecycleEvent);
    source.addEventListener("run.output.started", onLifecycleEvent);
    source.addEventListener("run.tool.started", onLifecycleEvent);
    source.addEventListener("run.tool.completed", onLifecycleEvent);
    source.addEventListener("run.message.created", onLifecycleEvent);
    return () => {
      source.removeEventListener("run.started", onLifecycleEvent);
      source.removeEventListener("run.completed", onLifecycleEvent);
      source.removeEventListener("run.failed", onLifecycleEvent);
      source.removeEventListener("run.stale", onLifecycleEvent);
      source.removeEventListener("run.output.started", onLifecycleEvent);
      source.removeEventListener("run.tool.started", onLifecycleEvent);
      source.removeEventListener("run.tool.completed", onLifecycleEvent);
      source.removeEventListener("run.message.created", onLifecycleEvent);
      source.close();
      if (runEventsRef.current === source) runEventsRef.current = null;
    };
  // handleRunLifecycleEvent is intentionally omitted; it closes over current
  // run refs and state setters, while activeRunId controls subscription scope.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeRunId, session.id, session.status]);

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

  // Auto-scroll the transcript to the bottom when entries grow, unless
  // the user has scrolled away. Mirrors cloudcli's `autoScrollToBottom`
  // + wheel-detection behaviour.
  useEffect(() => {
    if (userScrolledUp) return;
    const main = transcriptScrollRef.current;
    if (!main) return;
    main.scrollTop = main.scrollHeight;
  }, [entries.length, userScrolledUp, visible, activeTab]);

  // Detect user scroll-away from the bottom. Threshold of 24px so small
  // overshoots (image loads) don't disable auto-scroll.
  useEffect(() => {
    const main = transcriptScrollRef.current;
    if (!main) return;
    const onScroll = () => {
      const distanceFromBottom = main.scrollHeight - main.scrollTop - main.clientHeight;
      setUserScrolledUp(distanceFromBottom > 24);
    };
    main.addEventListener("scroll", onScroll, { passive: true });
    return () => main.removeEventListener("scroll", onScroll);
  }, []);

  // History replay — fetch provider JSONL from the pod and replay each event
  // through the matching provider parser. This is intentionally not limited
  // to empty localStorage: a run can finish while the tab is closed, leaving
  // localStorage with a stale partial transcript.
  const [historyAttempted, setHistoryAttempted] = useState(false);
  const [activeRunChecked, setActiveRunChecked] = useState(false);
  // Toggled briefly when entries are restored (from localStorage OR backend
  // history) so we can show a "Continuing previous conversation" hint.
  const [continueHintVisible, setContinueHintVisible] = useState(false);
  function refreshRunHistory(showHint: boolean) {
    if (session.status !== "Active" || running) return;
    if (historyRefreshRef.current) return;
    const refreshSessionId = session.id;
    // SDK pods have a durable canonical log in Cosmos session-events; hit
    // that directly. Legacy path tries the structured replay first and falls
    // back to raw JSONL history. Both feed the same applyProviderEvent.
    const initial =
      session.runtime === "sdk"
        ? refreshSdkRunHistory(showHint)
        : refreshRunHistoryFromLatestEvents(showHint);
    const refresh = initial
      .then((replayed) => {
        if (sessionIdRef.current !== refreshSessionId) return;
        if (!replayed && session.runtime !== "sdk") refreshRunHistoryFromBackend(showHint);
      })
      .finally(() => {
        if (historyRefreshRef.current === refresh) {
          historyRefreshRef.current = null;
        }
      });
    historyRefreshRef.current = refresh;
  }

  function applyReplayEventToEntries(entries: TranscriptEntry[], event: RunLifecycleEvent): TranscriptEntry[] {
    if (event.type === "run.message.created") return applyRunMessageEvent(entries, event);
    if (event.type === "run.tool.started") return applyRunToolStartedEvent(entries, event);
    if (event.type === "run.tool.completed") return applyRunToolCompletedEvent(entries, event);
    return entries;
  }

  function applyRunReplay(events: RunLifecycleEvent[], showHint: boolean): boolean {
    const replayEntries = events.reduce<TranscriptEntry[]>(
      (acc, event) => applyReplayEventToEntries(acc, event),
      [],
    );
    const replayedContent = replayEntries.length > 0;
    for (const event of events) {
      if (!isRunTerminalEvent(event)) continue;
      if (event.type === "run.completed") {
        setLastStatusText("Done");
        setRunStatus("done");
      } else {
        setLastStatusText("Error");
        setRunStatus("error");
      }
      setActiveTool(null);
      setRunning(false);
      setActiveRunId(null);
    }
    if (replayedContent) {
      setEntries((prev) =>
        transcriptComparable(prev) === transcriptComparable(replayEntries)
          ? prev
          : replayEntries,
      );
      if (showHint) {
        setContinueHintVisible(true);
        window.setTimeout(() => setContinueHintVisible(false), 3000);
      }
    }
    return replayedContent;
  }

  // SDK-runtime history replay. Hits the canonical event log written by the
  // pod-side agent-runner (Cosmos session-events container, exposed via
  // /api/sessions/{id}/events). Each event is the same shape applyProviderEvent
  // already consumes — the chat pane was built around Claude's stream-json,
  // which the SDK is a TypeScript wrapper over the same binary's stdout.
  function refreshSdkRunHistory(showHint: boolean): Promise<boolean> {
    const refreshSessionId = session.id;
    return authedFetch(
      `/api/sessions/${encodeURIComponent(refreshSessionId)}/events?limit=1000`,
    )
      .then(async (res) => {
        if (!res.ok) return false;
        const body = (await res.json()) as { session_id?: string; events?: unknown[] };
        if (sessionIdRef.current !== refreshSessionId) return false;
        if (!Array.isArray(body.events)) return false;
        let acc: TranscriptEntry[] = [];
        for (const ev of body.events) {
          if (isJsonObject(ev)) {
            acc = applyProviderEvent(acc, session.mode, ev);
          }
        }
        if (acc.length === 0) return false;
        setEntries((prev) => {
          if (transcriptComparable(prev) === transcriptComparable(acc)) return prev;
          // For SDK history we trust the server's canonical log over any
          // partial in-memory state. The dedupe-by-uuid in upsertEntry handles
          // any overlap if a live event arrived before history finished.
          return acc;
        });
        if (showHint) {
          setContinueHintVisible(true);
          window.setTimeout(() => setContinueHintVisible(false), 3000);
        }
        return true;
      })
      .catch(() => false);
  }

  function refreshRunHistoryFromLatestEvents(showHint: boolean): Promise<boolean> {
    const refreshSessionId = session.id;
    return authedFetch(
      `/api/sessions/${encodeURIComponent(refreshSessionId)}/runs/latest/events.json`,
    )
      .then(async (res) => {
        if (!res.ok) return false;
        const replay = (await res.json()) as RunReplayResponse;
        if (sessionIdRef.current !== refreshSessionId) return false;
        if (replay.session_id !== refreshSessionId || !Array.isArray(replay.events)) {
          return false;
        }
        const events = replay.events
          .map((event) => parseRunLifecycleEvent(JSON.stringify(event)))
          .filter((event): event is RunLifecycleEvent => event !== null);
        if (events.length === 0) return false;
        return applyRunReplay(events, showHint);
      })
      .catch(() => false);
  }

  function refreshRunHistoryFromBackend(showHint: boolean) {
    const refreshSessionId = session.id;
    void authedFetch(
      `/api/sessions/${refreshSessionId}/run/history?source=latest-events-fallback`,
    )
      .then(async (res) => {
        if (!res.ok) return "";
        return await res.text();
      })
      .then((text) => {
        if (sessionIdRef.current !== refreshSessionId) return;
        if (!text) return;
        const acc = parseRunHistory(text, session.mode);
        if (acc.length > 0) {
          let behind = false;
          setEntries((prev) => {
            if (transcriptComparable(prev) === transcriptComparable(acc)) return prev;
            // If the JSONL has fewer conversation messages than the current
            // state, the pod-side run is still in progress and the assistant
            // turn hasn't been written to the JSONL yet. Replacing would
            // regress the visible transcript — keep prev and retry shortly so
            // we pick up the completed response once the pod finishes.
            const prevMsgs = prev.filter((e) => e.kind === "message").length;
            const accMsgs = acc.filter((e) => e.kind === "message").length;
            if (accMsgs < prevMsgs) {
              behind = true;
              return prev;
            }
            return acc;
          });
          if (behind) {
            window.setTimeout(() => refreshRunHistory(false), 3000);
          }
          if (showHint) {
            setContinueHintVisible(true);
            window.setTimeout(() => setContinueHintVisible(false), 3000);
          }
        }
      })
      .catch(() => {
        /* no history is fine */
      });
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

  useEffect(() => {
    if (!visible || session.status !== "Active" || running) return;
    refreshRunHistory(false);
    const onFocus = () => refreshRunHistory(false);
    const onVisibilityChange = () => {
      if (document.visibilityState === "visible") refreshRunHistory(false);
    };
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  // refreshRunHistory is intentionally omitted for the same reason as above.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, session.id, session.status, running]);

  // Detect in-progress runs after history loads — covers the case where the
  // user refreshes the tab while an agent is actively running. The pid file
  // on the pod exists for the lifetime of the run, so we use it as the
  // liveness signal and reattach to the live stream.
  useEffect(() => {
    if (!historyAttempted || activeRunChecked || session.status !== "Active" || running) return;
    setActiveRunChecked(true);
    void authedFetch(`/api/sessions/${session.id}/run/active`)
      .then(async (res) => {
        if (!res.ok) return;
        const data = (await res.json()) as ActiveRunData | null;
        attachActiveRun(data);
      })
      .catch(() => undefined);
  // openRunSocket is defined in the same render scope and intentionally omitted.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [historyAttempted, activeRunChecked, session.id, session.status, running]);

  useEffect(() => {
    if (!visible || session.status !== "Active" || running || lastStatusText !== "Wakeup scheduled") return;
    let cancelled = false;
    let timer: number | null = null;
    let attempts = 0;
    const check = async () => {
      attempts += 1;
      try {
        const res = await authedFetch(`/api/sessions/${session.id}/run/active`);
        if (res.ok) {
          const data = (await res.json()) as ActiveRunData | null;
          if (!cancelled && attachActiveRun(data)) {
            if (timer !== null) window.clearInterval(timer);
            return;
          }
        }
      } catch {
        /* retry until the watch window expires */
      }
      if (!cancelled) refreshRunHistory(false);
      if (attempts >= 120 && timer !== null) {
        window.clearInterval(timer);
      }
    };
    void check();
    timer = window.setInterval(check, 5000);
    return () => {
      cancelled = true;
      if (timer !== null) window.clearInterval(timer);
    };
  // refreshRunHistory/openRunSocket are intentionally omitted; both close over
  // current session state and the polling gate above controls when this runs.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, session.id, session.status, running, lastStatusText]);

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
  useEffect(() => {
    sessionIdRef.current = session.id;
    setEntries([]);
    setQueuedMessages([]);
    historyRefreshRef.current = null;
    setHistoryAttempted(false);
    setActiveRunChecked(false);
    setContinueHintVisible(false);
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
    if (!running) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !slashOpen) {
        e.preventDefault();
        cancelRun();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [running, slashOpen]);

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
    activeToolNameRef.current = toolName;
    activeToolUseIdRef.current = toolName ? toolUseId : null;
    setActiveToolName(toolName);
  }

  function completeActiveTool(toolUseId: string | null = null) {
    if (
      toolUseId &&
      activeToolUseIdRef.current &&
      activeToolUseIdRef.current !== toolUseId
    ) {
      return;
    }
    if (isScheduleWakeupToolName(activeToolNameRef.current ?? undefined)) {
      scheduledWakeupRef.current = true;
    }
    setActiveTool(null);
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

  function applyStdoutLine(line: string) {
    const trimmed = line.trim();
    if (!trimmed) return;
    if (trimmed.startsWith("__TANK_RUN_EXIT__:")) return;
    let providerEvent: unknown;
    try {
      providerEvent = JSON.parse(trimmed);
    } catch {
      // Non-JSON line. The legacy stream path tails a file that's supposed
      // to contain JSON events, but codex CLI in some failure modes emits
      // plain text to the same stream (e.g. "ERROR: You've hit your usage
      // limit..." on quota). Previously we silently dropped these lines and
      // the user saw a generic kubeexec timeout in the SPA. Render the line
      // as a meta entry so the underlying agent error is at least visible.
      setEntries((prev) =>
        appendMeta(prev, nextEntryId("stdout-text"), "agent output", trimmed, "info"),
      );
      return;
    }
    if (!isJsonObject(providerEvent)) return;
    // Track context-window usage from claude's stream-json events. The
    // assistant message and the final result both carry a `usage` block;
    // the result is the most accurate "final state" so prefer it.
    const t = (providerEvent as JsonObject).type;
    if (t === "assistant") {
      const msg = (providerEvent as JsonObject).message;
      if (isJsonObject(msg)) {
        const u = (msg as JsonObject).usage as ClaudeUsage | undefined;
        const total = totalContextTokens(u);
        if (total > 0) setTokensUsed(total);
        // Track active tool — first tool_use block in content, if any.
        if (Array.isArray(msg.content)) {
          const toolBlock = (msg.content as unknown[]).find(
            (b): b is JsonObject => isJsonObject(b) && b.type === "tool_use",
          );
          const toolName = toolBlock && typeof toolBlock.name === "string" ? toolBlock.name : null;
          const toolUseId = toolBlock && typeof toolBlock.id === "string" ? toolBlock.id : null;
          setActiveTool(toolName, toolUseId);
        }
      }
    } else if (t === "user") {
      completeActiveTool();
    } else if (t === "result") {
      const u = (providerEvent as JsonObject).usage as ClaudeUsage | undefined;
      const total = totalContextTokens(u);
      if (total > 0) setTokensUsed(total);
      setActiveTool(null);
    }
    setEntries((prev) => applyProviderEvent(prev, session.mode, providerEvent));
  }

  function applyStdoutChunk(chunk: string) {
    stdoutBufferRef.current += chunk;
    const lines = stdoutBufferRef.current.split(/\r?\n/);
    stdoutBufferRef.current = lines.pop() ?? "";
    for (const line of lines) applyStdoutLine(line);
  }

  function flushStdoutBuffer() {
    const pending = stdoutBufferRef.current;
    stdoutBufferRef.current = "";
    if (pending.trim()) applyStdoutLine(pending);
  }

  function newRunId() {
    const cryptoObj = window.crypto;
    if (cryptoObj?.randomUUID) return cryptoObj.randomUUID();
    return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
  }

  function lifecycleStartedAtMs(event: RunLifecycleEvent): number | null {
    const startedAt = event.payload?.started_at;
    if (typeof startedAt !== "string") return null;
    const parsed = Date.parse(startedAt);
    return Number.isFinite(parsed) ? parsed : null;
  }

  function clearLifecycleFinishTimer() {
    if (runLifecycleFinishTimerRef.current !== null) {
      window.clearTimeout(runLifecycleFinishTimerRef.current);
      runLifecycleFinishTimerRef.current = null;
    }
  }

  function finalizeRunFromLifecycle(
    runId: string,
    status: "done" | "error",
    detail?: string,
  ) {
    clearLifecycleFinishTimer();
    const run = currentRunRef.current;
    if (!run || run.id !== runId || run.cancelled) return;
    flushStdoutBuffer();
    currentRunRef.current = null;
    const durationMs = Date.now() - run.turnStart;
    if (status === "done") {
      setEntries((prev) => {
        for (let i = prev.length - 1; i >= 0; i--) {
          if (prev[i].kind === "message" && prev[i].role === "assistant") {
            const updated = [...prev];
            updated[i] = { ...updated[i], durationMs } as TranscriptEntry;
            return updated;
          }
        }
        return prev;
      });
      setLastStatusText(
        scheduledWakeupRef.current
          ? "Wakeup scheduled"
          : activeToolNameRef.current
            ? `Used ${formatToolLabel(activeToolNameRef.current)}`
            : "Done",
      );
      setRunStatus("done");
      playTurnCompleteSound();
    } else {
      setLastStatusText(activeToolNameRef.current ? `Used ${formatToolLabel(activeToolNameRef.current)}` : "Error");
      setRunStatus("error");
      setEntries((prev) =>
        appendMeta(
          prev,
          nextEntryId("run-lifecycle-error"),
          "run failed",
          detail || "Run ended before the live stream reported completion.",
          "error",
        ),
      );
    }
    scheduledWakeupRef.current = false;
    setActiveTool(null);
    setRunning(false);
    setActiveRunId(null);
    wsRef.current?.close();
    wsRef.current = null;
    window.setTimeout(() => refreshRunHistoryFromBackend(false), 250);
  }

  function scheduleLifecycleFinish(
    runId: string,
    status: "done" | "error",
    detail?: string,
  ) {
    clearLifecycleFinishTimer();
    const delay = status === "done" ? 1500 : 500;
    runLifecycleFinishTimerRef.current = window.setTimeout(() => {
      runLifecycleFinishTimerRef.current = null;
      finalizeRunFromLifecycle(runId, status, detail);
    }, delay);
  }

  function handleRunLifecycleEvent(event: RunLifecycleEvent) {
    if (event.session_id !== session.id || event.run_id !== activeRunId) return;
    if (event.type === "run.started") {
      const startedAt = lifecycleStartedAtMs(event);
      if (startedAt !== null && currentRunRef.current?.id === event.run_id) {
        currentRunRef.current.turnStart = startedAt;
        setRunStartedAt(startedAt);
        setNow(Date.now());
      }
      setRunStatus("running");
      setRunning(true);
      return;
    }
    if (event.type === "run.output.started") {
      setRunStatus("running");
      setRunning(true);
      return;
    }
    if (event.type === "run.tool.started") {
      const toolName = event.payload?.name;
      if (typeof toolName === "string" && toolName) {
        const toolUseId = event.payload?.tool_use_id;
        setActiveTool(toolName, typeof toolUseId === "string" ? toolUseId : null);
      }
      setEntries((prev) => applyRunToolStartedEvent(prev, event));
      return;
    }
    if (event.type === "run.tool.completed") {
      const toolUseId = event.payload?.tool_use_id;
      completeActiveTool(typeof toolUseId === "string" ? toolUseId : null);
      setEntries((prev) => applyRunToolCompletedEvent(prev, event));
      return;
    }
    if (event.type === "run.message.created") {
      setEntries((prev) => applyRunMessageEvent(prev, event));
      return;
    }
    if (event.type === "run.completed") {
      scheduleLifecycleFinish(event.run_id, "done");
      return;
    }
    const detail =
      typeof event.payload?.detail === "string"
        ? event.payload.detail
        : event.type === "run.stale"
          ? "Run is no longer active."
          : undefined;
    scheduleLifecycleFinish(event.run_id, "error", detail);
  }

  function attachActiveRun(data: ActiveRunData | null): boolean {
    if (!data || currentRunRef.current) return false;
    const startedAt = activeRunStartedAtMs(data);
    const run = {
      id: data.run_id,
      prompt: "",
      followUp: false,
      model: "",
      permissionMode: "",
      turnStart: startedAt,
      reconnects: 0,
      offset: data.stream_offset,
      cancelled: false,
    };
    currentRunRef.current = run;
    setRunStatus("running");
    setRunning(true);
    setActiveRunId(run.id);
    setRunStartedAt(startedAt);
    setNow(Date.now());
    openRunSocket(run, true);
    return true;
  }

  function cancelRun() {
    const ws = wsRef.current;
    if (currentRunRef.current) currentRunRef.current.cancelled = true;
    if (ws?.readyState === WebSocket.OPEN) {
      // SDK and legacy speak different cancel frames. Agent-runner expects
      // {type:"interrupt"} per agent-runner/src/ws.ts; legacy run handler
      // expects {cancel:true}.
      const cancelFrame =
        session.runtime === "sdk"
          ? { type: "interrupt" }
          : { cancel: true };
      ws.send(JSON.stringify(cancelFrame));
    }
    ws?.close();
    wsRef.current = null;
    setLastStatusText(activeToolNameRef.current ? `Used ${formatToolLabel(activeToolNameRef.current)}` : "Stopped");
    scheduledWakeupRef.current = false;
    setActiveTool(null);
    setRunning(false);
    setActiveRunId(null);
    setRunStatus((prev) => (prev === "running" ? "done" : prev));
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
    if (running) {
      setQueuedMessages((prev) => [
        ...prev,
        { id: nextQueuedMessageId(), text: promptText, displayText, skillName },
      ]);
      return;
    }
    startRun(promptText, displayText, skillName);
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
    wsRef.current?.close();
    stdoutBufferRef.current = "";
    primeTurnCompleteSound();
    const followUp = entries.length > 0;
    const turnStart = Date.now();
    const run = {
      id: newRunId(),
      prompt: trimmed,
      skillName,
      followUp,
      model: selectedModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID ? "" : selectedModelId,
      permissionMode: composerMode,
      turnStart,
      reconnects: 0,
      offset: 0,
      cancelled: false,
    };
    currentRunRef.current = run;
    setActiveRunId(run.id);
    if (skillName) {
      setEntries((prev) => appendSkillInvocation(prev, skillName, trimmed, nowIso()));
    } else {
      setEntries((prev) => [
        ...prev,
        {
          id: nextEntryId("user"),
          kind: "message",
          role: "user",
          text: displayText,
          time: nowIso(),
        },
      ]);
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
    if (session.runtime === "sdk") {
      openSdkRunSocket(run);
    } else {
      openRunSocket(run, false);
    }
  }

  // SDK-runtime live tap. Connects to the orchestrator's /agent-ws reverse
  // proxy, which fans events from the pod-side agent-runner. Each WebSocket
  // frame is one SDK event (same shape applyProviderEvent already handles),
  // so the receive path is much simpler than the legacy stdout-chunking
  // pipeline. Lifecycle is "open per turn, close on result event" — the
  // agent-runner accepts user frames from any client and broadcasts events
  // to every connected client, so re-opening on each turn is harmless.
  function openSdkRunSocket(run: NonNullable<typeof currentRunRef.current>) {
    const wsUrl =
      `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}` +
      `/api/sessions/${session.id}/agent-ws`;
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;
    ws.onopen = () => {
      ws.send(
        JSON.stringify({
          type: "user",
          message: { role: "user", content: run.prompt },
        }),
      );
    };
    ws.onmessage = (event) => {
      let parsed: unknown;
      try {
        parsed = JSON.parse(String(event.data));
      } catch {
        setEntries((prev) =>
          appendMeta(
            prev,
            nextEntryId("agent-ws-message"),
            "agent-ws message",
            String(event.data),
          ),
        );
        return;
      }
      if (!isJsonObject(parsed)) return;

      // Bookkeeping for usage/active-tool. Two flavors of SDK events come
      // through this WS: claude's `{type:"assistant"|"user"|"result"}` and
      // codex's `{type:"item.started"|"item.completed"|"turn.completed"}`.
      // The chat pane renders both shapes via applyProviderEvent → the
      // right applyXxxEvent for session.mode; the bookkeeping here also
      // forks on the shape so the streaming pill + context pie work
      // regardless of which runtime is streaming.
      const t = parsed.type;
      if (t === "assistant") {
        const msg = parsed.message;
        if (isJsonObject(msg)) {
          const u = msg.usage as ClaudeUsage | undefined;
          const total = totalContextTokens(u);
          if (total > 0) setTokensUsed(total);
          if (Array.isArray(msg.content)) {
            const toolBlock = (msg.content as unknown[]).find(
              (b): b is JsonObject => isJsonObject(b) && b.type === "tool_use",
            );
            const toolName =
              toolBlock && typeof toolBlock.name === "string" ? toolBlock.name : null;
            const toolUseId =
              toolBlock && typeof toolBlock.id === "string" ? toolBlock.id : null;
            setActiveTool(toolName, toolUseId);
          }
        }
      } else if (t === "user") {
        completeActiveTool();
      } else if (t === "result") {
        const u = parsed.usage as ClaudeUsage | undefined;
        const total = totalContextTokens(u);
        if (total > 0) setTokensUsed(total);
        setActiveTool(null);
      } else if (t === "item.started" || t === "item.updated") {
        // Codex flag: a tool-like item (command_execution / file_change /
        // mcp_tool_call / web_search) is in flight. Show its kind on the
        // streaming pill the same way Claude's tool_use blocks do.
        const item = parsed.item;
        if (isJsonObject(item)) {
          const itemType = typeof item.type === "string" ? item.type : "";
          const toolish = new Set([
            "command_execution",
            "file_change",
            "mcp_tool_call",
            "web_search",
          ]);
          if (toolish.has(itemType)) {
            setActiveTool(itemType, typeof item.id === "string" ? item.id : null);
          }
        }
      } else if (t === "turn.completed") {
        // Codex's per-turn closing event. Usage rides on the event itself
        // (not nested under `message` like claude does).
        const u = parsed.usage as ClaudeUsage | undefined;
        const total = totalContextTokens(u);
        if (total > 0) setTokensUsed(total);
        setActiveTool(null);
      }

      setEntries((prev) => applyProviderEvent(prev, session.mode, parsed));

      // Terminal events. Each runtime closes the WS so a fresh open per
      // turn works. The runners themselves stay alive for the pod's
      // lifetime; only the SPA's WS lifecycle is per-turn.
      //   claude: `result` (analog of codex's turn.completed)
      //   codex:  `turn.completed` (success) / `turn.failed` (error)
      const isTerminal =
        t === "result" || t === "turn.completed" || t === "turn.failed";
      if (isTerminal) {
        currentRunRef.current = null;
        const durationMs = Date.now() - run.turnStart;
        setEntries((prev) => {
          for (let i = prev.length - 1; i >= 0; i--) {
            if (prev[i].kind === "message" && prev[i].role === "assistant") {
              const updated = [...prev];
              updated[i] = { ...updated[i], durationMs } as TranscriptEntry;
              return updated;
            }
          }
          return prev;
        });
        setLastStatusText(
          scheduledWakeupRef.current
            ? "Wakeup scheduled"
            : activeToolNameRef.current
              ? `Used ${formatToolLabel(activeToolNameRef.current)}`
              : "Done",
        );
        scheduledWakeupRef.current = false;
        setActiveTool(null);
        setRunStatus("done");
        setRunning(false);
        setActiveRunId(null);
        playTurnCompleteSound();
        ws.close();
      }
    };
    ws.onerror = () => {
      if (!run.cancelled) {
        setLastStatusText("Reconnecting");
      }
    };
    ws.onclose = (event) => {
      if (currentRunRef.current?.id !== run.id || run.cancelled) {
        return;
      }
      // If the WS dropped before a `result` arrived, the runner is still
      // chewing. The agent-runner replays unseen events to a fresh client
      // via the canonical Cosmos log, but for simplicity v1 just shows
      // "connection lost" — the user can refresh the page to pick up
      // history. (Reconnect with replay is a follow-up.)
      currentRunRef.current = null;
      setActiveRunId(null);
      setLastStatusText("Connection lost");
      scheduledWakeupRef.current = false;
      setActiveTool(null);
      setRunning(false);
      setEntries((entries) =>
        appendMeta(
          entries,
          nextEntryId("ws-close"),
          "Connection lost",
          `WebSocket closed with code ${event.code}${
            event.reason ? ` — ${event.reason}` : ""
          }. Reload to resume.`,
          "error",
        ),
      );
      setRunStatus("error");
    };
  }

  function openRunSocket(run: NonNullable<typeof currentRunRef.current>, resume: boolean) {
    const wsUrl =
      `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}` +
      `/api/sessions/${session.id}/run`;
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;
    ws.onopen = () => {
      ws.send(
        JSON.stringify({
          run_id: run.id,
          resume,
          prompt: resume ? "" : run.prompt,
          skill_name: resume ? undefined : run.skillName,
          offset: resume ? run.offset : 0,
          follow_up: run.followUp,
          model: run.model,
          permission_mode: run.permissionMode,
        }),
      );
    };
    ws.onmessage = (event) => {
      let msg: RunEvent;
      try {
        msg = JSON.parse(String(event.data));
      } catch {
        setEntries((prev) =>
          appendMeta(prev, nextEntryId("websocket-message"), "websocket message", String(event.data)),
        );
        return;
      }
      if (msg.stream === "stdout" && msg.data) {
        run.offset += msg.data.length;
        applyStdoutChunk(msg.data);
      } else if (msg.stream === "stderr" && msg.data) {
        run.offset += msg.data.length;
        setEntries((prev) =>
          appendMeta(prev, nextEntryId("stderr"), "stderr", msg.data, "error"),
        );
      } else if (msg.status === "done") {
        clearLifecycleFinishTimer();
        flushStdoutBuffer();
        currentRunRef.current = null;
        const durationMs = Date.now() - run.turnStart;
        setEntries((prev) => {
          for (let i = prev.length - 1; i >= 0; i--) {
            if (prev[i].kind === "message" && prev[i].role === "assistant") {
              const updated = [...prev];
              updated[i] = { ...updated[i], durationMs } as TranscriptEntry;
              return updated;
            }
          }
          return prev;
        });
        setLastStatusText(
          scheduledWakeupRef.current
            ? "Wakeup scheduled"
            : activeToolNameRef.current
              ? `Used ${formatToolLabel(activeToolNameRef.current)}`
              : "Done",
        );
        scheduledWakeupRef.current = false;
        setActiveTool(null);
        setRunStatus("done");
        setRunning(false);
        setActiveRunId(null);
        playTurnCompleteSound();
        ws.close();
      } else if (msg.status === "attached") {
        // Sync run_id from server in case it sanitised the client-provided value.
        if (msg.run_id && msg.run_id !== run.id) {
          run.id = msg.run_id;
          setActiveRunId(msg.run_id);
        }
      } else if (msg.status === "error") {
        clearLifecycleFinishTimer();
        flushStdoutBuffer();
        currentRunRef.current = null;
        setLastStatusText(activeToolNameRef.current ? `Used ${formatToolLabel(activeToolNameRef.current)}` : "Error");
        scheduledWakeupRef.current = false;
        setActiveTool(null);
        setRunStatus("error");
        setRunning(false);
        setActiveRunId(null);
        setEntries((prev) =>
          appendMeta(prev, nextEntryId("run-error"), "run failed", msg.detail, "error"),
        );
        ws.close();
      }
    };
    ws.onerror = () => {
      if (!run.cancelled) {
        setLastStatusText("Reconnecting");
      }
    };
    ws.onclose = (event) => {
      flushStdoutBuffer();
      if (currentRunRef.current?.id !== run.id || run.cancelled) {
        return;
      }
      // If the backend closes before a done/error frame, assume transport loss
      // and reattach to the pod-local run stream. Normal done/cancel paths clear
      // currentRunRef before this handler runs.
      if (run.reconnects < 8) {
        run.reconnects += 1;
        const delay = Math.min(5000, 250 * 2 ** (run.reconnects - 1));
        setLastStatusText("Reconnecting");
        window.setTimeout(() => {
          if (currentRunRef.current?.id === run.id && !run.cancelled) {
            openRunSocket(run, true);
          }
        }, delay);
        return;
      }
      currentRunRef.current = null;
      setActiveRunId(null);
      setLastStatusText("Connection lost");
      scheduledWakeupRef.current = false;
      setActiveTool(null);
      setRunning(false);
      setEntries((entries) =>
        appendMeta(
          entries,
          nextEntryId("ws-close"),
          "Connection lost",
          `WebSocket closed with code ${event.code}${
            event.reason ? ` — ${event.reason}` : ""
          }. Resend to continue.`,
          "error",
        ),
      );
      setRunStatus("error");
    };
  }

  const submitStatus =
    runStatus === "running"
      ? "streaming"
      : runStatus === "error"
        ? "error"
        : undefined;

  const provider: Provider = isClaude ? "anthropic" : "codex";
  const modeLabel = MODE_LABELS[session.mode];
  const ready = session.status === "Active";
  const currentSkillState = currentSessionSkillState(testState, rolloutState);
  const testActionActive = currentSkillState === "test";
  const rolloutActionActive = currentSkillState === "rollout";
  const selectedModel =
    modelOptions.find((m) => m.id === selectedModelId) ?? modelOptions[0];
  const contextWindow = getContextWindow(selectedModel.id);
  const usagePct = Math.min(100, (tokensUsed / contextWindow) * 100);
  const usageLevel = usagePct >= 75 ? "high" : usagePct >= 50 ? "mid" : "low";

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
  // When a tool call is in flight, show its name. Otherwise cycle the
  // generic verbs every 3s (matches cloudcli's ClaudeStatus pattern).
  const verbIndex = Math.floor(now / 3000) % STREAM_VERBS.length;
  const verb = activeToolName
    ? `Using ${formatToolLabel(activeToolName)}`
    : STREAM_VERBS[verbIndex];

  const sendStdin = (text: string) => {
    wsRef.current?.send(JSON.stringify({ stdin: text }));
  };
  const toggleRunTab = (tab: Exclude<RunTab, "chat">) => {
    setActiveTab((current) => (current === tab ? "chat" : tab));
  };

  return (
    <RunContext.Provider value={{ sendStdin, user }}>
    <section className="run-panel">
      <header className="run-header">
        <div className="run-header-title">
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
        </div>
        <nav className="run-tabs" aria-label="Session actions">
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
        </nav>
      </header>

      <main
        className={`run-main run-main-${runStatus}`}
        ref={transcriptScrollRef as React.RefObject<HTMLElement>}
        style={chatFontScaleStyle}
      >
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
              </DropdownMenuContent>
            </DropdownMenu>
            <p className="run-empty-status">
              Ready to use {selectedModel.label}. Start typing your message below.
            </p>
            <p className="run-empty-kbd">
              Press <kbd>⌘K</kbd> to switch model
            </p>
          </div>
        ) : (
          <>
            {continueHintVisible && (
              <div className="run-continue-hint" role="status">
                Continuing previous conversation
              </div>
            )}
            <RunMessages
              entries={entries}
              provider={provider}
              showThinking={runPrefs.showThinking}
              autoExpandTools={runPrefs.autoExpandTools}
              showTimestamps={runPrefs.showTimestamps}
              showDuration={runPrefs.showDuration}
              onQuote={appendQuotedMessage}
            />
          </>
        )}
      </main>

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
            <ProviderIcon provider={provider} />
          </span>
          <span className="run-status-text">
            <span className="run-status-verb">{running ? verb : lastStatusText}</span>
            {running && (
              <span className="run-status-dots" aria-hidden="true">
                {dots}
              </span>
            )}
          </span>
          {running && (
            <>
              <span className="run-status-elapsed" title="elapsed">
                {elapsedLabel}
              </span>
              <button
                type="button"
                className="run-status-stop"
                onClick={cancelRun}
                aria-label="Stop generating"
              >
                <SquareIcon className="run-status-stop-icon" aria-hidden="true" />
                <span>Stop</span>
                <kbd className="run-status-kbd">ESC</kbd>
              </button>
            </>
          )}
        </div>
      )}

      {/* Floating scroll-to-bottom button — fades in when the transcript
          has been scrolled up. Snaps the user back to the latest message
          and re-enables auto-scroll. Always rendered so the opacity
          transition reads cleanly; pointer-events handled in CSS. */}
      {activeTab === "chat" && entries.length > 0 && (
        <button
          type="button"
          className={`run-scroll-to-bottom${
            userScrolledUp ? "" : " run-scroll-to-bottom-hidden"
          }`}
          onClick={() => {
            const main = transcriptScrollRef.current;
            if (main) main.scrollTop = main.scrollHeight;
            setUserScrolledUp(false);
          }}
          aria-label="Scroll to latest"
        >
          <ArrowDownIcon size={16} strokeWidth={2.2} aria-hidden="true" />
        </button>
      )}

      {activeTab === "chat" && (
        <footer
          className={`run-composer-wrap${dragActive ? " run-composer-wrap-drag" : ""}`}
          ref={composerWrapRef}
          style={chatFontScaleStyle}
          onDragOver={(e) => {
            e.preventDefault();
            if (!dragActive) setDragActive(true);
          }}
          onDragLeave={(e) => {
            // dragleave fires on child crossings; only deactivate if
            // we've left the wrap entirely.
            if (e.currentTarget === e.target) setDragActive(false);
          }}
          onDrop={(e) => {
            e.preventDefault();
            setDragActive(false);
            handleAttachmentFiles(e.dataTransfer?.files ?? null);
          }}
          onPaste={(e) => {
            // Pull image/file data out of the clipboard. Plain text
            // continues to paste into the textarea naturally.
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
        >
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
                        const skillTrigger = `${isClaude ? "/" : "$"}${message.skillName}`;
                        setComposerValue(
                          message.skillName
                            ? message.text.trim()
                              ? `${skillTrigger}\n\n${message.text}`
                              : skillTrigger
                            : message.text,
                        );
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
          <PromptInput onSubmit={handleSubmit} className="run-composer">
            <PromptInputTextarea
              className="run-composer-textarea"
              placeholder={`Type / for commands, @ for files, or ask ${modeLabel} anything...`}
            />
            <PromptInputFooter className="run-composer-footer">
              <PromptInputTools className="run-composer-tools">
                {/* Image-attach button — opens the hidden file input.
                    Drag-and-drop and clipboard paste are wired
                    separately on the composer wrap. */}
                <button
                  type="button"
                  className="run-composer-icon-btn"
                  aria-label="Attach files"
                  onClick={() => fileInputRef.current?.click()}
                >
                  <ImageIcon className="run-composer-icon" aria-hidden="true" />
                </button>
                {/* Permission-mode dropdown — five cloudcli-equivalent
                    modes. Backend wire-through: `acceptEdits`/`auto`/
                    `bypassPermissions` map to `claude -p
                    --dangerously-skip-permissions`; `plan` is rendered
                    as a prompt prefix (Claude's headless mode doesn't
                    have a CLI flag for plan today); `default` is the
                    no-flag baseline. */}
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <button
                      type="button"
                      className="run-mode-pill run-mode-pill-button"
                      aria-label="Permission mode"
                    >
                      <span
                        className="run-mode-dot"
                        aria-hidden="true"
                        style={{ background: PERMISSION_MODE_INFO[composerMode].dotColor }}
                      />
                      {PERMISSION_MODE_INFO[composerMode].label}
                      <ChevronDownIcon
                        className="run-mode-chevron"
                        aria-hidden="true"
                      />
                    </button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent
                    side="top"
                    align="start"
                    className="run-mode-menu"
                  >
                    {(Object.keys(PERMISSION_MODE_INFO) as RunComposerMode[]).map(
                      (modeKey) => {
                        const info = PERMISSION_MODE_INFO[modeKey];
                        return (
                          <DropdownMenuItem
                            key={modeKey}
                            onSelect={() => setComposerMode(modeKey)}
                          >
                            <span className="run-mode-menu-row">
                              <span className="run-mode-menu-meta">
                                <span
                                  className="run-mode-menu-dot"
                                  aria-hidden="true"
                                  style={{ background: info.dotColor }}
                                />
                                <span className="run-mode-menu-label">
                                  {info.label}
                                </span>
                                <span className="run-mode-menu-desc">
                                  {info.desc}
                                </span>
                              </span>
                              {composerMode === modeKey && (
                                <CheckIcon
                                  className="run-mode-menu-check"
                                  aria-hidden="true"
                                />
                              )}
                            </span>
                          </DropdownMenuItem>
                        );
                      },
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
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
                    className={`run-composer-icon-btn run-composer-action-btn run-test-action-btn${testActionActive ? " is-active" : ""}`}
                    href={testState.url}
                    target="_blank"
                    rel="noreferrer"
                    onClick={() => {
                      void markTestState({ ...testState, active: true });
                    }}
                    aria-label="Open test environment"
                    title="Open test environment"
                  >
                    <FlaskConicalIcon className="run-composer-icon" aria-hidden="true" />
                    {testState.slot_index != null && (
                      <span className="run-command-menu-count">
                        {testState.slot_index}
                      </span>
                    )}
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
                    {testState?.slot_index != null && (
                      <span className="run-command-menu-count">
                        {testState.slot_index}
                      </span>
                    )}
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
              </PromptInputTools>
              <span
                className={`run-composer-hint${composerText.length > 0 ? " run-composer-hint-faded" : ""}`}
              >
                {runPrefs.sendByCtrlEnter
                  ? "⌘/Ctrl+Enter to send · Enter for new line · / for slash commands"
                  : "Enter to send · Shift+Enter for new line · / for slash commands"}
              </span>
              {composerText.length > 0 && (
                <button
                  type="button"
                  className="run-composer-clear"
                  aria-label="Clear input"
                  onMouseDown={(e) => {
                    // mousedown so the button doesn't blur the textarea
                    // before the click reaches it (which would also
                    // close the slash palette via blur).
                    e.preventDefault();
                    setComposerValue("");
                  }}
                >
                  <XIcon size={14} strokeWidth={2.2} aria-hidden="true" />
                </button>
              )}
              <PromptInputSubmit
                className="run-submit-btn"
                status={submitStatus}
                onStop={cancelRun}
                disabled={!ready}
              >
                {/* When idle, force the cloudcli-style paper-plane icon.
                    When streaming/error, fall through to AI Elements'
                    built-in Spinner/Stop/X (children is undefined). */}
                {submitStatus ? undefined : (
                  <SendHorizontalIcon className="run-submit-icon" aria-hidden="true" />
                )}
              </PromptInputSubmit>
            </PromptInputFooter>
          </PromptInput>
        </footer>
      )}
    </section>
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
  // Phase E: also persisted to the Cosmos profile row so prefs ride across
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
  const [sessionActivities, setSessionActivities] = useState<Record<string, AgentSessionActivity>>({});
  const [modeMenuOpen, setModeMenuOpen] = useState(false);
  const [interactionMenuOpen, setInteractionMenuOpen] = useState(false);
  const [profileMenuOpen, setProfileMenuOpen] = useState(false);
  const [defaultInteraction, setDefaultInteraction] =
    useState<SessionInteraction>(readDefaultInteraction);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [draggingSessionId, setDraggingSessionId] = useState<string | null>(null);
  const [dragOverSessionId, setDragOverSessionId] = useState<string | null>(null);
  const [defaultSessionMode, setDefaultSessionMode] =
    useState<DefaultSessionMode>(readDefaultSessionMode);
  // Inline rename state. The idle name control is intentionally only as wide
  // as the label plus a small floor so the rest of the row remains a tab target.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingValue, setEditingValue] = useState("");
  const initialSessionId = useRef<string | null>(readInitialSessionId());
  const glimmungLaunchContext = useRef<GlimmungLaunchContext | null>(
    readGlimmungLaunchContext()
  );

  const updateSessionActivity = useCallback(
    (id: string, activity: AgentSessionActivity) => {
      setSessionActivities((prev) => {
        if (prev[id] === activity) return prev;
        return { ...prev, [id]: activity };
      });
    },
    [],
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

  // Close any open dropdown on an outside click. Menus use a `data-menu`
  // attribute so a single listener can route by which menu is open.
  useEffect(() => {
    if (!modeMenuOpen && !profileMenuOpen && !interactionMenuOpen) return;
    const close = (e: MouseEvent) => {
      const target = e.target as HTMLElement | null;
      const root = target?.closest("[data-menu]") as HTMLElement | null;
      if (root?.dataset.menu === "mode") return;
      if (root?.dataset.menu === "profile") return;
      if (root?.dataset.menu === "interaction") return;
      setModeMenuOpen(false);
      setProfileMenuOpen(false);
      setInteractionMenuOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [modeMenuOpen, profileMenuOpen, interactionMenuOpen]);

  async function refresh() {
    try {
      const res = await authedFetch("/api/sessions");
      if (!res.ok) throw new Error(`list failed: ${res.status}`);
      const listed: Session[] = (await res.json()).map(normalizeSession);
      setSessions((prev) => {
        const previousById = new Map(prev.map((session) => [session.id, session]));
        const merged = listed.map((session) =>
          mergeMutualSessionSkillState(session, previousById.get(session.id)),
        );
        return user ? orderSessions(merged, readSessionOrder(sessionOrderStorageKey(user))) : merged;
      });
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }

  useEffect(() => {
    if (user) void refresh();
  }, [user]);

  useEffect(() => {
    if (!user) return;
    const t = setInterval(() => setNowMs(Date.now()), SESSION_RUNTIME_TICK_MS);
    return () => clearInterval(t);
  }, [user]);

  useEffect(() => {
    const context = glimmungLaunchContext.current;
    if (!user || user.installation_id == null || !context) return;
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

  useEffect(() => {
    if (!user) return;
    const hasPending = sessions.some((s) => s.status !== "Active") || closingIds.size > 0;
    if (!hasPending) return;
    const t = setInterval(refresh, POLL_INTERVAL_MS);
    return () => clearInterval(t);
  }, [sessions, user, closingIds]);

  useEffect(() => {
    if (!user) return;
    const source = new EventSource("/api/sessions/events", { withCredentials: true });
    const refreshSessions = () => void refresh();
    source.addEventListener("sessions-changed", refreshSessions);
    return () => {
      source.removeEventListener("sessions-changed", refreshSessions);
      source.close();
    };
  }, [user]);

  useEffect(() => {
    if (!user) return;
    const refreshIfVisible = () => {
      if (document.visibilityState === "visible") void refresh();
    };
    document.addEventListener("visibilitychange", refreshIfVisible);
    window.addEventListener("focus", refreshIfVisible);
    return () => {
      document.removeEventListener("visibilitychange", refreshIfVisible);
      window.removeEventListener("focus", refreshIfVisible);
    };
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
      const next: Record<string, AgentSessionActivity> = {};
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
      if (editingId) return;
      const targetId = shortcutSessionId(event.target) ?? active;
      if (!targetId || closingIds.has(targetId)) return;
      const session = sessions.find((s) => s.id === targetId);
      if (!session) return;
      event.preventDefault();
      event.stopPropagation();
      setSidebarCollapsed(false);
      startEditing(session.id, session.name);
    };
    window.addEventListener("keydown", renameHighlightedSession, { capture: true });
    return () => window.removeEventListener("keydown", renameHighlightedSession, { capture: true });
  }, [sessions, active, closingIds, editingId]);

  function activate(id: string) {
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

  async function createSession(mode: SessionMode = defaultSessionMode) {
    if (isDefaultSessionMode(mode)) {
      setDefaultSessionMode(mode);
      writeDefaultSessionMode(mode);
    }
    setBusy(true);
    setModeMenuOpen(false);
    setSidebarCollapsed(false);
    setError(null);
    try {
      const res = await authedFetch("/api/sessions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode }),
      });
      if (!res.ok) throw new Error(`create failed: ${res.status}`);
      const created: Session = normalizeSession(await res.json());
      if (HEADLESS_MODES.has(mode)) {
        writeSessionInteraction(created.id, defaultInteraction);
      }
      await refresh();
      activate(created.id);
      startEditing(created.id, created.name);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
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
    setModeMenuOpen(false);
  }

  function selectDefaultInteraction(interaction: SessionInteraction) {
    const provider = MODE_MENU_ICONS[defaultSessionMode];
    const mode = defaultModeFor(provider, interaction);
    setDefaultInteraction(interaction);
    writeDefaultInteraction(interaction);
    setDefaultSessionMode(mode);
    writeDefaultSessionMode(mode);
    setInteractionMenuOpen(false);
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

  function startEditing(id: string, current: string | null) {
    setEditingId(id);
    setEditingValue(current ?? "");
  }

  function commitEditing() {
    if (editingId) {
      const trimmed = editingValue.trim();
      const session = sessions.find((s) => s.id === editingId);
      const nextName = trimmed === "" && session ? defaultSessionName(session) : trimmed;
      void renameSession(editingId, nextName === "" ? null : nextName);
    }
    setEditingId(null);
    setEditingValue("");
  }

  function cancelEditing() {
    setEditingId(null);
    setEditingValue("");
  }

  async function deleteSession(id: string) {
    if (closingIds.has(id)) return;
    setError(null);
    setClosingIds((prev) => new Set(prev).add(id));
    setMounted((prev) => {
      if (!prev.has(id)) return prev;
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
    setEditingId((prev) => (prev === id ? null : prev));
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

  if (user.installation_id == null) {
    return <OnboardingWall user={user} onLogout={logout} />;
  }

  const selectedProvider = MODE_MENU_ICONS[defaultSessionMode];
  const configMode = PROVIDER_CONFIG_MODES[selectedProvider];

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

        <div className="sidebar-section">
          <div className="new-row new-row-launcher" data-menu="mode">
            <button
              className={`new-row-provider-toggle${modeMenuOpen ? " is-open" : ""}`}
              onClick={() => setModeMenuOpen((v) => !v)}
              disabled={busy}
              aria-label="choose provider"
              aria-expanded={modeMenuOpen}
            >
              <span className="new-row-provider-slot">
                <ProviderIcon
                  provider={selectedProvider}
                  className="new-row-provider-icon"
                />
              </span>
              <IconChevronDown className="new-row-provider-chevron" />
            </button>
            <div className="new-row-interaction-container" data-menu="interaction">
              <button
                className={`new-row-interaction-toggle${interactionMenuOpen ? " is-open" : ""}`}
                onClick={() => setInteractionMenuOpen((v) => !v)}
                disabled={busy}
                aria-label={`choose interaction: ${INTERACTION_LABELS[defaultInteraction]}`}
                aria-expanded={interactionMenuOpen}
                title={INTERACTION_LABELS[defaultInteraction]}
              >
                <InteractionIcon
                  interaction={defaultInteraction}
                  className="new-row-interaction-icon"
                />
              </button>
              {interactionMenuOpen && (
                <ul className="dropdown dropdown-interaction" role="menu">
                  {INTERACTION_OPTIONS.map((interaction) => (
                    <li key={interaction}>
                      <button
                        onClick={() => selectDefaultInteraction(interaction)}
                        disabled={
                          busy ||
                          PROVIDER_INTERACTION_MODES[MODE_MENU_ICONS[defaultSessionMode]][interaction] == null
                        }
                        aria-label={`Use ${INTERACTION_LABELS[interaction]} interaction`}
                        title={INTERACTION_LABELS[interaction]}
                        className={defaultInteraction === interaction ? "is-selected" : undefined}
                      >
                        <InteractionIcon
                          interaction={interaction}
                          className="dropdown-interaction-icon"
                        />
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
            <div className="new-row-action-group" role="group" aria-label="session actions">
              <button
                className="new-row-action"
                onClick={() => createSession(defaultSessionMode)}
                disabled={busy}
                aria-label={`Start ${MODE_LABELS[defaultSessionMode]} session`}
              >
                <span className="row-icon"><IconPlus /></span>
              </button>
              <button
                className="new-row-action"
                onClick={() => createSession("api_key")}
                disabled={busy}
                aria-label="Start API key session"
              >
                <IconKey className="new-row-action-icon" />
              </button>
              <button
                className="new-row-action"
                onClick={() => createSession(configMode)}
                disabled={busy}
                aria-label={`Start ${MODE_LABELS[configMode]} session`}
              >
                <IconWrench className="new-row-action-icon" />
              </button>
            </div>
            {modeMenuOpen && (
              <ul className="dropdown dropdown-provider" role="menu">
                {PROVIDERS.map((provider) => {
                  const mode = defaultModeFor(provider, defaultInteraction);
                  return (
                    <li key={provider}>
                    <button
                      onClick={() => setDefaultProvider(provider)}
                      disabled={busy}
                      aria-label={MODE_LABELS[mode]}
                    >
                      <ProviderIcon
                        provider={provider}
                        className="dropdown-provider-icon"
                      />
                    </button>
                  </li>
                  );
                })}
              </ul>
            )}
          </div>
        </div>

        {error && <pre className="error">{error}</pre>}

        <div className="sidebar-list">
          <div className="sidebar-section-label">Sessions</div>
          <ul className="sessions">
            {sessions.length === 0 && <li className="sessions-empty">no sessions</li>}
            {sessions.map((s) => {
              const isEditing = editingId === s.id;
              const isLive = s.status === "Active";
              const isClosing = closingIds.has(s.id);
              const isActive = active === s.id && !isClosing;
              const statusDotClass = sessionStatusDotClass(s, sessionActivities[s.id]);
              const statusLabel = sessionStatusLabel(s, sessionActivities[s.id]);
              const bootLabel = sessionBootLabel(s, nowMs);
              const runtimeLabel = sessionRuntimeLabel(s, nowMs);
              const skillStateClass = sessionSkillStateClass(s);
              return (
                <li
                  key={s.id}
                  data-session-id={s.id}
                  className={`${isActive ? "is-open" : ""}${isClosing ? " is-closing" : ""}${skillStateClass}${draggingSessionId === s.id ? " is-dragging" : ""}${dragOverSessionId === s.id && draggingSessionId !== s.id ? " is-drag-over" : ""}`}
                  draggable={!isEditing && !isClosing}
                  onDragStart={(e) => dragSessionStart(s.id, e)}
                  onDragOver={(e) => dragSessionOver(s.id, e)}
                  onDrop={(e) => dropSession(s.id, e)}
                  onDragEnd={dragSessionEnd}
                  onClick={isEditing || isClosing ? undefined : (e) => openSession(s.id, e)}
                  title={sidebarCollapsed ? `${sessionDisplayName(s)} (${statusLabel})` : undefined}
                >
                  <div className="session-row-top">
                    <span
                      className={statusDotClass}
                      title={statusLabel}
                      aria-label={`status: ${statusLabel}`}
                    />
                    <ProviderIcon provider={MODE_MENU_ICONS[s.mode]} className="session-provider-icon" />
                    {isEditing ? (
                      <input
                        className="session-name-input"
                        value={editingValue}
                        autoFocus
                        onClick={(e) => e.stopPropagation()}
                        onChange={(e) => setEditingValue(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") commitEditing();
                          else if (e.key === "Escape") cancelEditing();
                        }}
                        onBlur={commitEditing}
                        placeholder={defaultSessionName(s)}
                        maxLength={80}
                      />
                    ) : (
                      <button
                        className="session-open"
                        onClick={(e) => {
                          e.stopPropagation();
                          if (isClosing) return;
                          startEditing(s.id, s.name);
                        }}
                        disabled={isClosing}
                        title={
                          isClosing
                            ? "session is closing"
                            : s.name
                              ? `${defaultSessionName(s)} — click to rename`
                              : "click to rename"
                        }
                      >
                        <span className="session-id">{sessionDisplayName(s)}</span>
                      </button>
                    )}
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
          <div className="home">
            <div className="home-inner">
              <section className="home-hero" aria-labelledby="home-title">
                <div>
                  <h2 id="home-title" className="home-title">tank-operator</h2>
                  <p className="home-sub">Launchers, credentials, and active sessions</p>
                </div>
                <span className="home-count">{sessions.length} session{sessions.length === 1 ? "" : "s"}</span>
              </section>

              <div className="home-grid">
                <section className="home-panel home-panel-start" aria-labelledby="home-start-title">
                  <div className="home-panel-head">
                    <h3 id="home-start-title">Start</h3>
                    <span className="home-panel-meta">{INTERACTION_LABELS[defaultInteraction]}</span>
                  </div>
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
                        >
                          <span className={sessionStatusDotClass(s, sessionActivities[s.id])} />
                          <ProviderIcon provider={MODE_MENU_ICONS[s.mode]} className="home-session-icon" />
                          <span className="home-session-main">
                            <span className="home-session-title">{sessionDisplayName(s)}</span>
                            <span className="home-session-sub">{MODE_LABELS[s.mode]}</span>
                          </span>
                        </button>
                      ))
                    )}
                  </div>
                </section>
              </div>
            </div>
          </div>
        ) : (
          <div className="terminals">
            {sessions
              .filter((s) => mounted.has(s.id))
              .map((s) =>
                HEADLESS_MODES.has(s.mode) ? (
                  <div
                    key={s.id}
                    className="run-body"
                    hidden={active !== s.id}
                  >
                    <HeadlessRun
                      session={s}
                      visible={active === s.id}
                      onRename={renameSession}
                      onSessionPatch={patchSession}
                      runPrefs={runPrefs}
                      setRunPref={setRunPref}
                      user={user!}
                      onActivityChange={updateSessionActivity}
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
