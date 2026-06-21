#!/bin/sh
# Tank agent pre-push hook (POSIX sh, no bashisms).
#
# The agent-egress proxy (the wall) is the GitHub boundary for every Tank
# session, so the in-pod git/gh wrappers hand the wall the raw
# auth.romaine.life token and the wall governs pushes server-side (rejecting
# pushes to main and merges for restricted sessions). In egress-proxy mode this
# hook is NOT installed at all (repo-cloner / install-agent-git-template skip
# it) — the wall is the only push policy. This template remains only as a
# defensive block for any worktree where it was installed without the wall: it
# fails the push rather than letting a direct, ungoverned push proceed, and
# there is no longer an in-pod break-glass broker (token-mint / push) to
# consult.

cat <<'EOF'

[tank-agent] Direct git push is disabled outside the agent-egress proxy.

Tank routes every session's GitHub traffic through the agent-egress proxy (the
wall), which mints the right-scoped credential server-side, records the push,
and — for restricted sessions — rejects pushes to main and merges. With the
wall enabled, `git push` and `gh pr create|edit|ready|comment` just work; there
is no in-pod token to hand the shell.

Break-glass elevation (e.g. to push arbitrary branches / use the full GitHub
API) is requested through the Tank MCP request_git_break_glass approval flow;
do not bypass this with raw GitHub tokens.

EOF
exit 1
