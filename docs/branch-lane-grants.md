# Branch Lane Grants — unifying break-glass git and PR lanes

> **Superseded (in-pod brokers retired).** The in-pod governed broker HTTP
> routes (`push-head` / `pr-write` / `mint-git-token`) and their
> `tank_break_glass_push_total` / `tank_break_glass_pr_open_total` /
> `tank_break_glass_pr_write_total` counters described below were **removed**
> once the agent-egress proxy (the wall) became the GitHub boundary for every
> session. The wall now mints the scoped credential server-side and governs the
> push / PR write directly — so under an approved grant a plain `git push` /
> `gh pr create|edit|ready|comment` "just works", with no in-pod sidecar
> brokering. The break-glass *grant* model in this doc (request → admin approval
> → scope-bounded, audited writes) is unchanged; only the in-pod delivery
> mechanism moved to the wall. The `github.break_glass.*` durable audit ledger
> and the `request_git_break_glass` MCP tool remain. The sections below are
> retained as the design record of the retired broker routes.

Status: complete (Stages 0–3). Stage 0 (plan) and Stage 1 (unified model +
server-side brokering primitives) landed first; Stage 2 was the atomic cutover —
the governed push / PR-write broker routes went live through the pre-push hook
and `gh` wrapper, and the PR-lane mechanism (`request_pr_lane` / `create_pr_lane`
/ `github.pr_lane.*`) is retired end-to-end with a reintroduction guard. Stage 3
completed the observability: per-result counters for the brokered paths, a
retired-path guard counter, the `tank-operator.branch-lane` alerts, and dashboard
panels — on top of the durable `github.break_glass.*` audit ledger that records
every brokered op (its accept-list admits `github.break_glass.pr_write`, so PR-own
audits land rather than being silently dropped). The per-result broker counters
and broker routes were later retired with the in-pod brokers (see the Superseded
note above); the audit ledger and retired-path guard counter remain.

## Problem

A restricted (`TANK_RESTRICTED_GIT=true`) session has two separate, parallel
mechanisms for letting an agent do governed write work on a branch:

1. **Break-glass git** — `request_git_break_glass` → grant → `push_current_head`
   / `mint_full_git_token` / gh-git wrapper auto-elevation.
2. **PR lanes** — `request_pr_lane` → approve → `create_pr_lane` (creates a
   branch + draft PR).

They share the same scope model (`repoScope`, `branchScope`) and the same
approval UI, but they are otherwise disjoint event families
(`github.break_glass.*` vs `github.pr_lane.*`), tools, routes, and handlers.

This split produces the core defect:

- A **branch-scoped** break-glass grant lets the agent **push a branch but not
  open a PR for it**. `push_current_head` only pushes; `gh pr create` stays
  read-only (the wrapper only elevates for `unlimited` + `full_github_api`);
  mcp-github `create_pull_request` is on `_GITHUB_WRITE_TOOL_DENYLIST` in
  restricted mode regardless of grant. The PR half lives in the *other*
  mechanism (`create_pr_lane`), which the agent has no signal to use.
- So an agent reaching for the obvious-sounding `request_git_break_glass`,
  picking a branch scope (the least-privilege instinct), gets approved, pushes,
  and is **stranded with commits and no PR** — and the failure is silent.

To everyone except the original implementation, "permission to push a branch"
and "permission to open that branch's PR" are the same thing.

## Concept

**A grant is permission to do work on a branch (or N branches) in a repo —
whether the branch exists yet is Tank's problem, not the agent's.**

One grant covers the whole life of that branch's work:

- create the branch,
- push it (fast-forward — force-push needs an `unlimited` grant),
- open its draft PR and own that PR (edit title/body, mark ready, comment),

through review. **Scope (`named` / `count` / `unlimited`) bounds *which*
branches; the one capability it gates is history rewrite — `named`/`count` grants
push *fast-forward only*, while `unlimited` (a native token) can force-push. Push
and PR-open work at every scope.** `unlimited` is the widest version — the
whole-repo / full-GitHub-API escape hatch — not a precondition for basic branch
work. Scoped grants don't need force-push in practice: with squash-merge, branch
history is disposable, so forward-fix (a new commit) and `git merge` replace
amend/rebase.

Merge-to-base stays the separate, CI-gated step (`merge_current_session_pr` /
the existing governed merge). A branch lane gets work *to* review, not *through*
it.

## The constraint that shaped the old design (kept, but hidden)

GitHub installation tokens are scoped by **repository + permission**, never by
branch. There is no "push only to branch X" token. So a branch-scoped grant
cannot be honored by handing a raw token to the shell — that token would permit
pushing every branch and editing every PR, violating the very scope that was
approved.

