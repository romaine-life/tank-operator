# Session Lifecycle Contract

This contract applies to creating, loading, readying, stopping, deleting, and
terminating sessions, including the session pod boundary.

## Product Model

A Tank session is a user-owned pod-backed workspace with explicit lifecycle
state. The product should make the lifecycle legible without pretending that a
dead pod can be revived in place or that a request succeeded before durable
state confirms it. Pod death is terminal for the running session; a user may
nonetheless start a **new** session that resumes a dead session's
*conversation* from a durably-captured transcript (see "Conversation
resurrection" below) — that is a new lifecycle, not a revival of the old pod.

## Sources Of Truth

- `session_registry` owns session rows and lifecycle metadata.
- Kubernetes owns pod existence and pod phase.
- `session_events` owns user-visible chat/run lifecycle events.
- `sessions.activity_summary` may summarize current activity, but durable
  lifecycle and event rows explain the state.

## Migration Rules

- Do not keep old lifecycle branches, route aliases, pod allocators, or tests
  once a lifecycle path has moved.
- Do not introduce compatibility for unknown callers during lifecycle
  migrations.
- Do not use browser-only session state to stand in for durable lifecycle
  state.
- Do not silently continue a session after the pod-death boundary.

## Live Behavior

- Creating a session writes durable session state before the UI depends on the
  new session.
- Loading and ready status shown to the user must be durable when it appears in
  transcript or sidebar state.
- Session readiness should arrive through the live path without forcing a
  transcript or session-list reset.
- Stop/delete controls must move through requested and confirmed states that
  match durable outcomes.

## Failure And Recovery

- Browser disconnect, orchestrator rollout, and runner-process restart are
  inside the durability boundary while the same session pod is alive.
- Session-pod death is outside the messaging durability boundary. The session
  is terminal because the `emptyDir` workspace is gone.
- **Conversation resurrection.** The session's *conversation* may be captured
  durably (the Claude SDK transcript shipped off-pod to object storage) and
  re-seeded into a NEW session. Resurrection creates a new session row + pod,
  re-clones the same repos, and the runner `resume`s the captured transcript;
  it never revives the dead pod and never silently continues across the
  pod-death boundary. The workspace is still gone — only the conversation is
  restored. Resurrection failures (no captured transcript, SDK-version gap)
  start the new session fresh rather than producing a corrupt resume.
- Failed create, load, stop, and delete operations must leave visible failure
  state and durable or observable evidence.
- Repeated actions should be idempotent or return the already-terminal durable
  state.

## Governed Git Write And Branch Lane Grants

- A restricted (`TANK_RESTRICTED_GIT=true`) session has exactly one git-write
  escalation: the break-glass branch-lane grant. There is no separate PR-lane
  mechanism. Normal commits reach GitHub through a plain `git push` governed by
  the agent-egress proxy (the wall), which mints the credential server-side,
  records the push, and starts CI/mergeability watching; no agent action beyond
  `git push` is required for the session's own branch.
- A break-glass git grant is permission to do work on a branch (existing or not):
  create + push/force-push it **and** open + own its draft PR through review.
  Grant scope (`named` / `count` / `unlimited`) bounds *which* branches a grant
  covers; it must never bound *whether* push and PR-open succeed. A branch-scoped
  grant that can push but cannot open its branch's PR is a contract violation
  (the silent-stranding bug class).
- The agent requests once and a human approves once. After approval, plain
  `git push` (incl. force-push) and `gh pr create|edit|ready|comment` work for
  the granted branches with no second request, no MCP-registry reload, and no
  agent-visible choice between governed push/publish tools or a PR-lane tool.
- Branch-scoped writes are brokered server-side with the branch scope enforced by
  Tank; a raw repo+permission token is not handed to the shell for a scoped
  grant. `unlimited` is the deliberate whole-repo / full-GitHub-API escape hatch
  and is the only scope that mints the App's full permission set.
- Do not reintroduce the retired PR-lane surface (`request_pr_lane` /
  `create_pr_lane` tools, `github.pr_lane.*` events, their routes/handlers/UI, or
  tests pinning that behavior). A reintroduction guard must fail CI if those
  symbols return to live source.

## Observability

- Metrics must cover create, load, ready, stop, delete, pod-watch, and terminal
  outcomes.
- There must be enough durable and live telemetry to distinguish pod failure,
  runner failure, stream failure, and browser display lag.
- Stuck loading/running/deleting states need counters or alerts once they
  exceed their product boundary.
- Branch-lane grant, push, PR-open, and PR-write outcomes are recorded as
  durable `github.break_glass.*` control-action events and counted; a counter
  exists for any use of a retired PR-lane path.

## Acceptance Checks

- Create produces a durable session row and the expected initial events before
  user-visible live state depends on them.
- Ready state appears without transcript reset or session-list refresh.
- Stop/delete controls require durable confirmation before success display.
- Pod death moves the session to the terminal lifecycle state expected by the
  product.
- Repeating a lifecycle command after success returns or displays the durable
  terminal state rather than failing ambiguously.
- A branch-scoped break-glass grant lets the session both push the branch and
  open + own its PR after a single request and a single approval, with no second
  request and no MCP-registry reload; an `unlimited` grant is required only for
  whole-repo / full-GitHub-API work, and the retired PR-lane symbols are absent
  from live source (guarded).
