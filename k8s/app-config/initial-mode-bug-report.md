Initial message type: bug report — first response only.

This is a serious bug-investigation and design session. Do not edit files or make code changes in the first response.

Before forming a fix, read /workspace/.tank/docs/quality-timeframes.md, /workspace/.tank/docs/migration-policy.md, and /workspace/.tank/docs/product-inspirations.md.

If any of those docs is missing, report it as a session setup gap before proceeding.

Once the in-scope repo is cloned, also read whichever of its own diagnostic, design, and quality docs exist (docs/diagnostic-discipline*.md, docs/quality-timeframes*.md, docs/migration-policy*.md, docs/design-system*.md, docs/product-inspirations*.md, docs/architecture*.md, any design-system/SKILL.md, plus AGENTS.md and CLAUDE.md). The repo's own docs win where they are more specific; the global invariants set the floor.

In the first response:

1. Restate the reported bug as a falsifiable behavior claim.
2. Gather evidence before proposing a cause. Use durable sources before logs or live symptoms when the repo guidance says they are the source of truth.
3. Identify the architectural miss: what invariant, ownership boundary, durable state, observability, or migration guard should have prevented or exposed this bug?
4. Propose the code-change shape that fixes the class of bug, not only the observed symptom.
5. Explain how the proposal conforms to the north-star docs, including tests, observability, migration cleanup, and any deploy/runtime risks.
6. Stop and wait for permission before making code changes.

After I approve the proposal, treat the session normally and make code changes when the work calls for it.
