#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

native_init
native_require_env GLIMMUNG_ISSUE_NUMBER GLIMMUNG_ISSUE_TITLE

REPO_SLUG="${TANK_REPO_SLUG:-romaine-life/tank-operator}"
REPO_DIR="${TANK_REPO_DIR:-/workspace/tank-operator}"
RUNNER_NAMESPACE="${TANK_NATIVE_AGENT_NAMESPACE:-glimmung-runs}"
CLAUDE_NAMESPACE="${CLAUDE_NAMESPACE:-tank-operator}"
CLAUDE_CA_NAMESPACE="${CLAUDE_CA_NAMESPACE:-tank-operator-sessions}"
CLAUDE_CONTAINER_TAG="${CLAUDE_CONTAINER_TAG:-latest}"
VALIDATION_URL="${GLIMMUNG_INPUT_VALIDATION_URL:-$(tank_validation_url)}"
RUN_SLUG="$(tank_run_slug)"
JOB_NAME="tank-agent-${RUN_SLUG}"
CONFIGMAP_NAME="tank-agent-config-${RUN_SLUG}"
GITHUB_SECRET_NAME="tank-agent-github-${RUN_SLUG}"
CLAUDE_CA_CONFIGMAP="tank-claude-ca-${RUN_SLUG}"
BRANCH_NAME="glimmung/${GLIMMUNG_RUN_ID}"
ISSUE_URL="https://github.com/${REPO_SLUG}/issues/${GLIMMUNG_ISSUE_NUMBER}"

prepare() {
  local token auth_header proxy_ip
  token="$(native_github_token)"
  auth_header="$(native_git_auth_header "$token")"
  git -C "$REPO_DIR" config user.name "tank-operator-agent[bot]"
  git -C "$REPO_DIR" config user.email "tank-operator-agent@romaine.life"

  proxy_ip="$(kubectl -n "$CLAUDE_NAMESPACE" get svc claude-api-proxy -o jsonpath='{.spec.clusterIP}')"
  if [ -z "$proxy_ip" ]; then
    echo "claude-api-proxy Service not found in ${CLAUDE_NAMESPACE}" >&2
    return 1
  fi
  printf '%s' "$proxy_ip" >/tmp/tank-proxy-ip

  kubectl -n "$CLAUDE_CA_NAMESPACE" get configmap claude-oauth-ca -o json \
    | RUNNER_NAMESPACE="$RUNNER_NAMESPACE" CLAUDE_CA_CONFIGMAP="$CLAUDE_CA_CONFIGMAP" jq '
        del(
          .metadata.annotations,
          .metadata.uid,
          .metadata.resourceVersion,
          .metadata.generation,
          .metadata.creationTimestamp,
          .metadata.managedFields
        )
        | .metadata.name = env.CLAUDE_CA_CONFIGMAP
        | .metadata.namespace = env.RUNNER_NAMESPACE
      ' \
    | kubectl apply -f -

  kubectl -n "$RUNNER_NAMESPACE" create configmap "$CONFIGMAP_NAME" \
    --from-file=prompt.md="${REPO_DIR}/.github/agent/prompt.md" \
    --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n "$RUNNER_NAMESPACE" create secret generic "$GITHUB_SECRET_NAME" \
    --from-literal=token="$token" \
    --dry-run=client -o yaml | kubectl apply -f -

  printf '%s' "$auth_header" >/tmp/tank-auth-header
}

