#!/usr/bin/env bash
set -Eeuo pipefail

slot_name="${1:-tank-operator-slot-2}"
prod_claude_proxy="claude-api-proxy.tank-operator.svc.cluster.local"
prod_codex_proxy="codex-api-proxy.tank-operator.svc.cluster.local"
prod_antigravity_proxy="antigravity-api-proxy.tank-operator.svc.cluster.local"
prod_oauth_gateway="claude-oauth-gateway.tank-operator.svc.cluster.local"

hot_rendered="$(helm template "${slot_name}" k8s \
  --namespace "${slot_name}" \
  --set "renderMode=hot" \
  --set "testEnv.slotName=${slot_name}")"

warm_rendered="$(helm template "${slot_name}" k8s \
  --namespace "${slot_name}" \
  --set "renderMode=warm" \
  --set "testEnv.slotName=${slot_name}")"

combined_rendered="${hot_rendered}
---
${warm_rendered}"

if grep -Eq 'name: ANTHROPIC_API_KEY|key: anthropic-api-key|github-app-creds' <<<"${combined_rendered}"; then
  echo "slot render still contains the removed Anthropic API-key injection path" >&2
  exit 1
fi

if grep -Eq 'app.kubernetes.io/name: (claude-api-proxy|codex-api-proxy|antigravity-api-proxy)|name: (claude-api-proxy|codex-api-proxy|antigravity-api-proxy)$|name: tank-api-proxy$' <<<"${combined_rendered}"; then
  echo "slot render still contains a slot-local provider api-proxy surface" >&2
  exit 1
fi

if grep -Eq "name: ${slot_name}-(claude-code-credentials|codex-credentials|antigravity-credentials)|key: ${slot_name}-(claude-code-credentials|codex-credentials|antigravity-credentials)" <<<"${warm_rendered}"; then
  echo "warm slot render still declares slot-owned provider credential ExternalSecrets" >&2
  exit 1
fi

if grep -Eq 'name: (CLAUDE_CREDENTIALS_KV_KEY|CODEX_CREDENTIALS_KV_KEY|ANTIGRAVITY_CREDENTIALS_KV_KEY|CLAUDE_CREDENTIALS_FILE)' <<<"${hot_rendered}"; then
  echo "hot slot render exposes provider credential write/read env vars" >&2
  exit 1
fi

if grep -Eq 'secretName: (claude-code-credentials|codex-credentials|antigravity-credentials|.*-claude-code-credentials|.*-codex-credentials|.*-antigravity-credentials)' <<<"${hot_rendered}"; then
  echo "hot slot render still mounts provider credential Secrets" >&2
  exit 1
fi

if ! grep -Fq "value: \"${prod_oauth_gateway}\"" <<<"${hot_rendered}"; then
  echo "hot slot render did not route CLAUDE_OAUTH_GATEWAY_HOST to ${prod_oauth_gateway}" >&2
  exit 1
fi

if ! grep -Fq "value: \"${prod_claude_proxy}\"" <<<"${hot_rendered}"; then
  echo "hot slot render did not route CLAUDE_API_PROXY_HOST to ${prod_claude_proxy}" >&2
  exit 1
fi

if ! grep -Fq "value: \"${prod_codex_proxy}\"" <<<"${hot_rendered}"; then
  echo "hot slot render did not route CODEX_API_PROXY_HOST to ${prod_codex_proxy}" >&2
  exit 1
fi

if ! grep -Fq "value: \"${prod_antigravity_proxy}\"" <<<"${hot_rendered}"; then
  echo "hot slot render did not route ANTIGRAVITY_API_PROXY_HOST to ${prod_antigravity_proxy}" >&2
  exit 1
fi

if ! grep -Fq "name: ${slot_name}-claude-oauth-ca-reflector" <<<"${warm_rendered}"; then
  echo "warm slot render does not grant a slot-scoped reader for the production proxy CA" >&2
  exit 1
fi

if ! grep -Fq "namespace: ${slot_name}-sessions" <<<"${warm_rendered}" \
  || ! grep -Fq "name: claude-oauth-ca" <<<"${warm_rendered}"; then
  echo "warm slot render does not reflect the production proxy CA into the slot sessions namespace" >&2
  exit 1
fi
