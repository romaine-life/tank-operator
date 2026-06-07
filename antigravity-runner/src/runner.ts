// Long-lived Antigravity runner — drives agy (Gemini-Ultra) for one session
// pod's lifetime and publishes canonical Tank conversation events. Sibling of
// codex-runner/src/runner.ts with a subprocess inner loop instead of an SDK:
// pull a submit_turn off the data plane, run one agy turn (the driver tails
// agy's structured transcript and feeds the adapter), publish a durable
// terminal, ack. Boundary events (user_message.created, turn.submitted) are
// backend-owned. Raw agy steps never reach the bus.
//
// Stop semantics follow the four-outcome contract
// (docs/tank-conversation-protocol.md → #532): an interrupt arriving during the
// active turn kills agy and yields turn.interrupted (terminated_via_sdk); one
// arriving before its submit is buffered and drains to turn.interrupted on
// arrival (terminated_pre_sdk) or turn.failed{interrupt_orphaned} after a
// timeout (orphaned). agy runs with --dangerously-skip-permissions in -p mode,
// so it never pauses for AskUserQuestion; input_reply is acked as a no-op.

import {
  AntigravityTranscriptAdapter,
  type AgyStep,
  type AntigravityTurn,
} from "./adapters/antigravity.js";
import { AgyDriver } from "./driver.js";
import type { Config } from "./config.js";
import { expandSkillPrompt } from "./skills.js";
import { SessionEventSink } from "./sessionEvents.js";
import {
  SessionCommandBus,
  commandClientNonce,
  isInputReplyCommand,
  isInterruptCommand,
  type SessionCommandRecord,
} from "./sessionCommands.js";
import type { TankConversationEvent } from "../../runner-shared/conversation.js";
import {
  stampTankEvent,
  turnEvent,
  turnIDForClientNonce,
} from "../../runner-shared/conversation-builders.js";
import { truncateEventIfOversized } from "../../runner-shared/sessionBus.js";
import { registerScheduledWakeup } from "../../runner-shared/scheduledWakeup.js";
import { reportRuntimeConfig } from "../../runner-shared/runtimeConfig.js";
import {
  agyStepTotal,
  agyDiagnosticTotal,
  commandsConsumedTotal,
  eventTruncatedTotal,
  interruptOutcomeTotal,
  natsPublishFailureTotal,
  providerErrorTotal,
  scheduledWakeupRegisterTotal,
  turnDurationSeconds,
  turnTerminalTotal,
} from "./metrics.js";
import {
  extractScheduleWakeups,
  isAssistantPlannerTextStep,
  isNativeScheduleWakeResponse,
  scheduleAckGraceMs,
  type AntigravityScheduleWakeup,
} from "./wakeup.js";

const SOURCE = "antigravity";

function envInt(value: string | undefined, fallback: number): number {
  const n = Number.parseInt((value ?? "").trim(), 10);
  return Number.isFinite(n) && n > 0 ? n : fallback;
}

const INTERRUPT_BUFFER_MS = envInt(
  process.env.SESSION_INTERRUPT_BUFFER_MS,
  30_000,
);
const TERMINAL_PUBLISH_ATTEMPTS = envInt(
  process.env.SESSION_TERMINAL_PUBLISH_ATTEMPTS,
  3,
);
const TERMINAL_PUBLISH_BACKOFF_MS = envInt(
  process.env.SESSION_TERMINAL_PUBLISH_BACKOFF_MS,
  500,
);

type SubmitRecord = SessionCommandRecord & {
  prompt?: string;
  model?: string;
  skill_name?: string;
  client_nonce?: string;
  target_turn_id?: string;
};

class AsyncQueue<T> {
  private readonly items: T[] = [];
  private waiter: ((value: T | null) => void) | null = null;
  private closed = false;

  push(item: T): void {
    if (this.waiter) {
      const resolve = this.waiter;
      this.waiter = null;
      resolve(item);
      return;
    }
    this.items.push(item);
  }

