#!/usr/bin/env bash
# Shell wrapper for setup-gemini-credentials.js
set -euo pipefail
node "$(dirname "$0")/setup-gemini-credentials.js"
