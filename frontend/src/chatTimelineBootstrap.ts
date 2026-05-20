export type TimelineBootstrapStatus = "idle" | "loading" | "ready" | "error";

export type TimelineBootstrapState = {
  sessionId: string;
  epoch: number;
  status: TimelineBootstrapStatus;
  error: string | null;
};

export type TimelineBootstrapAction =
  | { type: "reset"; sessionId: string; epoch: number }
  | { type: "loading"; sessionId: string; epoch: number }
  | { type: "ready"; sessionId: string; epoch: number }
  | { type: "error"; sessionId: string; epoch: number; error: string };

export function initialTimelineBootstrapState(
  sessionId: string,
  epoch = 0,
): TimelineBootstrapState {
  return { sessionId, epoch, status: "idle", error: null };
}

export function reduceTimelineBootstrap(
  state: TimelineBootstrapState,
  action: TimelineBootstrapAction,
): TimelineBootstrapState {
  if (action.type === "reset") {
    return initialTimelineBootstrapState(action.sessionId, action.epoch);
  }

  if (state.sessionId !== action.sessionId || state.epoch !== action.epoch) {
    return state;
  }

  if (action.type === "loading") {
    return { ...state, status: "loading", error: null };
  }
  if (action.type === "ready") {
    return { ...state, status: "ready", error: null };
  }
  return { ...state, status: "error", error: action.error };
}
