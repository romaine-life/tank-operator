export interface BugLabelFilterCandidate {
  name: string;
  slug: string;
  display_name: string;
}

export function normalizeBugLabelDisplayName(value: string | null | undefined): string {
  const trimmed = (value ?? "").trim().replace(/\s+/g, " ");
  return trimmed.replace(/^bug:\s*/i, "").trim();
}

export function bugLabelNameKey(name: string): string {
  return normalizeBugLabelDisplayName(name).toLocaleLowerCase();
}

export function addBugLabelName(labels: string[], name: string): string[] {
  const displayName = normalizeBugLabelDisplayName(name);
  if (!displayName) return labels;
  const key = bugLabelNameKey(displayName);
  if (labels.some((label) => bugLabelNameKey(label) === key)) return labels;
  return [...labels, displayName];
}

export function filterBugLabelSuggestions<T extends BugLabelFilterCandidate>(
  labels: T[],
  query: string,
): T[] {
  const normalizedQuery = bugLabelNameKey(query);
  if (!normalizedQuery) return labels;
  const rawQuery = query.trim().toLocaleLowerCase();
  return labels.filter((label) => {
    const displayName = bugLabelNameKey(label.display_name);
    const name = bugLabelNameKey(label.name);
    const slug = label.slug.toLocaleLowerCase();
    return (
      displayName.includes(normalizedQuery) ||
      name.includes(normalizedQuery) ||
      slug.includes(rawQuery) ||
      slug.includes(normalizedQuery)
    );
  });
}
