import { PublicClientApplication, type AuthenticationResult } from "@azure/msal-browser";

interface AppConfig {
  entra_client_id: string;
  entra_authority: string;
}

interface SessionUser {
  sub: string;
  email: string;
  name: string;
  avatar_url: string;
  // Profile fields surfaced from /api/auth/me. Null until the user
  // completes the GitHub App install (#57 stage 2).
  github_login: string | null;
  installation_id: number | null;
  // Phase E: SPA run-pane preferences, persisted on the Cosmos profile
  // row so they ride across browsers. Null when the user has never
  // saved prefs (e.g., first sign-in on a new account — the SPA falls
  // back to localStorage, then to defaults).
  run_prefs: Record<string, unknown> | null;
}

const SCOPES = ["User.Read", "openid", "profile", "email"];
const TOKEN_KEY = "tank-operator-jwt";

let msal: PublicClientApplication | null = null;

declare global {
  interface Window {
    tankOperatorDesktop?: {
      microsoftLogin: () => Promise<{ idToken: string }>;
    };
  }
}

async function fetchConfig(): Promise<AppConfig> {
  const res = await fetch("/api/config");
  if (!res.ok) throw new Error(`config fetch failed: ${res.status}`);
  return res.json();
}

function decodeJwtPayload(token: string): Record<string, unknown> | null {
  const part = token.split(".")[1];
  if (!part) return null;
  try {
    const normalized = part.replace(/-/g, "+").replace(/_/g, "/");
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
    return JSON.parse(atob(padded)) as Record<string, unknown>;
  } catch {
    return null;
  }
}

function emailFromIdToken(idToken: string): string | null {
  const payload = decodeJwtPayload(idToken);
  const email = payload?.email ?? payload?.preferred_username;
  return typeof email === "string" && email ? email.toLowerCase() : null;
}

async function loginErrorMessage(res: Response, attemptedEmail: string | null): Promise<string> {
  const fallback = `Sign-in failed (${res.status}).`;
  const text = await res.text();
  if (!text) return fallback;
  const notAllowed = attemptedEmail
    ? `This Microsoft account is not allowed for Tank Operator: ${attemptedEmail}`
    : "This Microsoft account is not allowed for Tank Operator.";
  try {
    const body = JSON.parse(text) as {
      detail?: string | { code?: string; email?: string; message?: string };
    };
    const detail = body.detail;
    if (typeof detail === "string") {
      if (detail === "email not allowed") {
        return notAllowed;
      }
      return detail;
    }
    if (detail?.code === "email_not_allowed") {
      return detail.email
        ? `This Microsoft account is not allowed for Tank Operator: ${detail.email}`
        : "This Microsoft account is not allowed for Tank Operator.";
    }
    if (detail?.message) return detail.message;
  } catch {
    // Fall through to the compact status message below.
  }
  return fallback;
}

async function getMsal(): Promise<PublicClientApplication> {
  if (msal) return msal;
  const config = await fetchConfig();
  if (!config.entra_client_id) throw new Error("backend has no ENTRA_CLIENT_ID");
  msal = new PublicClientApplication({
    auth: {
      clientId: config.entra_client_id,
      authority: config.entra_authority,
      redirectUri: `${window.location.origin}/`,
    },
    cache: { cacheLocation: "sessionStorage" },
  });
  await msal.initialize();
  return msal;
}

async function exchange(idToken: string): Promise<SessionUser> {
  const res = await fetch("/api/auth/microsoft/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ credential: idToken }),
  });
  if (!res.ok) throw new Error(await loginErrorMessage(res, emailFromIdToken(idToken)));
  const body = await res.json();
  localStorage.setItem(TOKEN_KEY, body.token);
  return body.user;
}

export function getStoredToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function clearStoredToken(): void {
  localStorage.removeItem(TOKEN_KEY);
}

/** Run once on app boot. Resolves to the signed-in user, or null if not signed in.
 *  Does NOT trigger a login redirect on its own — the SPA shows a Sign-in button
 *  for that. Auto-redirecting on boot would silently re-SSO users who just clicked
 *  sign out (their Microsoft account session outlives our local logout). */
export async function bootstrapAuth(): Promise<SessionUser | null> {
  // 1. Do we already have a backend session?
  const existing = getStoredToken();
  if (existing) {
    const res = await fetch("/api/auth/me", {
      headers: { Authorization: `Bearer ${existing}` },
    });
    if (res.ok) return res.json();
    clearStoredToken();
  }

  // 2. Did we just come back from Entra? If config is unavailable in a
  // frontend-only dev server, still let the unauthenticated preview render.
  let client: PublicClientApplication;
  try {
    client = await getMsal();
  } catch (e) {
    console.info("auth config unavailable; rendering unauthenticated preview", e);
    return null;
  }

  let redirectResult: AuthenticationResult | null = null;
  try {
    redirectResult = await client.handleRedirectPromise();
  } catch (e) {
    console.error("MSAL handleRedirectPromise failed", e);
  }
  if (redirectResult?.idToken) {
    return exchange(redirectResult.idToken);
  }

  // 3. Not signed in. Wait for an explicit click to call startLogin().
  return null;
}

/** User-initiated sign-in. Navigates away to Entra. */
export async function startLogin(): Promise<void> {
  if (window.tankOperatorDesktop) {
    const result = await window.tankOperatorDesktop.microsoftLogin();
    await exchange(result.idToken);
    window.location.assign("/");
    return;
  }
  const client = await getMsal();
  await client.loginRedirect({ scopes: SCOPES, prompt: "select_account" });
}

export async function logout(): Promise<void> {
  clearStoredToken();
  try {
    await fetch("/api/auth/logout", { method: "POST" });
  } catch {
    // best-effort
  }
  // Local-only sign-out: drop MSAL's cached account so the next bootstrap
  // re-prompts, but don't hit Entra's end_session endpoint — that signs the
  // user out of their Microsoft account globally across every app.
  const client = await getMsal();
  await client.clearCache();
  window.location.assign("/");
}

/** fetch wrapper that adds the Bearer token. */
export async function authedFetch(input: RequestInfo, init: RequestInit = {}): Promise<Response> {
  const token = getStoredToken();
  const headers = new Headers(init.headers);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  return fetch(input, { ...init, headers });
}
