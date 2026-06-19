import { authedFetch } from "./auth";

// Fire-and-forget browser beacon for the sidebar-drag lifecycle. Each step is
// recorded server-side on tank_session_drag_step_total so a "the drag does
// nothing" report is diagnosable from the metrics stack (the last step that
// increments localizes where the gesture dies) instead of the user's DevTools.
//
// Telemetry must never affect the gesture it observes: every failure is
// swallowed, and the call is not awaited.
export type DragStep =
  | "mousedown"
  | "dragstart"
  | "dragover"
  | "drop"
  | "persist";

export function reportDragStep(step: DragStep, detail = ""): void {
  try {
    void authedFetch("/api/client-metrics/session-drag-step", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ step, detail }),
    }).catch(() => {});
  } catch {
    /* never throw from telemetry */
  }
}
