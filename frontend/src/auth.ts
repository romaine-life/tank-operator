import type { SessionRole } from "./authPolicy";

// Microsoft sign-in happens upstream at auth.romaine.life. This SPA stores
// the upstream JWT and presents it directly to tank-operator; tank no longer
// exchanges it for a locally minted session token.

interface AppConfig {
  auth_url: string;
  session_scope?: string;
}

interface SessionUser {
  sub: string;
  email: string;
  name: string;
  /** Platform role from the auth.romaine.life JWT. `admin` and `service`
   *  bypass the GitHub install wall; `user` is the standard signed-in caller. */
  role: SessionRole;
  is_admin: boolean;
  avatar_url: string;
  github_login: string | null;
  installation_id: number | null;
  pinned_repos: string[];
  run_prefs: Record<string, unknown> | null;
}

const TOKEN_KEY = "auth-romaine-jwt";
export const AUTH_TOKEN_UPDATED_EVENT = "tank-auth-token-updated";

let cachedConfig: AppConfig | null = null;
let tokenRefreshInFlight: Promise<string | null> | null = null;

async function fetchConfig(): Promise<AppConfig> {
  if (cachedConfig) return cachedConfig;
  const res = await fetch("/api/config");
  if (!res.ok) throw new Error(`config fetch failed: ${res.status}`);
  cachedConfig = (await res.json()) as AppConfig;
  return cachedConfig;
}

async function fetchUpstreamJWT(authURL: string): Promise<string | null> {
  try {
    const res = await fetch(`${authURL}/api/auth/token`, { credentials: "include" });
    if (!res.ok) return null;
    const data = (await res.json()) as { token?: string };
    return data.token ?? null;
  } catch {
    return null;
  }
}

async function fetchMe(token: string): Promise<SessionUser> {
  const res = await fetch("/api/auth/me", {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`auth validation failed (${res.status}): ${text}`);
  }
  return (await res.json()) as SessionUser;
}

// Same eviction-on-quota dance as before: a bloated localStorage shouldn't
// be allowed to break sign-in. Lost prefs are server-synced and recoverable;
// a broken auth write is a dead-end.
function storeToken(token: string): void {
  try {
    localStorage.setItem(TOKEN_KEY, token);
    dispatchTokenUpdated();
    return;
  } catch (err) {
    if (!isQuotaExceeded(err)) throw err;
  }
  console.warn("localStorage quota exceeded on auth write; evicting and retrying");
  for (const key of Object.keys(localStorage)) {
    if (key === TOKEN_KEY) continue;
    try {
      localStorage.removeItem(key);
    } catch {
      // best-effort
    }
  }
  localStorage.setItem(TOKEN_KEY, token);
  dispatchTokenUpdated();
}

function isQuotaExceeded(err: unknown): boolean {
  if (!(err instanceof DOMException)) return false;
  return err.name === "QuotaExceededError" || err.name === "QUOTA_EXCEEDED_ERR";
}

export function getStoredToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function clearStoredToken(): void {
  localStorage.removeItem(TOKEN_KEY);
  dispatchTokenUpdated();
}

function dispatchTokenUpdated(): void {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new Event(AUTH_TOKEN_UPDATED_EVENT));
}

async function refreshStoredToken(): Promise<string | null> {
  if (tokenRefreshInFlight) return tokenRefreshInFlight;
  tokenRefreshInFlight = (async () => {
    let config: AppConfig;
    try {
      config = await fetchConfig();
    } catch {
      return null;
    }
    const upstreamJWT = await fetchUpstreamJWT(config.auth_url);
    if (!upstreamJWT) {
      clearStoredToken();
      return null;
    }
    storeToken(upstreamJWT);
    return upstreamJWT;
  })().finally(() => {
    tokenRefreshInFlight = null;
  });
  return tokenRefreshInFlight;
}

export type StreamTicketRequest =
  | { stream: "session-list"; sessionScope?: string }
  | { stream: "session-events"; sessionId: string; sessionScope?: string };

