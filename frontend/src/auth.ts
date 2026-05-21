import type { SessionRole } from "./authPolicy";

// Microsoft sign-in happens upstream at auth.romaine.life. This SPA stores
// the upstream JWT and presents it directly to tank-operator; tank no longer
// exchanges it for a locally minted session token.

interface AppConfig {
  auth_url: string;
}

interface SessionUser {
  sub: string;
  email: string;
  name: string;
  /** Platform role from the auth.romaine.life JWT. `admin` and `service`
   *  bypass the GitHub install wall; `user` is the standard signed-in caller. */
  role: SessionRole;
  avatar_url: string;
  github_login: string | null;
  installation_id: number | null;
  run_prefs: Record<string, unknown> | null;
}

const TOKEN_KEY = "auth-romaine-jwt";

let cachedConfig: AppConfig | null = null;

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

/** fetch wrapper that adds the Bearer token. */
export async function authedFetch(input: RequestInfo, init: RequestInit = {}): Promise<Response> {
  const token = getStoredToken();
  const headers = new Headers(init.headers);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  return fetch(input, { ...init, headers });
}
