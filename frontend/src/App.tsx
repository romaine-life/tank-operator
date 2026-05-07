import { useEffect, useRef, useState } from "react";
import type {
  CSSProperties,
  DragEvent as ReactDragEvent,
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
} from "react";
import { AgentTranscript } from "@sandbox-agent/react";
import type { TranscriptEntry } from "@sandbox-agent/react";
import { Streamdown } from "streamdown";
import { Terminal, type AgentActivity, type TerminalHandle } from "./Terminal";
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
type SessionInteraction = "terminal" | "run";

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
  Record<SessionInteraction, DefaultSessionMode | null>
> = {
  anthropic: { terminal: "subscription", run: "subscription_headless" },
  openai: { terminal: "codex_subscription", run: "codex_headless" },
  pi: { terminal: "pi_subscription", run: null },
};

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
  "subscription",
  "subscription_headless",
  "api_key",
  "config",
  "codex_subscription",
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
    mode: "subscription",
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
    mode: "codex_subscription",
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
  const template = session.mode === "codex_subscription"
    ? DEMO_CODEX_LINES
    : session.mode === "pi_subscription"
      ? DEMO_PI_LINES
      : DEMO_CLAUDE_LINES;
  const lines = [...template];
  if (promptText) {
    if (session.mode === "codex_subscription") {
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
  const label = mode === "codex_subscription"
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
const COMPLETION_SOUND_ENABLED_KEY = "tank.completionSoundEnabled";
const COMPLETION_SOUND_VOLUME_KEY = "tank.completionSoundVolume";
const SESSION_ORDER_KEY_PREFIX = "tank.sessionOrder";
const DEFAULT_COMPLETION_SOUND_VOLUME = 0.55;
const MIN_COMPLETION_SOUND_VOLUME = 0.05;

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
    if (isDefaultSessionMode(stored)) return stored;
  } catch {
    // localStorage can be unavailable in hardened/private browser contexts.
  }
  return "subscription";
}

function writeDefaultSessionMode(mode: DefaultSessionMode): void {
  try {
    localStorage.setItem(DEFAULT_SESSION_MODE_KEY, mode);
  } catch {
    // Preference persistence is best-effort; session creation should continue.
  }
}

function readCompletionSoundEnabled(): boolean {
  try {
    return localStorage.getItem(COMPLETION_SOUND_ENABLED_KEY) !== "0";
  } catch {
    return true;
  }
}

function writeCompletionSoundEnabled(enabled: boolean): void {
  try {
    localStorage.setItem(COMPLETION_SOUND_ENABLED_KEY, enabled ? "1" : "0");
  } catch {
    // Preference persistence is best-effort.
  }
}

function readCompletionSoundVolume(): number {
  try {
    const storedValue = localStorage.getItem(COMPLETION_SOUND_VOLUME_KEY);
    if (storedValue == null || storedValue === "") return DEFAULT_COMPLETION_SOUND_VOLUME;
    const stored = Number(storedValue);
    if (Number.isFinite(stored) && stored > 0) {
      return Math.max(MIN_COMPLETION_SOUND_VOLUME, Math.min(1, stored));
    }
  } catch {
    // Fall through to the default.
  }
  return DEFAULT_COMPLETION_SOUND_VOLUME;
}