export async function authedEventSourceURL(
  path: string,
  request: StreamTicketRequest,
): Promise<string> {
  const res = await authedFetch("/api/auth/stream-ticket", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      stream: request.stream,
      ...(request.sessionScope ? { session_scope: request.sessionScope } : {}),
      ...(request.stream === "session-events" ? { session_id: request.sessionId } : {}),
    }),
  });
  if (!res.ok) {
    throw new Error(`stream ticket request failed: ${res.status}`);
  }
  const body = (await res.json()) as { ticket?: unknown };
  const ticket = typeof body.ticket === "string" ? body.ticket : "";
  if (!ticket) {
    throw new Error("stream ticket response did not include a ticket");
  }
  const separator = path.includes("?") ? "&" : "?";
  return `${path}${separator}stream_ticket=${encodeURIComponent(ticket)}`;
}

export async function authedEventSource(
  path: string,
  request: StreamTicketRequest,
): Promise<EventSource> {
  return new EventSource(await authedEventSourceURL(path, request), {
    withCredentials: true,
  });
}

/**
 * Boot-time auth check. Resolves to the signed-in user, or null. Does NOT
 * trigger a redirect on its own; the SPA shows a Sign-in button for that.
 * Auto-redirecting on boot would silently re-SSO users who just signed out.
 */
export async function bootstrapAuth(): Promise<SessionUser | null> {
  const existing = getStoredToken();
  if (existing) {
    try {
      return await fetchMe(existing);
    } catch {
      clearStoredToken();
    }
  }

  let config: AppConfig;
  try {
    config = await fetchConfig();
  } catch (e) {
    console.info("auth config unavailable; rendering unauthenticated preview", e);
    return null;
  }
  const upstreamJWT = await fetchUpstreamJWT(config.auth_url);
  if (upstreamJWT) {
    try {
      storeToken(upstreamJWT);
      return await fetchMe(upstreamJWT);
    } catch (e) {
      clearStoredToken();
      console.warn("silent auth bootstrap failed; user must click Sign-in", e);
    }
  }

  return null;
}

/** User-initiated sign-in: redirect to auth.romaine.life's Microsoft flow. */
export async function startLogin(): Promise<void> {
  const config = await fetchConfig();
  const current = new URL(window.location.href);
  const callbackTarget = current.searchParams.has("github_install_state")
    ? `${current.origin}${current.pathname}${current.search}`
    : `${current.origin}${current.pathname}`;
  const callbackURL = encodeURIComponent(callbackTarget);
  window.location.href = `${config.auth_url}/sign-in/microsoft?callbackURL=${callbackURL}`;
}

export async function logout(): Promise<void> {
  clearStoredToken();
  try {
    const config = await fetchConfig();
    await fetch(`${config.auth_url}/api/auth/sign-out`, {
      method: "POST",
      credentials: "include",
    });
  } catch {
    // best-effort
  }
  window.location.assign("/");
}

function authedInit(
  input: RequestInfo | URL,
  init: RequestInit,
  token: string | null,
): RequestInit {
  const headers = new Headers(input instanceof Request ? input.headers : undefined);
  new Headers(init.headers).forEach((value, key) => headers.set(key, value));
  if (token) {
    headers.set("Authorization", `Bearer ${token}`);
  } else {
    headers.delete("Authorization");
  }
  return { ...init, headers };
}

/** fetch wrapper that adds the Bearer token and refreshes it once on 401. */
export async function authedFetch(input: RequestInfo, init: RequestInit = {}): Promise<Response> {
  let token = getStoredToken();
  if (!token) {
    token = await refreshStoredToken();
  }
  const first = await fetch(input, authedInit(input, init, token));
  if (first.status !== 401) return first;

  const latest = getStoredToken();
  const refreshed = latest && latest !== token ? latest : await refreshStoredToken();
  if (!refreshed) return first;

  const retry = await fetch(input, authedInit(input, init, refreshed));
  if (retry.status === 401 && getStoredToken() === refreshed) {
    clearStoredToken();
  }
  return retry;
}
