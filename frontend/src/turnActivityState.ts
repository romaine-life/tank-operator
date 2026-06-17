export interface TurnActivityActiveSummary {
  turnId?: string;
  status?: string;
  active?: boolean;
}

export type TurnActivityLoadReason =
  | "initial"
  | "page"
  | "retry"
  | "force"
  | "live-refresh";

export type TurnActivityLoadProblemKind = "load" | "timeout" | "live-refresh";

export interface TurnActivityLoadProblem {
  kind: TurnActivityLoadProblemKind;
  attempts: number;
}

export interface TurnActivityLoadSnapshot<Entry, PageInfo, Collapse = unknown> {
  entries: Entry[];
  context: Entry | null;
  finalAnswerEntries?: Entry[];
  collapse?: Collapse;
  pageInfo: PageInfo;
  requestedPage?: number;
  loadedAt: number;
}

export type TurnActivityLoadState<Entry, PageInfo, Collapse = unknown> =
  | { status: "unloaded" }
  | {
      status: "loading";
      requestId: number;
      requestedPage?: number;
      reason: TurnActivityLoadReason;
      previous?: TurnActivityLoadSnapshot<Entry, PageInfo, Collapse>;
    }
  | {
      status: "loaded";
      snapshot: TurnActivityLoadSnapshot<Entry, PageInfo, Collapse>;
    }
  | {
      status: "error";
      requestedPage?: number;
      problem: TurnActivityLoadProblem;
      previous?: TurnActivityLoadSnapshot<Entry, PageInfo, Collapse>;
    };

export function beginTurnActivityLoad<Entry, PageInfo, Collapse = unknown>(
  current: TurnActivityLoadState<Entry, PageInfo, Collapse> | undefined,
  requestId: number,
  requestedPage: number | undefined,
  reason: TurnActivityLoadReason,
): TurnActivityLoadState<Entry, PageInfo, Collapse> {
  const previous =
    current?.status === "loaded"
      ? current.snapshot
      : current?.status === "loading" || current?.status === "error"
        ? current.previous
        : undefined;
  return {
    status: "loading",
    requestId,
    requestedPage,
    reason,
    previous,
  };
}

export function completeTurnActivityLoad<Entry, PageInfo, Collapse = unknown>(
  current: TurnActivityLoadState<Entry, PageInfo, Collapse> | undefined,
  requestId: number,
  snapshot: TurnActivityLoadSnapshot<Entry, PageInfo, Collapse>,
): TurnActivityLoadState<Entry, PageInfo, Collapse> | undefined {
  if (current?.status !== "loading" || current.requestId !== requestId) {
    return current;
  }
  return { status: "loaded", snapshot };
}

export function failTurnActivityLoad<Entry, PageInfo, Collapse = unknown>(
  current: TurnActivityLoadState<Entry, PageInfo, Collapse> | undefined,
  requestId: number,
  problem: TurnActivityLoadProblem,
): TurnActivityLoadState<Entry, PageInfo, Collapse> | undefined {
  if (current?.status !== "loading" || current.requestId !== requestId) {
    return current;
  }
  return {
    status: "error",
    requestedPage: current.requestedPage,
    problem,
    previous: current.previous,
  };
}

export function turnActivityShouldStartLoad<Entry, PageInfo, Collapse = unknown>(
  current: TurnActivityLoadState<Entry, PageInfo, Collapse> | undefined,
  requestedPage: number | undefined,
  force: boolean,
): boolean {
  if (current?.status === "loading" && current.requestedPage === requestedPage) {
    return false;
  }
  if (!force && current?.status === "loaded") {
    return current.snapshot.requestedPage !== requestedPage;
  }
  return true;
}

// The per-turn load lifecycle discriminant.
export type TurnActivityLoadStatus = TurnActivityLoadState<
  unknown,
  unknown,
  unknown
>["status"];

// turnActivityShouldReconcileLoad is the level-triggered gate for the selected
// turn's activity body. The body is loaded by edge-triggered effects (selecting
// a turn, switching to the Turns tab, pressing R). Edge triggers alone strand
// the view when a reset drops the selected turn back to "unloaded" without one
// of those edges firing: the tabs view hides (not unmounts) panes, and a
// hidden -> visible reactivation reset clears the per-turn load map, yet the
// selected turn id and active tab are unchanged, so nothing re-fires and the
// body sits on "Loading activity..." until a remount (reload / nav-away-back).
//
// Keying a reconcile effect on the selected turn's load STATUS closes that gap:
// when the status drops to "unloaded" the effect re-runs and ensures a load.
// "error" is intentionally NOT reconciled (it has its own retry affordance;
// auto-retrying would hot-loop a hard failure), and neither is "loading" /
// "loaded" (a load is in flight or done). Only absent / "unloaded" reconciles.
export function turnActivityShouldReconcileLoad(
  status: TurnActivityLoadStatus | undefined,
  opts: { activeTabIsTurns: boolean; hasSelectedTurn: boolean },
): boolean {
  if (!opts.activeTabIsTurns || !opts.hasSelectedTurn) return false;
  return status == null || status === "unloaded";
}

export function turnActivityLoadVisibleSnapshot<Entry, PageInfo, Collapse = unknown>(
  state: TurnActivityLoadState<Entry, PageInfo, Collapse> | undefined,
): TurnActivityLoadSnapshot<Entry, PageInfo, Collapse> | undefined {
  if (state?.status === "loaded") return state.snapshot;
  if (state?.status === "error") return state.previous;
  if (state?.status === "loading" && state.reason === "live-refresh") {
    return state.previous;
  }
  return undefined;
}

export function turnActivityLoadIsLoaded<Entry, PageInfo>(
  state: TurnActivityLoadState<Entry, PageInfo> | undefined,
): boolean {
  return state?.status === "loaded";
}

export function turnActivityShellIsDurablyActive(
  summary: TurnActivityActiveSummary | undefined,
): boolean {
  if (summary?.status === "needs_input") return false;
  return summary?.active === true || summary?.status === "active";
}

export function turnActivityGroupIsActive(
  summary: TurnActivityActiveSummary | undefined,
  turnId: string,
  activeTurnId: string | null,
): boolean {
  if (summary?.status === "needs_input") return false;
  if (turnActivityShellIsDurablyActive(summary)) return true;
  const active = activeTurnId?.trim() ?? "";
  if (!active) return false;
  const shellTurnId = (summary?.turnId ?? turnId).trim();
  return shellTurnId === active;
}