  next(signal: AbortSignal): Promise<T | null> {
    if (this.items.length > 0) return Promise.resolve(this.items.shift() as T);
    if (this.closed || signal.aborted) return Promise.resolve(null);
    return new Promise<T | null>((resolve) => {
      this.waiter = resolve;
      signal.addEventListener("abort", () => resolve(null), { once: true });
    });
  }

  close(): void {
    this.closed = true;
    if (this.waiter) {
      this.waiter(null);
      this.waiter = null;
    }
  }
}

interface ActiveTurn extends AntigravityTurn {
  abort: AbortController;
}

export function modelForAgyTurn(recordModel: unknown): string | null {
  const model = String(recordModel ?? "").trim();
  return model || null;
}

export class Runner {
  private readonly events: SessionEventSink;
  private readonly commands: SessionCommandBus;
  private readonly adapter: AntigravityTranscriptAdapter;
  private readonly driver: AgyDriver;
  private readonly queue = new AsyncQueue<SubmitRecord>();
  private readonly orphanInterrupts = new Map<string, number>();
  private active: ActiveTurn | null = null;
  private hasConversation = false;
  private lastReportedModel = "";

  constructor(private readonly cfg: Config) {
    this.events = new SessionEventSink(cfg);
    this.commands = new SessionCommandBus(cfg, SOURCE);
    this.adapter = new AntigravityTranscriptAdapter(cfg.sessionId);
    this.driver = new AgyDriver(cfg.agyHome);
  }

  async run(signal: AbortSignal): Promise<void> {
    signal.addEventListener("abort", () => {
      this.queue.close();
      this.active?.abort.abort();
    });
    await this.commands.startControlConsumer(async (rec) => {
      this.handleControl(rec as SubmitRecord);
    }, signal);
    await this.commands.startCommandConsumer(async (rec) => {
      commandsConsumedTotal.labels("submit_turn").inc();
      this.queue.push(rec as SubmitRecord);
    }, signal);

    while (!signal.aborted) {
      const rec = await this.queue.next(signal);
      if (!rec) break;
      await this.handleSubmit(rec, signal);
    }
    await this.events.close().catch(() => {});
  }

