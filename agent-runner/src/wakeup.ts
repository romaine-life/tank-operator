// ScheduleWakeup detection in SDK events. The pod-side runner detects
// the agent's tool_use call and publishes the wakeup prompt as a delayed
// JetStream session command. The same live pod runner consumes it when due.

import type { SDKMessage } from "@anthropic-ai/claude-agent-sdk";

export interface WakeupRequest {
  delayMs: number;
  prompt: string;
}

// Scan an assistant SDK event's content blocks for a ScheduleWakeup
// tool_use. The agent emits content as an array of typed blocks;
// tool_use blocks carry { type, id, name, input }. Multiple wakeups
// in one turn aren't a documented protocol - the last one wins.
export function extractWakeup(message: SDKMessage): WakeupRequest | null {
  if ((message as any).type !== "assistant") return null;
  const inner = (message as any).message;
  if (!inner || typeof inner !== "object") return null;
  const content = inner.content;
  if (!Array.isArray(content)) return null;

  let found: WakeupRequest | null = null;
  for (const block of content) {
    if (!block || typeof block !== "object") continue;
    if (block.type !== "tool_use") continue;
    const name = String(block.name ?? "").toLowerCase();
    if (name !== "schedulewakeup") continue;
    const input = block.input;
    if (!input || typeof input !== "object") continue;
    const delaySeconds = Number((input as any).delaySeconds);
    const prompt = String((input as any).prompt ?? "");
    if (!Number.isFinite(delaySeconds) || delaySeconds < 0) continue;
    if (!prompt) continue;
    found = {
      delayMs: Math.floor(delaySeconds * 1000),
      prompt,
    };
  }
  return found;
}
