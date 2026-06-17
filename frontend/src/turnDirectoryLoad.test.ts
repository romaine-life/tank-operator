import { describe, test, expect } from "vitest";

import {
  evaluateTurnDirectoryReconcile,
  evaluateStuckWatchdog,
  shouldArmStuckWatchdog,
  TURN_DIRECTORY_LOAD_TIMEOUT_MS,
  TURN_DIRECTORY_STUCK_THRESHOLD_MS,
  type TurnDirectoryReconcileInput,
  type TurnDirectoryStatus,
} from "./turnDirectoryLoad.ts";

function reconcileInput(
  over: Partial<TurnDirectoryReconcileInput> = {},
): TurnDirectoryReconcileInput {
  return {
    visible: true,
    blockedPublicView: false,
    status: "idle",
    hasLiveLoadForSession: false,
    ...over,
  };
}

describe("evaluateTurnDirectoryReconcile (level-triggered load rule)", () => {
  test("fresh visible pane with nothing loaded starts an 'open' load", () => {
    expect(evaluateTurnDirectoryReconcile(reconcileInput({ status: "idle" }))).toEqual({
      action: "load",
      source: "open",
    });
  });

  // The strand fix, pinned: a pane left at "loading" with no in-flight load —
  // the exact state the retired latch produced on a cross-session supersede —
  // ALWAYS re-drives a load rather than spinning forever. The "reconcile"
  // source is the durable signal that the strand was caught and auto-recovered.
  test("'loading' with no live load re-drives a 'reconcile' load (strand heal)", () => {
    expect(
      evaluateTurnDirectoryReconcile(
        reconcileInput({ status: "loading", hasLiveLoadForSession: false }),
      ),
    ).toEqual({ action: "load", source: "reconcile" });
  });

  test("'loading' with a genuine in-flight load for this session does nothing", () => {
    expect(
      evaluateTurnDirectoryReconcile(
        reconcileInput({ status: "loading", hasLiveLoadForSession: true }),
      ),
    ).toEqual({ action: "idle" });
  });

  test("'ready' is the desired state — no load", () => {
    expect(evaluateTurnDirectoryReconcile(reconcileInput({ status: "ready" }))).toEqual({
      action: "idle",
    });
  });

  // error must NOT auto-retry from the reconciler, or a hard 5xx becomes a hot
  // loop hammering the endpoint. Recovery from error is the explicit Retry
  // button or a session change (which resets status to idle).
  test("'error' is terminal for the reconciler — no auto-retry hot loop", () => {
    for (const hasLiveLoadForSession of [false, true]) {
      expect(
        evaluateTurnDirectoryReconcile(
          reconcileInput({ status: "error", hasLiveLoadForSession }),
        ),
      ).toEqual({ action: "idle" });
    }
  });

  test("a hidden pane never loads (it reconciles when shown again)", () => {
    for (const status of ["idle", "loading"] as TurnDirectoryStatus[]) {
      expect(
        evaluateTurnDirectoryReconcile(reconcileInput({ visible: false, status })),
      ).toEqual({ action: "idle" });
    }
  });

  test("a public surface with no usable token does nothing", () => {
    expect(
      evaluateTurnDirectoryReconcile(
        reconcileInput({ blockedPublicView: true, status: "idle" }),
      ),
    ).toEqual({ action: "idle" });
  });

  // Exhaustive truth table: for every (visible, status, hasLiveLoadForSession)
  // there is exactly one decision, and a visible non-terminal pane with nothing
  // in flight is NEVER left idle. This is the property that makes a
  // strand-without-recovery unrepresentable.
  test("exhaustive: visible non-terminal + no live load always loads", () => {
    const statuses: TurnDirectoryStatus[] = ["idle", "loading", "ready", "error"];
    for (const status of statuses) {
      for (const hasLiveLoadForSession of [false, true]) {
        const decision = evaluateTurnDirectoryReconcile(
          reconcileInput({ visible: true, status, hasLiveLoadForSession }),
        );
        const terminal = status === "ready" || status === "error";
        if (!terminal && !hasLiveLoadForSession) {
          expect(decision.action).toBe("load");
        } else {
          expect(decision.action).toBe("idle");
        }
      }
    }
  });
});

describe("evaluateStuckWatchdog", () => {
  test("non-terminal + no live load: emit the stuck signal and recover", () => {
    expect(
      evaluateStuckWatchdog({ status: "loading", hasLiveLoadForSession: false }),
    ).toEqual({ emitStuck: true, recover: true });
    expect(
      evaluateStuckWatchdog({ status: "idle", hasLiveLoadForSession: false }),
    ).toEqual({ emitStuck: true, recover: true });
  });

  test("non-terminal + a live (slow) load: emit the signal, do NOT duplicate", () => {
    expect(
      evaluateStuckWatchdog({ status: "loading", hasLiveLoadForSession: true }),
    ).toEqual({ emitStuck: true, recover: false });
  });

  test("terminal states never report stuck", () => {
    for (const status of ["ready", "error"] as TurnDirectoryStatus[]) {
      expect(
        evaluateStuckWatchdog({ status, hasLiveLoadForSession: false }),
      ).toEqual({ emitStuck: false, recover: false });
    }
  });
});

describe("shouldArmStuckWatchdog", () => {
  test("armed only while a visible pane shows the spinner", () => {
    expect(shouldArmStuckWatchdog("idle", true)).toBe(true);
    expect(shouldArmStuckWatchdog("loading", true)).toBe(true);
    expect(shouldArmStuckWatchdog("ready", true)).toBe(false);
    expect(shouldArmStuckWatchdog("error", true)).toBe(false);
    expect(shouldArmStuckWatchdog("idle", false)).toBe(false);
    expect(shouldArmStuckWatchdog("loading", false)).toBe(false);
  });
});

describe("thresholds", () => {
  test("the stuck watchdog fires strictly before a live load times out", () => {
    // A true strand self-heals well before a slow-but-live load is abandoned.
    expect(TURN_DIRECTORY_STUCK_THRESHOLD_MS).toBeLessThan(
      TURN_DIRECTORY_LOAD_TIMEOUT_MS,
    );
  });
});
