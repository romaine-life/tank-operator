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

export interface TurnActivityLoadSnapshot<Entry, PageInfo> {
  entries: Entry[];
  context: Entry | null;
  pageInfo: PageInfo;
  requestedPage?: number;
  loadedAt: number;
}

export type TurnActivityLoadState<Entry, PageInfo> =
  | { status: "unloaded" }
  | {
      status: "loading";
      requestId: number;
      requestedPage?: number;
      reason: TurnActivityLoadReason;
      previous?: TurnActivityLoadSnapshot<Entry, PageInfo>;
    }
  | {
      status: "loaded";
      snapshot: TurnActivityLoadSnapshot<Entry, PageInfo>;
    }
  | {
      status: "error";
      requestedPage?: number;
      problem: TurnActivityLoadProblem;
      previous?: TurnActivityLoadSnapshot<Entry, PageInfo>;
    };

export function beginTurnActivityLoad<Entry, PageInfo>(
  current: TurnActivityLoadState<Entry, PageInfo> | undefined,
  requestId: number,
  requestedPage: number | undefined,
  reason: TurnActivityLoadReason,
): TurnActivityLoadState<Entry, PageInfo> {
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

export function completeTurnActivityLoad<Entry, PageInfo>(
  current: TurnActivityLoadState<Entry, PageInfo> | undefined,
  requestId: number,
  snapshot: TurnActivityLoadSnapshot<Entry, PageInfo>,
): TurnActivityLoadState<Entry, PageInfo> | undefined {
  if (current?.status !== "loading" || current.requestId !== requestId) {
    return current;
  }
  return { status: "loaded", snapshot };
}

export function failTurnActivityLoad<Entry, PageInfo>(
  current: TurnActivityLoadState<Entry, PageInfo> | undefined,
  requestId: number,
  problem: TurnActivityLoadProblem,
): TurnActivityLoadState<Entry, PageInfo> | undefined {
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

export function turnActivityShouldStartLoad<Entry, PageInfo>(
  current: TurnActivityLoadState<Entry, PageInfo> | undefined,
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

export function turnActivityLoadVisibleSnapshot<Entry, PageInfo>(
  state: TurnActivityLoadState<Entry, PageInfo> | undefined,
): TurnActivityLoadSnapshot<Entry, PageInfo> | undefined {
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
