// Prometheus instrumentation for the pod-side agent runner. The metric
// taxonomy mirrors the orchestrator's tank_* namespace and is documented
// in docs/observability.md. Cardinality discipline: no `pod_name`,
// `session_id`, `turn_id`, or `email` labels — anything that grows per
// session-pod blows up Prometheus active series at scale.
//
// The single mutable label is `mode`, set to "claude" here. The
// codex-runner ships an identical module with `mode: "codex"`.

import { Counter, Gauge, Histogram, Registry, collectDefaultMetrics } from "prom-client";
import { createServer, type Server } from "node:http";

const RUNNER_MODE = "claude";

export const registry = new Registry();
registry.setDefaultLabels({ mode: RUNNER_MODE });
collectDefaultMetrics({ register: registry });

export const commandsConsumedTotal = new Counter({
  name: "tank_runner_commands_consumed_total",
  help: "Session commands consumed from the JetStream command subject.",
  labelNames: ["kind", "result"],
  registers: [registry],
});

export const turnDurationSeconds = new Histogram({
  name: "tank_runner_turn_duration_seconds",
  help: "End-to-end duration from turn.started to the terminal turn event.",
  labelNames: ["outcome"],
  buckets: [0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300],
  registers: [registry],
});

export const turnPreStartLatencySeconds = new Histogram({
  name: "tank_runner_turn_pre_start_latency_seconds",
  help: "Pre-provider turn latency observed by the runner, split into command_created_to_claimed and claimed_to_started stages.",
  labelNames: ["stage"],
  buckets: [0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300],
  registers: [registry],
});

export const providerErrorTotal = new Counter({
  name: "tank_runner_provider_error_total",
  help: "Errors raised by the provider SDK (the query iterator or interrupt() call).",
  labelNames: ["kind"],
  registers: [registry],
});

// providerFailureClassTotal classifies every turn.failed{reason:
// "provider_failure"} terminal by the *shape* of the upstream Anthropic
// error, not just that one occurred. The load-bearing class is
// `thinking_block_modified`: the extended-thinking resume bug behind
// session 340, where resuming a long interleaved-thinking turn (e.g.
// after an AskUserQuestion permission-pause) replays the latest
// assistant message with a mutated `thinking`/`redacted_thinking` block
// and Anthropic rejects it with a 400 ("thinking blocks ... cannot be
// modified"). The SDK owns the message array and the replay, so this is
// an engine-version signal: it should fall to zero on the
// @anthropic-ai/claude-agent-sdk ^0.3.158 bump (romaine-life/tank-operator
// #743) and any later non-zero rate is a regression worth paging on.
// The other classes (`overloaded`, `rate_limit`, `context_length`,
// `auth`, `other`) keep the counter useful for the general "why did a
// turn fail at the provider boundary?" question without inflating
// cardinality — the class set is closed and derived from a fixed
// message-signature table (see classifyProviderFailure in runner.ts).
export const providerFailureClassTotal = new Counter({
  name: "tank_runner_provider_failure_class_total",
  help: "turn.failed{reason:provider_failure} terminals classified by upstream Anthropic error signature. `thinking_block_modified` is the extended-thinking resume bug (session 340); it must stay at zero after the SDK ^0.3.158 bump (romaine-life/tank-operator#743) — any increment is a regression.",
  labelNames: ["class"],
  registers: [registry],
});

export const providerRateLimitEventTotal = new Counter({
  name: "tank_runner_provider_rate_limit_event_total",
  help: "Provider SDK rate-limit stream frames observed by the runner. Each frame must resolve the active turn to durable user-visible state instead of leaving it stranded.",
  registers: [registry],
});

export const providerControlTotal = new Counter({
  name: "tank_runner_provider_control_total",
  help: "Provider control-plane calls issued by the runner, such as interrupt and background foreground tasks.",
  labelNames: ["action", "outcome"],
  registers: [registry],
});

export const itemOutcomeTotal = new Counter({
  name: "tank_runner_item_outcome_total",
  help: "Provider item outcomes emitted into the Tank conversation ledger.",
  labelNames: ["outcome", "reason"],
  registers: [registry],
});

// turnUsageEmittedTotal counts the durable usage events this runner
// publishes, split by `kind`. It exists to make the Claude
// context-window-occupancy fix observable and regression-proof:
//
//   - `snapshot` — a per-assistant-message `turn.usage` carrying that
//     single model call's usage (input + cache_read + cache_creation =
//     the live prompt size). Claude reports usage ONLY on the cumulative
//     terminal (`result.usage`), whose `input_tokens` is the tiny
//     uncached sliver once prompt caching folds the context into
//     `cache_read_input_tokens`. Without these snapshots the context
//     gauge has no per-call signal and renders ~0 tokens. This mirrors
//     the codex-runner's `thread.tokenUsage.updated` stream.
//   - `terminal` — the cumulative usage that rides
//     turn.completed/failed/interrupted (used for cost, not occupancy).
//
// The regression signature of the bug this fixes: a Claude turn that
// emits assistant messages but increments `snapshot` zero times. Labels
// are a closed two-value set; `mode="claude"` is added by the registry.
export const turnUsageEmittedTotal = new Counter({
  name: "tank_runner_turn_usage_emitted_total",
  help: "Durable usage events published by the runner. kind='snapshot' is the per-assistant-message context-occupancy turn.usage; kind='terminal' is the cumulative usage on the turn terminal. A Claude turn with assistant messages but zero snapshots is the context-gauge regression signature.",
  labelNames: ["kind"],
  registers: [registry],
});

