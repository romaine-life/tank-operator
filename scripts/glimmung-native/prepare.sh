#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

native_init

VALIDATION_URL="$(tank_validation_url)"

check_styleguide() {
  curl -fsS --retry 5 --retry-delay 2 "${VALIDATION_URL}/_styleguide" >/dev/null
}

emit_env_outputs() {
  jq -nc --arg validation_url "$VALIDATION_URL" '{validation_url: $validation_url}' \
    >/tmp/tank-env-outputs.json
  cat /tmp/tank-env-outputs.json
}

native_step "check-styleguide" check_styleguide
native_step "emit-env-outputs" emit_env_outputs

native_completed "$(cat /tmp/tank-env-outputs.json)"
