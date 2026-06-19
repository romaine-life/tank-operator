#!/bin/sh
# kubectl client.authentication.k8s.io exec credential plugin for NON-RESTRICTED
# Tank sessions. It mints a short-lived token for the trusted (cluster-admin)
# session SA via the orchestrator's cluster-credential endpoint, authenticated
# with the pod's auth.romaine.life service identity, and prints the
# ExecCredential JSON verbatim for kubectl.
#
# Installed (with a ~/.kube/config that references it) only in non-restricted
# sessions by install-agent-git-template.sh. Restricted sessions never get this
# kubeconfig, so their kubectl falls back to the pod's read-only SA — the two
# modes stay cleanly separate.
set -eu

internal_url="${TANK_OPERATOR_INTERNAL_URL:-http://tank-operator.tank-operator.svc.cluster.local}"
auth_token_path="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"
auth_exchange_url="${AUTH_ROMAINE_EXCHANGE_URL:-https://auth.romaine.life/api/auth/exchange/k8s}"
legacy_token_path="${TANK_OPERATOR_TOKEN_PATH:-/var/run/secrets/tank-operator/token}"

tok=""
if [ -r "$auth_token_path" ]; then
  response="$(
    curl -fsS -m 25 \
      -X POST "${auth_exchange_url%/}" \
      -H "Authorization: Bearer $(cat "$auth_token_path")" \
      -H "Content-Type: application/json" \
      -d '{}' \
      2>/dev/null || true
  )"
  tok="$(printf '%s' "$response" | jq -r '.token // empty' 2>/dev/null || true)"
fi
if [ -z "$tok" ]; then
  tok="$(cat "$legacy_token_path" 2>/dev/null || true)"
fi
if [ -z "$tok" ]; then
  echo "tank kubectl credential: no auth.romaine token at $auth_token_path and no legacy token at $legacy_token_path" >&2
  exit 1
fi

curl -fsS -m 25 \
  -H "Authorization: Bearer ${tok}" \
  -H "Accept: application/json" \
  -X POST "${internal_url%/}/api/internal/session-cluster-credential"
