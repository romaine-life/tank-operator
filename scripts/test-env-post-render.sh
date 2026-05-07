#!/usr/bin/env sh
set -eu

yq eval-all '
  select(.kind != "ClusterRole" or .metadata.name != "tank-operator-terminal-invoker")
' -
