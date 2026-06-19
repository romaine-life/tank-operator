#!/bin/sh

cat <<'EOF'

[tank-agent] Direct git push is disabled in Tank normal mode.

Tank owns the session branch, PR, GitHub write credential, and CI watcher.
Commits are auto-published by the post-commit hook. To retry explicitly, call
the Tank MCP publish_current_head tool for this repo.

Break-glass full-token access must be requested through the Tank MCP
request_git_break_glass approval flow; do not bypass this with raw GitHub
tokens. If approval has been granted, call request_git_break_glass again to
activate the separate tank-git-break-glass MCP server for this session/repo.

EOF
exit 1
