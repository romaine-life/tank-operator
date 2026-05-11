#!/usr/bin/env sh
set -eu

slot="${1:?usage: render-test-env.sh SLOT_INDEX}"
name="tank-slot-${slot}"

helm template "$name" "$(dirname "$0")/../k8s" \
  --namespace "$name" \
  --post-renderer "$(dirname "$0")/test-env-post-render.sh" \
  --set "testEnv.enabled=true" \
  --set "goShadow.enabled=true"
