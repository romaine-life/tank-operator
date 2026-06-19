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

// effectiveAskUserQuestionSelection enforces the normal never-empty invariant
// at read time: a question with no stored selection (or one toggled back to
// empty) answers as the default "Something else". Plan approval can opt out by
// passing null because Approve/Request-changes are exhaustive there.
export function effectiveAskUserQuestionSelection(
  selections: Record<string, string[]>,
  question: string,
  defaultSelectionLabel: string | null = SOMETHING_ELSE_LABEL,
): string[] {
  const stored = selections[question];
  if (stored && stored.length > 0) return stored;
  return defaultSelectionLabel ? [defaultSelectionLabel] : [];
}

export interface AskUserQuestionAnswerableQuestion {
  question: string;
  options: Array<{ label: string; preview?: string }>;
  // Omitted preserves the AskUserQuestion safety fallback. Set null for
  // workflows like plan approval where the offered options are exhaustive.
  defaultSelectionLabel?: string | null;
  // Label to submit when the user typed free-form text without choosing a
  // concrete option. Defaults to defaultSelectionLabel.
  freeFormSelectionLabel?: string | null;
}

export interface AskUserQuestionAnswerPayload {
  answers: Record<string, string[]>;
  annotations: Record<string, { preview?: string; notes?: string }>;
}

// buildAskUserQuestionAnswerPayload assembles the wire payload from the current
// per-question selections + companion notes. It owns three contract invariants
// so they are provable in isolation (see
// docs/features/transcript/contract.md -> "AskUserQuestion answer input"):
//   1. Never-empty by default: a normal question with no explicit pick answers
//      as the default "Something else" sentinel, so `answers` carries >=1 label
//      per question and an empty pass is a valid, honest answer. Callers can
//      set defaultSelectionLabel=null for exhaustive workflows like plan
//      approval.
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
    const noteText = notes[q.question]?.trim() ?? "";
    const defaultSelectionLabel =
      q.defaultSelectionLabel === undefined
        ? SOMETHING_ELSE_LABEL
        : q.defaultSelectionLabel;
    const freeFormSelectionLabel =
      q.freeFormSelectionLabel === undefined
        ? defaultSelectionLabel
        : q.freeFormSelectionLabel;
    let labels = effectiveAskUserQuestionSelection(
      selections,
      q.question,
      defaultSelectionLabel,
    );
    if (labels.length === 0 && noteText && freeFormSelectionLabel) {
      labels = [freeFormSelectionLabel];
    }
    if (labels.length === 0) continue;
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
