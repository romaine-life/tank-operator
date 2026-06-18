import type { ConversationRunStatus } from "./conversationReducer";
import type { ConversationActivityStatus } from "./sessionActivity";

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

// describeRunBlock answers the user-facing question "why is my typed message
// queued instead of sent?". The queue gate is session-level: the drain effect
// submits the queue head the moment `running` goes false, so there is exactly
// one reason for the whole queue — "why is the run still considered active?".
//
// This is a READ-ONLY diagnostic. It does NOT decide whether to queue (that is
// decideFollowupSubmit above and must stay the single gate); it only explains
// the live latch state behind a block that already happened.
//
// Two collapses in the durable-activity handler hide the real cause from the
// local run flags, so this helper takes the *raw* durable activity status as a
// distinct input:
//   - needs_input / streaming / submitted / claimed / scheduled all map to
//     runStatus === "running", so the local flag alone can't tell them apart.
//   - `running` stays true after the durable ledger goes idle until the local
//     optimistic run latch reconciles its terminal (App.tsx: the
//     `if (currentRunRef.current) return;` guard). That latch lag is the
//     "looks done but still queues" case, and it lives only in browser memory.
export type RunBlockKind =
  | "agent-working" // durable submitted | claimed | streaming
  | "agent-needs-input" // durable needs_input (agent paused on a question)
  | "scheduled" // durable scheduled (agent self-parked on a timer)
  | "stopping" // a stop request is being applied
  | "settling" // durable idle, local latch in flight, no terminal yet
  | "reconciling"; // durable idle, terminal landed, latch not cleared (refresh-needed)

export interface RunBlockDescription {
  kind: RunBlockKind;
  // Reuses the sessionActivity dot vocabulary so the existing
  // `.status-dot.status-*` CSS colors the indicator with no new rules.
  dotStatus: string;
  // Short human label; matches sessionActivityStatusLabel wording where a
  // durable status exists ("Running" / "Needs input" / "Stopping" /
  // "Scheduled"). The latch-lag tail is client-only, so it gets "Finishing up".
  label: string;
  // One-line plain-English explanation for a tooltip / focused page.
  detail: string;
}

export function describeRunBlock(args: {
  durableActivityStatus: ConversationActivityStatus | null;
  runStatus: ConversationRunStatus | "idle" | "running" | "done";
  hasLocalRun: boolean;
  localRunHasTerminal: boolean;
}): RunBlockDescription {
  const { durableActivityStatus, runStatus, hasLocalRun, localRunHasTerminal } =
    args;

  // 1. Stop in progress wins — runStatus is the authoritative user-stop latch,
  //    and the durable ledger also reports `stopping` while it winds down.
  if (runStatus === "stopping" || durableActivityStatus === "stopping") {
    return {
      kind: "stopping",
      dotStatus: "agent-stopping",
      label: "Stopping",
      detail:
        "A stop request is being applied. Your queued input sends once the turn finishes winding down.",
    };
  }

  // 2. Agent is parked waiting for your answer (AskUserQuestion / needs_input).
  if (durableActivityStatus === "needs_input") {
    return {
      kind: "agent-needs-input",
      dotStatus: "agent-needs-input",
      label: "Needs input",
      detail:
        "The agent is waiting on a question. Answer it to release the queued input.",
    };
  }

  // 3. Agent self-parked on its own clock (ScheduleWakeup / background task).
  if (durableActivityStatus === "scheduled") {
    return {
      kind: "scheduled",
      dotStatus: "agent-scheduled",
      label: "Scheduled",
      detail:
        "The agent parked itself on a timer or background task. Your queued input sends when it resumes.",
    };
  }

  // 4. Agent actively working (the durable ledger reports in-flight work).
  if (
    durableActivityStatus === "submitted" ||
    durableActivityStatus === "claimed" ||
    durableActivityStatus === "streaming"
  ) {
    return {
      kind: "agent-working",
      dotStatus: "agent-working",
      label: "Running",
      detail:
        "The agent is actively working. Your queued input sends when this turn finishes.",
    };
  }

  // 5. Durable ledger is idle but the LOCAL run latch is still in flight — this
  //    is the "looks done but still queues" case. Distinguish "terminal already
  //    landed" (the page is stuck reconciling; a refresh clears it) from
  //    "terminal not arrived yet" (a normal brief settle).
  if (hasLocalRun && localRunHasTerminal) {
    return {
      kind: "reconciling",
      dotStatus: "agent-working",
      label: "Finishing up",
      detail:
        "This turn has finished but the page hasn't reconciled its run state yet. It usually clears on its own within a moment; if it sticks, reloading the page will release the queued input.",
    };
  }
  return {
    kind: "settling",
    dotStatus: "agent-working",
    label: "Finishing up",
    detail:
      "Wrapping up the previous turn. Your queued input sends as soon as it settles.",
  };
}
