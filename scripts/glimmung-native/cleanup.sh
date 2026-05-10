#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

native_init

RUNNER_NAMESPACE="${TANK_NATIVE_AGENT_NAMESPACE:-glimmung-runs}"
RUN_SLUG="$(tank_run_slug)"
JOB_NAME="tank-agent-${RUN_SLUG}"
CONFIGMAP_NAME="tank-agent-config-${RUN_SLUG}"
GITHUB_SECRET_NAME="tank-agent-github-${RUN_SLUG}"
CLAUDE_CA_CONFIGMAP="tank-claude-ca-${RUN_SLUG}"

cleanup() {
  kubectl -n "$RUNNER_NAMESPACE" delete job "$JOB_NAME" --ignore-not-found=true --wait=false || true
  kubectl -n "$RUNNER_NAMESPACE" delete configmap "$CONFIGMAP_NAME" --ignore-not-found=true || true
  kubectl -n "$RUNNER_NAMESPACE" delete configmap "$CLAUDE_CA_CONFIGMAP" --ignore-not-found=true || true
  kubectl -n "$RUNNER_NAMESPACE" delete secret "$GITHUB_SECRET_NAME" --ignore-not-found=true || true
}

native_step "cleanup" cleanup

native_completed "{}"
