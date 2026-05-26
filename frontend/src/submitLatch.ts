import type { ConversationRunStatus } from "./conversationReducer";

export type FollowupSubmitDecision =
  | { action: "queue"; reason: "durable_active" | "local_run_pending" }
  | {
      action: "submit";
      staleReason?: "running_without_local_run" | "local_run_after_durable_terminal";
    };

export function conversationRunIsActive(status: ConversationRunStatus): boolean {
  return (
    status === "submitted" ||
    status === "streaming" ||
    status === "needs_input" ||
    status === "stopping"
  );
}

export function decideFollowupSubmit(args: {
  running: boolean;
  durableRunStatus: ConversationRunStatus;
  hasLocalRun: boolean;
  localRunHasDurableTerminal: boolean;
}): FollowupSubmitDecision {
  if (conversationRunIsActive(args.durableRunStatus)) {
    return { action: "queue", reason: "durable_active" };
  }
  if (!args.running) {
    return { action: "submit" };
  }
  if (!args.hasLocalRun) {
    return { action: "submit", staleReason: "running_without_local_run" };
  }
  if (args.localRunHasDurableTerminal) {
    return { action: "submit", staleReason: "local_run_after_durable_terminal" };
  }
  return { action: "queue", reason: "local_run_pending" };
}
