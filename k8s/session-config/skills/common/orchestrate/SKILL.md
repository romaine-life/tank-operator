---
name: orchestrate
description: Run a multi-slice task as the hub of a spoke fleet — delegate each
  slice to a fresh session, have spokes report back to YOU (not the user), and
  integrate their work yourself with full git authority. Use when a task would
  otherwise become ship-a-slice-then-ask-the-user, turn after turn.
---

# Orchestrate — you are the hub

## Why this exists
Big tasks default to a wasteful loop: do a slice, stop, ask the user for
assurance, repeat — a human turn per slice. This skill replaces that. YOU
become the persistent orchestrator (hub): you delegate each slice to a fresh
spoke session, and the spoke reports back to *you*. The human is consulted
once — to approve the plan — then the fleet runs hub-to-spoke.

## What you are
- You are the hub — this very session. You were handed a spoke config
  (provider/model/mode/reasoning to use for the agents you spawn) and full git
  authority (break-glass is active for you). You hold the plan and own integration.
- Spokes are tank-operator sessions you spawn with `spawn_run_session`, one per
  slice, using your spoke config.

## Precondition (non-negotiable): an approved plan
Before delegating anything, make sure a concrete plan has been presented to and
approved by the user — the slices, their order/dependencies, and the definition
of done for each. This is the ONE human checkpoint. No plan yet? Produce one,
get approval, then proceed. The human must not approve slice-by-slice — that is
the anti-pattern this skill kills.

## The per-slice loop
1. Brief & spawn. `spawn_run_session` with your spoke config and a tight,
   self-contained brief for exactly one slice: goal, acceptance criteria, the
   repo/branch to work on, and the report-back contract.
2. Demand a ping-back. The brief MUST tell the spoke to call `send_prompt` back
   to YOUR session id the moment it (a) finishes or (b) hits a blocker — with a
   structured report (what changed, branch/PR/SHA, tests, or the exact blocker).
   Give it your session id explicitly.
3. Hand off and end your turn. Do not spin or poll — the spoke's ping-back
   arrives as a new turn that wakes you. If a spoke goes silent past a
   reasonable window, fall back to `list_sessions`/`read_transcript` or a
   scheduled wake-up.
4. Integrate on wake. Evaluate the result, then use your git authority: merge
   the slice to main, OR leave it on its feature branch and retrieve/rebase it
   later — your call, per slice. Update plan state; dispatch the next slice
   (independent slices may run in parallel).
5. Close out. When every slice is integrated, summarize the whole run for the
   user in one shot.

## Git authority
Break-glass is active for you (all repos, full GitHub write + direct push).
Integration is yours: merge-through when a slice is independently shippable;
hold-on-branch (then cherry-pick/rebase) when slices must land together. If the
privileged git MCP tools (`mint_full_git_token`, `push_current_head`) aren't
visible yet, call `request_git_break_glass` once to activate them; `gh`/`git`
shell writes already work under the grant.

## The one rule that makes this worth it
Spokes report to you, not to the user. You absorb the slice-by-slice check-ins
so the human doesn't have to. Return to the human only for plan approval, a
blocker you genuinely can't resolve, or the final summary.
