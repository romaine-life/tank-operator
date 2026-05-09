import { createContext, useContext, useEffect, useMemo, useRef, useState } from "react";
import type {
  AnchorHTMLAttributes,
  ComponentProps,
  CSSProperties,
  DragEvent as ReactDragEvent,
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
  ReactNode,
} from "react";
import type { TranscriptEntry } from "@sandbox-agent/react";
import {
  CodeBlock,
  CodeBlockContainer,
  CodeBlockCopyButton,
  CodeBlockHeader,
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
  ArrowUpFromLineIcon,
  BotIcon,
  BrainIcon,
  CheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  ClipboardListIcon,
  CopyIcon,
  FileIcon,
  FileTextIcon,
  FolderIcon,
  FolderOpenIcon,
  ImageIcon,
  InfoIcon,
  ListChecksIcon,
  Loader2Icon,
  MessageSquareIcon,
  MinusIcon,
  MonitorIcon,
  PlugIcon,
  PlusIcon,
  RotateCcwIcon,
  SearchIcon,
  SendHorizontalIcon,
  SettingsIcon,
  SquareIcon,
  SquarePenIcon,
  TerminalIcon,
  TimerIcon,
  WrenchIcon,
  XIcon,
  type LucideIcon,
} from "lucide-react";
import { authedFetch, bootstrapAuth, logout, startLogin } from "./auth";
import { ProviderIcon } from "./providerIcons";
import { ANSI_256_OVERRIDES, ANSI_STANDARD_OVERRIDES } from "./terminalTheme";

type SessionMode =
  | "api_key"
  | "subscription"
  | "subscription_headless"
  | "config"
  | "codex_subscription"
  | "codex_headless"
  | "codex_config"
  | "pi_subscription"
  | "pi_config";
type DefaultSessionMode = Extract<
  SessionMode,
  | "subscription"
  | "subscription_headless"
  | "codex_subscription"
  | "codex_headless"
  | "pi_subscription"
>;
type Provider = "anthropic" | "openai" | "pi";
type SessionInteraction = "run" | "newterm";

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
}

const MODE_LABELS: Record<SessionMode, string> = {
  api_key: "Claude API key",
  subscription: "Claude",
  subscription_headless: "Claude run",
  config: "Claude config",
  codex_subscription: "Codex",
  codex_headless: "Codex run",
  codex_config: "Codex config",
  pi_subscription: "Pi",
  pi_config: "Pi config",
};

// Compact labels for the inline session-row chip. Falls back to MODE_LABELS
// elsewhere.
const MODE_CHIP_LABELS: Record<SessionMode, string> = {
  api_key: "api",
  subscription: "claude",
  subscription_headless: "claude-run",
  config: "config",
  codex_subscription: "codex",
  codex_headless: "codex-run",
  codex_config: "codex-cfg",
  pi_subscription: "pi",
  pi_config: "pi-cfg",
};

const MODE_CHIP_ICONS: Partial<Record<SessionMode, Provider>> = {
  subscription: "anthropic",
  subscription_headless: "anthropic",
  codex_subscription: "openai",
  codex_headless: "openai",
  pi_subscription: "pi",
};

const MODE_MENU_ICONS: Record<SessionMode, Provider> = {
  api_key: "anthropic",
  subscription: "anthropic",
  subscription_headless: "anthropic",
  config: "anthropic",
  codex_subscription: "openai",
  codex_headless: "openai",
  codex_config: "openai",
  pi_subscription: "pi",
  pi_config: "pi",
};

const PROVIDER_INTERACTION_MODES: Record<
  Provider,
  Partial<Record<SessionInteraction, DefaultSessionMode | null>>
> = {
  anthropic: { run: "subscription_headless", newterm: "subscription_headless" },
  openai: { run: "codex_headless", newterm: "codex_headless" },
  pi: { run: null, newterm: "pi_subscription" },
};

const INTERACTION_LABELS: Record<SessionInteraction, string> = {
  run: "gui",
  newterm: "terminal",
};

const INTERACTION_OPTIONS: SessionInteraction[] = ["run", "newterm"];

const PROVIDER_CONFIG_MODES: Record<Provider, SessionMode> = {
  anthropic: "config",
  openai: "codex_config",
  pi: "pi_config",
};

const MODE_HINTS: Record<SessionMode, string> = {
  subscription: "Uses claude.ai login",
  subscription_headless: "Headless claude -p output",
  api_key: "Specify an API key fallback",
  config: "Log in once · seeds KV for future sessions",
  codex_subscription: "Uses ChatGPT login from KV",
  codex_headless: "Headless codex exec output",
  codex_config: "codex login --device-auth · seeds KV for Codex",
  pi_subscription: "Uses Tank Claude/Codex subscriptions",
  pi_config: "Pi /login sandbox",
};

const MODE_ORDER: SessionMode[] = [
  "subscription_headless",
  "api_key",
  "config",
  "codex_headless",
  "codex_config",
  "pi_subscription",
  "pi_config",
];

