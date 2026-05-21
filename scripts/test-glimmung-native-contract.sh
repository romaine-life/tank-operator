#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

cat >"${TMP_DIR}/curl" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail

: "${NATIVE_CONTRACT_CURL_CAPTURE:?set NATIVE_CONTRACT_CURL_CAPTURE}"

data=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -d)
      shift
      data="${1:-}"
      ;;
    -H|--retry|--retry-delay|-X)
      shift
      ;;
    --retry-all-errors|-fsS)
      ;;
    *)
      url="$1"
      ;;
  esac
  shift || true
done

printf '%s\n' "$url" >"${NATIVE_CONTRACT_CURL_CAPTURE}.url"
printf '%s\n' "$data" >"${NATIVE_CONTRACT_CURL_CAPTURE}.body"
SH
chmod +x "${TMP_DIR}/curl"

unset GLIMMUNG_FAILED_URL
export GLIMMUNG_ATTEMPT_TOKEN="contract-token"
export GLIMMUNG_EVENTS_URL="http://glimmung.test/v1/run-callbacks/cb/native/events"
export GLIMMUNG_COMPLETED_URL="http://glimmung.test/v1/run-callbacks/cb/native/completed"
export GLIMMUNG_GITHUB_TOKEN_URL="http://glimmung.test/v1/run-callbacks/cb/native/github-token"
export GLIMMUNG_JOB_ID="prepare"
export GLIMMUNG_RUN_ID="run-1"
export NATIVE_CONTRACT_CURL_CAPTURE="${TMP_DIR}/native-failed"
export PATH="${TMP_DIR}:${PATH}"

# shellcheck source=glimmung-native/lib.sh
source "${SCRIPT_DIR}/glimmung-native/lib.sh"

native_init
native_failed "contract failure"

if [ "$(cat "${NATIVE_CONTRACT_CURL_CAPTURE}.url")" != "$GLIMMUNG_COMPLETED_URL" ]; then
  echo "native_failed must post to GLIMMUNG_COMPLETED_URL" >&2
  exit 1
fi

jq -e '
  .conclusion == "failure"
  and .job_id == "prepare"
  and .summary_markdown == "contract failure"
' "${NATIVE_CONTRACT_CURL_CAPTURE}.body" >/dev/null

export GLIMMUNG_MANAGED_RUNNER=1
export GLIMMUNG_OUTPUT_FILE="${TMP_DIR}/managed-output.jsonl"
export GLIMMUNG_COMPLETION_FILE="${TMP_DIR}/managed-completion.json"
rm -f "$GLIMMUNG_OUTPUT_FILE" "$GLIMMUNG_COMPLETION_FILE" "${NATIVE_CONTRACT_CURL_CAPTURE}.url" "${NATIVE_CONTRACT_CURL_CAPTURE}.body"

native_completed \
  '{"validation_url":"https://operator.example"}' \
  '{"status":"pass","reasons":["ok"]}' \
  'managed summary'

jq -e '.validation_url == "https://operator.example"' "$GLIMMUNG_OUTPUT_FILE" >/dev/null
jq -e '
  .verification.status == "pass"
  and .summary_markdown == "managed summary"
' "$GLIMMUNG_COMPLETION_FILE" >/dev/null

if [ -e "${NATIVE_CONTRACT_CURL_CAPTURE}.url" ]; then
  echo "managed native_completed must not post callbacks" >&2
  exit 1
fi

native_failed "managed failure"
jq -e '.summary_markdown == "managed failure"' "$GLIMMUNG_COMPLETION_FILE" >/dev/null
