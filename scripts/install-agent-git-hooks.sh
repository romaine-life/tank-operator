#!/bin/sh
set -eu

repo_root=$(git rev-parse --show-toplevel)
hooks_path="$repo_root/.githooks"

if [ ! -f "$hooks_path/post-commit" ]; then
  echo "missing tracked post-commit hook at $hooks_path/post-commit" >&2
  exit 1
fi

chmod +x "$hooks_path/post-commit"
git config core.hooksPath "$hooks_path"

echo "Installed tracked agent git hooks: core.hooksPath=$hooks_path"
