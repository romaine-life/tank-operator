import { useEffect, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";
import { Terminal, type AgentActivity, type TerminalHandle } from "./Terminal";
import { authedFetch, bootstrapAuth, logout, startLogin } from "./auth";
import { ProviderIcon } from "./providerIcons";

type SessionMode =
  | "api_key"
  | "subscription"
  | "config"
  | "codex_subscription"
  | "codex_config";
type DefaultSessionMode = Extract<SessionMode, "subscription" | "codex_subscription">;
type Provider = "anthropic" | "openai";

interface Session {
  id: string;
  pod_name: string | null;
  owner: string;
  status: string;
  mode: SessionMode;
  // User-set friendly name. Null when unset; UI falls back to the id slug.
  name: string | null;
}

const MODE_LABELS: Record<SessionMode, string> = {
  api_key: "Claude API key",
  subscription: "Claude",
  config: "Claude config",
  codex_subscription: "Codex",
  codex_config: "Codex config",
};

// Compact labels for the inline session-row chip. Falls back to MODE_LABELS
// elsewhere.
const MODE_CHIP_LABELS: Record<SessionMode, string> = {
  api_key: "api",
  subscription: "claude",
  config: "config",
  codex_subscription: "codex",
  codex_config: "codex-cfg",
};

const MODE_CHIP_ICONS: Partial<Record<SessionMode, "anthropic" | "openai">> = {
  subscription: "anthropic",
  codex_subscription: "openai",
};

const MODE_MENU_ICONS: Record<SessionMode, Provider> = {
  api_key: "anthropic",
  subscription: "anthropic",
  config: "anthropic",
  codex_subscription: "openai",
  codex_config: "openai",
};

const PROVIDER_DEFAULT_MODES: Record<Provider, DefaultSessionMode> = {
  anthropic: "subscription",
  openai: "codex_subscription",
};

const PROVIDER_CONFIG_MODES: Record<Provider, SessionMode> = {
  anthropic: "config",
  openai: "codex_config",
};

const MODE_HINTS: Record<SessionMode, string> = {
  subscription: "Uses claude.ai login",
  api_key: "Specify an API key fallback",
  config: "Log in once · seeds KV for future sessions",
  codex_subscription: "Uses ChatGPT login from KV",
  codex_config: "codex login --device-auth · seeds KV for Codex",
};

const MODE_ORDER: SessionMode[] = [
  "subscription",
  "api_key",
  "config",
  "codex_subscription",
  "codex_config",
];

const DEFAULT_SESSION_MODE_KEY = "tank.defaultSessionMode";
const COMPLETION_SOUND_ENABLED_KEY = "tank.completionSoundEnabled";
const COMPLETION_SOUND_VOLUME_KEY = "tank.completionSoundVolume";
const DEFAULT_COMPLETION_SOUND_VOLUME = 0.55;
const MIN_COMPLETION_SOUND_VOLUME = 0.05;

function isDefaultSessionMode(value: string | null): value is DefaultSessionMode {
  return value === "subscription" || value === "codex_subscription";
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

// Modes whose pods carry harvestable credentials — the "save" button
// surfaces on session rows in these modes. Kept as a Set so adding a third
// future config mode doesn't grow an OR chain.
const CONFIG_MODES = new Set<SessionMode>(["config", "codex_config"]);
const CODEX_MODES = new Set<SessionMode>(["codex_subscription", "codex_config"]);

interface SessionUser {
  sub: string;
  email: string;
  name: string;
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

function readGlimmungLaunchContext(): GlimmungLaunchContext | null {
  const params = new URLSearchParams(window.location.search);
  const runId = params.get("glimmung_run_id");
  const issueId = params.get("glimmung_issue_id");
  if (!runId || !issueId) return null;
  return {
    glimmung_run_id: runId,
    glimmung_issue_id: issueId,
    glimmung_pr_id: params.get("glimmung_pr_id"),
    validation_url: params.get("validation_url"),
  };
}

function clearGlimmungLaunchContext(): void {
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

function initials(user: SessionUser): string {
  const source = (user.name || user.email || "?").trim();
  const parts = source.split(/[\s@._-]+/).filter(Boolean);
  const first = parts[0]?.[0] ?? source[0];
  const second = parts[1]?.[0] ?? "";
  return (first + second).toUpperCase().slice(0, 2);
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

export function App() {
  const [user, setUser] = useState<SessionUser | null>(null);
  const [booted, setBooted] = useState(false);
  const [authError, setAuthError] = useState<string | null>(null);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [active, setActive] = useState<string | null>(null);
  const [closingIds, setClosingIds] = useState<Set<string>>(() => new Set());
  const [agentActivityBySession, setAgentActivityBySession] = useState<Record<string, AgentActivity>>({});
  // Sessions whose Terminal stays mounted (so the WS keeps draining and
  // scrollback survives switching). A session is mounted the first time it
  // becomes active and unmounts only on deletion. Sessions you haven't
  // touched don't open a WS — same opt-in semantic the old tab list had.
  const [mounted, setMounted] = useState<Set<string>>(() => new Set());
  const [modeMenuOpen, setModeMenuOpen] = useState(false);
  const [profileMenuOpen, setProfileMenuOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
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
      setSessions(await res.json());
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }

  useEffect(() => {
    if (user) void refresh();
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
    setAgentActivityBySession((prev) => {
      const existing = new Set(sessions.map((s) => s.id));
      const next = Object.fromEntries(
        Object.entries(prev).filter(([id]) => existing.has(id))
      ) as Record<string, AgentActivity>;
      return Object.keys(next).length === Object.keys(prev).length ? prev : next;
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

  function activate(id: string) {
    setActive(id);
    setMounted((prev) => (prev.has(id) ? prev : new Set(prev).add(id)));
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

  async function createSession(mode: SessionMode = defaultSessionMode) {
    if (isDefaultSessionMode(mode)) {
      setDefaultSessionMode(mode);
      writeDefaultSessionMode(mode);
    }
    setBusy(true);
    setModeMenuOpen(false);
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
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  function setDefaultProvider(provider: Provider) {
    const mode = PROVIDER_DEFAULT_MODES[provider];
    setDefaultSessionMode(mode);
    writeDefaultSessionMode(mode);
    setModeMenuOpen(false);
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
      void renameSession(editingId, trimmed === "" ? null : trimmed);
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
    return (
      <div className="boot-state">
        <button className="btn-primary" onClick={() => { startLogin(); }}>Sign in</button>
      </div>
    );
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
                {(["anthropic", "openai"] as Provider[]).map((provider) => {
                  const mode = PROVIDER_DEFAULT_MODES[provider];
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
              const codexActivity = isLive && CODEX_MODES.has(s.mode)
                ? agentActivityBySession[s.id] ?? "waiting"
                : null;
              const statusDotClass = codexActivity
                ? `status-dot status-codex-${codexActivity}`
                : `status-dot status-${s.status.toLowerCase()}`;
              const statusLabel = codexActivity
                ? `Codex ${codexActivity}`
                : s.status;
              return (
                <li
                  key={s.id}
                  className={`${isActive ? "is-open" : ""}${isClosing ? " is-closing" : ""}`}
                  onClick={isEditing || isClosing ? undefined : (e) => openSession(s.id, e)}
                  title={sidebarCollapsed ? `${s.name ?? s.id} (${statusLabel})` : undefined}
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
                        placeholder={s.id}
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
                              ? `${s.id} — click to rename`
                              : "click to rename"
                        }
                      >
                        <span className="session-id">{s.name ?? s.id}</span>
                      </button>
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
            <span className="avatar" aria-hidden="true">{initials(user)}</span>
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
              <p className="welcome-sub">Spin up a Claude Code session</p>
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
              .map((s) => (
                <Terminal
                  key={s.id}
                  ref={(h) => {
                    if (h) terminalRefs.current.set(s.id, h);
                    else terminalRefs.current.delete(s.id);
                  }}
                  sessionId={s.id}
                  mode={s.mode}
                  status={s.status}
                  completionSoundEnabled={completionSoundEnabled}
                  completionSoundVolume={completionSoundVolume}
                  visible={active === s.id}
                  onAgentActivityChange={setAgentActivity}
                />
              ))}
          </div>
        )}
      </main>
    </div>
  );
}
