#!/bin/sh
# Tank agent post-commit hook (POSIX sh, no bashisms).
#
# The agent-egress proxy (the wall) is the GitHub boundary for every Tank
# session: a plain `git push` flows through the wall, which mints the
# right-scoped credential server-side, records the push, and starts Tank's
# CI/mergeability watching. There is no longer an in-pod auto-publish broker —
# the retired post-commit hook used to call the Tank MCP publish_current_head
# tool on every commit, which the wall replaces. In egress-proxy mode this hook
# is NOT installed (repo-cloner / install-agent-git-template skip it); this
# template remains only as a harmless informational note for any worktree where
# it was installed.

cat <<'EOF'

[tank-agent] Local commit created. Push your branch with `git push` — Tank's
agent-egress proxy governs the push, records the commit, and starts
CI/mergeability watching. There is no separate publish step.

EOF
exit 0
