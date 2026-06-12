#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/install-agent-post-commit-reminder.sh [--force]

Installs the tracked tank-operator agent Git policy hooks as this clone's local
.git/hooks/post-commit and .git/hooks/pre-push hooks. Use --force only when
replacing existing local hooks is intentional.
EOF
}

force=0
case "${1:-}" in
  "")
    ;;
  --force)
    force=1
    ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

repo_root=$(git rev-parse --show-toplevel)
hook_src="$repo_root/.githooks/post-commit"
hook_dst=$(git rev-parse --git-path hooks/post-commit)
pre_push_src="$repo_root/.githooks/pre-push"
pre_push_dst=$(git rev-parse --git-path hooks/pre-push)

if [ ! -f "$hook_src" ]; then
  echo "missing tracked hook template: $hook_src" >&2
  exit 1
fi
if [ ! -f "$pre_push_src" ]; then
  echo "missing tracked hook template: $pre_push_src" >&2
  exit 1
fi

managed_marker='[tank-agent] Local commit created'
push_managed_marker='[tank-agent] Direct git push is disabled'

if [ -e "$hook_dst" ] && ! cmp -s "$hook_src" "$hook_dst" && [ "$force" -ne 1 ] && ! grep -Fq "$managed_marker" "$hook_dst"; then
  cat >&2 <<EOF
Refusing to replace existing local hook:
  $hook_dst

Re-run with --force if replacing it is intentional.
EOF
  exit 1
fi
if [ -e "$pre_push_dst" ] && ! cmp -s "$pre_push_src" "$pre_push_dst" && [ "$force" -ne 1 ] && ! grep -Fq "$push_managed_marker" "$pre_push_dst"; then
  cat >&2 <<EOF
Refusing to replace existing local hook:
  $pre_push_dst

Re-run with --force if replacing it is intentional.
EOF
  exit 1
fi

mkdir -p "$(dirname "$hook_dst")"
cp "$hook_src" "$hook_dst"
chmod 755 "$hook_dst"
mkdir -p "$(dirname "$pre_push_dst")"
cp "$pre_push_src" "$pre_push_dst"
chmod 755 "$pre_push_dst"

echo "Installed tank-operator agent post-commit reminder:"
echo "  $hook_dst"
echo "Installed tank-operator agent pre-push reminder:"
echo "  $pre_push_dst"
