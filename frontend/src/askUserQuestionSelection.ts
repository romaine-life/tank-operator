export interface AskUserQuestionSelectionInput {
  previousSelections: Record<string, string[]>;
  question: string;
  label: string;
  multiSelect: boolean;
}

// The synthetic "Something else" choice every AskUserQuestion carries. It is the
// default selection AND the "none of your concrete options — I'll say my own
// thing" sentinel, so a user is never trapped inside an agent's enumerated
// options. An agent's option list is a set of shortcuts, never a fence: the
// companion composer text works with ANY selection, and "Something else" is
// simply the selection that has no preset label behind it. See
// docs/features/transcript/contract.md -> "AskUserQuestion answer input".
export const SOMETHING_ELSE_LABEL = "Something else";

// effectiveAskUserQuestionSelection enforces the never-empty invariant at read
// time: a question with no stored selection (or one toggled back to empty)
// answers as the default "Something else". The reducer is free to store [] on a
// deselect; every reader (render, submit, answered-gate) routes through here so
// there is exactly one place that owns "there is no nothing-selected state."
export function effectiveAskUserQuestionSelection(
  selections: Record<string, string[]>,
  question: string,
): string[] {
  const stored = selections[question];
  return stored && stored.length > 0 ? stored : [SOMETHING_ELSE_LABEL];
}

export interface AskUserQuestionAnswerableQuestion {
  question: string;
  options: Array<{ label: string; preview?: string }>;
}

export interface AskUserQuestionAnswerPayload {
  answers: Record<string, string[]>;
  annotations: Record<string, { preview?: string; notes?: string }>;
}

// buildAskUserQuestionAnswerPayload assembles the wire payload from the current
// per-question selections + companion notes. It owns three contract invariants
// so they are provable in isolation (see
// docs/features/transcript/contract.md -> "AskUserQuestion answer input"):
//   1. Never-empty: a question with no explicit pick answers as the default
//      "Something else" sentinel, so `answers` always carries >=1 label per
//      question and an empty pass is a valid, honest answer.
//   2. Companion text works with ANY selection: it rides as annotations.notes
//      whether the selection is a real option or "Something else", and is never
//      dropped on an options-only question.
//   3. A selected real option's preview rides along as annotations.preview.
export function buildAskUserQuestionAnswerPayload(
  questions: AskUserQuestionAnswerableQuestion[],
  selections: Record<string, string[]>,
  notes: Record<string, string>,
): AskUserQuestionAnswerPayload {
  const answers: Record<string, string[]> = {};
  const annotations: Record<string, { preview?: string; notes?: string }> = {};
  for (const q of questions) {
    const labels = effectiveAskUserQuestionSelection(selections, q.question);
    const noteText = notes[q.question]?.trim() ?? "";
    answers[q.question] = labels;
    const preview = q.options.find((opt) => labels.includes(opt.label))?.preview;
    const ann: { preview?: string; notes?: string } = {};
    if (preview) ann.preview = preview;
    if (noteText) ann.notes = noteText;
    if (ann.preview || ann.notes) annotations[q.question] = ann;
  }
  return { answers, annotations };
}

export function nextAskUserQuestionSelections({
  previousSelections,
  question,
  label,
  multiSelect,
}: AskUserQuestionSelectionInput): Record<string, string[]> {
  const current = previousSelections[question] ?? [];
  if (!multiSelect) {
    // Radio: clicking the selected choice clears it (the read-time fallback
    // restores the "Something else" default); clicking any other choice
    // replaces. Storing [] here is fine — effectiveAskUserQuestionSelection
    // turns it back into the default at read time.
    const next = current.includes(label) ? [] : [label];
    return { ...previousSelections, [question]: next };
  }
  // Multi-select. "Something else" means "none of your concrete options," so it
  // is mutually exclusive with the real picks rather than a peer checkbox:
  // selecting it clears the real set, and selecting any real option drops it.
  // This keeps an incoherent "B, C, and also none-of-these" state impossible.
  if (label === SOMETHING_ELSE_LABEL) {
    const next = current.includes(SOMETHING_ELSE_LABEL)
      ? []
      : [SOMETHING_ELSE_LABEL];
    return { ...previousSelections, [question]: next };
  }
  const withoutSentinel = current.filter(
    (selectedLabel) => selectedLabel !== SOMETHING_ELSE_LABEL,
  );
  const next = withoutSentinel.includes(label)
    ? withoutSentinel.filter((selectedLabel) => selectedLabel !== label)
    : [...withoutSentinel, label];
  return { ...previousSelections, [question]: next };
}
