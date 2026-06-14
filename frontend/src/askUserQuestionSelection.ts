export interface AskUserQuestionSelectionInput {
  previousSelections: Record<string, string[]>;
  question: string;
  label: string;
  multiSelect: boolean;
}

export function nextAskUserQuestionSelections({
  previousSelections,
  question,
  label,
  multiSelect,
}: AskUserQuestionSelectionInput): Record<string, string[]> {
  const current = previousSelections[question] ?? [];
  if (multiSelect) {
    const next = current.includes(label)
      ? current.filter((selectedLabel) => selectedLabel !== label)
      : [...current, label];
    return { ...previousSelections, [question]: next };
  }
  const next = current.includes(label) ? [] : [label];
  return { ...previousSelections, [question]: next };
}
