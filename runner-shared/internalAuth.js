import { readFile } from "node:fs/promises";

const DEFAULT_EXCHANGE_URL = "https://auth.romaine.life/api/auth/exchange/k8s";
const REFRESH_SKEW_MS = 30_000;
const cachedTokens = new Map();

function trimTrailingSlashes(value) {
  return String(value || "").replace(/\/+$/, "");
}

function decodeBase64URL(value) {
  const padded = value + "=".repeat((4 - (value.length % 4)) % 4);
  return Buffer.from(padded.replace(/-/g, "+").replace(/_/g, "/"), "base64").toString("utf8");
}

function jwtExpiresAtMs(token) {
  try {
    const payload = JSON.parse(decodeBase64URL(String(token || "").split(".")[1] || ""));
    const exp = Number(payload?.exp || 0);
    return exp > 0 ? exp * 1000 : 0;
  } catch {
    return 0;
  }
}

function responseExpiresAtMs(value, token) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value > 1_000_000_000_000 ? value : value * 1000;
  }
  if (typeof value === "string" && value.trim()) {
    const parsed = Date.parse(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return jwtExpiresAtMs(token);
}

export function hasInternalAuthConfig(cfg) {
  return Boolean(
    String(cfg?.authRomaineTokenPath || "").trim() ||
      String(cfg?.operatorTokenPath || "").trim(),
  );
}

export async function internalBearerToken(cfg) {
  const authRomaineTokenPath = String(cfg?.authRomaineTokenPath || "").trim();
  if (authRomaineTokenPath) {
    const exchangeURL = trimTrailingSlashes(cfg?.authRomaineExchangeURL || DEFAULT_EXCHANGE_URL);
    const cacheKey = `${exchangeURL}\n${authRomaineTokenPath}`;
    const cached = cachedTokens.get(cacheKey);
    const now = Date.now();
    if (cached?.token && cached.expiresAtMs > now + REFRESH_SKEW_MS) {
      return cached.token;
    }

    const saToken = (await readFile(authRomaineTokenPath, "utf8")).trim();
    const response = await fetch(exchangeURL, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${saToken}`,
        "Content-Type": "application/json",
      },
      body: "{}",
    });
    if (!response.ok) {
      throw new Error(`auth.romaine exchange failed: ${response.status}`);
    }
    const body = await response.json();
    const token = String(body?.token || "").trim();
    const expiresAtMs = responseExpiresAtMs(body?.expires_at, token);
    if (!token || expiresAtMs <= now) {
      throw new Error("auth.romaine exchange returned no valid token");
    }
    cachedTokens.set(cacheKey, { token, expiresAtMs });
    return token;
  }

  const legacyTokenPath = String(cfg?.operatorTokenPath || "").trim();
  if (!legacyTokenPath) {
    return "";
  }
  return (await readFile(legacyTokenPath, "utf8")).trim();
}