apply_agent_job() {
  local proxy_ip
  proxy_ip="$(cat /tmp/tank-proxy-ip)"
  kubectl apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: ${JOB_NAME}
  namespace: ${RUNNER_NAMESPACE}
  labels:
    app.kubernetes.io/name: tank-operator-native-agent
    glimmung.romaine.life/run-id: ${RUN_SLUG}
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 1800
  template:
    metadata:
      labels:
        app.kubernetes.io/name: tank-operator-native-agent
        glimmung.romaine.life/run-id: ${RUN_SLUG}
    spec:
      restartPolicy: Never
      hostAliases:
        - ip: ${proxy_ip}
          hostnames:
            - api.anthropic.com
      volumes:
        - name: claude-ca
          configMap:
            name: ${CLAUDE_CA_CONFIGMAP}
            items:
              - key: ca.crt
                path: ca.crt
        - name: workspace
          emptyDir: {}
        - name: agent-config
          configMap:
            name: ${CONFIGMAP_NAME}
      containers:
        - name: agent
          image: romainecr.azurecr.io/claude-container:${CLAUDE_CONTAINER_TAG}
          imagePullPolicy: IfNotPresent
          env:
            - name: NODE_EXTRA_CA_CERTS
              value: /etc/claude-ca/ca.crt
            - name: HOME
              value: /workspace
            - name: ISSUE_NUMBER
              value: "${GLIMMUNG_ISSUE_NUMBER}"
            - name: ISSUE_TITLE
              value: "${GLIMMUNG_ISSUE_TITLE}"
            - name: ISSUE_URL
              value: "${ISSUE_URL}"
            - name: VALIDATION_URL
              value: "${VALIDATION_URL}"
            - name: BRANCH_NAME
              value: "${BRANCH_NAME}"
            - name: REPO_SLUG
              value: "${REPO_SLUG}"
            - name: GH_TOKEN
              valueFrom:
                secretKeyRef:
                  name: ${GITHUB_SECRET_NAME}
                  key: token
            - name: CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC
              value: "1"
          volumeMounts:
            - name: claude-ca
              mountPath: /etc/claude-ca
              readOnly: true
            - name: workspace
              mountPath: /workspace
            - name: agent-config
              mountPath: /agent-config
              readOnly: true
          command:
            - /bin/bash
            - -lc
            - |
              set -Eeuo pipefail
              mkdir -p "\$HOME/.claude"
              printf '%s\n' \
                '{' \
                '  "claudeAiOauth": {' \
                '    "accessToken": "managed-by-tank-operator",' \
                '    "refreshToken": "managed-by-tank-operator",' \
                '    "expiresAt": 9999999999000,' \
                '    "scopes": ["user:inference", "user:profile"],' \
                '    "subscriptionType": "max",' \
                '    "rateLimitTier": "max"' \
                '  }' \
                '}' \
                > "\$HOME/.claude/.credentials.json"
              chmod 600 "\$HOME/.claude/.credentials.json"
              printf '%s\n' '{"theme":"dark","permissions":{"defaultMode":"bypassPermissions"},"skipDangerousModePermissionPrompt":true}' > "\$HOME/.claude/settings.json"
              printf '%s\n' \
                '{' \
                '  "hasCompletedOnboarding": true,' \
                '  "officialMarketplaceAutoInstallAttempted": true,' \
                '  "officialMarketplaceAutoInstalled": true,' \
                '  "projects": {' \
                '    "/workspace/repo": {' \
                '      "allowedTools": [],' \
                '      "hasTrustDialogAccepted": true,' \
                '      "projectOnboardingSeenCount": 1' \
                '    }' \
                '  }' \
                '}' \
                > "\$HOME/.claude.json"
              git config --global user.name "tank-operator-agent[bot]"
              git config --global user.email "tank-operator-agent@romaine.life"
              git clone "https://x-access-token:\${GH_TOKEN}@github.com/\${REPO_SLUG}.git" /workspace/repo
              cd /workspace/repo
              git checkout -B "\${BRANCH_NAME}"
              printf '# Issue #%s: %s\nURL: %s\nValidation URL: %s\n' "\${ISSUE_NUMBER}" "\${ISSUE_TITLE}" "\${ISSUE_URL}" "\${VALIDATION_URL}" > /tmp/issue-context.md
              cat /agent-config/prompt.md /tmp/issue-context.md > /tmp/agent-input.md
              claude --print --dangerously-skip-permissions < /tmp/agent-input.md 2>&1 | tee /tmp/claude-stream.log
              git add -A
              if git diff --cached --quiet; then
                echo "agent produced no changes; failing job so Glimmung does not open an empty PR" >&2
                exit 1
              fi
              git commit -m "agent: address issue #\${ISSUE_NUMBER}" -m "\${ISSUE_TITLE}" -m "Closes #\${ISSUE_NUMBER}"
              git push origin "HEAD:\${BRANCH_NAME}"
EOF
}

wait_agent_job() {
  local pod=""
  for _ in $(seq 1 60); do
    pod="$(kubectl -n "$RUNNER_NAMESPACE" get pods -l "job-name=${JOB_NAME}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
    [ -n "$pod" ] && break
    sleep 2
  done
  if [ -z "$pod" ]; then
    echo "Job pod never appeared" >&2
    return 1
  fi
  kubectl -n "$RUNNER_NAMESPACE" logs -f "$pod" || true
  kubectl -n "$RUNNER_NAMESPACE" wait --for=condition=complete --timeout=35m "job/${JOB_NAME}" && return 0
  kubectl -n "$RUNNER_NAMESPACE" wait --for=condition=failed --timeout=10s "job/${JOB_NAME}" || true
  return 1
}

emit_outputs() {
  jq -nc \
    --arg branch_name "$BRANCH_NAME" \
    --arg job_name "$JOB_NAME" \
    '{branch_name: $branch_name, implementation: {status: "pushed", job_name: $job_name}}' \
    >/tmp/tank-implementation-outputs.json
  cat /tmp/tank-implementation-outputs.json
}

run_agent() {
  apply_agent_job
  wait_agent_job
}

native_step "prepare" prepare
native_step "run-agent" run_agent
native_step "emit" emit_outputs

native_completed "$(cat /tmp/tank-implementation-outputs.json)"
