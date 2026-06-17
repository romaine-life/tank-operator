import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ExternalLinkIcon,
  GitBranchIcon,
  GitPullRequestIcon,
  Loader2Icon,
  PlayIcon,
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
} from "lucide-react";
import { authedEventSource, authedFetch } from "./auth";

type OrchestrationState =
  | "draft"
  | "approved"
  | "running"
  | "awaiting_review"
  | "done"
  | "failed";

type PhaseStatus =
  | "pending"
  | "ready"
  | "running"
  | "pr_open"
  | "merged"
  | "blocked"
  | "skipped";

type PhaseTarget = "main" | "integration";

type OrchestrationRun = {
  id: string;
  repo: string;
  repo_owner: string;
  repo_name: string;
  integration_branch?: string;
  state: OrchestrationState;
  phase_count: number;
  created_at?: string;
  updated_at?: string;
};

type OrchestrationPhase = {
  phase_id: string;
  key: string;
  brief: string;
  depends_on: string[];
  target: PhaseTarget;
  status: PhaseStatus;
  spoke_session_id?: string;
  pr_number?: number;
  pr_url?: string;
  merge_sha?: string;
};

type OrchestrationDetail = {
  orchestration: OrchestrationRun;
  phases: OrchestrationPhase[];
};

type DraftPhase = {
  key: string;
  brief: string;
  depends_on: string[];
  target: PhaseTarget;
};

const newPhase = (index: number): DraftPhase => ({
  key: index === 0 ? "root" : `phase-${index + 1}`,
  brief: "",
  depends_on: [],
  target: "main",
});

function pickString(value: any, ...keys: string[]): string {
  for (const key of keys) {
    const next = value?.[key];
    if (typeof next === "string") return next;
  }
  return "";
}

function pickNumber(value: any, ...keys: string[]): number {
  for (const key of keys) {
    const next = value?.[key];
    if (typeof next === "number") return next;
  }
  return 0;
}

function normalizeRun(value: any): OrchestrationRun {
  const repoOwner = pickString(value, "repo_owner", "RepoOwner");
  const repoName = pickString(value, "repo_name", "RepoName");
  return {
    id: pickString(value, "id", "orchestration_id", "OrchestrationID"),
    repo: pickString(value, "repo") || `${repoOwner}/${repoName}`,
    repo_owner: repoOwner,
    repo_name: repoName,
    integration_branch: pickString(value, "integration_branch", "IntegrationBranch"),
    state: pickString(value, "state", "State") as OrchestrationState,
    phase_count: pickNumber(value, "phase_count", "PhaseCount"),
    created_at: pickString(value, "created_at", "CreatedAt"),
    updated_at: pickString(value, "updated_at", "UpdatedAt"),
  };
}

function normalizePhase(value: any): OrchestrationPhase {
  return {
    phase_id: pickString(value, "phase_id", "PhaseID"),
    key: pickString(value, "key", "Key"),
    brief: pickString(value, "brief", "Brief"),
    depends_on: Array.isArray(value?.depends_on)
      ? value.depends_on
      : Array.isArray(value?.DependsOn)
        ? value.DependsOn
        : [],
    target: (pickString(value, "target", "Target") || "main") as PhaseTarget,
    status: pickString(value, "status", "Status") as PhaseStatus,
    spoke_session_id: pickString(value, "spoke_session_id", "SpokeSessionID"),
    pr_number: pickNumber(value, "pr_number", "PRNumber"),
    pr_url: pickString(value, "pr_url", "PRURL"),
    merge_sha: pickString(value, "merge_sha", "MergeSHA"),
  };
}

async function readJSON(path: string): Promise<any> {
  const res = await authedFetch(path);
  if (!res.ok) throw new Error(`${path} returned ${res.status}`);
  return res.json();
}

function statusLabel(value: string): string {
  return value.replaceAll("_", " ");
}

