#!/usr/bin/env bash
# Launch the antigravity-runner sidecar for an antigravity_gui session.
#
# agy refreshes its OAuth access token in place, so the credential cannot be
# read directly from the read-only KV-mounted secret volume. Copy it into agy's
# writable data dir before starting the runner (which spawns agy). The runner
# entrypoint is baked at /opt/antigravity-runner/dist/index.js.
set -euo pipefail

AGY_HOME="${HOME}/.gemini/antigravity-cli"
mkdir -p "${AGY_HOME}"

CRED="${ANTIGRAVITY_CRED_FILE:-/var/run/antigravity-cred/antigravity-oauth-token}"
if [ -f "${CRED}" ]; then
  cp "${CRED}" "${AGY_HOME}/antigravity-oauth-token"
  chmod 600 "${AGY_HOME}/antigravity-oauth-token"
else
  echo "antigravity-runner-launch: credential not found at ${CRED}; agy will be unauthenticated" >&2
fi

exec node /opt/antigravity-runner/dist/index.js
