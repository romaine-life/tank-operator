export type GlimmungRunLink = {
  key: string;
  label: string;
  href: string;
  project: string;
  issueNumber: number;
  runDisplay: string;
  state?: string;
  sourceEntryId?: string;
  sourceTurnId?: string;
  toolName?: string;
  observedAt?: string;
};

export type GlimmungRunTranscriptEntry = {
  id: string;
  kind: string;
  turnId?: string;
  toolKind?: string;
  toolName?: string;
  toolServer?: string;
  toolAction?: string;
  toolInput?: string;
  toolOutput?: string;
  time?: string;
  startedAt?: string;
  completedAt?: string;
  updatedAt?: string;
  [key: string]: unknown;
};

const GLIMMUNG_ORIGIN = "https://glimmung.romaine.life";
const RUN_REF_RE =
  /\b([A-Za-z0-9][A-Za-z0-9_-]*)#([1-9]\d*)\/runs\/([1-9]\d*(?:\.[1-9]\d*)?)\b/g;
const DASHBOARD_RUN_PATH_RE =
  /(?:https?:\/\/glimmung\.romaine\.life)?\/projects\/([^/\s?#]+)\/issues\/([1-9]\d*)\/runs\/([1-9]\d*)(?:\/cycles\/([1-9]\d*)|\.(\d+))?/g;

export function collectGlimmungRunsFromEntries(
  entries: readonly GlimmungRunTranscriptEntry[],
): GlimmungRunLink[] {
  const byKey = new Map<string, GlimmungRunLink>();
  for (const entry of entries) {
    if (!isRelevantGlimmungEntry(entry)) continue;
    for (const candidate of extractCandidatesFromEntry(entry)) {
      const key = `${candidate.project}#${candidate.issueNumber}/runs/${candidate.runDisplay}`;
      const existing = byKey.get(key);
      if (!existing || compareObservedAt(candidate, existing) >= 0) {
        byKey.set(key, candidate);
      }
    }
  }
  return Array.from(byKey.values()).sort(compareRunLinks);
}

function isRelevantGlimmungEntry(entry: GlimmungRunTranscriptEntry): boolean {
  if (entry.kind !== "tool") return false;
  const identity = [
    entry.toolKind,
    entry.toolServer,
    entry.toolName,
    entry.toolAction,
  ]
    .filter((value): value is string => typeof value === "string")
    .join(" ")
    .toLowerCase();
  if (identity.includes("glimmung")) return true;
  return textCandidates(entry).some((text) =>
    text.includes("glimmung.romaine.life/projects/"),
  );
}

function extractCandidatesFromEntry(
  entry: GlimmungRunTranscriptEntry,
): GlimmungRunLink[] {
  const observedAt =
    entry.completedAt ?? entry.updatedAt ?? entry.startedAt ?? entry.time;
  const candidates: GlimmungRunLink[] = [];
  const push = (
    project: string,
    issueNumber: number,
    runDisplay: string,
    state?: string,
  ) => {
    const normalizedProject = project.trim();
    const normalizedRun = normalizeRunDisplay(runDisplay);
    if (!normalizedProject || !Number.isInteger(issueNumber) || issueNumber < 1) {
      return;
    }
    if (!normalizedRun) return;
    const href = dashboardRunHref(normalizedProject, issueNumber, normalizedRun);
    candidates.push({
      key: `${normalizedProject}#${issueNumber}/runs/${normalizedRun}`,
      label: `${normalizedProject}#${issueNumber} run ${normalizedRun}`,
      href,
      project: normalizedProject,
      issueNumber,
      runDisplay: normalizedRun,
      state,
      sourceEntryId: entry.id,
      sourceTurnId: entry.turnId,
      toolName: entry.toolName,
      observedAt,
    });
  };

  for (const text of textCandidates(entry)) {
    for (const ref of parseRunRefs(text)) {
      push(ref.project, ref.issueNumber, ref.runDisplay);
    }
    for (const url of parseDashboardRunPaths(text)) {
      push(url.project, url.issueNumber, url.runDisplay);
    }
    const parsed = parseJson(text);
    if (parsed !== undefined) {
      for (const ref of runRefsFromJson(parsed)) {
        push(ref.project, ref.issueNumber, ref.runDisplay, ref.state);
      }
    }
  }
  return candidates;
}

function textCandidates(entry: GlimmungRunTranscriptEntry): string[] {
  const values = [
    entry.toolInput,
    entry.toolOutput,
    entry.toolName,
    entry.toolServer,
    entry.toolAction,
  ];
  return values.flatMap((value) =>
    typeof value === "string" && value.trim() ? [value] : [],
  );
}

function parseRunRefs(text: string): Array<{
  project: string;
  issueNumber: number;
  runDisplay: string;
}> {
  const out: Array<{ project: string; issueNumber: number; runDisplay: string }> =
    [];
  for (const match of text.matchAll(RUN_REF_RE)) {
    out.push({
      project: match[1] ?? "",
      issueNumber: Number(match[2]),
      runDisplay: match[3] ?? "",
    });
  }
  return out;
}

function parseDashboardRunPaths(text: string): Array<{
  project: string;
  issueNumber: number;
  runDisplay: string;
}> {
  const out: Array<{ project: string; issueNumber: number; runDisplay: string }> =
    [];
  for (const match of text.matchAll(DASHBOARD_RUN_PATH_RE)) {
    const run = match[3] ?? "";
    const cycle = match[4] ?? match[5] ?? "";
    if (!cycle) continue;
    out.push({
      project: decodeURIComponent(match[1] ?? ""),
      issueNumber: Number(match[2]),
      runDisplay: `${run}.${cycle}`,
    });
  }
  return out;
}

function parseJson(text: string): unknown {
  const trimmed = text.trim();
  if (!trimmed || (!trimmed.startsWith("{") && !trimmed.startsWith("["))) {
    return undefined;
  }
  try {
    return JSON.parse(trimmed);
  } catch {
    return undefined;
  }
}

function runRefsFromJson(value: unknown): Array<{
  project: string;
  issueNumber: number;
  runDisplay: string;
  state?: string;
}> {
  const out: Array<{
    project: string;
    issueNumber: number;
    runDisplay: string;
    state?: string;
  }> = [];
  const visit = (current: unknown) => {
    if (!current || typeof current !== "object") return;
    if (Array.isArray(current)) {
      for (const item of current) visit(item);
      return;
    }
    const record = current as Record<string, unknown>;
    const state = stringValue(record.state) ?? stringValue(record.status);
    const runRef = stringValue(record.run_ref) ?? stringValue(record.runRef);
    if (runRef) {
      for (const ref of parseRunRefs(runRef)) out.push({ ...ref, state });
    }
    const issueRef =
      stringValue(record.issue_ref) ??
      stringValue(record.issueRef) ??
      stringValue(record.created_issue_ref);
    const project =
      stringValue(record.project) ?? parseIssueRef(issueRef)?.project ?? "";
    const issueNumber =
      numberValue(record.issue_number) ??
      numberValue(record.issueNumber) ??
      parseIssueRef(issueRef)?.issueNumber;
    const runNumber =
      numberValue(record.run_number) ?? numberValue(record.runNumber);
    const cycleNumber =
      numberValue(record.cycle_number) ??
      numberValue(record.cycleNumber) ??
      numberValue(record.run_cycle_number) ??
      numberValue(record.runCycleNumber);
    if (project && issueNumber && runNumber && cycleNumber) {
      out.push({
        project,
        issueNumber,
        runDisplay: `${runNumber}.${cycleNumber}`,
        state,
      });
    }
    for (const child of Object.values(record)) visit(child);
  };
  visit(value);
  return out;
}

function parseIssueRef(
  ref: string | undefined,
): { project: string; issueNumber: number } | undefined {
  const match = /^([A-Za-z0-9][A-Za-z0-9_-]*)#([1-9]\d*)$/.exec(ref ?? "");
  if (!match) return undefined;
  return { project: match[1] ?? "", issueNumber: Number(match[2]) };
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function numberValue(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isInteger(value) && value > 0) {
    return value;
  }
  if (typeof value === "string" && /^[1-9]\d*$/.test(value.trim())) {
    return Number(value.trim());
  }
  return undefined;
}

function normalizeRunDisplay(value: string): string | null {
  const trimmed = value.trim();
  if (!/^[1-9]\d*(?:\.[1-9]\d*)?$/.test(trimmed)) return null;
  return trimmed;
}

function dashboardRunHref(
  project: string,
  issueNumber: number,
  runDisplay: string,
): string {
  const [run, cycle] = runDisplay.split(".");
  if (run && cycle) {
    return `${GLIMMUNG_ORIGIN}/projects/${encodeURIComponent(project)}/issues/${issueNumber}/runs/${run}/cycles/${cycle}`;
  }
  return `${GLIMMUNG_ORIGIN}/projects/${encodeURIComponent(project)}/issues/${issueNumber}/runs`;
}

function compareObservedAt(a: GlimmungRunLink, b: GlimmungRunLink): number {
  return Date.parse(a.observedAt ?? "") - Date.parse(b.observedAt ?? "");
}

function compareRunLinks(a: GlimmungRunLink, b: GlimmungRunLink): number {
  const time = compareObservedAt(b, a);
  if (time !== 0 && Number.isFinite(time)) return time;
  const project = a.project.localeCompare(b.project);
  if (project !== 0) return project;
  if (a.issueNumber !== b.issueNumber) return b.issueNumber - a.issueNumber;
  return compareRunDisplay(b.runDisplay, a.runDisplay);
}

function compareRunDisplay(a: string, b: string): number {
  const aParts = a.split(".").map(Number);
  const bParts = b.split(".").map(Number);
  const aRun = aParts[0] ?? 0;
  const aCycle = aParts[1] ?? 0;
  const bRun = bParts[0] ?? 0;
  const bCycle = bParts[1] ?? 0;
  if (aRun !== bRun) return aRun - bRun;
  return aCycle - bCycle;
}
