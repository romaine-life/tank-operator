#!/bin/sh
# Tank git credential helper (egress-proxy / "the wall" only).
#
# Every Tank session — restricted or not — routes github.com through the
# agent-egress proxy (the wall). On a git network operation this helper hands
# git the pod's RAW auth.romaine.life service-account token; the wall exchanges
# it, mints the right-scoped GitHub App token server-side (least-privilege for
# restricted sessions, full for unrestricted), records the action, and — for
# restricted sessions — enforces no-push-to-main / no-merge. The wall is the
# policy boundary, so this helper does NOT mint any GitHub token in-pod and
# there is no in-pod broker fallback: if the wall is not enabled the helper
# returns no credential and git fails loudly rather than silently bypassing the
# wall with a directly-minted token.
#
# git invokes it as: git-credential-tank <get|store|erase>
# POSIX sh (no bashisms) so it runs under dash as well as bash.
set -eu

op="${1:-get}"
# Only `get` mints; nothing to persist or forget for store/erase.
[ "$op" = "get" ] || exit 0

AUTH_TOKEN_PATH="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"

# Read git's request: key=value lines terminated by a blank line.
host=""
while IFS='=' read -r key val; do
  [ -z "$key" ] && break
  case "$key" in
    host) host="$val" ;;
  esac
done

# Only handle github.com; let any other helper or prompt handle the rest.
[ "$host" = "github.com" ] || exit 0

auth_tok="$(cat "$AUTH_TOKEN_PATH" 2>/dev/null || true)"
[ -n "$auth_tok" ] || exit 0

# Egress-proxy mode (the wall fronts EVERY session now — restricted or not).
# github.com is pinned at the agent egress proxy, which exchanges this token, mints
# the GitHub App token server-side, records the action, and — for restricted
# sessions — enforces no-push-to-main / no-merge. So hand git the pod's RAW
# auth.romaine.life token (NOT an in-pod-minted GitHub token) and let the wall do
# the minting.
case "$(printf '%s' "${TANK_GIT_EGRESS_PROXY:-false}" | tr '[:upper:]' '[:lower:]')" in
  1|true|yes|on)
    printf 'username=x-access-token\n'
    printf 'password=%s\n' "$auth_tok"
    exit 0
    ;;
esac

# The wall is mandatory. With no egress proxy there is no governed path, so we
# deliberately do NOT mint a GitHub token in-pod (that would be a silent
# direct-GitHub bypass of the wall). Emit no credential and let git fail loudly.
printf 'tank(git-credential): the agent-egress proxy (TANK_GIT_EGRESS_PROXY) is not enabled; refusing to mint a GitHub token in-pod. GitHub access for Tank sessions flows through the wall.\n' >&2
exit 0