The resolution is unchanged and correct: **Tank brokers the writes server-side
and enforces the branch scope itself**, never exposing a raw token for a scoped
grant. A consequence of brokering server-side is that a scoped push is
**fast-forward only** — rewriting history (force-push) needs the native token an
`unlimited` grant mints. What this redesign deletes is not that enforcement — it is the agent
having to *see* it, *choose* between `push_current_head` / `publish_current_head`
/ `create_pr_lane` / raw git, and *guess* a scope that secretly decides whether
anything works.

## Durable data model

The unified grant rides the **existing `github.break_glass.*` event family**. A
full rename to a `github.branch_lane.*` taxonomy was considered but deferred:
renaming the durable action strings is its own ledger migration (it orphans live
`github.break_glass.grant` rows), and the agent-facing tool stays
`request_git_break_glass`, so the rename is a clean, separable follow-up rather
than scope inside this change. The events the brokering uses:

- `github.break_glass.request` — agent asks to work on a branch (reason, repo
  scope, optional branch hint).
- `github.break_glass.grant` — human (or policy) approves; carries `repo_scope`,
  `branch_scope`, `operations`, TTL.
- `github.break_glass.push` — a brokered governed push (audit).
- `github.break_glass.pr_write` — a brokered PR edit/ready/comment (audit).
- `github.pull_request.open` — a brokered PR open (audit; existing namespace,
  shared with the session-branch PR provisioning).

`operations` is the explicit, audited capability set: `push_current_head`,
`mint_full_git_token`, and `full_github_api` (the whole-repo GitHub API write —
present **only** on `unlimited` grants). `repoScope` / `branchScope` structs are
reused unchanged. The audit-ledger write pattern is **kept** — every brokered
operation records a `github.break_glass.*` control action; only the *capability*
(push + PR-own for any scope) widened.

## Agent surface — one tool, one flow

```
request_git_break_glass(reason, repo_scope?, branch hint?)   # one call
  → human approves in Tank                                   # one approval
  → git push / git push -f / gh pr create|edit|ready|comment # just work
```

After approval the agent just uses git/gh:

- `git push` / `git push -f` for an in-scope branch → the pre-push hook performs
  a governed push (creating the branch if absent).
- `gh pr create|edit|ready|comment` → the `gh` wrapper brokers the PR write.

There is **no second `request_*` call and no MCP-registry reload**: the
`/push-head` and `/pr-write` routes authorize off the durable grant directly
(exactly like `/mint-git-token`), so they go live the instant the grant is
approved. The branch and its draft PR are created lazily by the agent's own
first `git push` / `gh pr create` — the agent never names `push_current_head`,
`publish_current_head`, `create_pr_lane`, `mint_full_git_token`, or a branch
scope it has to reason about. It says *why*; the human decides *how much*; the
wrapper/hooks do the plumbing.

## Server-side enforcement (the brokering)

- **`git push` / `git push -f`** → the pre-push hook performs a governed push for
  any branch in the lane scope (creating it if absent), instead of `exit 1`.
  Scope checked server-side. (Extends `push_current_head` with create-if-absent;
  the wall governs the normal session-branch `git push` — the `publish_current_head`
  auto-publish it once relied on was retired when the wall became the one
  governed egress path.)
- **`gh pr create|edit|ready|comment`, issue comments on the PR** → the `gh`
  wrapper routes through a new governed PR-write endpoint that resolves the PR to
  its head branch, verifies head ∈ lane scope, performs the write with Tank's
  credential, and audits it. No raw token, no denylist wall.
- **`unlimited`** grants still additionally surface the whole-repo API (the
  `full_api` operation) for the rare "I need everything" case.

## Migration (delete end to end — no compat, atomic cutover)

Per `docs/migration-policy.md`: the old path is deleted, not wrapped.

**RETIRE** (no live route, tool, event, UI, or test may remain):
- `request_pr_lane`, `create_pr_lane` MCP tools + handlers + routes.
- `github.pr_lane.*` event family and its reader/writer/auto-approval logic.
- The "scoped grant returns `{"active":false}` to the wrapper / `full_github_api`
  only on `unlimited`" split that makes scoped grants useless for branch work.
- Old break-glass/PR-lane tests pinning the retired behavior.
- The separate PR-lane approval UI surface.

**KEEP** (unchanged):
- The control-action audit ledger (new event names only).
- The `unlimited` / `full_api` whole-repo escape hatch.
- Server-side branch-scope enforcement.
- The normal session-branch push path — now governed by the wall (plain
  `git push`); the `publish_current_head` post-commit auto-publish it replaced
  is retired.