function writeCompletionSoundVolume(volume: number): void {
  try {
    localStorage.setItem(
      COMPLETION_SOUND_VOLUME_KEY,
      String(Math.max(MIN_COMPLETION_SOUND_VOLUME, Math.min(1, volume))),
    );
  } catch {
    // Preference persistence is best-effort.
  }
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
const CODEX_MODES = new Set<SessionMode>([
  "codex_subscription",
  "codex_headless",
  "codex_config",
]);
const PI_MODES = new Set<SessionMode>(["pi_subscription", "pi_config"]);
const HEADLESS_MODES = new Set<SessionMode>(["subscription_headless", "codex_headless"]);
const CLAUDE_ROLLOUT_MODES = new Set<SessionMode>(["subscription", "api_key"]);
const CODEX_ROLLOUT_MODES = new Set<SessionMode>(["codex_subscription"]);
const ROLLOUT_MODES = new Set<SessionMode>([
  ...CLAUDE_ROLLOUT_MODES,
  ...CODEX_ROLLOUT_MODES,
]);
const CODEX_ROLLOUT_SUBMIT_DELAY_MS = 200;
const AGENT_ACTIVITY_MODES = new Set<SessionMode>([...CODEX_MODES, ...PI_MODES]);
const PROVIDERS: Provider[] = ["anthropic", "openai", "pi"];

function sessionInteraction(mode: SessionMode): SessionInteraction {
  return HEADLESS_MODES.has(mode) ? "run" : "terminal";
}

function defaultModeFor(provider: Provider, interaction: SessionInteraction): DefaultSessionMode {
  return (
    PROVIDER_INTERACTION_MODES[provider][interaction] ??
    PROVIDER_INTERACTION_MODES[provider].terminal!
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

type RolloutTimer = {
  startedAtMs: number;
  stoppedAtMs: number | null;
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

function formatRolloutElapsed(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 100) return `${seconds}s`;
  if (seconds < 60 * 60) {
    const minutes = Math.floor(seconds / 60);
    const remaining = seconds % 60;
    return `${minutes}:${String(remaining).padStart(2, "0")}`;
  }
  const hours = Math.floor(seconds / 3600);
  return `${hours}h`;
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

function isSessionShortcutEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.closest(".xterm")) return false;
  if (target.closest("input, textarea, select")) return true;
  return target.isContentEditable;
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

function ModeChip({ mode }: { mode: SessionMode }) {
  const icon = MODE_CHIP_ICONS[mode];
  const label = MODE_CHIP_LABELS[mode] ?? mode;

  return (
    <span
      className={`mode mode-${mode}${icon ? " mode-icon-only" : ""}`}
      title={MODE_LABELS[mode]}
      aria-label={MODE_LABELS[mode]}
    >
      {icon ? (
        <>
          <ProviderIcon provider={icon} className="mode-provider-icon" />
          <span className="sr-only">{label}</span>
        </>
      ) : (
        label
      )}
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
  const selectedMode = defaultModeFor(selectedProvider, "terminal");
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
                  const mode = defaultModeFor(provider, "terminal");
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
                    <ModeChip mode={s.mode} />
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
          className={`demo-terminal${selected?.mode === "subscription" ? " is-claude" : " is-codex"}`}
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
      toolOutput: shortJson(block.content),
      toolStatus: block.is_error === true ? "failed" : "completed",
      time: existing?.time ?? nowIso(),
    });
  }, entries);
}

