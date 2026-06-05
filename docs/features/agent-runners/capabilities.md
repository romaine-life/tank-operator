# Agent Runners Capabilities

This ledger names user-facing behavior under the agent-runners feature area. It
is not a backlog. Add entries only when the behavior needs a stable handle for
planning, review, tests, incident follow-up, or retirement.

## Background-task completion wake

Status: in progress

Intent:
When a Claude session backgrounds a task (`run_in_background`) and then ends its
turn, the task finishing later must re-invoke the agent — the base Bash tool's
"re-invokes you when it exits" promise. Before this, a task-lifecycle SDK frame
never started a turn, so a task that finished while the session was idle left the
follow-up silently stranded (the originating incident: a session that backgrounded
a "Wait for CI" task, ended its turn, and never woke).

Affected contracts:
- Agent Runners

Contract impact:
- Wakes go through the same backend-owned turn boundary as a user turn
  (`source=background-task`); the runner never fabricates a turn.
- Idempotent per task id via the durable `session_background_task_wakes` row
  (`wake_id = sha256(tank_session_id, provider, task_id)`), so SDK frame repeats
  and runner restarts cannot double-wake — "command redelivery must be idempotent
  through command keys, turn IDs, or provider item IDs."
- Must not clobber an in-flight question: the fire loop defers (release + retry)
  while the session's durable activity is `needs_input` (an AskUserQuestion
  awaiting an answer).
- Closes a "silent stranding" — a counted bug class — rather than adding one.

Evidence:
- Backend: `backend-go/cmd/tank-operator/background_task_wakes_test.go`
  (durable turn boundary + `source=background-task`, defer-on-awaiting-input,
  fail-on-inactive, `sdkTurnSource`, turn-id-safe nonce);
  `backend-go/internal/pgstore/background_task_wakes.go` (idempotent `Register`).
- Runner: `agent-runner/src/runner.test.ts`
  (register-once-when-idle, skip-when-active, ignore user-stop/lifecycle-start);
  `agent-runner/src/adapters/claude.test.ts` (natural-vs-user terminal split).
- Metrics: `tank_runner_background_task_wake_total{result}`,
  `tank_background_task_wake_register_total`,
  `tank_background_task_wake_fire_total`, `tank_background_task_wakes_due`.
- Durable schema: migrations 0121–0124 (`session_background_task_wakes`).

## Proxyless Gemini runner with usage tracking

Status: in progress

Intent:
Gemini is offered as a single GUI chat mode, `gemini_gui`, driven by the
`gemini-runner` (a pod-side sibling of agent-runner/codex-runner that spawns the
`@google/gemini-cli`). It is *proxyless*: the pod mounts the real Google OAuth
(Code Assist) credential directly at `/etc/gemini-credentials/oauth_creds.json`
and the CLI refreshes the token itself — there is no `gemini-api-proxy`. This is
the "gemini test" shape that stayed healthy with many concurrent sessions; the
proxied variant (and its `gemini_config` / `gemini_test` modes) was deliberately
not restored. The earlier Gemini runtime shipped without usage tracking, so a
Gemini session was invisible to the provider-capacity surface; this capability
adds it so a user can see when Gemini's daily budget is running low and switch
providers.

Affected contracts:
- Agent Runners

Contract impact:
- The runner parses the gemini CLI's terminal `result` stats into a durable
  `turn.usage` event (token counts) — the same per-turn usage signal Claude/Codex
  emit — and reports a `provider_rate_limit_info` snapshot through
  `/api/internal/sessions/{id}/runtime-config`.
- The snapshot models a single `gemini:daily` window (Code Assist bills a daily
  request budget, not Claude's 5h/weekly windows) that resets at UTC midnight.
  Utilization is derived from a configurable daily request cap
  (`GEMINI_DAILY_REQUEST_CAP`, default 1000); it is per-session-observed, not
  account-wide — a named follow-up, not a silent fabrication.
- The SPA renders the snapshot in the same provider-capacity strip + session
  settings used for Claude/Codex (`PROVIDER_QUOTA_WINDOW_DEFS.gemini`).
- Usage capture is best-effort: a parse or report failure never fails the turn.

Evidence:
- Runner: `gemini-runner/src/usage.ts` + `gemini-runner/src/usage.test.ts`
  (stats parsing across CLI shapes, daily-window rollover, capped utilization);
  `gemini-runner/src/runner.ts` `recordTurnUsage` (emit + report path).
- Backend: `backend-go/internal/sessionmodel/sessionmodel_test.go`
  (`TestPodManifestGeminiUsesMountedCredentialsWithoutProxy`,
  `TestPodManifestSlotModeAttachesGeminiRunnerHotSwap`).
- Metrics: `tank_runner_turn_usage_emitted_total{kind}`,
  `tank_runner_provider_usage_report_total{result}` (mode label `gemini`).
- Guard: `scripts/check-removed-chat-runtime.mjs` still blocks `gemini_config` /
  `gemini_test` / `gemini-api-proxy` so the proxied path can't quietly return.
