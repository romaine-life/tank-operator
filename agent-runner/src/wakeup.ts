// ScheduleWakeup detection in SDK events. The pod-side runner extracts
// the agent's tool_use call and schedules an in-process setTimeout; when
// the timer fires, the runner publishes the wakeup prompt as a normal
// submit_turn command (source=schedule-wakeup) to the session command
// subject, which the same runner then consumes as if it were a user turn.
// Timer state lives in the runner process: it survives orchestrator
// rollouts (the session pod is independent), but a runner-process restart
// inside a live pod loses pending wakeups. That matches the durability
// boundary in docs/product-inspirations.md, which scopes scheduled-wake
// state to runtime state rather than durable messaging.

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
