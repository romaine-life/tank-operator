import type { SessionRole } from "./authPolicy";

// Microsoft sign-in happens upstream at auth.romaine.life. This SPA:
//   1. On boot, checks for a stored tank-operator session JWT and validates
//      it via /api/auth/me.
//   2. If no valid session, tries to fetch an auth.romaine.life JWT from
//      that service's /api/auth/token endpoint — the auth-service session
//      cookie is on `.romaine.life` so it's auto-attached. If the user is
//      already signed into auth.romaine.life from another app, this is the
//      seamless path that lands them signed in here without any redirect.
//      The JWT is then exchanged at /api/auth/exchange for a tank-operator-
//      signed session JWT.
//   3. If both fail, render the Sign-in button. Clicking it redirects to
//      auth.romaine.life's Microsoft sign-in flow, which sets the
//      .romaine.life session cookie and returns the user here. Step 2 then
//      runs again on bootstrap and succeeds.

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

const TOKEN_KEY = "tank-operator-jwt";

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

async function exchange(upstreamJWT: string): Promise<SessionUser> {
  const res = await fetch("/api/auth/exchange", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ auth_jwt: upstreamJWT }),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`Sign-in exchange failed (${res.status}): ${text}`);
  }
  const body = (await res.json()) as { token: string; user: SessionUser };
  storeToken(body.token);
  return body.user;
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
 * trigger a redirect on its own — the SPA shows a Sign-in button for that.
 * Auto-redirecting on boot would silently re-SSO users who just signed out.
 */
export async function bootstrapAuth(): Promise<SessionUser | null> {
  // 1. Existing tank-operator session?
  const existing = getStoredToken();
  if (existing) {
    const res = await fetch("/api/auth/me", {
      headers: { Authorization: `Bearer ${existing}` },
    });
    if (res.ok) return (await res.json()) as SessionUser;
    clearStoredToken();
  }

  // 2. Try to silently exchange an auth.romaine.life session cookie.
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
      return await exchange(upstreamJWT);
    } catch (e) {
      console.warn("silent exchange failed; user must click Sign-in", e);
    }
  }

  // 3. Not signed in. Wait for startLogin().
  return null;
}

/** User-initiated sign-in: redirect to auth.romaine.life's Microsoft flow. */
export async function startLogin(): Promise<void> {
  const config = await fetchConfig();
  const callbackURL = encodeURIComponent(window.location.origin + window.location.pathname);
  // auth.romaine.life exposes a GET endpoint at /sign-in/microsoft that
  // takes callbackURL as a query param, kicks off Better Auth's social
  // flow, and 302s back to the callback once Microsoft completes. The
  // Better Auth routes under /api/auth/* are POST-only, so a top-level
  // GET redirect there 404s.
  window.location.href = `${config.auth_url}/sign-in/microsoft?callbackURL=${callbackURL}`;
}

export async function logout(): Promise<void> {
  clearStoredToken();
  try {
    await fetch("/api/auth/logout", { method: "POST" });
  } catch {
    // best-effort
  }
  // Also clear the auth.romaine.life session cookie so the next page load
  // doesn't silently re-SSO via fetchUpstreamJWT.
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
