#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
Usage: scripts/install-agent-post-commit-reminder.sh [--force]

Installs the tracked tank-operator agent reminder as this clone's local
.git/hooks/post-commit hook. Use --force only when replacing an existing local
post-commit hook is intentional.
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

if [ ! -f "$hook_src" ]; then
  echo "missing tracked hook template: $hook_src" >&2
  exit 1
fi

if [ -e "$hook_dst" ] && ! cmp -s "$hook_src" "$hook_dst" && [ "$force" -ne 1 ]; then
  cat >&2 <<EOF
Refusing to replace existing local hook:
  $hook_dst

Re-run with --force if replacing it is intentional.
EOF
  exit 1
fi

mkdir -p "$(dirname "$hook_dst")"
cp "$hook_src" "$hook_dst"
chmod 755 "$hook_dst"

echo "Installed tank-operator agent post-commit reminder:"
echo "  $hook_dst"