  private async handleSubmit(
    rec: SubmitRecord,
    signal: AbortSignal,
  ): Promise<void> {
    const clientNonce = commandClientNonce(rec) || rec.client_nonce || "";
    const turnID = turnIDForClientNonce(clientNonce);
    const turn: AntigravityTurn = { turnID, clientNonce };

    // Stop clicked before this submit dispatched: never feed the prompt to agy.
    if (this.orphanInterrupts.delete(clientNonce)) {
      await this.publishTerminal(this.adapter.interruptTurn(turn));
      interruptOutcomeTotal.labels("terminated_pre_sdk").inc();
      rec.ack();
      return;
    }

    // Restart dedupe: a turn that already has a durable terminal is done.
    try {
      if (await this.events.findTurnTerminal(turnID)) {
        rec.ack();
        return;
      }
    } catch {
      // bus lookup failed; proceed — a duplicate terminal dedupes by event_id.
    }

    const abort = new AbortController();
    if (signal.aborted) abort.abort();
    signal.addEventListener("abort", () => abort.abort(), { once: true });
    this.active = { ...turn, abort };
    const stopHeartbeat = this.commands.startCommandHeartbeat(rec);
    const endTimer = turnDurationSeconds.startTimer();

    try {
      await this.publish(
        turnEvent({
          sessionID: this.cfg.sessionId,
          turnID,
          clientNonce,
          source: SOURCE,
          type: "turn.claimed",
        }) as TankConversationEvent,
      );

      const basePrompt = String(rec.prompt ?? "").trim();
      if (!basePrompt) {
        providerErrorTotal.labels("missing_prompt").inc();
        await this.publishTerminal(
          this.adapter.failTurn(turn, "missing_prompt"),
        );
        return;
      }
      const expanded = await expandSkillPrompt(basePrompt, rec.skill_name);
      if (expanded.reason !== "no_skill") {
        agyDiagnosticTotal.labels(
          expanded.loaded ? "skill_loaded" : "skill_missing",
        ).inc();
      }
      if (expanded.reason === "missing") {
        providerErrorTotal.labels("skill_missing").inc();
        await this.publishTerminal(
          this.adapter.failTurn(
            turn,
            `skill_missing: ${String(rec.skill_name ?? "").trim()}`,
          ),
        );
        return;
      }
      const prompt = expanded.prompt;
      const model = modelForAgyTurn(rec.model);
      if (!model) {
        providerErrorTotal.labels("missing_model").inc();
        await this.publishTerminal(
          this.adapter.failTurn(turn, "missing_model"),
        );
        return;
      }
      void this.reportRuntime(model);

      let observedStepCount = 0;
      let wakeupRegistrationFailed = false;
      let scheduledWakeupParked = false;
      const registeredSchedulePrompts: string[] = [];
      const scheduleParkTimers = new Set<NodeJS.Timeout>();
      const clearScheduleParkTimers = () => {
        for (const timer of scheduleParkTimers) clearTimeout(timer);
        scheduleParkTimers.clear();
      };
      const parkNativeSchedule = () => {
        if (scheduledWakeupParked) return;
        scheduledWakeupParked = true;
        clearScheduleParkTimers();
        this.driver.interrupt();
      };
      const result = await (async () => {
        try {
          return await this.driver.runTurn(
            {
              prompt,
              model,
              resume: this.hasConversation,
              workspace: this.cfg.workspace,
            },
            async (step) => {
              observedStepCount += 1;
              agyStepTotal.labels(stepKind(step)).inc();
              for (const wakeup of extractScheduleWakeups(step)) {
                const ok = await this.registerWakeup(wakeup, turn.turnID);
                wakeupRegistrationFailed = wakeupRegistrationFailed || !ok;
                if (ok) {
                  registeredSchedulePrompts.push(wakeup.prompt);
                  const timer = setTimeout(
                    parkNativeSchedule,
                    scheduleAckGraceMs(wakeup.delayMs),
                  );
                  timer.unref();
                  scheduleParkTimers.add(timer);
                }
              }
              if (
                registeredSchedulePrompts.length > 0 &&
                isNativeScheduleWakeResponse(step, registeredSchedulePrompts)
              ) {
                parkNativeSchedule();
                return;
              }
              for (const ev of this.adapter.stepEvents(turn, step)) {
                await this.publish(ev);
              }
              if (
                registeredSchedulePrompts.length > 0 &&
                isAssistantPlannerTextStep(step)
              ) {
                parkNativeSchedule();
              }
            },
            abort.signal,
          );
        } finally {
          clearScheduleParkTimers();
        }
      })();
      for (const kind of agyDiagnostics(result)) {
        agyDiagnosticTotal.labels(kind).inc();
      }
      this.hasConversation = true;

      const terminal = classifyAgyTerminal(
        result,
        observedStepCount,
        abort.signal.aborted,
      );
      if (terminal.kind === "interrupted") {
        if (scheduledWakeupParked && !abort.signal.aborted) {
          await this.publishTerminal(this.adapter.completeTurn(turn));
        } else {
          await this.publishTerminal(this.adapter.interruptTurn(turn));
          interruptOutcomeTotal.labels("terminated_via_sdk").inc();
        }
      } else if (terminal.kind === "failed") {
        providerErrorTotal.labels(terminal.metricReason).inc();
        await this.publishTerminal(
          this.adapter.failTurn(turn, terminal.reason),
        );
      } else if (wakeupRegistrationFailed) {
        providerErrorTotal.labels("scheduled_wakeup_register_failed").inc();
        await this.publishTerminal(
          this.adapter.failTurn(turn, "scheduled_wakeup_register_failed"),
        );
      } else {
        await this.publishTerminal(this.adapter.completeTurn(turn));
      }
    } catch (err) {
      providerErrorTotal.labels("exception").inc();
      await this.publishTerminal(this.adapter.failTurn(turn, String(err)));
    } finally {
      endTimer();
      stopHeartbeat();
      this.active = null;
      rec.ack();
    }
  }

