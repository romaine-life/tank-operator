// Turn-level navigation affordance for the Turns view.
//
// The Turns view is becoming the primary surface for reading a session, so the
// turn selector (a dropdown) is no longer enough on its own: stepping one turn
// at a time, or jumping straight to the first / last turn, should be a single
// click rather than open-the-dropdown-then-hunt. This module owns the single,
// pure rule for that stepper given the ordered list of turn ids and the
// currently selected turn.
//
// The list is in submission order (oldest first, latest last) — the same order
// the Turns view renders and whose default selection is the latest turn. So
// "first" is the oldest turn and "last" is the newest, matching the per-turn
// page stepper where page 1 is the oldest sealed page (see turnActivityPager).
//
// Discipline mirrors turnActivityPagerState on purpose: the control is always
// present and merely *disables* a direction at the boundary, and a disabled
// direction's target equals the currently selected turn so a stray click is
// inert. Keeping the decision here (pure + unit-tested) is the regression guard
// against the boundary math drifting back into the render layer.

export type TurnViewNavigationState = {
  // 1-based position of the selected turn within the list, and the total turn
  // count. `position` is 0 only when there are no turns. Together they drive a
  // "turn 3 of 12" style label.
  position: number;
  count: number;
  // Human label, e.g. "turn 3 of 12". Empty when there are no turns.
  label: string;
  // Whether each control is actionable. first/prev share the "not already at
  // the oldest" condition; next/last share "not already at the newest".
  canFirst: boolean;
  canPrev: boolean;
  canNext: boolean;
  canLast: boolean;
  // The turn each control navigates to. When the matching can* flag is false
  // these equal the currently selected turn (or null when there are no turns),
  // so wiring a disabled-but-clicked control is inert rather than a jump.
  firstTurnId: string | null;
  prevTurnId: string | null;
  nextTurnId: string | null;
  lastTurnId: string | null;
};

const EMPTY: TurnViewNavigationState = {
  position: 0,
  count: 0,
  label: "",
  canFirst: false,
  canPrev: false,
  canNext: false,
  canLast: false,
  firstTurnId: null,
  prevTurnId: null,
  nextTurnId: null,
  lastTurnId: null,
};

export function turnViewTurnNavigation(
  turnIds: string[],
  selectedTurnId: string | null,
): TurnViewNavigationState {
  const count = turnIds.length;
  if (count === 0) return EMPTY;

  // Match the Turns view's own fallback: when the selection is absent or stale
  // (not in the list), the rendered "selected" turn is the latest one, so the
  // stepper anchors there too. This keeps the controls consistent with what the
  // reader actually sees instead of pointing at a turn that is not on screen.
  let index = selectedTurnId ? turnIds.indexOf(selectedTurnId) : -1;
  if (index < 0) index = count - 1;

  const current = turnIds[index]!;
  const canPrev = index > 0;
  const canNext = index < count - 1;
  return {
    position: index + 1,
    count,
    label: `turn ${index + 1} of ${count}`,
    canFirst: canPrev,
    canPrev,
    canNext,
    canLast: canNext,
    firstTurnId: turnIds[0]!,
    prevTurnId: canPrev ? turnIds[index - 1]! : current,
    nextTurnId: canNext ? turnIds[index + 1]! : current,
    lastTurnId: turnIds[count - 1]!,
  };
}
