import {
  toolCallProviderItemID,
  type AgyStep,
  type AgyToolCall,
} from "./adapters/antigravity.js";

export interface AntigravityScheduleWakeup {
  delayMs: number;
  prompt: string;
  providerItemID: string;
}

export interface AntigravityScheduleInspection {
  wakeups: AntigravityScheduleWakeup[];
  scheduleCallCount: number;
  malformedScheduleCallCount: number;
}

const MIN_SCHEDULE_ACK_GRACE_MS = 100;
const MAX_SCHEDULE_ACK_GRACE_MS = 1_000;

function scheduleToolName(call: AgyToolCall): boolean {
  return String(call.name ?? "").trim().toLowerCase() === "schedule";
}

function scheduleDelayMs(value: unknown): number | null {
  const n = Number(value);
  if (!Number.isFinite(n) || n < 0) return null;
  return Math.floor(n * 1000);
}

function isDone(step: AgyStep): boolean {
  return String(step.status ?? "").trim().toUpperCase() === "DONE";
}

export function inspectScheduleWakeups(
  step: AgyStep,
): AntigravityScheduleInspection {
  const empty = {
    wakeups: [],
    scheduleCallCount: 0,
    malformedScheduleCallCount: 0,
  };
  if (String(step.source ?? "").trim().toUpperCase() !== "MODEL") return empty;
  if (String(step.type ?? "").trim().toUpperCase() !== "PLANNER_RESPONSE") {
    return empty;
  }
  if (!isDone(step)) return empty;
  if (!Array.isArray(step.tool_calls)) return empty;

  const out: AntigravityScheduleWakeup[] = [];
  let scheduleCallCount = 0;
  let malformedScheduleCallCount = 0;
  step.tool_calls.forEach((call, index) => {
    if (!scheduleToolName(call)) return;
    scheduleCallCount += 1;
    const args = call.args ?? {};
    const delayMs = scheduleDelayMs(args.DurationSeconds);
    if (delayMs === null) {
      malformedScheduleCallCount += 1;
      return;
    }
    const prompt = String(args.Prompt ?? "").trim();
    if (!prompt) {
      malformedScheduleCallCount += 1;
      return;
    }
    out.push({
      delayMs,
      prompt,
      providerItemID: toolCallProviderItemID(step.step_index, index),
    });
  });
  return {
    wakeups: out,
    scheduleCallCount,
    malformedScheduleCallCount,
  };
}

export function extractScheduleWakeups(
  step: AgyStep,
): AntigravityScheduleWakeup[] {
  return inspectScheduleWakeups(step).wakeups;
}

export function scheduleAckGraceMs(delayMs: number): number {
  if (!Number.isFinite(delayMs) || delayMs <= 0) return MIN_SCHEDULE_ACK_GRACE_MS;
  return Math.min(
    MAX_SCHEDULE_ACK_GRACE_MS,
    Math.max(MIN_SCHEDULE_ACK_GRACE_MS, Math.floor(delayMs / 4)),
  );
}

export function isAssistantPlannerTextStep(step: AgyStep): boolean {
  if (String(step.source ?? "").trim().toUpperCase() !== "MODEL") return false;
  if (String(step.type ?? "").trim().toUpperCase() !== "PLANNER_RESPONSE") {
    return false;
  }
  if (!isDone(step)) return false;
  if (Array.isArray(step.tool_calls) && step.tool_calls.length > 0) return false;
  return String(step.content ?? "").trim().length > 0;
}

export function isWaitIntentWithoutScheduleStep(step: AgyStep): boolean {
  if (!isAssistantPlannerTextStep(step)) return false;
  const text = normalizeText(String(step.content ?? ""));
  if (!text) return false;
  return (
    /\bi(?:'ll| will| am)\s+(?:now\s+)?wait\b/.test(text) ||
    /\bi\s+am\s+waiting\b/.test(text) ||
    /\bi(?:'ll| will)\s+check\s+back\b/.test(text) ||
    /\bwait(?:ing)?\s+for\b/.test(text)
  );
}

function normalizeText(value: string): string {
  return value.trim().replace(/\s+/g, " ").toLowerCase();
}

export function isNativeScheduleWakeResponse(
  step: AgyStep,
  prompts: readonly string[],
): boolean {
  if (!isAssistantPlannerTextStep(step)) return false;
  const text = normalizeText(String(step.content ?? ""));
  if (!text) return false;
  return prompts.some((prompt) => normalizeText(prompt) === text);
}