  private handleControl(rec: SubmitRecord): void {
    if (isInterruptCommand(rec)) {
      this.handleInterrupt(rec);
      return;
    }
    if (isInputReplyCommand(rec)) {
      // agy -p --dangerously-skip-permissions never pauses for input.
      commandsConsumedTotal.labels("input_reply").inc();
      rec.ack();
      return;
    }
    commandsConsumedTotal.labels("other_control").inc();
    rec.ack();
  }

  private handleInterrupt(rec: SubmitRecord): void {
    commandsConsumedTotal.labels("interrupt_turn").inc();
    interruptOutcomeTotal.labels("buffered").inc();
    const nonce = commandClientNonce(rec) || rec.client_nonce || "";
    const targetTurnID =
      rec.target_turn_id || (nonce ? turnIDForClientNonce(nonce) : "");
    if (!nonce && !targetTurnID) {
      interruptOutcomeTotal.labels("invalid_target").inc();
      rec.ack();
      return;
    }

    if (
      this.active &&
      (this.active.turnID === targetTurnID || this.active.clientNonce === nonce)
    ) {
      // Active turn: kill agy. handleSubmit publishes turn.interrupted and
      // counts terminated_via_sdk when the driver returns killed.
      this.active.abort.abort();
      this.driver.interrupt();
      rec.ack();
      return;
    }

    // Pre-start: buffer; drain on the matching submit or time out as orphaned.
    this.orphanInterrupts.set(nonce, Date.now());
    rec.ack();
    setTimeout(() => void this.checkOrphan(nonce), INTERRUPT_BUFFER_MS).unref();
  }

  private async checkOrphan(nonce: string): Promise<void> {
    if (!this.orphanInterrupts.delete(nonce)) return;
    const turnID = turnIDForClientNonce(nonce);
    try {
      await this.publishTerminal(
        this.adapter.failTurn(
          { turnID, clientNonce: nonce },
          "interrupt_orphaned",
        ),
      );
      interruptOutcomeTotal.labels("orphaned").inc();
    } catch {
      interruptOutcomeTotal.labels("publish_failed").inc();
    }
  }

  private async publish(event: TankConversationEvent): Promise<void> {
    const stamped = stampTankEvent(event);
    const guard = truncateEventIfOversized(stamped);
    if (guard.truncated) {
      const severity =
        guard.reason === "payload-dropped"
          ? "payload-dropped"
          : "strings-truncated";
      eventTruncatedTotal.labels(stamped.type, severity).inc();
    }
    try {
      await this.events.upsert(guard.event);
    } catch (err) {
      natsPublishFailureTotal.inc();
      throw err;
    }
  }

  private async publishTerminal(event: TankConversationEvent): Promise<void> {
    let lastErr: unknown;
    for (let attempt = 1; attempt <= TERMINAL_PUBLISH_ATTEMPTS; attempt++) {
      try {
        await this.publish(event);
        turnTerminalTotal.labels(event.type).inc();
        return;
      } catch (err) {
        lastErr = err;
        await sleep(TERMINAL_PUBLISH_BACKOFF_MS * attempt);
      }
    }
    interruptOutcomeTotal.labels("publish_failed").inc();
    console.error(
      JSON.stringify({
        msg: "terminal publish failed",
        type: event.type,
        error: String(lastErr),
      }),
    );
  }

  private async reportRuntime(model: string): Promise<void> {
    if (model === this.lastReportedModel) return;
    this.lastReportedModel = model;
    try {
      await reportRuntimeConfig(this.cfg, {
        model,
        contextWindowSource: "antigravity",
      });
    } catch {
      // best-effort; the composer footer just lacks the applied model
    }
  }

