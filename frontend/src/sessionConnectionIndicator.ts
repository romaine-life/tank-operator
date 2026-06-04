// User-visible policy for the per-session SSE connection indicator.
//
// The raw stream lifecycle remains telemetry-owned: App.tsx still records
// opened/ready/error/reconnect/resync events as they happen. This module owns
// the smaller product question: which of those states are worth app chrome?
// Routine stream setup after tab/session reactivation is not user-visible
// until it lasts long enough to affect trust in the live tail.

export type SdkConnectionState =
  | "idle"
  | "connecting"
  | "connected"
  | "connection_lost"
  | "resyncing";

export const CONNECTION_CONNECTING_VISIBLE_AFTER_MS = 1000;

export const CONNECTION_RECONNECTING_LABEL = "reconnecting";
export const CONNECTION_LOST_LABEL = "connection lost";
export const CONNECTION_RESYNCING_LABEL = "resyncing";

export interface SessionConnectionIndicatorContext {
  /** Raw stream lifecycle state for the pane. */
  state: SdkConnectionState;
  /** Whether this pane is the visible/active workspace pane. */
  visible: boolean;
  /** The active workspace tab. */
  activeTab: string;
  /** True after connecting has exceeded the display threshold. */
  delayedConnectingVisible: boolean;
}

export function sessionConnectionIndicatorLabel(
  ctx: SessionConnectionIndicatorContext,
): string | null {
  if (!ctx.visible) return null;
  if (ctx.activeTab !== "chat") return null;

  switch (ctx.state) {
    case "connecting":
      return ctx.delayedConnectingVisible ? CONNECTION_RECONNECTING_LABEL : null;
    case "connection_lost":
      return CONNECTION_LOST_LABEL;
    case "resyncing":
      return CONNECTION_RESYNCING_LABEL;
    case "idle":
    case "connected":
      return null;
  }
}
