export type ConversationActivityStatus =
  | "ready"
  | "submitted"
  | "streaming"
  | "needs_input"
  | "stopping"
  | "stopped"
  | "error";

export interface SessionActivitySummary {
  session_id: string;
  status: ConversationActivityStatus;
  last_order_key: string | null;
  unread_count: number;
  needs_input: boolean;
  failed: boolean;
  active_turn_id: string | null;
  updated_at: string | null;
}

export interface SessionActivityLegendItem {
  key: string;
  label: string;
  detail: string;
  dotStatus: string | null;
}

export const SESSION_ACTIVITY_STATUS_LEGEND: SessionActivityLegendItem[] = [
  {
    key: "ready",
    label: "Ready / waiting",
    detail: "No active turn is running.",
    dotStatus: "agent-waiting",
  },
  {
    key: "running",
    label: "Submitted / running",
    detail: "The agent has queued or active work.",
    dotStatus: "agent-working",
  },
  {
    key: "needs-input",
    label: "Needs input",
    detail: "The session is waiting for your response.",
    dotStatus: "agent-needs-input",
  },
  {
    key: "stopping",
    label: "Stopping",
    detail: "A stop request is being applied.",
    dotStatus: "agent-stopping",
  },
  {
    key: "stopped",
    label: "Stopped",
    detail: "The latest turn was stopped.",
    dotStatus: "agent-stopped",
  },
  {
    key: "failed",
    label: "Failed",
    detail: "The latest turn ended with an error.",
    dotStatus: "agent-error",
  },
];

export function normalizeSessionActivity(value: unknown): SessionActivitySummary | null {
  if (!isRecord(value)) return null;
  const sessionId = stringField(value, "session_id");
  if (!sessionId) return null;
  const status = activityStatus(stringField(value, "status")) ?? "ready";
  return {
    session_id: sessionId,
    status,
    last_order_key: nullableStringField(value, "last_order_key"),
    unread_count: nonNegativeInt(value.unread_count),
    needs_input: value.needs_input === true,
    failed: value.failed === true,
    active_turn_id: nullableStringField(value, "active_turn_id"),
    updated_at: nullableStringField(value, "updated_at"),
  };
}

export function sessionActivityDotStatus(
  sessionStatus: string,
  isChatMode: boolean,
  activity?: SessionActivitySummary,
): string {
  if (!isChatMode || sessionStatus !== "Active") return sessionStatus.toLowerCase();
  if (activity?.failed || activity?.status === "error") return "agent-error";
  if (activity?.needs_input || activity?.status === "needs_input") return "agent-needs-input";
  if (activity?.status === "stopping") return "agent-stopping";
  if (activity?.status === "stopped") return "agent-stopped";
  if (activity?.status === "submitted" || activity?.status === "streaming") {
    return "agent-working";
  }
  return "agent-waiting";
}

export function sessionActivityStatusLabel(
  sessionStatus: string,
  isChatMode: boolean,
  activity?: SessionActivitySummary,
): string {
  if (!isChatMode || sessionStatus !== "Active") return sessionStatus;
  if (activity?.failed || activity?.status === "error") return "Failed";
  if (activity?.needs_input || activity?.status === "needs_input") return "Needs input";
  if (activity?.status === "submitted") return "Submitted";
  if (activity?.status === "streaming") return "Running";
  if (activity?.status === "stopping") return "Stopping";
  if (activity?.status === "stopped") return "Stopped";
  return "Waiting";
}

// orderKeyAfter compares two BIGSERIAL-shaped order_key strings
// numerically. Lexicographic string compare looks correct most of the
// time but breaks at digit-count boundaries: `"100" <= "99"` is true
// as strings even though 100 > 99 numerically. We hit that in the
// turn-complete-sound dedup gate — every transition that crossed a
// power-of-ten boundary was silenced. Parse with BigInt to handle
// values past Number.MAX_SAFE_INTEGER for long-running ledgers; fall
// back to string compare if parsing fails (defensive only — backend
// formats with strconv.FormatInt).
export function orderKeyAfter(later: string, earlier: string): boolean {
  try {
    return BigInt(later) > BigInt(earlier);
  } catch {
    return later > earlier;
  }
}

// shouldRingForActivityTransition is the centralized "play the turn-complete
// sound?" predicate. The session-list SSE consumer in App.tsx calls it on
// every session.activity_changed delivery; one place owns the
// transition-to-user-turn rule.
//
// Modeled on Mattermost's shouldSkipNotification and Zulip's
// should_send_audible_notification (docs/product-inspirations.md). Centralizing
// the predicate keeps the transition matrix testable and prevents per-pane
// drift — the bug class that produced the "sound only plays when I return to
// the session" regression (per-pane SSE was the only place this logic lived,
// and it stopped firing while the pane was hidden).
//
// Returns true when the new state is "user's turn" AND the prior state wasn't
// already a user-turn state. Three cases ring:
//   1. working → ready/needs_input (the canonical "your turn now" transition)
//   2. error → ready/needs_input (agent recovered from a previous failure)
//   3. no prior → ready/needs_input (first activity_changed the page sees for
//      this session — including fast turns where the backend's
//      lifecycle_emitter coalesces submitted → streaming → ready into a
//      single activity_changed event because all three chat events landed
//      in the persister's fold window. We observed this directly with
//      haiku-fast turns: only one activity_changed event arrives, with
//      status=ready and no prior in the page's session state)
//
// Does NOT ring on user-initiated stops (stopping/stopped → ready) or terminal
// errors (error stays as error), because the user is presumably already aware
// of the action they just took or the failure they're already looking at.
//
// The bootstrap-replay safety net is provided by two outer gates in App.tsx,
// NOT this predicate: (a) activitySnapshotAppliedRef silences events until
// /api/sessions has returned; (b) lastSoundedOrderKeyRef seeded from the
// snapshot's per-session last_order_key dedups SSE catchup replays of events
// already represented in the snapshot. A session absent from the snapshot
// activity map has no historical activity_changed events in the ledger
// (sessions.Reader.fillActivity calls LatestActivity, which is what the
// catchup loop replays from); so no SSE catchup event can land here with
// prior=undefined except a genuinely-fresh post-bootstrap event.
export function shouldRingForActivityTransition(
  prior: SessionActivitySummary | undefined,
  next: SessionActivitySummary,
): boolean {
  const isUserTurn = (status: ConversationActivityStatus | undefined): boolean =>
    status === "ready" || status === "needs_input";
  if (!isUserTurn(next.status)) return false;
  if (prior && isUserTurn(prior.status)) return false;
  // Don't ring on stop-then-ready: the user just pressed Stop and the agent
  // winding back to ready is the expected consequence, not a new signal.
  if (prior && (prior.status === "stopping" || prior.status === "stopped")) {
    return false;
  }
  return true;
}

function activityStatus(value: string | null): ConversationActivityStatus | null {
  switch (value) {
    case "ready":
    case "submitted":
    case "streaming":
    case "needs_input":
    case "stopping":
    case "stopped":
    case "error":
      return value;
    default:
      return null;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function stringField(value: Record<string, unknown>, key: string): string | null {
  const field = value[key];
  return typeof field === "string" && field ? field : null;
}

function nullableStringField(value: Record<string, unknown>, key: string): string | null {
  const field = value[key];
  return typeof field === "string" && field ? field : null;
}

function nonNegativeInt(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return 0;
  return Math.max(0, Math.trunc(value));
}