function applyClaudeEvent(entries: TranscriptEntry[], event: JsonObject): TranscriptEntry[] {
  const type = event.type;
  if (type === "system" || type === "rate_limit_event") {
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
    return applyClaudeToolResults(entries, event);
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

function getRunToolGroupSummary(entries: TranscriptEntry[], mode: SessionMode): string {
  const toolCount = entries.filter((entry) => entry.kind === "tool").length;
  const errorCount = entries.filter((entry) => entry.kind === "meta" && entry.meta?.severity === "error").length;
  const eventCount = entries.length;
  if (isClaudeRunMode(mode)) {
    if (toolCount > 0 && errorCount > 0) return `${toolCount} tool${toolCount === 1 ? "" : "s"} · ${errorCount} error${errorCount === 1 ? "" : "s"}`;
    if (toolCount > 0) return `${toolCount} Claude tool${toolCount === 1 ? "" : "s"}`;
    if (errorCount > 0) return `${errorCount} Claude error${errorCount === 1 ? "" : "s"}`;
    return `${eventCount} Claude event${eventCount === 1 ? "" : "s"}`;
  }
  return `${eventCount} Event${eventCount === 1 ? "" : "s"}`;
}

function getRunToolItemIcon(entry: TranscriptEntry): string {
  if (entry.kind === "reasoning") return "think";
  if (entry.kind === "meta") return entry.meta?.severity === "error" ? "error" : "info";
  const name = entry.toolName ?? "";
  if (name === "Bash" || name === "command" || name.includes("bash")) return "$";
  if (name === "Read" || name === "Write" || name === "Edit" || name === "MultiEdit") return "file";
  if (name.includes("mcp")) return "mcp";
  return "tool";
}

const transcriptClassNames = {
  root: "run-transcript",
  divider: "run-transcript-divider",
  dividerLine: "run-transcript-divider-line",
  dividerText: "run-transcript-divider-text",
  message: "run-transcript-message",
  messageContent: "run-transcript-message-content",
  messageText: "run-transcript-message-text",
  toolGroupSingle: "run-transcript-tool-single",
  toolGroupContainer: "run-transcript-tools",
  toolGroupHeader: "run-transcript-tools-header",
  toolGroupIcon: "run-transcript-tools-icon",
  toolGroupLabel: "run-transcript-tools-label",
  toolGroupChevron: "run-transcript-tools-chevron",
  toolGroupBody: "run-transcript-tools-body",
  toolItem: "run-transcript-tool",
  toolItemConnector: "run-transcript-tool-connector",
  toolItemDot: "run-transcript-tool-dot",
  toolItemLine: "run-transcript-tool-line",
  toolItemContent: "run-transcript-tool-content",
  toolItemHeader: "run-transcript-tool-header",
  toolItemIcon: "run-transcript-tool-icon",
  toolItemLabel: "run-transcript-tool-label",
  toolItemSpinner: "run-transcript-tool-spinner",
  toolItemChevron: "run-transcript-tool-chevron",
  toolItemBody: "run-transcript-tool-body",
  toolSection: "run-transcript-tool-section",
  toolSectionTitle: "run-transcript-tool-section-title",
  toolCode: "run-transcript-code",
  toolCodeMuted: "run-transcript-code-muted",
  meta: "run-transcript-meta",
  error: "run-transcript-error",
  thinkingRow: "run-transcript-thinking",
  thinkingIndicator: "run-transcript-thinking-indicator",
};

function HeadlessRun({ session, visible }: { session: Session; visible: boolean }) {
  const [prompt, setPrompt] = useState("");
  const [entries, setEntries] = useState<TranscriptEntry[]>([]);
  const [running, setRunning] = useState(false);
  const [runStatus, setRunStatus] = useState<"idle" | "running" | "done" | "error">("idle");
  const wsRef = useRef<WebSocket | null>(null);
  const stdoutBufferRef = useRef("");

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

  function applyStdoutLine(line: string) {
    const trimmed = line.trim();
    if (!trimmed) return;
    let providerEvent: unknown;
    try {
      providerEvent = JSON.parse(trimmed);
    } catch {
      setEntries((prev) =>
        appendMeta(prev, `raw-stdout-${Date.now()}`, "Output", line),
      );
      return;
    }
    if (!isJsonObject(providerEvent)) {
      setEntries((prev) =>
        appendMeta(prev, `raw-stdout-${Date.now()}`, "Output", shortJson(providerEvent)),
      );
      return;
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

  function startRun() {
    const trimmed = prompt.trim();
    if (!trimmed || running || session.status !== "Active") return;
    wsRef.current?.close();
    stdoutBufferRef.current = "";
    setEntries([
      {
        id: `user-${Date.now()}`,
        kind: "message",
        role: "user",
        text: trimmed,
        time: nowIso(),
      },
    ]);
    setRunStatus("running");
    setRunning(true);
    const wsUrl =
      `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}` +
      `/api/sessions/${session.id}/run`;
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;
    ws.onopen = () => {
      ws.send(JSON.stringify({ prompt: trimmed }));
    };
    ws.onmessage = (event) => {
      let msg: RunEvent;
      try {
        msg = JSON.parse(String(event.data));
      } catch {
        setEntries((prev) =>
          appendMeta(prev, `websocket-message-${Date.now()}`, "websocket message", String(event.data)),
        );
        return;
      }
      if (msg.stream === "stdout" && msg.data) {
        applyStdoutChunk(msg.data);
      } else if (msg.stream === "stderr" && msg.data) {
        setEntries((prev) =>
          appendMeta(prev, `stderr-${Date.now()}`, "stderr", msg.data, "error"),
        );
      } else if (msg.status === "done") {
        flushStdoutBuffer();
        setRunStatus("done");
        setRunning(false);
        ws.close();
      } else if (msg.status === "error") {
        flushStdoutBuffer();
        setRunStatus("error");
        setRunning(false);
        setEntries((prev) =>
          appendMeta(prev, `run-error-${Date.now()}`, "run failed", msg.detail, "error"),
        );
        ws.close();
      }
    };
    ws.onerror = () => {
      setRunStatus("error");
      setRunning(false);
      setEntries((prev) =>
        appendMeta(prev, `websocket-error-${Date.now()}`, "websocket error", undefined, "error"),
      );
    };
    ws.onclose = () => {
      flushStdoutBuffer();
      setRunning(false);
      setRunStatus((prev) => (prev === "running" ? "done" : prev));
    };
  }

  return (
    <section className="run-panel">
      <div className="run-composer">
        <textarea
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          placeholder={`Ask ${MODE_LABELS[session.mode]} to work in /workspace`}
          disabled={running}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              startRun();
            }
          }}
        />
        <button
          className="run-submit"
          onClick={startRun}
          disabled={running || session.status !== "Active" || !prompt.trim()}
        >
          {running ? "running" : "run"}
        </button>
      </div>
      <div className={`run-output run-output-${runStatus}`}>
        {session.status !== "Active" ? (
          <span className="run-muted">waiting for session pod</span>
        ) : entries.length ? (
          <AgentTranscript
            entries={entries}
            className={isClaudeRunMode(session.mode) ? "run-transcript-claude" : "run-transcript-codex"}
            classNames={transcriptClassNames}
            isThinking={running}
            getToolGroupSummary={(toolEntries) => getRunToolGroupSummary(toolEntries, session.mode)}
            renderMessageText={(entry) => (
              <Streamdown>{entry.text ?? ""}</Streamdown>
            )}
            renderInlinePendingIndicator={() => <span className="run-pending">...</span>}
            renderToolItemIcon={(entry) => <span>{getRunToolItemIcon(entry)}</span>}
            renderToolGroupIcon={() => <span className="run-tool-icon">{isClaudeRunMode(session.mode) ? "claude" : "tools"}</span>}
            renderChevron={(expanded) => (
              <span className="run-chevron">{expanded ? "less" : "more"}</span>
            )}
            renderThinkingState={() => (
              <div className="run-transcript-thinking">
                <span className="run-transcript-thinking-indicator">
                  {isClaudeRunMode(session.mode) ? "Claude is working..." : "Codex is working..."}
                </span>
              </div>
            )}
          />
        ) : (
          <span className="run-muted">ready</span>
        )}
      </div>
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
  const [closingIds, setClosingIds] = useState<Set<string>>(() => new Set());
  const [rolloutTimers, setRolloutTimers] = useState<Record<string, RolloutTimer>>({});
  const [agentActivityBySession, setAgentActivityBySession] = useState<Record<string, AgentActivity>>({});
  // Sessions whose Terminal stays mounted (so the WS keeps draining and
  // scrollback survives switching). A session is mounted the first time it
  // becomes active and unmounts only on deletion. Sessions you haven't
  // touched don't open a WS — same opt-in semantic the old tab list had.
  const [mounted, setMounted] = useState<Set<string>>(() => new Set());
  const [modeMenuOpen, setModeMenuOpen] = useState(false);
  const [profileMenuOpen, setProfileMenuOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [draggingSessionId, setDraggingSessionId] = useState<string | null>(null);
  const [dragOverSessionId, setDragOverSessionId] = useState<string | null>(null);
  const [defaultSessionMode, setDefaultSessionMode] =
    useState<DefaultSessionMode>(readDefaultSessionMode);
  const [completionSoundEnabled, setCompletionSoundEnabled] =
    useState(readCompletionSoundEnabled);
  const [completionSoundVolume, setCompletionSoundVolume] =
    useState(readCompletionSoundVolume);
  // Inline rename state. `editingId` is the session whose row is currently
  // an <input>; `editingValue` holds the in-progress name. Reset on commit
  // or cancel. Triggered by clicking the session name.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingValue, setEditingValue] = useState("");
  // One Terminal handle per session — populated by Terminal's forwardRef
  // callback. Used by the inline "remote control" button to inject the
  // /remote-control slash command into the live WS.
  const terminalRefs = useRef<Map<string, TerminalHandle>>(new Map());
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

  // Close any open dropdown on an outside click. Both menus use a `data-menu`
  // attribute so a single listener can route by which menu is open.
  useEffect(() => {
    if (!modeMenuOpen && !profileMenuOpen) return;
    const close = (e: MouseEvent) => {
      const target = e.target as HTMLElement | null;
      const root = target?.closest("[data-menu]") as HTMLElement | null;
      if (root?.dataset.menu === "mode") return;
      if (root?.dataset.menu === "profile") return;
      setModeMenuOpen(false);
      setProfileMenuOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [modeMenuOpen, profileMenuOpen]);

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
    const hasRunningRolloutTimer = Object.values(rolloutTimers).some((timer) => timer.stoppedAtMs == null);
    const tickMs = hasRunningRolloutTimer ? 1000 : SESSION_RUNTIME_TICK_MS;
    const t = setInterval(() => setNowMs(Date.now()), tickMs);
    return () => clearInterval(t);
  }, [user, rolloutTimers]);

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
    setRolloutTimers((prev) => {
      const existing = new Set(sessions.map((s) => s.id));
      let changed = false;
      const next = Object.fromEntries(
        Object.entries(prev).filter(([id]) => {
          const keep = existing.has(id);
          if (!keep) changed = true;
          return keep;
        })
      ) as Record<string, RolloutTimer>;
      return changed ? next : prev;
    });
    setAgentActivityBySession((prev) => {
      const existing = new Set(sessions.map((s) => s.id));
      const next = Object.fromEntries(
        Object.entries(prev).filter(([id]) => existing.has(id))
      ) as Record<string, AgentActivity>;
      return Object.keys(next).length === Object.keys(prev).length ? prev : next;
    });
  }, [sessions, active, closingIds]);

  useEffect(() => {
    if (Object.values(rolloutTimers).some((timer) => timer.stoppedAtMs == null)) {
      setNowMs(Date.now());
    }
  }, [rolloutTimers]);

  function rolloutTimerLabel(sessionId: string): string | null {
    const timer = rolloutTimers[sessionId];
    if (!timer) return null;
    return formatRolloutElapsed((timer.stoppedAtMs ?? nowMs) - timer.startedAtMs);
  }

  function rolloutTimerTitle(sessionId: string, mode: SessionMode): string {
    const timer = rolloutTimers[sessionId];
    const action = CODEX_ROLLOUT_MODES.has(mode)
      ? "type $rollout into this Codex session"
      : "type /rollout into this Claude session";
    if (!timer) return action;
    const elapsed = formatRolloutElapsed((timer.stoppedAtMs ?? nowMs) - timer.startedAtMs);
    return timer.stoppedAtMs == null
      ? `rollout running for ${elapsed}`
      : `rollout finished in ${elapsed}`;
  }

  function stopRolloutTimer(sessionId: string) {
    setRolloutTimers((prev) => {
      const timer = prev[sessionId];
      if (!timer || timer.stoppedAtMs != null) return prev;
      return {
        ...prev,
        [sessionId]: { ...timer, stoppedAtMs: Date.now() },
      };
    });
  }

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
      focusTerminalAfterRender(nextId);
    };
    window.addEventListener("keydown", cycleTabs, { capture: true });
    return () => window.removeEventListener("keydown", cycleTabs, { capture: true });
  }, [sessions, active, closingIds]);

  function activate(id: string) {
    setActive(id);
    setMounted((prev) => (prev.has(id) ? prev : new Set(prev).add(id)));
  }

  function focusTerminalAfterRender(id: string, attempts = 6) {
    window.requestAnimationFrame(() => {
      const focused = terminalRefs.current.get(id)?.focus() ?? false;
      if (!focused && attempts > 1) {
        window.setTimeout(() => focusTerminalAfterRender(id, attempts - 1), 25);
      }
    });
  }

  function setAgentActivity(sessionId: string, activity: AgentActivity) {
    setAgentActivityBySession((prev) =>
      prev[sessionId] === activity ? prev : { ...prev, [sessionId]: activity }
    );
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
    const mode = defaultModeFor(provider, sessionInteraction(defaultSessionMode));
    setDefaultSessionMode(mode);
    writeDefaultSessionMode(mode);
    setModeMenuOpen(false);
  }

  function toggleDefaultInteraction() {
    const provider = MODE_MENU_ICONS[defaultSessionMode];
    const nextInteraction = sessionInteraction(defaultSessionMode) === "run" ? "terminal" : "run";
    const mode = defaultModeFor(provider, nextInteraction);
    setDefaultSessionMode(mode);
    writeDefaultSessionMode(mode);
  }

  function updateCompletionSoundEnabled(enabled: boolean) {
    setCompletionSoundEnabled(enabled);
    writeCompletionSoundEnabled(enabled);
    if (enabled && completionSoundVolume <= 0) {
      setCompletionSoundVolume(DEFAULT_COMPLETION_SOUND_VOLUME);
      writeCompletionSoundVolume(DEFAULT_COMPLETION_SOUND_VOLUME);
    }
  }

  function updateCompletionSoundVolume(volume: number) {
    const nextVolume = Math.max(MIN_COMPLETION_SOUND_VOLUME, Math.min(1, volume));
    setCompletionSoundVolume(nextVolume);
    writeCompletionSoundVolume(nextVolume);
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
    terminalRefs.current.delete(id);
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

  function startRemoteControl(id: string) {
    // \r is what the terminal would send for the Enter key, so claude
    // submits the line. Slash commands are evaluated client-side by the
    // claude TUI, so this needs no orchestrator round-trip.
    terminalRefs.current.get(id)?.sendInput("/remote-control\r");
  }

  function startRollout(id: string, mode: SessionMode) {
    const startedAtMs = Date.now();
    setNowMs(startedAtMs);
    setRolloutTimers((prev) => ({
      ...prev,
      [id]: { startedAtMs, stoppedAtMs: null },
    }));
    const terminal = terminalRefs.current.get(id);
    if (!terminal) return;
    if (!CODEX_ROLLOUT_MODES.has(mode)) {
      terminal.sendInput("/rollout\r");
      return;
    }
    terminal.sendInput("$rollout ");
    window.setTimeout(() => terminal.sendInput("\r"), CODEX_ROLLOUT_SUBMIT_DELAY_MS);
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
            <button
              className="new-row-interaction-toggle"
              onClick={toggleDefaultInteraction}
              disabled={busy || MODE_MENU_ICONS[defaultSessionMode] === "pi"}
              aria-label={`Use ${
                sessionInteraction(defaultSessionMode) === "run" ? "terminal" : "run"
              } interaction`}
              title={
                MODE_MENU_ICONS[defaultSessionMode] === "pi"
                  ? "Pi only supports terminal sessions"
                  : sessionInteraction(defaultSessionMode) === "run"
                    ? "switch to terminal interaction"
                    : "switch to run interaction"
              }
            >
              {sessionInteraction(defaultSessionMode)}
            </button>
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
                  const mode = defaultModeFor(provider, sessionInteraction(defaultSessionMode));
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
              const agentActivity = isLive && AGENT_ACTIVITY_MODES.has(s.mode)
                ? agentActivityBySession[s.id] ?? "waiting"
                : null;
              const statusDotClass = agentActivity
                ? `status-dot status-codex-${agentActivity}`
                : `status-dot status-${s.status.toLowerCase()}`;
              const statusLabel = agentActivity
                ? `${MODE_LABELS[s.mode]} ${agentActivity}`
                : s.status;
              const bootLabel = sessionBootLabel(s, nowMs);
              const runtimeLabel = sessionRuntimeLabel(s, nowMs);
              const rolloutLabel = rolloutTimerLabel(s.id);
              const isRolloutTiming = Boolean(rolloutLabel);
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
                    <ModeChip mode={s.mode} />
                    {isClosing && <span className="session-closing-chip">closing</span>}
                    {s.mode === "subscription" && isLive && (
                      <button
                        className="session-action session-remote is-icon"
                        onClick={(e) => { e.stopPropagation(); startRemoteControl(s.id); }}
                        disabled={isClosing}
                        title="type /remote-control into this session — claude will print a https://claude.ai/code/session_… URL you can open"
                        aria-label="open remote control link"
                      >
                        <IconExternal />
                      </button>
                    )}
                    {ROLLOUT_MODES.has(s.mode) && isLive && (
                      <button
                        className={`session-action session-rollout is-icon${isRolloutTiming ? " is-clicked is-timing" : ""}`}
                        onClick={(e) => { e.stopPropagation(); startRollout(s.id, s.mode); }}
                        disabled={isClosing}
                        title={rolloutTimerTitle(s.id, s.mode)}
                        aria-label="start rollout"
                      >
                        <TankIcon className="session-action-tank-icon" />
                        {rolloutLabel && (
                          <span className="session-rollout-timer" aria-hidden="true">
                            {rolloutLabel}
                          </span>
                        )}
                      </button>
                    )}
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
              <li className="dropdown-settings" role="none">
                <label className="setting-toggle">
                  <input
                    type="checkbox"
                    checked={completionSoundEnabled}
                    onChange={(e) => updateCompletionSoundEnabled(e.target.checked)}
                  />
                  <span>Completion sound</span>
                </label>
                <label className="setting-range">
                  <span>Volume</span>
                  <input
                    type="range"
                    min={MIN_COMPLETION_SOUND_VOLUME}
                    max="1"
                    step="0.01"
                    value={completionSoundVolume}
                    disabled={!completionSoundEnabled}
                    onChange={(e) => updateCompletionSoundVolume(Number(e.target.value))}
                    aria-label="Completion sound volume"
                  />
                  <span className="setting-value">{Math.round(completionSoundVolume * 100)}%</span>
                </label>
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
                    <HeadlessRun session={s} visible={active === s.id} />
                  </div>
                ) : (
                  <Terminal
                    key={s.id}
                    ref={(h) => {
                      if (h) terminalRefs.current.set(s.id, h);
                      else terminalRefs.current.delete(s.id);
                    }}
                    sessionId={s.id}
                    mode={s.mode}
                    status={s.status}
                    bootLabel={sessionBootLabel(s, nowMs)}
                    bootTitle={sessionBootTitle(s, nowMs)}
                    completionSoundEnabled={completionSoundEnabled}
                    completionSoundVolume={completionSoundVolume}
                    visible={active === s.id}
                    onAgentActivityChange={setAgentActivity}
                    onAgentCompletion={stopRolloutTimer}
                  />
                )
              )}
          </div>
        )}
      </main>
    </div>
  );
}
