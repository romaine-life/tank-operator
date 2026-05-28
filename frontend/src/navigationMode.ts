// Transcript navigation mode. The chat view is in exactly one of two
// modes at all times:
//
//   - "live-tail": the user is reading the live tail. New events should
//     append at the bottom and follow the viewport. The read cursor
//     advances. The scroll-to-bottom affordance is hidden (modulo the
//     server-window check on sdkFoundNewest).
//
//   - "historical-anchor": the user is reading earlier history. The
//     viewport is anchored to a specific cursor and should NOT be moved
//     by new live events. The read cursor does not advance. The
//     scroll-to-bottom affordance is visible so the user can explicitly
//     return to the live tail.
//
// The contract that owns this concept is
// docs/features/transcript-navigation/contract.md → "Returning to live
// tail is an explicit state transition." Specifically: the mode is
// driven by intent, not by continuous DOM measurement of the scroll
// container.
//
// Intent enters this module as a NavigationModeReason. The reason set
// is closed; every transition is named, observable in client
// telemetry, and one of the two new bounded server-side metric event
// labels picks it up.
//
// What is intentionally NOT a reason:
//   - SSE row arrival (turn activity, tool calls, text deltas)
//   - Smooth-scroll catch-up by react-virtuoso's followOutput
//   - Programmatic scrollTo calls
//   - "scrollHeight - scrollTop > N px" DOM heuristics
//
// Those are layout effects of mode-preserving behavior. Reading them as
// mode inputs is the bug class this module retired (the "userScrolledUp
// latched true while user is at the visual bottom" failure observed in
// session 269, 2026-05-27).

export type NavigationMode = "live-tail" | "historical-anchor";

// NavigationModeReason names every intent input the mode machine
// accepts. The set is closed: a new transition source MUST add a name
// here AND a corresponding server-side allowlist entry in
// backend-go/cmd/tank-operator/handlers_client_metrics.go for telemetry
// to label the transition correctly. Server-side bucketing is the
// boundary; the SPA never sets bare metric labels.
//
// Reasons split into two intent classes:
//   - Reasons that target "live-tail" can be raised in any prior mode.
//   - Reasons that target "historical-anchor" only apply while the
//     prior mode is "live-tail" — entering historical-anchor a second
//     time from itself is a no-op (the anchor is already what the user
//     is looking at).
export type NavigationModeReason =
  // → live-tail
  | "session-open-tail"
  | "submit"
  | "down-button"
  | "jump-latest"
  | "keyboard-end"
  | "virtuoso-at-bottom-true"
  // → historical-anchor
  | "session-open-anchored"
  | "up-button"
  | "jump-oldest"
  | "keyboard-home"
  | "user-scroll-up";

export interface NavigationModeTransition {
  from: NavigationMode;
  to: NavigationMode;
  reason: NavigationModeReason;
  // True when the from/to are different. Callers should only fire
  // telemetry / re-renders when this is true; same-mode reasons are
  // legal (they're durable intent statements even when they're already
  // satisfied) but they don't change state.
  changed: boolean;
}

// targetModeForReason returns the mode each reason is intended to
// produce. It is a total function over the closed reason set, so adding
// a new reason without a target causes a TypeScript exhaustiveness
// error at the switch — that's the migration guard for "new reason
// added without picking a target."
export function targetModeForReason(reason: NavigationModeReason): NavigationMode {
  switch (reason) {
    case "session-open-tail":
    case "submit":
    case "down-button":
    case "jump-latest":
    case "keyboard-end":
    case "virtuoso-at-bottom-true":
      return "live-tail";
    case "session-open-anchored":
    case "up-button":
    case "jump-oldest":
    case "keyboard-home":
    case "user-scroll-up":
      return "historical-anchor";
    default: {
      // Unreachable when the caller respects the closed reason set.
      // Throw so a future bug that wires an unknown reason name (e.g.
      // a typo in a call site) fails loudly instead of silently
      // re-deriving the wrong mode.
      const exhaustive: never = reason;
      throw new Error(`unknown navigation mode reason: ${String(exhaustive)}`);
    }
  }
}

// Default mode for a freshly opened session with no deep link. The
// contract says "Normal session navigation lands at the live tail."
export const DEFAULT_NAVIGATION_MODE: NavigationMode = "live-tail";

// transitionNavigationMode is the only function that mutates the
// navigation mode. It is pure: given (currentMode, reason) it returns
// the next mode and a NavigationModeTransition record. The transition
// record is the wire shape for telemetry and structured logs.
//
// One subtle rule: virtuoso-at-bottom-true is only honored when the
// current mode is historical-anchor. While in live-tail it is a no-op
// — the user is already at the tail, so an "I'm at the bottom" signal
// from the scroll library is information we already have. This
// asymmetry is the design defense against followOutput races: we never
// trust Virtuoso to TAKE us out of live-tail (we use explicit user
// gestures for that), only to return us to it.
export function transitionNavigationMode(
  current: NavigationMode,
  reason: NavigationModeReason,
): NavigationModeTransition {
  const target = targetModeForReason(reason);
  return {
    from: current,
    to: target,
    reason,
    changed: current !== target,
  };
}

// navigationModeTelemetryEvent maps a transition's target mode to the
// bounded server-side event-label name. The two labels are added to
// chatScrollMetricEventLabels in
// backend-go/cmd/tank-operator/handlers_client_metrics.go and are
// alerted on by the TankChatScrollUserAtBottomLatched rule in
// k8s/templates/observability.yaml.
export function navigationModeTelemetryEvent(
  mode: NavigationMode,
): "navigation-mode-entered-live-tail" | "navigation-mode-entered-historical-anchor" {
  return mode === "live-tail"
    ? "navigation-mode-entered-live-tail"
    : "navigation-mode-entered-historical-anchor";
}
