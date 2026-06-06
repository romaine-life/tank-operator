#!/usr/bin/env bash
set -Eeuo pipefail

slot_name="${1:-tank-operator-slot-2}"
expected_claude="${slot_name}-claude-code-credentials"
expected_codex="${slot_name}-codex-credentials"
expected_antigravity="${slot_name}-antigravity-credentials"

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

if ! grep -Fq "value: \"${expected_claude}\"" <<<"${hot_rendered}"; then
  echo "hot slot render did not set CLAUDE_CREDENTIALS_KV_KEY to ${expected_claude}" >&2
  exit 1
fi

if ! grep -Fq "key: ${expected_claude}" <<<"${warm_rendered}"; then
  echo "warm slot ExternalSecret did not read Claude credentials from KV key ${expected_claude}" >&2
  exit 1
fi

if grep -A1 -F "name: CLAUDE_CREDENTIALS_KV_KEY" <<<"${hot_rendered}" \
  | grep -Fq 'value: "claude-code-credentials"'; then
  echo "hot slot render still points a Claude proxy/config env at production claude-code-credentials" >&2
  exit 1
fi

if ! grep -Fq "value: \"${expected_codex}\"" <<<"${hot_rendered}"; then
  echo "hot slot render did not set CODEX_CREDENTIALS_KV_KEY to ${expected_codex}" >&2
  exit 1
fi

if ! grep -Fq "key: ${expected_codex}" <<<"${warm_rendered}"; then
  echo "warm slot ExternalSecret did not read Codex credentials from KV key ${expected_codex}" >&2
  exit 1
fi

if grep -A1 -F "name: CODEX_CREDENTIALS_KV_KEY" <<<"${hot_rendered}" \
  | grep -Fq 'value: "codex-credentials"'; then
  echo "hot slot render still points a Codex proxy/config env at production codex-credentials" >&2
  exit 1
fi

if ! grep -Fq "value: \"${expected_antigravity}\"" <<<"${hot_rendered}"; then
  echo "hot slot render did not set ANTIGRAVITY_CREDENTIALS_KV_KEY to ${expected_antigravity}" >&2
  exit 1
fi

if grep -A1 -F "name: ANTIGRAVITY_CREDENTIALS_KV_KEY" <<<"${hot_rendered}" \
  | grep -Fq 'value: "antigravity-credentials"'; then
  echo "hot slot render still points an Antigravity config env at production antigravity-credentials" >&2
  exit 1
fi