  private async registerWakeup(
    req: AntigravityScheduleWakeup,
    scheduledTurnID: string,
  ): Promise<boolean> {
    try {
      const registered = await registerScheduledWakeup(this.cfg, {
        delayMs: req.delayMs,
        prompt: req.prompt,
        providerItemID: req.providerItemID,
        scheduledTurnID,
      });
      scheduledWakeupRegisterTotal
        .labels(registered ? "ok" : "disabled")
        .inc();
      return registered;
    } catch (err) {
      scheduledWakeupRegisterTotal.labels("failed").inc();
      console.error("antigravity scheduled wakeup register failed:", err);
      return false;
    }
  }
}

export function agyDiagnostics(result: {
  stdout: string;
  stderr: string;
}): string[] {
  const text = `${result.stderr}\n${result.stdout}`.toLowerCase();
  const kinds: string[] = [];
  if (
    text.includes("failed to fetch user info: 401") ||
    text.includes("userinfo returned status 401")
  ) {
    kinds.push("auxiliary_userinfo_401");
  }
  if (text.includes("clearcut responded with http code: 401")) {
    kinds.push("telemetry_clearcut_401");
  }
  return kinds;
}

function stepKind(step: AgyStep): string {
  const source = (step.source ?? "").toUpperCase();
  if (source === "USER_EXPLICIT" || source === "SYSTEM") return "dropped";
  if (Array.isArray(step.tool_calls) && step.tool_calls.length > 0)
    return "tool_call";
  if ((step.type ?? "").toUpperCase() === "PLANNER_RESPONSE") return "message";
  return "tool_result";
}

export type AgyTerminalClassification =
  | { kind: "completed" }
  | { kind: "interrupted" }
  | { kind: "failed"; metricReason: string; reason: string };

export function classifyAgyTerminal(
  result: {
    exitCode: number | null;
    killed: boolean;
    stdout: string;
    stderr: string;
  },
  observedStepCount: number,
  aborted: boolean,
): AgyTerminalClassification {
  if (result.killed || aborted) return { kind: "interrupted" };
  if (result.exitCode !== 0) {
    return {
      kind: "failed",
      metricReason: "nonzero_exit",
      reason: agyFailReason(result.exitCode, result.stderr),
    };
  }
  if (observedStepCount <= 0) {
    const metricReason = agySemanticFailureMetric(result);
    return {
      kind: "failed",
      metricReason,
      reason: agySemanticFailReason(metricReason, result),
    };
  }
  return { kind: "completed" };
}

function agySemanticFailureMetric(result: {
  stdout: string;
  stderr: string;
}): string {
  const text = `${result.stderr}\n${result.stdout}`.toLowerCase();
  if (text.includes("timed out waiting for cascade to start running")) {
    return "provider_start_timeout";
  }
  if (
    text.includes("neither planmodel nor requestedmodel specified") ||
    text.includes("failed to get model config")
  ) {
    return "provider_model_unavailable";
  }
  if (
    text.includes("invalid authentication credentials") ||
    text.includes("you are not logged into antigravity") ||
    text.includes("unauthenticated")
  ) {
    return "provider_auth_failed";
  }
  return "provider_no_output";
}

function agySemanticFailReason(
  metricReason: string,
  result: { stdout: string; stderr: string },
): string {
  const tail = agyOutputTail(result);
  return tail ? `${metricReason}: ${tail}` : metricReason;
}

function agyFailReason(exitCode: number | null, stderr: string): string {
  const tail = stderr.trim().split("\n").slice(-1)[0]?.slice(0, 200) ?? "";
  return tail ? `agy exit ${exitCode}: ${tail}` : `agy exit ${exitCode}`;
}

function agyOutputTail(result: { stdout: string; stderr: string }): string {
  return (
    `${result.stderr}\n${result.stdout}`
      .trim()
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line.length > 0)
      .slice(-1)[0]
      ?.slice(0, 200) ?? ""
  );
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
