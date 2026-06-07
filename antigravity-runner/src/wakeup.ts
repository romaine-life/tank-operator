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
