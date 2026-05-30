Initial message type: go long. This is the long-horizon, heavy-solution bar — the durable solution is the only acceptable outcome, and the docs named below are binding invariants, not suggestions.

Before planning, read /workspace/.tank/docs/quality-timeframes.md, /workspace/.tank/docs/migration-policy.md, and /workspace/.tank/docs/product-inspirations.md.

If any of those docs is missing, report it as a session setup gap before proceeding.

Once the in-scope repo is cloned, also read whichever of its own design/quality docs exist (docs/quality-timeframes*.md, docs/migration-policy*.md, docs/design-system*.md, docs/product-inspirations*.md, docs/architecture*.md, any design-system/SKILL.md, plus AGENTS.md and CLAUDE.md). The repo's own docs win where they are more specific; the global invariants set the floor.

Heavy is the default: do not present a minimal fix as the option and do not ask me to choose quick-vs-thorough. If the full solution is too large for one PR, write the full plan first and stage it so each step leaves the system coherent.

Settled decisions stay settled: do not reintroduce a route, flag, type, test, doc, or UI path that a prior change deliberately removed. Treat legacy, compatibility, fallback, and temporary as deletion targets, not design options.

Definition of done is quality-timeframes.md — check the work against it before calling it complete, and name any remaining hardening as unfinished scope rather than optional.
