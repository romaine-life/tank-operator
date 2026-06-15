#!/bin/sh
# kubectl client.authentication.k8s.io exec credential plugin for NON-RESTRICTED
# Tank sessions. It mints a short-lived token for the trusted (cluster-admin)
# session SA via the orchestrator's cluster-credential endpoint, authenticated
# with the pod's tank-operator-audience SA token, and prints the ExecCredential
# JSON verbatim for kubectl.
#
# Installed (with a ~/.kube/config that references it) only in non-restricted
# sessions by install-agent-git-template.sh. Restricted sessions never get this
# kubeconfig, so their kubectl falls back to the pod's read-only SA — the two
# modes stay cleanly separate.
set -eu

internal_url="${TANK_OPERATOR_INTERNAL_URL:-http://tank-operator.tank-operator.svc.cluster.local}"
token_path="${TANK_OPERATOR_TOKEN_PATH:-/var/run/secrets/tank-operator/token}"

tok="$(cat "$token_path" 2>/dev/null || true)"
if [ -z "$tok" ]; then
  echo "tank kubectl credential: no tank-operator SA token at $token_path" >&2
  exit 1
fi

curl -fsS -m 25 \
  -H "Authorization: Bearer ${tok}" \
  -H "Accept: application/json" \
  -X POST "${internal_url%/}/api/internal/session-cluster-credential"