// unmappedProviderEventTotal counts provider SDK messages that reached the
// adapter fall-through with no Tank-event mapping AND are not on the
// explicit-ignore list (assistant / user / result / Claude task-lifecycle /
// stream_event / system:init / system:compact_boundary). Steady state is
// zero. A nonzero rate is the architectural alarm for the silent-drop class
// that hid context compaction: a semantically-significant provider event is
// vanishing from the durable ledger instead of being mapped or explicitly
// ignored with a test, which the Tank conversation protocol requires. The
// labels are the bounded SDK message type and subtype so a spike names the
// event to investigate (e.g. a new system subtype a provider upgrade added).
export const unmappedProviderEventTotal = new Counter({
  name: "tank_runner_unmapped_provider_event_total",
  help: "Provider SDK messages dropped at the adapter with no Tank mapping and not on the explicit-ignore list. Nonzero means a provider event is silently missing from the durable ledger; labels name the SDK type/subtype to investigate.",
  labelNames: ["type", "subtype"],
  registers: [registry],
});

// interruptOutcomeTotal records the disposition of every `interrupt_turn`
// command this runner accepts. The four-outcome contract (see
// docs/tank-conversation-protocol.md → "Durable turn interruption" and
// romaine-life/tank-operator#532) is the invariant this counter pins:
//
//   - `terminated_via_sdk` — interrupt arrived during an in-flight turn;
//     sdkQuery.interrupt() was awaited and turn.interrupted published.
//   - `terminated_pre_sdk` — interrupt arrived before the runner had
//     dispatched the matching submit_turn to the SDK; the turn was never
//     fed to query() and turn.interrupted published synthetically.
//   - `buffered` — interrupt arrived with no matching active/pending turn;
//     held in the in-process pendingInterrupts buffer awaiting a
//     submit_turn or the orphan timeout. Transient state; every `buffered`
//     increment must drain to exactly one of the other outcomes.
//   - `orphaned` — buffered interrupt never matched a submit_turn within
//     the buffer window; emitted turn.failed{reason:"interrupt_orphaned"}
//     so the UI's "stopping" projection resolves to a durable terminal.
//   - `publish_failed` — sdkQuery.interrupt() was attempted (or skipped
//     because no SDK), but every retry to publish the durable terminal
//     failed. A fallback turn.failed{reason:"publish_interrupt_failed"}
//     attempt was made on the same channel; this outcome means the
//     fallback also failed. JetStream redelivery will retry; until then
//     the UI is stuck. Alert-worthy.
//   - `turn_already_terminal` — interrupt arrived after the targeted
//     turn had already emitted its own terminal (turn.completed or
//     turn.failed). The race is legitimate; the durable ledger shows
//     the natural terminal; no follow-up is needed.
//   - `invalid_target` — interrupt command missing both target_turn_id
//     and client_nonce. Backend bug; should be zero in production.
//
// The PromQL invariant this enables: every backend
// tank_turn_interrupt_request_total{outcome="persisted"} should be
// followed within bounded time by exactly one increment of this counter
// in a terminal-outcome bucket. The `buffered` bucket counts arrivals,
// not terminals; subtract it when alerting.
export const interruptOutcomeTotal = new Counter({
  name: "tank_runner_interrupt_outcome_total",
  help: "Disposition of every interrupt_turn command accepted by the runner. See docs/tank-conversation-protocol.md and romaine-life/tank-operator#532 for the four-outcome contract.",
  labelNames: ["outcome"],
  registers: [registry],
});

export const scheduledWakeupRegisterTotal = new Counter({
  name: "tank_runner_scheduled_wakeup_register_total",
  help: "ScheduleWakeup registrations attempted against the orchestrator durable wakeup API.",
  labelNames: ["result"],
  registers: [registry],
});

export const natsPublishFailureTotal = new Counter({
  name: "tank_runner_nats_publish_failure_total",
  help: "Publish attempts to the JetStream session bus that returned an error.",
  registers: [registry],
});

