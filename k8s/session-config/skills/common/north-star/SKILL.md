---
name: north-star
description: Re-assert the long-horizon quality bar — read the binding policy/inspiration docs and the repo's own design docs, then go deep and stop cutting corners
---

# /north-star — Invoke the long-horizon standard

When the user invokes `/north-star` (or `$north-star`), they are deliberately
raising the bar for this work. This is the "go long" / ban-hammer signal: the
durable solution is the only acceptable outcome, and the docs named below are
binding invariants, not suggestions.

Use it in two situations:

1. **Starting substantial work** — the user wants the heavy, long-term design,
   not a minimal patch.
2. **An agent has drifted** — it offered a quick-vs-thorough choice, started
   cutting corners, or reintroduced something a previous change deliberately
   removed. This is the re-assertion that those decisions are settled.

## Do this now

1. **Read the binding invariants** (materialized in every session):
   - `/workspace/.tank/docs/quality-timeframes.md` — the quality bar and the
     definition of done.
   - `/workspace/.tank/docs/migration-policy.md` — old paths get deleted end to
     end; compatibility layers are prohibited.
   - `/workspace/.tank/docs/product-inspirations.md` — the taste document:
     borrow primitives, not boundaries.

   If any of these is missing, report it as a session setup gap before
   proceeding — do not silently continue without the invariant.

2. **Read the repo's own design/quality docs**, once the in-scope repo is
   cloned. Look for and read whichever of these exist:
   - `docs/quality-timeframes*.md`, `docs/migration-policy*.md`,
     `docs/design-system*.md`, `docs/product-inspirations*.md`,
     `docs/architecture*.md`, and any `design-system/SKILL.md`
   - `AGENTS.md`, `CLAUDE.md`
   The repo's own docs win where they are more specific; the global invariants
   set the floor.

## Then hold this standard

- **Heavy is the default.** Do not present a minimal fix as the option, and do
  not ask the user to choose quick-vs-thorough. Assume thorough. If the full
  solution is too large for one PR, write the full plan first and stage it so
  each step leaves the system coherent.
- **Settled decisions stay settled.** Do not reintroduce a route, flag, type,
  test, doc, or UI path that a prior change removed. Treat `legacy`,
  `compatibility`, `fallback`, and `temporary` as deletion targets, not design
  options.
- **Durable state over optimism.** Prefer durable models, settled contracts,
  observable systems, and migration guards over local convenience.
- **Definition of done is the docs above.** Before calling work complete, check
  it against `quality-timeframes.md`. If something is unfinished, name it as
  unfinished scope — do not frame remaining hardening as optional.

Carry this standard for the rest of the session unless the user explicitly
downgrades it.
