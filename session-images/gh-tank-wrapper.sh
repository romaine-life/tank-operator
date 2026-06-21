#!/bin/sh
# Durable `gh` for Tank sessions (egress-proxy / "the wall" only).
#
# Installed at /usr/local/bin/gh — ahead of the real /usr/bin/gh on PATH — so it
# shadows gh. Every Tank session routes api.github.com through the agent-egress
# proxy (the wall): this wrapper hands the real `gh` the pod's RAW
# auth.romaine.life token and the wall exchanges it, mints the right-scoped
# GitHub App token server-side (least-privilege for restricted sessions, full
# for unrestricted), records the action, and — for restricted sessions —
# rejects merges / pushes to main. The wall is the boundary, so this wrapper
# does NOT mint any GitHub token in-pod and there is no in-pod broker fallback
# (no session-PR-open, PR-write, or token-mint delegation to the sidecar):
# writes are governed by the wall, not by the sidecar.
set -u
REAL_GH="${TANK_REAL_GH:-/usr/bin/gh}"

# Honor an explicitly-provided token.
if [ -n "${GH_TOKEN:-}" ] || [ -n "${GITHUB_TOKEN:-}" ]; then
  exec "$REAL_GH" "$@"
fi

auth_tok="$(cat "${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}" 2>/dev/null || true)"
[ -n "$auth_tok" ] || exec "$REAL_GH" "$@"

# Egress-proxy mode (the wall fronts EVERY session now — restricted or not).
# api.github.com is pinned at the agent egress proxy, which exchanges this token, mints
# the App token server-side (least-privilege for restricted sessions, full for
# unrestricted), records the action, and — for restricted sessions — rejects merges. So
# hand gh the pod's RAW token and let the wall govern — no in-pod mint, no
# session-PR-open or PR-write brokering. gh (Go)
# reads SSL_CERT_FILE as its WHOLE root set, so trust the proxy leaf by combining the
# OS roots with the gateway CA (non-proxied hosts like uploads.github.com keep working).
case "$(printf '%s' "${TANK_GIT_EGRESS_PROXY:-false}" | tr '[:upper:]' '[:lower:]')" in
  1|true|yes|on)
    _ca="${TANK_GIT_PROXY_CA:-/etc/oauth-gateway-ca/ca.crt}"
    _bundle="${TANK_GIT_PROXY_CA_BUNDLE:-$HOME/.config/tank/gh-ca-bundle.crt}"
    if [ ! -s "$_bundle" ] && [ -s "$_ca" ]; then
      mkdir -p "$(dirname "$_bundle")"
      if [ -s /etc/ssl/certs/ca-certificates.crt ]; then
        cat /etc/ssl/certs/ca-certificates.crt "$_ca" > "$_bundle" 2>/dev/null || cp "$_ca" "$_bundle"
      else
        cp "$_ca" "$_bundle"
      fi
    fi
    [ -s "$_bundle" ] && export SSL_CERT_FILE="$_bundle" GIT_SSL_CAINFO="$_bundle"
    export GH_TOKEN="$auth_tok"
    exec "$REAL_GH" "$@"
    ;;
esac

# The wall is mandatory. With no egress proxy there is no governed path, so we
# deliberately do NOT mint a GitHub token in-pod (that would be a silent
# direct-GitHub bypass of the wall). Run the real gh with no Tank token so any
# write fails loudly rather than going through an ungoverned in-pod credential.
printf 'tank(gh): the agent-egress proxy (TANK_GIT_EGRESS_PROXY) is not enabled; running gh without a Tank-minted token. GitHub access for Tank sessions flows through the wall.\n' >&2
exec "$REAL_GH" "$@"