// eventTruncatedTotal counts Tank conversation events whose stamped
// JSON-encoded size exceeded the per-runner transport budget (default
// 900 KiB, configurable via SESSION_EVENT_MAX_BYTES) and were
// truncated by runner-shared/sessionBus.js's truncateEventIfOversized
// before reaching the JetStream publish. Severity:
//
//   - `strings-truncated` — one or more oversized string fields were
//     replaced with a typed marker; the event's envelope (turn_id,
//     event_id, type, payload shape) is preserved. The transcript
//     renders a "[truncated N bytes]" marker for the affected field.
//   - `payload-dropped` — even after aggressive string truncation the
//     event was still over budget; the entire payload was replaced
//     with `{__payload_dropped: true, original_bytes, reason}`. The
//     event still lands durably (the user sees that an event existed)
//     but the body is unrecoverable from the wire path. Alert-worthy.
//
// Each increment corresponds to one Tank event lost-or-degraded
// because of payload size; sustained `payload-dropped` traffic
// indicates a producer (typically a tool_result.output) that needs to
// chunk or stream rather than emit one giant event. See
// romaine-life/tank-operator#532 Stage 3 for context.
export const eventTruncatedTotal = new Counter({
  name: "tank_runner_event_truncated_total",
  help: "Tank conversation events that exceeded the transport budget and were truncated before publish. Severity 'strings-truncated' preserves envelope; 'payload-dropped' loses body. See romaine-life/tank-operator#532 Stage 3.",
  labelNames: ["event_type", "severity"],
  registers: [registry],
});

// optionsPinnedTotal records the model/effort decision the runner made
// on its first observable submit_turn. Cardinality is bounded by the
// SDK's EffortLevel enum (5 values + "default") and the small set of
// shipped Claude models. The labels are deliberately the *applied*
// values, not the requested ones — so a downgrade from xhigh on a
// model that doesn't support it would be invisible here (the SDK does
// the downgrade silently per its docstring). This counter is the
// operator's answer to "what did this pod actually boot with?" when a
// session looks slower or thinkier than expected.
export const optionsPinnedTotal = new Counter({
  name: "tank_runner_options_pinned_total",
  help: "Model and effort that the runner pinned into SDK Options at first turn.",
  labelNames: ["model", "effort"],
  registers: [registry],
});

// optionsOverrideIgnoredTotal increments when a submit_turn after the
// first one carries a different model or effort than what's already
// pinned. The runner ignores the override (Options is sealed once
// query() runs), but the counter exposes the silent-divergence so a
// future bug or product change ("let me switch model mid-session") is
// visible in metrics before it surfaces as a user-reported "why didn't
// my pick take effect?" ticket. Labels intentionally name the *field*
// that diverged, not the values, to keep cardinality low.
export const optionsOverrideIgnoredTotal = new Counter({
  name: "tank_runner_options_override_ignored_total",
  help: "Submit_turn commands whose model/effort differed from the pinned options (override is silently ignored — model/effort are pod-lifetime).",
  labelNames: ["field"],
  registers: [registry],
});

// turnStartTimes tracks turn.started timestamps so we can observe a
// duration when the terminal turn event arrives. The map is per-runner-
// process and unbounded only if turns leak (no terminal event ever
// arrives); steady-state size is the active-turn count, at most a
// handful even under fan-in.
const turnStartTimes = new Map<string, number>();

export function recordTurnStart(turnID: string): void {
  if (!turnID) return;
  turnStartTimes.set(turnID, Date.now());
}

export function recordTurnPreStartLatency(
  stage: "command_created_to_claimed" | "claimed_to_started",
  startMs: number | null | undefined,
  endMs = Date.now(),
): void {
  if (typeof startMs !== "number" || !Number.isFinite(startMs) || startMs <= 0) return;
  if (!Number.isFinite(endMs) || endMs < startMs) return;
  turnPreStartLatencySeconds.labels(stage).observe((endMs - startMs) / 1000);
}

export function recordTurnTerminal(
  turnID: string,
  outcome: "completed" | "failed" | "interrupted" | "awaiting_input",
): void {
  if (!turnID) return;
  const start = turnStartTimes.get(turnID);
  if (start === undefined) return;
  turnStartTimes.delete(turnID);
  turnDurationSeconds.labels(outcome).observe((Date.now() - start) / 1000);
}

export function startMetricsServer(port: number): Server {
  const server = createServer((req, res) => {
    if (!req.url) {
      res.statusCode = 400;
      res.end();
      return;
    }
    if (req.url === "/metrics" || req.url.startsWith("/metrics?")) {
      registry
        .metrics()
        .then((body) => {
          res.statusCode = 200;
          res.setHeader("Content-Type", registry.contentType);
          res.end(body);
        })
        .catch((err: unknown) => {
          res.statusCode = 500;
          res.end(`metrics collection failed: ${err instanceof Error ? err.message : String(err)}`);
        });
      return;
    }
    if (req.url === "/healthz") {
      res.statusCode = 200;
      res.end("ok");
      return;
    }
    res.statusCode = 404;
    res.end();
  });
  server.listen(port);
  return server;
}
