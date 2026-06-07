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

export function extractScheduleWakeups(
  step: AgyStep,
): AntigravityScheduleWakeup[] {
  if (String(step.source ?? "").trim().toUpperCase() !== "MODEL") return [];
  if (String(step.type ?? "").trim().toUpperCase() !== "PLANNER_RESPONSE") {
    return [];
  }
  if (!Array.isArray(step.tool_calls)) return [];

  const out: AntigravityScheduleWakeup[] = [];
  step.tool_calls.forEach((call, index) => {
    if (!scheduleToolName(call)) return;
    const args = call.args ?? {};
    const delayMs = scheduleDelayMs(args.DurationSeconds);
    if (delayMs === null) return;
    const prompt = String(args.Prompt ?? "").trim();
    if (!prompt) return;
    out.push({
      delayMs,
      prompt,
      providerItemID: toolCallProviderItemID(step.step_index, index),
    });
  });
  return out;
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
  if (Array.isArray(step.tool_calls) && step.tool_calls.length > 0) return false;
  return String(step.content ?? "").trim().length > 0;
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