- `_GITHUB_WRITE_TOOL_DENYLIST` for restricted mode (raw mcp-github writes stay
  off; writes flow through the governed brokering).

**GUARDS**: `scripts/check-*` fails CI if `request_pr_lane`, `create_pr_lane`, or
`github.pr_lane.` reappear in live code (mirrors
`scripts/check-removed-chat-runtime.mjs`).

## Stages (every stage required for "done")

**Stage 0 — this document.** The full plan, written first.

**Stage 1 — unified model + brokering primitives, inactive infra.**
- Extend the existing `github.break_glass.*` grant + add the
  `github.break_glass.pr_write` audit subtype (no new event taxonomy; the rename
  to `branch_lane.*` is a deferred follow-up).
- mcp-auth-proxy: governed PR-write endpoint (resolve PR→head, enforce scope,
  broker, audit) + create-if-absent governed push.
- Unit tests for the new model and enforcement.
- Built but **not yet wired** to the agent-facing tool (a schema/infra step that
  is inactive until the complete path is ready — allowed chunking, no compat).

**Stage 2 — atomic cutover.** Old dies, new goes live, in one coherent change:
- `request_git_break_glass` grants a branch lane (provisions the draft PR on
  approval; enables scoped push + PR-own).
- Pre-push hook → governed push for in-scope branches; `gh` wrapper → governed
  PR-write; approval writes activation state (kills the second call + reload).
- DELETE the retired tools/events/UI/tests/split listed above; add reintro
  guards.
- Unify `AdminBreakGlassPanel` (+ the PR-lane menu) into one branch-lane approval
  panel.
- Rewrite the just-in-time messages (approval prompt, pre-push hook stderr, tool
  description) and the CLAUDE.md git-write decision tree.
- Update `docs/features/session-lifecycle/capabilities.md` + `contract.md`.
- Core counters (below).

**Stage 3 — observability completion + hardening.**
- Grafana panels + PrometheusRule alerts for branch-lane outcomes and a "retired
  path used again" alert.
- Final hardening pass; cost/scale note for the brokered-PR path.

## Observability (as built)

Brokered-path counters, emitted by the mcp-auth-proxy sidecar (the whole family
is named `tank_break_glass_*` regardless of emitting service so it dashboards and
alerts together):

- `tank_break_glass_push_total{result}` — `/push-head` governed push.
- `tank_break_glass_pr_open_total{result}` — `/pr-write` draft-PR open.
- `tank_break_glass_pr_write_total{result}` — `/pr-write` PR-own write
  (edit/ready/comment).

`result` is `succeeded`, `error` (a genuine 500 broker fault), or a bounded
refusal reason (`no_grant`, `branch_out_of_scope`, `dirty`, …). Because a refusal
writes **no** control-action ledger row, these counters are the only signal for
the scope boundary firing (`branch_out_of_scope`) and the no-grant wall.

Orchestrator counter:

- `tank_break_glass_retired_path_total` — guard counter; any increment means a
  retired `github.pr_lane.*` action reached the durable ledger and is a counted
  migration bug. Label-less (the rejected action string is caller-controlled, so
  it must never become a metric label).

Grant lifecycle is observed through the **existing**
`tank_control_action_events_total{action="github.break_glass.grant|request|deny",status}`;
the original sketch's dedicated `tank_break_glass_grant_total` was dropped because
it would only duplicate that series (the repo's metric discipline rejects
redundant counters).

Alerts (`tank-operator.branch-lane`): `TankBranchLaneRetiredPathUsed` (critical)
and `TankBranchLaneBrokerErrors` (warning). Grafana: a "Branch-lane brokered
push/PR outcomes" panel and a "Branch-lane grants & retired-path guard" panel.
Full taxonomy + cost/scale note live in `docs/observability.md` → "Branch-lane
grants".

## Contract impact

`docs/features/session-lifecycle/capabilities.md` gains the **Branch Lane
Grants** capability and retires the separate read-only-git / PR-lane wording.
Acceptance evidence for the contract:

- A branch-scoped grant can **push the branch AND open + own its PR** — no
  `unlimited` required.
- The agent reaches it in **one request + one approval**, with **no second call
  and no registry reload**.
- `git push` / `gh pr …` are the only commands the agent runs; no governed-tool
  names leak into the agent's workflow.
- The retired `request_pr_lane` / `create_pr_lane` / `github.pr_lane.*` paths are
  gone from live code, tests, UI, and docs, and a guard prevents their return.
