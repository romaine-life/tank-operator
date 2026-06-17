// Turn-directory load reconciliation.
//
// The Turns selector's turn set is owned by the durable turn directory
// (`GET /turns/directory`), loaded by the chat pane. Loading that durable list
// into local React state is the concern this module owns.
//
// It owns the single rule for *when a load should be running*, kept pure and
// unit-tested. The rule is deliberately **level-triggered** (reconcile desired
// vs. actual on every render) rather than edge-triggered (fire once on a
// visibility/session edge), because the edge-triggered loader it replaces could
// strand:
//
//   The retired loader guarded a single in-flight load with a permanent boolean
//   latch (`loadTurnDirectoryInFlightRef`) and started loads only on three
//   edges (visible-rising, new-turn-while-ready, manual retry). In the tabs
//   view a pane persists across session switches, so switching from session A
//   (load still in flight) to session B took the latch's early-return — B's
//   load never started — and when A's stale load resolved it returned without
//   setting a terminal status. The pane was left at `idle`/`loading` with the
//   "Loading turns…" spinner, no in-flight work, and no edge left to re-fire.
//   Only a remount (reload, or nav-away-and-back) recovered it. Both `idle` and
//   `loading` render the same spinner and neither had an exit; Retry was wired
//   only to `error`, the one state the telemetry showed practically never
//   happened. See docs/features/transcript-navigation/contract.md and the
//   `loadTurnDirectoryInFlightRef` guard in
//   scripts/check-removed-chat-runtime.mjs.
//
// Keeping the decision here is also the regression guard: the truth table
// (tested in turnDirectoryLoad.test.ts) pins that a visible pane with a
// non-terminal status and nothing in flight ALWAYS resolves to "load", so a
// future change cannot quietly reintroduce a strand-without-recovery path.

export type TurnDirectoryStatus = "idle" | "loading" | "ready" | "error";

// The bounded client telemetry sources for a directory load. `open` is the
// first load of a freshly (re)opened pane; `new-turn` is the live-tail refetch
// when a turn appears the directory does not yet list; `retry` is the user
// pressing Retry. `reconcile` and `watchdog` are the self-healing sources this
// module introduces — they are the durable signal that the strand bug class
// was caught and auto-recovered (a load that the retired design would have left
// stranded with no telemetry at all).
export type TurnDirectoryLoadSource =
  | "open"
  | "new-turn"
  | "retry"
  | "reconcile"
  | "watchdog";

export type TurnDirectoryReconcileInput = {
  // The pane is on-screen. A hidden pane never loads (it reconciles when it
  // becomes visible again).
  visible: boolean;
  // A public read-only surface (admin-hidden / share) with no usable token
  // cannot read a directory; treat it as "do nothing" rather than erroring.
  blockedPublicView: boolean;
  status: TurnDirectoryStatus;
  // Whether a load is genuinely in flight for the CURRENT session epoch. This
  // is the AbortController-keyed replacement for the retired boolean latch:
  // a load for a superseded session does not count, so a stale in-flight load
  // can never gate the current session's load.
  hasLiveLoadForSession: boolean;
};

export type TurnDirectoryReconcileDecision =
  | { action: "idle" }
  | { action: "load"; source: Extract<TurnDirectoryLoadSource, "open" | "reconcile"> };

// evaluateTurnDirectoryReconcile is the level-triggered desired-vs-actual rule.
// Desired state: a visible, loadable pane has its durable directory loaded
// (status "ready"), and exactly one load runs while it is not. The function is
// pure so the reconcile effect can call it on every relevant render and the
// truth table can be tested directly.
//
//   visible=false                         → idle (hidden panes don't load)
//   blockedPublicView=true                → idle (no token to read with)
//   status=ready                          → idle (desired state reached)
//   status=error                          → idle (terminal; Retry / session
//                                                  change re-drives, never an
//                                                  auto-retry hot loop)
//   status=loading, live load for session → idle (a load is genuinely running)
//   status=loading, no live load          → LOAD (source "reconcile": a load
//                                                  vanished — the strand heal)
//   status=idle,    no live load          → LOAD (source "open": fresh load)
export function evaluateTurnDirectoryReconcile(
  input: TurnDirectoryReconcileInput,
): TurnDirectoryReconcileDecision {
  const { visible, blockedPublicView, status, hasLiveLoadForSession } = input;
  if (!visible) return { action: "idle" };
  if (blockedPublicView) return { action: "idle" };
  if (status === "ready" || status === "error") return { action: "idle" };
  if (hasLiveLoadForSession) return { action: "idle" };
  // Non-terminal (idle | loading) with nothing in flight for this session: a
  // load must run. "loading" with no live load is a vanished/superseded load —
  // the exact strand state — so it is counted as a reconcile recovery.
  return { action: "load", source: status === "loading" ? "reconcile" : "open" };
}

export type StuckWatchdogInput = {
  status: TurnDirectoryStatus;
  hasLiveLoadForSession: boolean;
};

export type StuckWatchdogDecision = {
  // Emit the user-trust "the spinner has been visible too long" signal.
  emitStuck: boolean;
  // Also kick a recovery load. Only when nothing is in flight: a genuinely slow
  // (but live) load is left alone — its own timeout turns it into a retryable
  // error rather than racing it with a duplicate.
  recover: boolean;
};

// evaluateStuckWatchdog decides what the spinner watchdog does when it fires
// after TURN_DIRECTORY_STUCK_THRESHOLD_MS. The watchdog is both the
// observability signal for the failure the retired design recorded nothing for
// ("the Turns spinner is stuck") and the belt-and-suspenders recovery for the
// residual strand a level-triggered effect cannot catch on its own (status
// stays "loading" while an aborted load clears its controller ref — a ref
// mutation re-renders nothing, so no reconcile effect re-fires).
export function evaluateStuckWatchdog(
  input: StuckWatchdogInput,
): StuckWatchdogDecision {
  const { status, hasLiveLoadForSession } = input;
  if (status === "ready" || status === "error") {
    return { emitStuck: false, recover: false };
  }
  return { emitStuck: true, recover: !hasLiveLoadForSession };
}

// Whether the spinner watchdog timer should be armed for this render. It runs
// only while a visible pane shows the spinner (idle/loading); ready/error tear
// it down.
export function shouldArmStuckWatchdog(
  status: TurnDirectoryStatus,
  visible: boolean,
): boolean {
  return visible && (status === "idle" || status === "loading");
}

// A directory load that exceeds this wall-clock budget is aborted and surfaced
// as the retryable `error` state instead of an eternal spinner — a wedged
// connection must not latch the Turns view forever.
export const TURN_DIRECTORY_LOAD_TIMEOUT_MS = 15_000;

// How long the "Loading turns…" spinner may stay up before the watchdog treats
// it as stuck: it emits the user-trust signal and (if nothing is in flight)
// re-drives a load. Shorter than the load timeout so a true strand self-heals
// well before a slow-but-live load would time out.
export const TURN_DIRECTORY_STUCK_THRESHOLD_MS = 8_000;
