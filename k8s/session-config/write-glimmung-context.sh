#!/bin/bash
# Idempotently materialize attended-pickup context inside a session pod.
#
# This runs at pod start so automation can verify context before the browser
# terminal opens. The interactive bootstrap also calls it for older pods whose
# container command predates this script.

if [ -z "${TANK_GLIMMUNG_CONTEXT_JSON:-}" ]; then
  exit 0
fi

cat > /workspace/GLIMMUNG_CONTEXT.json <<EOF
${TANK_GLIMMUNG_CONTEXT_JSON}
EOF

cat > /workspace/GLIMMUNG_CONTEXT.md <<EOF
# Glimmung Context

This session was launched from glimmung for an attended pickup.

- Run ref: ${TANK_GLIMMUNG_RUN_REF:-}
- Issue ref: ${TANK_GLIMMUNG_ISSUE_REF:-}
- Touchpoint ref: ${TANK_GLIMMUNG_TOUCHPOINT_REF:-}
- Validation URL: ${TANK_GLIMMUNG_VALIDATION_URL:-}

Use the glimmung MCP server to read the canonical Issue, Run, PR, graph,
comments, reviews, and signals before making changes. Treat GitHub as a
syndication surface when glimmung has the richer record.
EOF

if [ -w /workspace/CLAUDE.md ] && ! grep -q "## Glimmung attended pickup" /workspace/CLAUDE.md; then
  cat >> /workspace/CLAUDE.md <<'EOF'

## Glimmung attended pickup

This pod was launched from glimmung. Read `/workspace/GLIMMUNG_CONTEXT.md`
first, then use the glimmung MCP server to fetch the canonical Issue / Run /
PR state before acting.
EOF
fi
