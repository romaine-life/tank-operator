#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

native_init

VALIDATION_URL="${GLIMMUNG_INPUT_VALIDATION_URL:-$(tank_validation_url)}"
BRANCH_NAME="${GLIMMUNG_INPUT_BRANCH_NAME:-}"

check_styleguide() {
  curl -fsS --retry 5 --retry-delay 2 "${VALIDATION_URL}/_styleguide" >/dev/null
}

emit_verification() {
  jq -nc \
    --arg validation_url "$VALIDATION_URL" \
    --arg branch_name "$BRANCH_NAME" \
    '{
      status: "pass",
      reasons: ["Live Tank styleguide route responded successfully."],
      validation_url: $validation_url,
      branch_name: $branch_name
    }' >/tmp/tank-verification.json
  cat /tmp/tank-verification.json
}

native_step "check-styleguide" check_styleguide
native_step "emit" emit_verification

native_completed "$(jq -nc --argjson verification "$(cat /tmp/tank-verification.json)" '{verification: $verification}')" "$(cat /tmp/tank-verification.json)"