const DEMO_BASE_SESSIONS: Session[] = [
  {
    id: "claude-code",
    pod_name: "tank-demo-claude-code",
    owner: "preview",
    status: "Active",
    mode: "subscription_headless",
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
    mode: "codex_headless",
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
    mode: "pi_subscription",
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
  const template = session.mode === "codex_subscription" || session.mode === "codex_headless"
    ? DEMO_CODEX_LINES
    : session.mode === "pi_subscription"
      ? DEMO_PI_LINES
      : DEMO_CLAUDE_LINES;
  const lines = [...template];
  if (promptText) {
    if (session.mode === "codex_subscription" || session.mode === "codex_headless") {
      lines[lines.length - 1] = `\x1b[1m›\x1b[0m ${promptText}`;
    } else if (session.mode === "pi_subscription") {
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
  const label = mode === "codex_subscription" || mode === "codex_headless"
    ? "Codex"
    : mode === "pi_subscription"
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

function isDefaultSessionMode(value: string | null): value is DefaultSessionMode {
  return (
    value === "subscription" ||
    value === "subscription_headless" ||
    value === "codex_subscription" ||
    value === "codex_headless" ||
    value === "pi_subscription"
  );
}

function readDefaultSessionMode(): DefaultSessionMode {
  try {
    const stored = localStorage.getItem(DEFAULT_SESSION_MODE_KEY);
    if (stored === "subscription") return "subscription_headless";
    if (stored === "codex_subscription") return "codex_headless";
    if (isDefaultSessionMode(stored)) return stored;
  } catch {
    // localStorage can be unavailable in hardened/private browser contexts.
  }
  return "subscription_headless";
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
    if (stored === "run" || stored === "newterm") return stored;
    if (stored === "terminal") return "newterm";
  } catch {}
  // Back-compat: derive from stored session mode.
  const mode = readDefaultSessionMode();
  return HEADLESS_MODES.has(mode) ? "run" : "newterm";
}

function writeDefaultInteraction(interaction: SessionInteraction): void {
  try {
    localStorage.setItem(DEFAULT_INTERACTION_KEY, interaction);
  } catch {}
}

function readSessionInteraction(id: string): SessionInteraction | null {
  try {
    const stored = localStorage.getItem(SESSION_INTERACTION_KEY_PREFIX + id);
    if (stored === "run" || stored === "newterm") return stored;
  } catch {}
  return null;
}

function writeSessionInteraction(id: string, interaction: SessionInteraction): void {
  try {
    localStorage.setItem(SESSION_INTERACTION_KEY_PREFIX + id, interaction);
  } catch {}
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
const HEADLESS_MODES = new Set<SessionMode>(["subscription_headless", "codex_headless"]);
const CLAUDE_ROLLOUT_MODES = new Set<SessionMode>(["subscription", "api_key"]);
const CODEX_ROLLOUT_MODES = new Set<SessionMode>(["codex_subscription"]);
const ROLLOUT_MODES = new Set<SessionMode>([
  ...CLAUDE_ROLLOUT_MODES,
  ...CODEX_ROLLOUT_MODES,
]);
const PROVIDERS: Provider[] = ["anthropic", "openai", "pi"];


function defaultModeFor(provider: Provider, interaction: SessionInteraction): DefaultSessionMode {
  return (
    PROVIDER_INTERACTION_MODES[provider][interaction] ??
    PROVIDER_INTERACTION_MODES[provider].newterm!
  );
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
}

type GlimmungLaunchContext = {
  glimmung_run_id: string;
  glimmung_issue_id: string;
  glimmung_pr_id: string | null;
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
  const runId = params.get("glimmung_run_id");
  const issueId = params.get("glimmung_issue_id");
  if (!runId || !issueId) {
    try {
      const stored = window.sessionStorage.getItem(GLIMMUNG_LAUNCH_CONTEXT_KEY);
      if (!stored) return null;
      const parsed = JSON.parse(stored) as Partial<GlimmungLaunchContext>;
      if (!parsed.glimmung_run_id || !parsed.glimmung_issue_id) return null;
      return {
        glimmung_run_id: parsed.glimmung_run_id,
        glimmung_issue_id: parsed.glimmung_issue_id,
        glimmung_pr_id: parsed.glimmung_pr_id ?? null,
        validation_url: parsed.validation_url ?? null,
      };
    } catch {
      return null;
    }
  }

  const context = {
    glimmung_run_id: runId,
    glimmung_issue_id: issueId,
    glimmung_pr_id: params.get("glimmung_pr_id"),
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
    "glimmung_run_id",
    "glimmung_issue_id",
    "glimmung_pr_id",
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

function IconReload() {
  return (
    <svg viewBox="0 0 16 16" width="12" height="12" fill="none"
         stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
      <path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9" />
      <polyline points="13.5 2.5 13.5 5 11 5" />
    </svg>
  );
}

function sessionInteractionForSession(session: Session): SessionInteraction | null {
  const stored = readSessionInteraction(session.id);
  if (stored) return stored;
  if (HEADLESS_MODES.has(session.mode)) return "run";
  return session.mode === "subscription" || session.mode === "codex_subscription" || session.mode === "pi_subscription"
    ? "newterm"
    : null;
}

function InteractionIcon({
  interaction,
  className,
}: {
  interaction: SessionInteraction;
  className?: string;
}) {
  const Icon: LucideIcon = interaction === "run" ? MonitorIcon : TerminalIcon;
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

function TankIcon({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 64 64"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.5"
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
  const selectedMode = defaultModeFor(selectedProvider, "newterm");
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
                  const mode = defaultModeFor(provider, "newterm");
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
              const statusDotClass = s.mode.startsWith("codex")
                ? "status-dot status-codex-waiting"
                : `status-dot status-${s.status.toLowerCase()}`;
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
                    {s.mode === "subscription" && (
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
          className={`demo-terminal${selected?.mode === "subscription" || selected?.mode === "subscription_headless" ? " is-claude" : " is-codex"}`}
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
  status?: "done" | "error";
  detail?: string;
};

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

function upsertEntry(entries: TranscriptEntry[], entry: TranscriptEntry): TranscriptEntry[] {
  const index = entries.findIndex((candidate) => candidate.id === entry.id);
  if (index === -1) return [...entries, entry];
  const next = [...entries];
  next[index] = { ...next[index], ...entry };
  return next;
}

function appendMeta(
  entries: TranscriptEntry[],
  id: string,
  title: string,
  detail?: string,
  severity: "info" | "error" = "info",
): TranscriptEntry[] {
  return [
    ...entries,
    {
      id,
      kind: "meta",
      time: nowIso(),
      meta: { title, detail, severity },
    },
  ];
}

function appendAssistantMessage(
  entries: TranscriptEntry[],
  id: string,
  text: string,
): TranscriptEntry[] {
  if (!text.trim()) return entries;
  return [
    ...entries,
    {
      id,
      kind: "message",
      role: "assistant",
      text,
      time: nowIso(),
    },
  ];
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

  if (itemType === "command_execution") {
    const command = typeof item.command === "string" ? item.command : "command";
    return {
      id,
      kind: "tool",
      toolName: command,
      toolInput: command,
      toolOutput: shortJson(item.aggregated_output),
      toolStatus: status,
      time: nowIso(),
    };
  }

  if (itemType === "file_change") {
    return {
      id,
      kind: "tool",
      toolName: "file change",
      toolInput: shortJson(item.changes),
      toolStatus: status,
      time: nowIso(),
    };
  }

  if (itemType === "mcp_tool_call") {
    const server = typeof item.server === "string" ? item.server : "mcp";
    const tool = typeof item.tool === "string" ? item.tool : "tool";
    return {
      id,
      kind: "tool",
      toolName: `${server}.${tool}`,
      toolInput: shortJson(item.arguments),
      toolOutput: shortJson(item.result ?? item.error),
      toolStatus: status,
      time: nowIso(),
    };
  }

  if (itemType === "web_search") {
    return {
      id,
      kind: "tool",
      toolName: "web search",
      toolInput: typeof item.query === "string" ? item.query : shortJson(item),
      toolStatus: status,
      time: nowIso(),
    };
  }

  return null;
}

function applyCodexEvent(entries: TranscriptEntry[], event: JsonObject): TranscriptEntry[] {
  const type = event.type;
  if (type === "tank.user_message") {
    const text = typeof event.message === "string" ? event.message.trim() : "";
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
        time: nowIso(),
      },
    ];
  }
  if (type === "thread.started") {
    const threadId = typeof event.thread_id === "string" ? event.thread_id : "";
    return appendMeta(entries, `codex-thread-${threadId || Date.now()}`, "Codex thread started", threadId);
  }
  if (type === "turn.started") {
    return appendMeta(entries, `codex-turn-started-${Date.now()}`, "Turn started");
  }
  if (type === "turn.completed") {
    return appendMeta(entries, `codex-turn-completed-${Date.now()}`, "Turn completed", describeUsage(event.usage));
  }
  if (type === "turn.failed" || type === "error") {
    const error = isJsonObject(event.error) ? event.error.message : event.message;
    return appendMeta(
      entries,
      `codex-error-${Date.now()}`,
      type === "turn.failed" ? "Turn failed" : "Codex error",
      typeof error === "string" ? error : shortJson(event),
      "error",
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
        time: nowIso(),
        reasoning: { text: typeof item.text === "string" ? item.text : shortJson(item) },
      });
    }
    const toolEntry = codexToolEntry(event);
    return toolEntry ? upsertEntry(entries, toolEntry) : entries;
  }
  return appendMeta(entries, `codex-event-${Date.now()}`, String(type || "Codex event"), shortJson(event));
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
  return message.content.flatMap((block): TranscriptEntry[] => {
    if (!isJsonObject(block) || block.type !== "tool_use") return [];
    const id = typeof block.id === "string" ? block.id : `claude-tool-${Date.now()}`;
    return [
      {
        id,
        kind: "tool",
        toolName: typeof block.name === "string" ? block.name : "tool",
        toolInput: shortJson(block.input),
        toolStatus: "started",
        time: nowIso(),
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

function applyClaudeToolResults(entries: TranscriptEntry[], event: JsonObject): TranscriptEntry[] {
  const message = event.message;
  if (!isJsonObject(message) || !Array.isArray(message.content)) return entries;
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
      time: existing?.time ?? nowIso(),
    });
  }, entries);
}

function applyClaudeEvent(entries: TranscriptEntry[], event: JsonObject): TranscriptEntry[] {
  const type = event.type;
  // Skip internal claude-code events that appear in the JSONL file but are
  // not streamed via WebSocket and have no chat-visible meaning.
  if (
    type === "system" ||
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
        for (const block of message.content as unknown[]) {
          if (!isJsonObject(block) || block.type !== "text") continue;
          const t = typeof block.text === "string" ? block.text.trim() : "";
          if (t) texts.push(t);
        }
      }
      for (const text of texts) {
        if (!nextEntries.some((e) => e.kind === "message" && e.role === "user" && e.text === text)) {
          nextEntries = [
            ...nextEntries,
            {
              id: typeof event.uuid === "string" ? event.uuid : `user-msg-${Date.now()}`,
              kind: "message" as const,
              role: "user" as const,
              text,
              time: nowIso(),
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
        return appendAssistantMessage(entries, `claude-result-message-${Date.now()}`, result);
      }
      return entries;
    }
    let nextEntries = appendMeta(
      entries,
      `claude-result-${Date.now()}`,
      "Claude run failed",
      result,
      "error",
    );
    if (result && !entries.some((entry) => entry.kind === "message" && entry.text === result)) {
      nextEntries = appendAssistantMessage(nextEntries, `claude-result-message-${Date.now()}`, result);
    }
    return nextEntries;
  }
  return appendMeta(entries, `claude-event-${Date.now()}`, String(type || "Claude event"), shortJson(event));
}

function applyProviderEvent(
  entries: TranscriptEntry[],
  mode: SessionMode,
  event: JsonObject,
): TranscriptEntry[] {
  if (mode === "codex_headless") return applyCodexEvent(entries, event);
  return applyClaudeEvent(entries, event);
}

function isClaudeRunMode(mode: SessionMode): boolean {
  return mode === "subscription_headless";
}

// (formerly: getRunToolGroupSummary — replaced by RunToolGroup's inline
// summary computation now that AgentTranscript is unused.)

interface ToolVisualConfig {
  Icon: LucideIcon;
  /** CSS class added to the icon span — drives the color stripe + icon hue. */
  colorClass: string;
}

/** Map a tool entry to a Lucide icon + cloudcli-flavored color stripe. */
function getToolVisualConfig(entry: TranscriptEntry): ToolVisualConfig {
  const name = entry.toolName ?? "";
  if (name === "Bash" || name === "command" || name.toLowerCase().includes("bash")) {
    return { Icon: TerminalIcon, colorClass: "tool-color-bash" };
  }
  if (name === "Read") {
    return { Icon: FileTextIcon, colorClass: "tool-color-read" };
  }
  if (name === "Write" || name === "Edit" || name === "MultiEdit" || name === "ApplyPatch") {
    return { Icon: SquarePenIcon, colorClass: "tool-color-edit" };
  }
  if (name === "Glob" || name === "Grep") {
    return { Icon: SearchIcon, colorClass: "tool-color-search" };
  }
  if (name === "TodoWrite" || name === "Todo") {
    return { Icon: ListChecksIcon, colorClass: "tool-color-todo" };
  }
  if (name === "Task" || name === "Agent") {
    return { Icon: BotIcon, colorClass: "tool-color-task" };
  }
  if (name === "ExitPlanMode" || name === "EnterPlanMode") {
    return { Icon: ClipboardListIcon, colorClass: "tool-color-plan" };
  }
  if (name.toLowerCase().includes("mcp")) {
    return { Icon: PlugIcon, colorClass: "tool-color-mcp" };
  }
  return { Icon: WrenchIcon, colorClass: "tool-color-default" };
}

// (formerly: transcriptClassNames slot map for AgentTranscript — gone
// now that the inline RunMessages renderer owns class names directly.)

type RunTab = "chat" | "files";

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

interface RunPrefs {
  sendByCtrlEnter: boolean;
  showThinking: boolean;
  autoExpandTools: boolean;
  showTimestamps: boolean;
  showDuration: boolean;
  chatFontScale: number;
}

const DEFAULT_RUN_PREFS: RunPrefs = {
  sendByCtrlEnter: false,
  showThinking: true,
  autoExpandTools: false,
  showTimestamps: true,
  showDuration: true,
  chatFontScale: 1,
};

const CHAT_FONT_SCALE_MIN = 0.8;
const CHAT_FONT_SCALE_MAX = 1.4;
const CHAT_FONT_SCALE_STEP = 0.1;

function clampChatFontScale(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_RUN_PREFS.chatFontScale;
  return Math.min(CHAT_FONT_SCALE_MAX, Math.max(CHAT_FONT_SCALE_MIN, value));
}

function loadRunPrefs(): RunPrefs {
  const out = { ...DEFAULT_RUN_PREFS };
  try {
    for (const key of Object.keys(out) as (keyof RunPrefs)[]) {
      const raw = localStorage.getItem(RUN_PREF_PREFIX + key);
      if (key === "chatFontScale") {
        if (raw != null) out[key] = clampChatFontScale(Number(raw));
      } else if (raw === "true" || raw === "false") {
        out[key] = raw === "true";
      }
    }
  } catch {
    /* ignore */
  }
  return out;
}

// localStorage key for persisting a single run's transcript entries.
// Backend-side persistence (replay JSONL from
// ~/.claude/projects/<encoded-cwd>/<session>.jsonl in the session pod)
// is a follow-up; localStorage is sufficient for refresh-survives-tab
// and same-browser cross-tab.
const RUN_STORAGE_PREFIX = "tank-run-entries-";

function loadStoredEntries(sessionId: string): TranscriptEntry[] {
  try {
    const raw = localStorage.getItem(RUN_STORAGE_PREFIX + sessionId);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? (parsed as TranscriptEntry[]) : [];
  } catch {
    return [];
  }
}

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

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="run-msg-copy"
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

function textFromCodeChildren(children: ReactNode): string {
  if (Array.isArray(children)) return children.map(textFromCodeChildren).join("");
  if (typeof children === "string" || typeof children === "number") return String(children);
  return "";
}

type RunMarkdownCodeProps = ComponentProps<"code"> & {
  "data-block"?: boolean | string;
  node?: unknown;
};

function RunMarkdownCode({ children, className, node: _node, ...props }: RunMarkdownCodeProps) {
  const code = textFromCodeChildren(children);
  const isBlock = "data-block" in props;
  const language = className?.match(/language-(\S+)/)?.[1] ?? "";
  if (!isBlock) {
    return (
      <code className={`run-markdown-inline-code${className ? ` ${className}` : ""}`} {...props}>
        {children}
      </code>
    );
  }
  if (!hasUrl(code)) {
    return (
      <CodeBlock code={code} language={language}>
        <CodeBlockCopyButton code={code} />
      </CodeBlock>
    );
  }
  return (
    <CodeBlockContainer className="run-markdown-linked-code" language={language}>
      <CodeBlockHeader language={language} />
      <div className="run-markdown-code-actions" data-streamdown="code-block-actions">
        <CodeBlockCopyButton code={code} />
      </div>
      <div className="run-markdown-linked-code-body" data-streamdown="code-block-body">
        <pre>
          <code className={className}>{linkifyUrls(code.replace(/\n$/, ""))}</code>
        </pre>
      </div>
    </CodeBlockContainer>
  );
}

function RunMarkdownLink(props: AnchorHTMLAttributes<HTMLAnchorElement>) {
  return <a {...props} rel="noreferrer" target="_blank" />;
}

const RUN_MARKDOWN_COMPONENTS: StreamdownComponents = {
  a: RunMarkdownLink,
  code: RunMarkdownCode,
} as StreamdownComponents;

function RunMarkdown({ children }: { children: string }) {
  return (
    <Streamdown components={RUN_MARKDOWN_COMPONENTS} linkSafety={{ enabled: false }}>
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
  showTimestamps,
  showDuration,
}: {
  entry: TranscriptEntry;
  showTimestamps: boolean;
  showDuration: boolean;
}) {
  const variant = entry.role === "user" ? "user" : "assistant";
  const { user } = useContext(RunContext);
  const text = entry.text ?? "";
  const time = formatMessageTime(entry.time);
  const durationMs = (entry as Record<string, unknown>).durationMs as number | undefined;
  const alwaysVisible = showTimestamps || showDuration;
  return (
    <div
      className="run-transcript-message"
      data-slot="message"
      data-variant={variant}
      data-role={variant}
      data-kind="message"
    >
      {variant === "assistant" && (
        <span className="run-msg-ai-avatar" aria-hidden="true">
          <BotIcon size={14} strokeWidth={2} />
        </span>
      )}
      <div
        className="run-transcript-message-content"
        data-slot="message-content"
      >
        <div className="run-transcript-message-text" data-slot="message-text">
          <RunMarkdown>{text}</RunMarkdown>
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
          <pre>{entry.toolOutput}</pre>
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
          <pre className="run-tool-bash-out">{entry.toolOutput}</pre>
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
          <pre className="run-tool-default-pre">{entry.toolOutput}</pre>
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
  const state = (entry.toolStatus ?? "completed") as string;
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
  const errorCount = entries.filter(
    (e) => (e.toolStatus ?? "") === "failed" || (e.toolStatus ?? "") === "error",
  ).length;
  const summary =
    errorCount > 0
      ? `${entries.length} tool calls · ${errorCount} error${errorCount === 1 ? "" : "s"}`
      : `${entries.length} tool calls`;
  return (
    <div className="run-transcript-tools" data-slot="tool-group">
      <button
        type="button"
        className="run-transcript-tools-header"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
      >
        <span className="run-transcript-tools-icon">
          <WrenchIcon size={14} strokeWidth={2} aria-hidden="true" />
        </span>
        <span className="run-transcript-tools-label">{summary}</span>
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
  showThinking,
  autoExpandTools,
  showTimestamps,
  showDuration,
}: {
  entries: TranscriptEntry[];
  showThinking: boolean;
  autoExpandTools: boolean;
  showTimestamps: boolean;
  showDuration: boolean;
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
            showTimestamps={showTimestamps}
            showDuration={showDuration}
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
  user,
}: {
  session: Session;
  visible: boolean;
  onRename: (id: string, name: string | null) => void;
  user: SessionUser;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[]>(() =>
    loadStoredEntries(session.id),
  );
  const [running, setRunning] = useState(false);
  const [editingTitle, setEditingTitle] = useState(false);
  const [editingTitleValue, setEditingTitleValue] = useState("");
  const [runStatus, setRunStatus] = useState<"idle" | "running" | "done" | "error">("idle");
  const [activeToolName, setActiveToolName] = useState<string | null>(null);
  const activeToolNameRef = useRef<string | null>(null);
  // Mirrors cloudcli's ClaudeStatus idle state: persists last status text
  // after the run ends (amber/static pill) instead of vanishing.
  const [lastStatusText, setLastStatusText] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<RunTab>("chat");
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
  const [queuedMessages, setQueuedMessages] = useState<
    { id: string; text: string }[]
  >([]);
  // Slash-command palette state. `slashOpen` gates rendering; `slashQuery`
  // and `slashIndex` drive filtering and keyboard selection.
  const [slashOpen, setSlashOpen] = useState(false);
  const [slashQuery, setSlashQuery] = useState("");
  const [slashIndex, setSlashIndex] = useState(0);
  const [slashCommands, setSlashCommands] = useState<SlashCommand[]>(SLASH_COMMANDS);
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
  // Auto-scroll bookkeeping — track whether the user has scrolled away from
  // the bottom; if so, suppress auto-scroll on new entries and offer the
  // floating "scroll to bottom" button.
  const [userScrolledUp, setUserScrolledUp] = useState(false);
  // Composer attachments — uploaded to /workspace/.attachments and referenced
  // in the prompt so Claude can Read them via tool use.
  const [attachments, setAttachments] = useState<ComposerAttachment[]>([]);
  const [dragActive, setDragActive] = useState(false);
  // Per-user prefs (persisted in localStorage; keyed by RUN_PREF_PREFIX).
  const [runPrefs, setRunPrefs] = useState<RunPrefs>(() => loadRunPrefs());
  function setRunPref<K extends keyof RunPrefs>(key: K, value: RunPrefs[K]) {
    setRunPrefs((p) => ({ ...p, [key]: value }));
    try {
      localStorage.setItem(RUN_PREF_PREFIX + String(key), String(value));
    } catch {
      /* ignore */
    }
  }
  const setChatFontScale = (value: number) => {
    setRunPref("chatFontScale", Number(clampChatFontScale(value).toFixed(2)));
  };
  const paneFontScale = runPrefs.chatFontScale;
  const paneFontScalePct = Math.round(paneFontScale * 100);
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
  const stdoutBufferRef = useRef("");
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

  const slashFiltered = slashOpen ? filterSlashCommands(slashCommands, slashQuery) : [];
  const mentionFiltered =
    mentionOpen && mentionPaths
      ? filterMentionPaths(mentionPaths, mentionQuery)
      : [];

  useEffect(() => {
    return () => {
      wsRef.current?.close();
      wsRef.current = null;
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

  // Auto-send the next queued message once the current run finishes.
  useEffect(() => {
    if (!running && queuedMessages.length > 0) {
      const [nextMessage, ...remaining] = queuedMessages;
      setQueuedMessages(remaining);
      startRun(nextMessage.text);
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

  // Persist transcript entries per-session in localStorage. Survives page
  // refresh; restored on initial state via loadStoredEntries above.
  useEffect(() => {
    try {
      if (entries.length > 0) {
        localStorage.setItem(
          RUN_STORAGE_PREFIX + session.id,
          JSON.stringify(entries),
        );
      } else {
        localStorage.removeItem(RUN_STORAGE_PREFIX + session.id);
      }
    } catch {
      // Quota exceeded or storage unavailable — drop silently. Future
      // backend replay would cover this case anyway.
    }
  }, [entries, session.id]);

  // History replay — fetch provider JSONL from the pod and replay each event
  // through the matching provider parser. This is intentionally not limited
  // to empty localStorage: a run can finish while the tab is closed, leaving
  // localStorage with a stale partial transcript.
  const [historyAttempted, setHistoryAttempted] = useState(false);
  // Toggled briefly when entries are restored (from localStorage OR backend
  // history) so we can show a "Continuing previous conversation" hint.
  const [continueHintVisible, setContinueHintVisible] = useState(false);
  function refreshRunHistory(showHint: boolean) {
    if (session.status !== "Active" || running) return;
    void authedFetch(`/api/sessions/${session.id}/run/history`)
      .then(async (res) => {
        if (!res.ok) return "";
        return await res.text();
      })
      .then((text) => {
        if (!text) return;
        const acc = parseRunHistory(text, session.mode);
        if (acc.length > 0) {
          setEntries((prev) =>
            transcriptComparable(prev) === transcriptComparable(acc) ? prev : acc,
          );
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
      setContinueHintVisible(true);
      const t = window.setTimeout(() => setContinueHintVisible(false), 3000);
      setHistoryAttempted(true);
      refreshRunHistory(false);
      return () => window.clearTimeout(t);
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

  // When the session id changes, reset local transcript state to that
  // session's cached entries and allow the history sync to run again.
  useEffect(() => {
    setEntries(loadStoredEntries(session.id));
    setQueuedMessages([]);
    setHistoryAttempted(false);
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
        setComposerText("");
        return;
      }
      setComposerText(ta.value);
      const slash = findSlashContext(ta);
      const mention = findMentionContext(ta);
      if (slash) {
        slashManualOpenRef.current = false;
        setSlashOpen(true);
        setSlashQuery((prev) => {
          if (prev !== slash.query) setSlashIndex(0);
          return slash.query;
        });
      } else if (!slashManualOpenRef.current) {
        setSlashOpen(false);
      }
      if (mention) {
        setMentionOpen(true);
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

  function openSlashCommandMenu() {
    if (slashOpen && slashManualOpenRef.current) {
      slashManualOpenRef.current = false;
      setSlashOpen(false);
      return;
    }
    slashManualOpenRef.current = true;
    setSlashQuery("");
    setSlashIndex(0);
    setSlashOpen(true);
    const ta = composerWrapRef.current?.querySelector("textarea") as HTMLTextAreaElement | null;
    ta?.focus();
  }

  function applyStdoutLine(line: string) {
    const trimmed = line.trim();
    if (!trimmed) return;
    let providerEvent: unknown;
    try {
      providerEvent = JSON.parse(trimmed);
    } catch {
      setEntries((prev) =>
        appendMeta(prev, nextEntryId("raw-stdout"), "Output", line),
      );
      return;
    }
    if (!isJsonObject(providerEvent)) {
      setEntries((prev) =>
        appendMeta(prev, nextEntryId("raw-stdout"), "Output", shortJson(providerEvent)),
      );
      return;
    }
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
          activeToolNameRef.current = toolName;
          setActiveToolName(toolName);
        }
      }
    } else if (t === "user") {
      activeToolNameRef.current = null;
      setActiveToolName(null);
    } else if (t === "result") {
      const u = (providerEvent as JsonObject).usage as ClaudeUsage | undefined;
      const total = totalContextTokens(u);
      if (total > 0) setTokensUsed(total);
      activeToolNameRef.current = null;
      setActiveToolName(null);
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

  function cancelRun() {
    const ws = wsRef.current;
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ cancel: true }));
    }
    ws?.close();
    wsRef.current = null;
    setLastStatusText(activeToolNameRef.current ? `Used ${formatToolLabel(activeToolNameRef.current)}` : "Stopped");
    activeToolNameRef.current = null;
    setActiveToolName(null);
    setRunning(false);
    setRunStatus((prev) => (prev === "running" ? "done" : prev));
  }

  function handleSubmit(message: PromptInputMessage) {
    const trimmed = message.text.trim();
    if (!trimmed || session.status !== "Active") return;
    // Wait until all attachments have finished uploading. If any errored
    // out, surface it but still let the run go ahead with what's ready.
    const ready = attachments.filter((a) => a.status === "ready");
    const stillUploading = attachments.some((a) => a.status === "uploading");
    if (stillUploading) return;
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

  function startRun(trimmed: string) {
    wsRef.current?.close();
    stdoutBufferRef.current = "";
    const followUp = entries.length > 0;
    const turnStart = Date.now();
    setEntries((prev) => [
      ...prev,
      {
        id: nextEntryId("user"),
        kind: "message",
        role: "user",
        text: trimmed,
        time: nowIso(),
      },
    ]);
    setRunStatus("running");
    setRunning(true);
    activeToolNameRef.current = null;
    setActiveToolName(null);
    setLastStatusText(null);
    setActiveToolName(null);
    setRunStartedAt(Date.now());
    setNow(Date.now());
    // The form clears the textarea internally on submit but doesn't
    // always fire an input event in time, so my mirror lingers and the
    // X-clear button stays visible. Force the mirror clean.
    setComposerText("");
    const wsUrl =
      `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}` +
      `/api/sessions/${session.id}/run`;
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;
    ws.onopen = () => {
      ws.send(
        JSON.stringify({
          prompt: trimmed,
          follow_up: followUp,
          model: selectedModelId === CODEX_ACCOUNT_DEFAULT_MODEL_ID ? "" : selectedModelId,
          permission_mode: composerMode,
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
        applyStdoutChunk(msg.data);
      } else if (msg.stream === "stderr" && msg.data) {
        setEntries((prev) =>
          appendMeta(prev, nextEntryId("stderr"), "stderr", msg.data, "error"),
        );
      } else if (msg.status === "done") {
        flushStdoutBuffer();
        const durationMs = Date.now() - turnStart;
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
        setLastStatusText(activeToolNameRef.current ? `Used ${formatToolLabel(activeToolNameRef.current)}` : "Done");
        activeToolNameRef.current = null;
        setActiveToolName(null);
        setRunStatus("done");
        setRunning(false);
        ws.close();
      } else if (msg.status === "error") {
        flushStdoutBuffer();
        setLastStatusText(activeToolNameRef.current ? `Used ${formatToolLabel(activeToolNameRef.current)}` : "Error");
        activeToolNameRef.current = null;
        setActiveToolName(null);
        setRunStatus("error");
        setRunning(false);
        setEntries((prev) =>
          appendMeta(prev, nextEntryId("run-error"), "run failed", msg.detail, "error"),
        );
        ws.close();
      }
    };
    ws.onerror = () => {
      setLastStatusText(activeToolNameRef.current ? `Used ${formatToolLabel(activeToolNameRef.current)}` : "Error");
      activeToolNameRef.current = null;
      setActiveToolName(null);
      setRunStatus("error");
      setRunning(false);
      setEntries((prev) =>
        appendMeta(prev, nextEntryId("websocket-error"), "websocket error", undefined, "error"),
      );
    };
    ws.onclose = (event) => {
      flushStdoutBuffer();
      setLastStatusText(activeToolNameRef.current ? `Used ${formatToolLabel(activeToolNameRef.current)}` : "Done");
      activeToolNameRef.current = null;
      setActiveToolName(null);
      setRunning(false);
      // 1000 = normal close, 1005 = no status (most cancel/done paths since
      // we call ws.close() with no code), 1001 = going away (page nav).
      // Anything else mid-run we surface as a connection error so the user
      // knows to resend rather than waiting on a silent dropped run.
      const abnormal =
        event.code !== 1000 && event.code !== 1005 && event.code !== 1001;
      // Use functional setter so we read the latest runStatus, not the
      // closure value from when the WS was created.
      setRunStatus((prev) => {
        if (abnormal && prev === "running") {
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
          return "error";
        }
        return prev === "running" ? "done" : prev;
      });
    };
  }

  const submitStatus =
    runStatus === "running"
      ? "streaming"
      : runStatus === "error"
        ? "error"
        : undefined;

  const provider: Provider = isClaude ? "anthropic" : "openai";
  const modeLabel = MODE_LABELS[session.mode];
  const ready = session.status === "Active";
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
        <nav className="run-tabs" role="tablist" aria-label="Session views">
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === "chat"}
            className={`run-tab${activeTab === "chat" ? " run-tab-active" : ""}`}
            onClick={() => {
              setActiveTab("chat");
            }}
            title="Return to the session"
          >
            <BotIcon
              className="run-tab-icon"
              strokeWidth={activeTab === "chat" ? 2.4 : 1.8}
              aria-hidden="true"
            />
            <span>Session</span>
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === "files"}
            className={`run-tab${activeTab === "files" ? " run-tab-active" : ""}`}
            onClick={() => setActiveTab("files")}
            title="Browse files in /workspace"
          >
            <FolderIcon
              className="run-tab-icon"
              strokeWidth={activeTab === "files" ? 2.4 : 1.8}
              aria-hidden="true"
            />
            <span>Files</span>
          </button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                className="run-tab run-tab-icononly"
                aria-label="Run settings"
                title="Settings"
              >
                <SettingsIcon className="run-tab-icon" aria-hidden="true" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="run-settings-menu">
              <DropdownMenuLabel>Composer</DropdownMenuLabel>
              <DropdownMenuItem
                onSelect={(e) => {
                  e.preventDefault();
                  setRunPref("sendByCtrlEnter", !runPrefs.sendByCtrlEnter);
                }}
              >
                <span className="run-settings-row">
                  <span className="run-settings-label">
                    Send with ⌘/Ctrl+Enter
                  </span>
                  {runPrefs.sendByCtrlEnter && (
                    <CheckIcon className="run-settings-check" aria-hidden="true" />
                  )}
                </span>
              </DropdownMenuItem>
              <DropdownMenuLabel>Transcript</DropdownMenuLabel>
              <DropdownMenuItem
                className="run-settings-zoom-item"
                onSelect={(e) => e.preventDefault()}
              >
                <span className="run-settings-zoom-row">
                  <span className="run-settings-label">Text zoom</span>
                  <span className="run-settings-zoom-controls">
                    <button
                      type="button"
                      className="run-settings-zoom-btn"
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        setPaneFontScale(paneFontScale - CHAT_FONT_SCALE_STEP);
                      }}
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
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        setPaneFontScale(paneFontScale + CHAT_FONT_SCALE_STEP);
                      }}
                      disabled={paneFontScale >= CHAT_FONT_SCALE_MAX}
                      aria-label="Increase pane text size"
                      title="Increase text size"
                    >
                      <PlusIcon aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className="run-settings-zoom-btn"
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        setPaneFontScale(DEFAULT_RUN_PREFS.chatFontScale);
                      }}
                      disabled={paneFontScale === DEFAULT_RUN_PREFS.chatFontScale}
                      aria-label="Reset pane text size"
                      title="Reset text size"
                    >
                      <RotateCcwIcon aria-hidden="true" />
                    </button>
                  </span>
                </span>
              </DropdownMenuItem>
              <DropdownMenuLabel>Transcript</DropdownMenuLabel>
              <DropdownMenuItem
                onSelect={(e) => {
                  e.preventDefault();
                  setRunPref("showThinking", !runPrefs.showThinking);
                }}
              >
                <span className="run-settings-row">
                  <span className="run-settings-label">Show reasoning</span>
                  {runPrefs.showThinking && (
                    <CheckIcon className="run-settings-check" aria-hidden="true" />
                  )}
                </span>
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={(e) => {
                  e.preventDefault();
                  setRunPref("autoExpandTools", !runPrefs.autoExpandTools);
                }}
              >
                <span className="run-settings-row">
                  <span className="run-settings-label">Auto-expand tools</span>
                  {runPrefs.autoExpandTools && (
                    <CheckIcon className="run-settings-check" aria-hidden="true" />
                  )}
                </span>
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={(e) => {
                  e.preventDefault();
                  setRunPref("showTimestamps", !runPrefs.showTimestamps);
                }}
              >
                <span className="run-settings-row">
                  <span className="run-settings-label">Show timestamps</span>
                  {runPrefs.showTimestamps && (
                    <CheckIcon className="run-settings-check" aria-hidden="true" />
                  )}
                </span>
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={(e) => {
                  e.preventDefault();
                  setRunPref("showDuration", !runPrefs.showDuration);
                }}
              >
                <span className="run-settings-row">
                  <span className="run-settings-label">Show duration</span>
                  {runPrefs.showDuration && (
                    <CheckIcon className="run-settings-check" aria-hidden="true" />
                  )}
                </span>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
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
                      <button
                        key={e.name}
                        type="button"
                        className={`run-files-row${
                          selectedFile?.path === joinFilesPath(filesPath, e.name)
                            ? " run-files-row-active"
                            : ""
                        }`}
                        onClick={() => openFileEntry(e.name, e.type)}
                      >
                        <Icon
                          size={14}
                          className={`run-files-row-icon run-files-row-${e.type}`}
                          aria-hidden="true"
                        />
                        <span className="run-files-row-name">{e.name}</span>
                        {e.type === "file" && (
                          <span className="run-files-row-size">
                            {humanFileSize(e.size)}
                          </span>
                        )}
                      </button>
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
                        <Streamdown>
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
                        <Streamdown>
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
              showThinking={runPrefs.showThinking}
              autoExpandTools={runPrefs.autoExpandTools}
              showTimestamps={runPrefs.showTimestamps}
              showDuration={runPrefs.showDuration}
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
                    <div className="run-queued-followup-text" title={message.text}>
                      {message.text}
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

function LegacyInteractiveSession({ session }: { session: Session }) {
  return (
    <section className="run-panel">
      <main className="run-main">
        <div className="run-empty">
          <AlertCircleIcon size={20} aria-hidden="true" />
          <span className="run-muted">
            {MODE_LABELS[session.mode]} used the removed interactive terminal path.
            Create a GUI or terminal session from the launcher.
          </span>
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
  const [closingIds, setClosingIds] = useState<Set<string>>(() => new Set());
  // Sessions stay mounted after first activation so chat state and websocket
  // runs survive switching. Unopened sessions do not initialize their panel.
  const [mounted, setMounted] = useState<Set<string>>(() => new Set());
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
  // Inline rename state. `editingId` is the session whose row is currently
  // an <input>; `editingValue` holds the in-progress name. Reset on commit
  // or cancel. Triggered by clicking the session name.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingValue, setEditingValue] = useState("");
  const initialSessionId = useRef<string | null>(readInitialSessionId());
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
        setAuthError(String(e));
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
      const listed: Session[] = await res.json();
      setSessions(user ? orderSessions(listed, readSessionOrder(sessionOrderStorageKey(user))) : listed);
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

  function activate(id: string) {
    setActive(id);
    setMounted((prev) => (prev.has(id) ? prev : new Set(prev).add(id)));
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
      const created: Session = await res.json();
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
        ? "newterm"
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
      const updated: Session = await res.json();
      setSessions((prev) =>
        prev.map((s) => (s.id === id ? { ...s, name: updated.name ?? null } : s))
      );
    } catch (e) {
      setError(String(e));
    }
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

  async function clearSession(id: string) {
    const existing = sessions.find((s) => s.id === id);
    if (!existing) return;
    setBusy(true);
    setError(null);
    try {
      const delRes = await authedFetch(`/api/sessions/${id}`, { method: "DELETE" });
      if (!delRes.ok) throw new Error(`delete failed: ${delRes.status}`);
      const createRes = await authedFetch("/api/sessions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode: existing.mode }),
      });
      if (!createRes.ok) throw new Error(`create failed: ${createRes.status}`);
      const created: Session = await createRes.json();
      const existingInteraction = readSessionInteraction(existing.id);
      if (existingInteraction) {
        writeSessionInteraction(created.id, existingInteraction);
      }
      if (existing.name) {
        await renameSession(created.id, existing.name);
      }
      await refresh();
      activate(created.id);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
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
          <h1>tank-operator</h1>
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
              const statusDotClass = `status-dot status-${s.status.toLowerCase()}`;
              const statusLabel = s.status;
              const bootLabel = sessionBootLabel(s, nowMs);
              const runtimeLabel = sessionRuntimeLabel(s, nowMs);
              return (
                <li
                  key={s.id}
                  className={`${isActive ? "is-open" : ""}${isClosing ? " is-closing" : ""}${draggingSessionId === s.id ? " is-dragging" : ""}${dragOverSessionId === s.id && draggingSessionId !== s.id ? " is-drag-over" : ""}`}
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
                    <button
                      className="session-action is-icon"
                      onClick={(e) => { e.stopPropagation(); clearSession(s.id); }}
                      disabled={busy || isClosing}
                      title="delete this pod and replace it with a fresh one"
                      aria-label="refresh session pod"
                    >
                      <IconReload />
                    </button>
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
          <div className="welcome">
            <div className="welcome-inner">
              <h2 className="welcome-title">tank-operator</h2>
              <p className="welcome-sub">Spin up an agent session</p>
              <div className="welcome-cards" role="list">
                {MODE_ORDER.map((m) => (
                  <button
                    key={m}
                    className="welcome-card"
                    onClick={() => createSession(m)}
                    disabled={busy}
                    role="listitem"
                  >
                    <span className="welcome-card-title">{MODE_LABELS[m]}</span>
                    <span className="welcome-card-sub">{MODE_HINTS[m]}</span>
                  </button>
                ))}
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
                      user={user!}
                    />
                  </div>
                ) : (
                  <div
                    key={s.id}
                    className="run-body"
                    hidden={active !== s.id}
                  >
                    <LegacyInteractiveSession session={s} />
                  </div>
                )
              )}
          </div>
        )}
      </main>
    </div>
  );
}
