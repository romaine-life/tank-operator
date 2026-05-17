#!/usr/bin/env bash

# tank-jwt-from-az.sh — mint a tank-operator session JWT from the Entra
# access token sitting in your local `az` session. The output is a JWT
# (one line, stdout) suitable for `Authorization: Bearer ...`.
#
# Flow (three hops):
#   1. `az account get-access-token --resource $ENTRA_AUDIENCE` →
#      Entra ID access token (RS256, signed by login.microsoftonline.com).
#   2. POST it to https://auth.romaine.life/api/auth/entra-exchange →
#      auth.romaine.life user JWT (RS256, signed by auth's JWKS key).
#   3. POST that to https://tank.romaine.life/api/auth/exchange →
#      tank-operator session JWT (RS256, signed by tank's KV Key).
#
# Result is cached at $TANK_JWT_CACHE (default ~/.config/tank-operator/jwt)
# with the exp claim parsed; re-runs return the cached JWT until ≤60s
# before expiry, then run the full chain again. The Entra access token
# is never written to disk.
#
# One-time setup before this works:
#   1. Register an Entra app in your tenant (auth-romaine-life-cli),
#      expose api://<appId>. See auth.romaine.life/README.md.
#   2. Set ENTRA_EXCHANGE_AUDIENCE / ENTRA_EXCHANGE_TENANT_ID on the
#      auth-romaine-life deployment via k8s/values.yaml.
#   3. Sign in once at https://auth.romaine.life as the identity you'll
#      use here (the `az account show` user) so a Better Auth row
#      exists, then promote its role from `pending` to `admin` (or
#      `user`) via the admin console. Exchange will not auto-provision
#      users.

set -euo pipefail

ENTRA_AUDIENCE="${ENTRA_AUDIENCE:?ENTRA_AUDIENCE must be set, e.g. api://<appId> matching auth.romaine.life's ENTRA_EXCHANGE_AUDIENCE}"
AUTH_BASE_URL="${AUTH_BASE_URL:-https://auth.romaine.life}"
TANK_BASE_URL="${TANK_BASE_URL:-https://tank.romaine.life}"
CACHE_FILE="${TANK_JWT_CACHE:-$HOME/.config/tank-operator/jwt}"

# Re-use a cached JWT until it's within REFRESH_LEEWAY_SECONDS of expiry.
# 60s gives the caller enough time to issue the curl before the token
# expires upstream; smaller windows churn the cache, larger ones risk
# in-flight 401s on long-running operations.
REFRESH_LEEWAY_SECONDS=60

decode_jwt_exp() {
  local jwt="$1"
  local payload
  payload="$(echo -n "${jwt#*.}" | cut -d. -f1)"
  # Pad base64url to 4-byte boundary; some decoders care, others don't.
  local pad=$(( 4 - ${#payload} % 4 ))
  [[ $pad -eq 4 ]] || payload="${payload}$(printf '=%.0s' $(seq 1 $pad))"
  echo "$payload" | tr '_-' '/+' | base64 -d 2>/dev/null | \
    python3 -c 'import json, sys; print(json.load(sys.stdin).get("exp", 0))'
}

cached_jwt_if_fresh() {
  [[ -f "$CACHE_FILE" ]] || return 1
  local jwt
  jwt="$(<"$CACHE_FILE")"
  [[ -n "$jwt" ]] || return 1
  local exp
  exp="$(decode_jwt_exp "$jwt" 2>/dev/null || echo 0)"
  local now
  now="$(date +%s)"
  if (( exp - now > REFRESH_LEEWAY_SECONDS )); then
    echo "$jwt"
    return 0
  fi
  return 1
}

mint_fresh() {
  local entra_token auth_jwt tank_jwt
  entra_token="$(
    az account get-access-token --resource "$ENTRA_AUDIENCE" \
      --query accessToken --output tsv
  )"
  [[ -n "$entra_token" ]] || { echo "az returned empty token" >&2; exit 1; }

  auth_jwt="$(
    curl --silent --show-error --fail \
      -H 'Content-Type: application/json' \
      --data "$(jq -nc --arg t "$entra_token" '{access_token: $t}')" \
      "$AUTH_BASE_URL/api/auth/entra-exchange" \
    | jq -r '.token'
  )"
  [[ -n "$auth_jwt" && "$auth_jwt" != "null" ]] || {
    echo "auth.romaine.life entra-exchange returned no token" >&2
    exit 1
  }

  tank_jwt="$(
    curl --silent --show-error --fail \
      -H 'Content-Type: application/json' \
      --data "$(jq -nc --arg t "$auth_jwt" '{auth_jwt: $t}')" \
      "$TANK_BASE_URL/api/auth/exchange" \
    | jq -r '.token'
  )"
  [[ -n "$tank_jwt" && "$tank_jwt" != "null" ]] || {
    echo "tank.romaine.life exchange returned no token" >&2
    exit 1
  }

  mkdir -p "$(dirname "$CACHE_FILE")"
  # Restrict to caller — the JWT is bearer-equivalent.
  umask 077
  printf '%s' "$tank_jwt" > "$CACHE_FILE"
  echo "$tank_jwt"
}

if jwt="$(cached_jwt_if_fresh)"; then
  echo "$jwt"
else
  mint_fresh
fi