function formatTime(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function upsertRun(runs: OrchestrationRun[], run: OrchestrationRun): OrchestrationRun[] {
  if (!run.id) return runs;
  const next = runs.filter((item) => item.id !== run.id);
  return [run, ...next];
}

export function OrchestrationsDashboard() {
  const [runs, setRuns] = useState<OrchestrationRun[]>([]);
  const [detail, setDetail] = useState<OrchestrationDetail | null>(null);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [loadingList, setLoadingList] = useState(false);
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [launching, setLaunching] = useState(false);
  const [approving, setApproving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [repoOwner, setRepoOwner] = useState("romaine-life");
  const [repoName, setRepoName] = useState("tank-operator");
  const [phases, setPhases] = useState<DraftPhase[]>([newPhase(0)]);
  const orchestrationEventSourceRef = useRef<EventSource | null>(null);

  const loadList = useCallback(async () => {
    setLoadingList(true);
    setError(null);
    try {
      const body = await readJSON("/api/orchestrations");
      const items = Array.isArray(body?.orchestrations) ? body.orchestrations : [];
      setRuns(items.map(normalizeRun));
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to load orchestrations");
    } finally {
      setLoadingList(false);
    }
  }, []);

  const loadDetail = useCallback(async (id: string, quiet = false) => {
    if (!quiet) setLoadingDetail(true);
    setError(null);
    try {
      const body = await readJSON(`/api/orchestrations/${encodeURIComponent(id)}`);
      setDetail({
        orchestration: normalizeRun(body?.orchestration ?? body?.Orchestration),
        phases: (Array.isArray(body?.phases) ? body.phases : body?.Phases ?? []).map(normalizePhase),
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to load orchestration");
    } finally {
      if (!quiet) setLoadingDetail(false);
    }
  }, []);

  useEffect(() => {
    void loadList();
  }, [loadList]);

  useEffect(() => {
    if (!selectedID) return;
    void loadDetail(selectedID);
  }, [loadDetail, selectedID]);

  useEffect(() => {
    if (!selectedID) return;
    const streamID = selectedID;
    let canceled = false;
    orchestrationEventSourceRef.current?.close();
    orchestrationEventSourceRef.current = null;

    async function openStream() {
      try {
        const source = await authedEventSource(
          `/api/orchestrations/${encodeURIComponent(streamID)}/events`,
          { stream: "orchestration-events", sessionId: streamID },
        );
        if (canceled) {
          source.close();
          return;
        }
        orchestrationEventSourceRef.current = source;
        source.addEventListener("orchestration-snapshot", (event) => {
          const message = event as MessageEvent;
          let parsed: any;
          try {
            parsed = JSON.parse(String(message.data));
          } catch {
            return;
          }
          setDetail({
            orchestration: normalizeRun(parsed?.orchestration ?? parsed?.Orchestration),
            phases: (Array.isArray(parsed?.phases) ? parsed.phases : parsed?.Phases ?? []).map(normalizePhase),
          });
          setRuns((current) => upsertRun(current, normalizeRun(parsed?.orchestration ?? parsed?.Orchestration)));
        });
        source.addEventListener("phase-status", (event) => {
          const message = event as MessageEvent;
          let parsed: any;
          try {
            parsed = JSON.parse(String(message.data));
          } catch {
            return;
          }
          const phase = normalizePhase(parsed?.phase);
          if (!phase.key && !phase.phase_id) return;
          setDetail((current) => {
            if (!current || current.orchestration.id !== streamID) return current;
            return {
              ...current,
              phases: current.phases.map((existing) =>
                existing.phase_id === phase.phase_id || existing.key === phase.key ? phase : existing,
              ),
            };
          });
        });
        source.addEventListener("stream-error", (event) => {
          const message = event as MessageEvent;
          setError(message.data ? String(message.data) : "orchestration event stream failed");
        });
        source.onerror = () => {
          setError("orchestration event stream disconnected");
        };
      } catch (err) {
        if (!canceled) {
          setError(err instanceof Error ? err.message : "failed to open orchestration event stream");
        }
      }
    }

    void openStream();
    return () => {
      canceled = true;
      orchestrationEventSourceRef.current?.close();
      orchestrationEventSourceRef.current = null;
    };
  }, [selectedID]);

  const phaseKeys = useMemo(
    () => phases.map((phase) => phase.key.trim()).filter(Boolean),
    [phases],
  );
  const canLaunch =
    repoOwner.trim() !== "" &&
    repoName.trim() !== "" &&
    phases.length > 0 &&
    phases.every((phase) => phase.key.trim() !== "" && phase.brief.trim() !== "");

  function updatePhase(index: number, patch: Partial<DraftPhase>) {
    setPhases((current) =>
      current.map((phase, i) => (i === index ? { ...phase, ...patch } : phase)),
    );
  }

  async function launch() {
    if (!canLaunch || launching) return;
    setLaunching(true);
    setError(null);
    try {
      const res = await authedFetch("/api/orchestrations", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          repo_owner: repoOwner.trim(),
          repo_name: repoName.trim(),
          phases: phases.map((phase) => ({
            key: phase.key.trim(),
            brief: phase.brief.trim(),
            depends_on: phase.depends_on,
            target: phase.target,
          })),
        }),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) {
        throw new Error(String(body?.detail ?? `create returned ${res.status}`));
      }
      const run = normalizeRun(body?.orchestration ?? body?.Orchestration);
      await loadList();
      setSelectedID(run.id);
      setDetail({
        orchestration: run,
        phases: (Array.isArray(body?.phases) ? body.phases : body?.Phases ?? []).map(normalizePhase),
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to launch orchestration");
    } finally {
      setLaunching(false);
    }
  }

  async function approve() {
    if (!detail || approving) return;
    setApproving(true);
    setError(null);
    try {
      const id = detail.orchestration.id;
      const res = await authedFetch(`/api/orchestrations/${encodeURIComponent(id)}/review/approve`, {
        method: "POST",
      });
      if (!res.ok) throw new Error(`approve returned ${res.status}`);
      await loadDetail(id, true);
      await loadList();
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to approve orchestration");
    } finally {
      setApproving(false);
    }
  }

  return (
    <div className="run-settings-diagnostics orchestrations-dashboard">
      <div className="run-settings-diagnostics-head">
        <span className="run-settings-link-label">
          <GitBranchIcon className="run-settings-link-icon" aria-hidden="true" />
          <span>Orchestrations</span>
        </span>
        <button
          type="button"
          className="run-settings-icon-btn"
          onClick={() => void loadList()}
          aria-label="Refresh orchestrations"
          title="Refresh orchestrations"
        >
          {loadingList ? <Loader2Icon className="spin" aria-hidden="true" /> : <RefreshCwIcon aria-hidden="true" />}
        </button>
      </div>
      {error && <div className="orchestrations-error">{error}</div>}
      <div className="orchestrations-layout">
        <section className="orchestrations-list-panel" aria-label="Orchestration runs">
          <div className="orchestrations-table-wrap">
            <table className="orchestrations-table">
              <thead>
                <tr>
                  <th>State</th>
                  <th>Repo</th>
                  <th>Phases</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((run) => (
                  <tr
                    key={run.id}
                    className={selectedID === run.id ? "is-selected" : ""}
                    onClick={() => setSelectedID(run.id)}
                  >
                    <td><StateBadge state={run.state} /></td>
                    <td>{run.repo}</td>
                    <td>{run.phase_count}</td>
                    <td>{formatTime(run.updated_at)}</td>
                  </tr>
                ))}
                {runs.length === 0 && (
                  <tr>
                    <td colSpan={4} className="orchestrations-empty">
                      {loadingList ? "Loading runs..." : "No orchestration runs"}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          <LaunchForm
            repoOwner={repoOwner}
            repoName={repoName}
            phases={phases}
            phaseKeys={phaseKeys}
            launching={launching}
            canLaunch={canLaunch}
            onRepoOwner={setRepoOwner}
            onRepoName={setRepoName}
            onPhase={updatePhase}
            onAddPhase={() => setPhases((current) => [...current, newPhase(current.length)])}
            onRemovePhase={(index) => setPhases((current) => current.filter((_, i) => i !== index))}
            onLaunch={() => void launch()}
          />
        </section>
        <section className="orchestrations-detail-panel" aria-label="Orchestration detail">
          {detail ? (
            <RunDetail
              detail={detail}
              loading={loadingDetail}
              approving={approving}
              onApprove={() => void approve()}
            />
          ) : (
            <div className="orchestrations-empty">Select a run to watch its phases</div>
          )}
        </section>
      </div>
    </div>
  );
}

function StateBadge({ state }: { state: OrchestrationState | PhaseStatus }) {
  return <span className={`orchestration-badge is-${state}`}>{statusLabel(state)}</span>;
}

function LaunchForm({
  repoOwner,
  repoName,
  phases,
  phaseKeys,
  launching,
  canLaunch,
  onRepoOwner,
  onRepoName,
  onPhase,
  onAddPhase,
  onRemovePhase,
  onLaunch,
}: {
  repoOwner: string;
  repoName: string;
  phases: DraftPhase[];
  phaseKeys: string[];
  launching: boolean;
  canLaunch: boolean;
  onRepoOwner: (value: string) => void;
  onRepoName: (value: string) => void;
  onPhase: (index: number, patch: Partial<DraftPhase>) => void;
  onAddPhase: () => void;
  onRemovePhase: (index: number) => void;
  onLaunch: () => void;
}) {
  return (
    <div className="orchestration-launch">
      <div className="orchestration-launch-head">
        <span>New run</span>
        <button type="button" className="run-settings-back-btn" onClick={onAddPhase}>
          <PlusIcon aria-hidden="true" />
          <span>Phase</span>
        </button>
      </div>
      <div className="break-glass-grid">
        <label className="run-settings-label">
          Repo owner
          <input className="break-glass-input" value={repoOwner} onChange={(event) => onRepoOwner(event.target.value)} />
        </label>
        <label className="run-settings-label">
          Repo name
          <input className="break-glass-input" value={repoName} onChange={(event) => onRepoName(event.target.value)} />
        </label>
      </div>
      <div className="orchestration-phase-editor">
        {phases.map((phase, index) => (
          <div className="orchestration-phase-draft" key={index}>
            <div className="orchestration-phase-draft-head">
              <label className="run-settings-label">
                Key
                <input
                  className="break-glass-input"
                  value={phase.key}
                  onChange={(event) => onPhase(index, { key: event.target.value })}
                />
              </label>
              <label className="run-settings-label">
                Target
                <select
                  className="break-glass-input"
                  value={phase.target}
                  onChange={(event) => onPhase(index, { target: event.target.value as PhaseTarget })}
                >
                  <option value="main">main</option>
                  <option value="integration">integration</option>
                </select>
              </label>
              <button
                type="button"
                className="run-settings-icon-btn"
                onClick={() => onRemovePhase(index)}
                disabled={phases.length === 1}
                aria-label="Remove phase"
                title="Remove phase"
              >
                <Trash2Icon aria-hidden="true" />
              </button>
            </div>
            <label className="run-settings-label">
              Brief
              <textarea
                className="break-glass-input orchestration-brief"
                value={phase.brief}
                onChange={(event) => onPhase(index, { brief: event.target.value })}
              />
            </label>
            <label className="run-settings-label">
              Depends on
              <select
                className="break-glass-input orchestration-deps"
                multiple
                value={phase.depends_on}
                onChange={(event) =>
                  onPhase(index, {
                    depends_on: Array.from(event.target.selectedOptions).map((option) => option.value),
                  })
                }
              >
                {phaseKeys
                  .filter((key) => key !== phase.key.trim())
                  .map((key) => (
                    <option key={key} value={key}>
                      {key}
                    </option>
                  ))}
              </select>
            </label>
          </div>
        ))}
      </div>
      <button type="button" className="run-settings-admin-save" onClick={onLaunch} disabled={!canLaunch || launching}>
        {launching ? <Loader2Icon className="spin" aria-hidden="true" /> : <PlayIcon aria-hidden="true" />}
        <span>Launch run</span>
      </button>
    </div>
  );
}

function RunDetail({
  detail,
  loading,
  approving,
  onApprove,
}: {
  detail: OrchestrationDetail;
  loading: boolean;
  approving: boolean;
  onApprove: () => void;
}) {
  const run = detail.orchestration;
  return (
    <div className="orchestration-detail">
      <div className="orchestration-detail-head">
        <div>
          <div className="orchestration-detail-title">{run.repo}</div>
          <div className="orchestration-detail-meta">{run.id}</div>
        </div>
        <div className="orchestration-detail-actions">
          {loading && <Loader2Icon className="spin" aria-hidden="true" />}
          <StateBadge state={run.state} />
          {run.state === "awaiting_review" && (
            <button type="button" className="run-settings-admin-save" onClick={onApprove} disabled={approving}>
              {approving ? <Loader2Icon className="spin" aria-hidden="true" /> : <GitPullRequestIcon aria-hidden="true" />}
              <span>Approve &amp; merge</span>
            </button>
          )}
        </div>
      </div>
      {run.integration_branch && (
        <div className="orchestration-detail-meta">integration: {run.integration_branch}</div>
      )}
      <div className="orchestration-phases">
        {detail.phases.map((phase) => (
          <article className="orchestration-phase" key={phase.phase_id || phase.key}>
            <div className="orchestration-phase-head">
              <div>
                <div className="orchestration-phase-key">{phase.key}</div>
                <div className="orchestration-detail-meta">
                  {phase.target}
                  {phase.depends_on.length ? ` after ${phase.depends_on.join(", ")}` : ""}
                </div>
              </div>
              <StateBadge state={phase.status} />
            </div>
            <p>{phase.brief}</p>
            <div className="orchestration-phase-links">
              {phase.pr_url && (
                <a href={phase.pr_url} target="_blank" rel="noreferrer">
                  <GitPullRequestIcon aria-hidden="true" />
                  <span>PR {phase.pr_number || ""}</span>
                  <ExternalLinkIcon aria-hidden="true" />
                </a>
              )}
              {phase.spoke_session_id && (
                <a href={`/sessions/${encodeURIComponent(phase.spoke_session_id)}`} target="_blank" rel="noreferrer">
                  <ExternalLinkIcon aria-hidden="true" />
                  <span>{phase.spoke_session_id}</span>
                </a>
              )}
            </div>
          </article>
        ))}
      </div>
    </div>
  );
}
