# CI Watch Capabilities

Named behaviors for the event-driven rollout / CI-watch surface. See
[../../event-driven-rollout.md](../../event-driven-rollout.md) for the design,
and [../README.md](../README.md) for how capability ledgers are used.

## authoritative-pr-read

- **Status:** shipped
- **Intent:** The agent never reads GitHub mergeability itself. `watch_current_session_pr`
  resolves GitHub's *asynchronous* `mergeable_state` (polling past `unknown`) plus
  auditable CI evidence, and returns `conflict | failed | ready | watching`. Exact
  head-SHA check runs satisfy a check directly. A missing path-filtered check can
  satisfy only when Tank finds the same PR branch's prior green run and proves no
  commit since that run changed the workflow's `pull_request.paths` inputs. This is
  the fix for both the "reports it's good while the PR actually has a conflict" class
  and the "path-filtered checks never appear on unchanged HEAD" class.
- **Durable source:** `session_ci_watches` row (status `watching`) registered via
  `POST /api/internal/sessions/{id}/ci-watches`; per-check CI evidence is recorded in
  the `github.commit.ci` control-action payload.

## ci-watch-wake

- **Status:** shipped
- **Intent:** When a watched PR's *current head SHA* goes red or conflicted, Tank wakes
  the owning session with an actionable `ci-failure` / `ci-conflict` turn (webhook
  receiver → `enqueueSDKTurn`). The agent fixes and re-publishes. The agent is **never**
  woken on success.
- **Durable source:** `POST /webhooks/github` (HMAC-verified) → `session_ci_watches`
  reverse lookup → wake. Stale deliveries (superseded head SHA) and duplicates
  (already-terminal watch) are dropped.

## ci-watch-reaper-protection

- **Status:** shipped
- **Intent:** A session with a `watching` CI watch is excluded from the idle reaper so a
  red/conflict wake can land. Once `ready`/terminal, protection drops — the originating
  session may reap, and the human merges independently.
- **Durable source:** `ClaimIdleForReap` `NOT EXISTS (session_ci_watches ... status='watching')`.

## ci-status-record

- **Status:** shipped (event + webhook merge path); in-Tank merge surface in progress
- **Intent:** A merge surfaces as a display-only `ci_status.updated` record in the turns
  view — it renders and (will) ring, but never invokes the agent and never enters the
  model's replayed context. External merges are recorded via the `pull_request`
  closed/merged webhook.
- **Durable source:** `ci_status.updated` event (actor=system, source=tank).
