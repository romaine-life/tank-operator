// OrchestratePanel renders inside the session workspace when the user
// navigates to /sessions/{id}/orchestrate. It has two states:
//
//   1. NOT yet a hub (session.spoke_config absent/null): a launch form where
//      the user picks provider / surface / model / effort and POSTs to
//      POST /api/sessions/{id}/orchestrate. The backend self-grants git
//      break-glass and enqueues the kickoff turn; the UI just submits and
//      waits for the durable spoke_config to arrive via SSE.
//
//   2. IS a hub (spoke_config present): a status view showing the config
//      and the spawned spoke sessions.
//
// Deliberately no optimistic state flips — the durable row drives everything.

import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Loader2Icon, Wand2Icon, ExternalLinkIcon } from "lucide-react";
import { authedFetch } from "./auth";
import type { SpawnedSessionRef } from "./spawnedSessions";

// ---------------------------------------------------------------------------
// Types mirroring the backend contract
// ---------------------------------------------------------------------------

type OrchestrateProvider = "claude" | "codex";
type OrchestrateSurface = "gui" | "cli";

interface OrchestrateRequestBody {
  provider: OrchestrateProvider;
  surface: OrchestrateSurface;
  model: string;
  effort: string;
}

interface OrchestrateResponse {
  active: boolean;
  session_id: string;
  spoke_config: Record<string, unknown>;
  break_glass: { active: boolean; all_repos: boolean; expires_at?: string };
  kickoff_turn: string;
  grant_event_id: string;
}

// Minimal run-options shape — only the fields OrchestratePanel needs.
interface OrchestrateRunOptions {
  models: {
    claude: string[];
    codex: string[];
  };
  efforts: {
    claude: string[];
    codex: string[];
  };
  default_models: {
    claude: string;
    codex: string;
  };
  default_efforts: {
    claude: string;
    codex: string;
  };
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function normalizeOrchestrateRunOptions(raw: unknown): OrchestrateRunOptions | null {
  if (!raw || typeof raw !== "object") return null;
  const v = raw as Record<string, unknown>;
  const stringArray = (x: unknown): string[] =>
    Array.isArray(x) ? x.filter((s): s is string => typeof s === "string") : [];
  const stringFrom = (x: unknown): string =>
    typeof x === "string" ? x : "";
  const models = v.models && typeof v.models === "object" ? (v.models as Record<string, unknown>) : {};
  const efforts = v.efforts && typeof v.efforts === "object" ? (v.efforts as Record<string, unknown>) : {};
  const defModels =
    v.default_models && typeof v.default_models === "object"
      ? (v.default_models as Record<string, unknown>)
      : {};
  const defEfforts =
    v.default_efforts && typeof v.default_efforts === "object"
      ? (v.default_efforts as Record<string, unknown>)
      : {};
  return {
    models: {
      claude: stringArray(models.claude ?? models.anthropic),
      codex: stringArray(models.codex),
    },
    efforts: {
      claude: stringArray(efforts.claude ?? efforts.anthropic),
      codex: stringArray(efforts.codex),
    },
    default_models: {
      claude: stringFrom(defModels.claude ?? defModels.anthropic),
      codex: stringFrom(defModels.codex),
    },
    default_efforts: {
      claude: stringFrom(defEfforts.claude ?? defEfforts.anthropic),
      codex: stringFrom(defEfforts.codex),
    },
  };
}

function stringField(obj: Record<string, unknown>, key: string): string {
  const v = obj[key];
  return typeof v === "string" ? v : "";
}

// ---------------------------------------------------------------------------
// OrchestratePanel
// ---------------------------------------------------------------------------

interface OrchestratePanelProps {
  sessionId: string;
  spokeConfig: Record<string, unknown> | undefined;
  spawnedSessions: SpawnedSessionRef[];
  ready: boolean;
}

export function OrchestratePanel({
  sessionId,
  spokeConfig,
  spawnedSessions,
  ready,
}: OrchestratePanelProps) {
  const isHub = Boolean(spokeConfig && Object.keys(spokeConfig).length > 0);

  if (isHub) {
    return (
      <OrchestrateStatusView
        sessionId={sessionId}
        spokeConfig={spokeConfig!}
        spawnedSessions={spawnedSessions}
      />
    );
  }

  return (
    <OrchestrateForm sessionId={sessionId} ready={ready} />
  );
}

// ---------------------------------------------------------------------------
// Launch form (not yet a hub)
// ---------------------------------------------------------------------------

function OrchestrateForm({
  sessionId,
  ready,
}: {
  sessionId: string;
  ready: boolean;
}) {
  const [runOptions, setRunOptions] = useState<OrchestrateRunOptions | null>(null);
  const [runOptionsError, setRunOptionsError] = useState<string | null>(null);
  const runOptionsFetchedRef = useRef(false);

  const [provider, setProvider] = useState<OrchestrateProvider>("claude");
  const [surface, setSurface] = useState<OrchestrateSurface>("gui");
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");

  const [submitState, setSubmitState] = useState<"idle" | "pending" | "error">(
    "idle",
  );
  const [submitError, setSubmitError] = useState<string | null>(null);

  // Fetch run options once
  useEffect(() => {
    if (runOptionsFetchedRef.current) return;
    runOptionsFetchedRef.current = true;
    void authedFetch("/api/session-run-options")
      .then(async (res) => {
        if (!res.ok) throw new Error(`run options: ${res.status}`);
        const body = await res.json();
        const normalized = normalizeOrchestrateRunOptions(body);
        if (!normalized) throw new Error("run options: unexpected shape");
        setRunOptions(normalized);
      })
      .catch((err: unknown) => {
        setRunOptionsError(err instanceof Error ? err.message : String(err));
      });
  }, []);

  // Reset model/effort to defaults when provider changes or options load
  useEffect(() => {
    if (!runOptions) return;
    setModel(runOptions.default_models[provider] ?? runOptions.models[provider][0] ?? "");
    setEffort(runOptions.default_efforts[provider] ?? runOptions.efforts[provider][0] ?? "");
  }, [provider, runOptions]);

  const modelChoices = runOptions?.models[provider] ?? [];
  const effortChoices = runOptions?.efforts[provider] ?? [];

  const handleSubmit = useCallback(
    async (event: FormEvent) => {
      event.preventDefault();
      if (submitState === "pending" || !ready) return;
      const body: OrchestrateRequestBody = { provider, surface, model, effort };
      setSubmitState("pending");
      setSubmitError(null);
      try {
        const res = await authedFetch(
          `/api/sessions/${encodeURIComponent(sessionId)}/orchestrate`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
          },
        );
        if (res.status === 202) {
          // Accepted — wait for durable spoke_config via SSE; don't flip state here.
          return;
        }
        let detail = `HTTP ${res.status}`;
        try {
          const respBody = (await res.json()) as { detail?: string };
          if (typeof respBody?.detail === "string" && respBody.detail) {
            detail = respBody.detail;
          }
        } catch {
          // keep the status-derived detail
        }
        setSubmitState("error");
        setSubmitError(detail);
      } catch (err: unknown) {
        setSubmitState("error");
        setSubmitError(err instanceof Error ? err.message : "Request failed");
      }
    },
    [effort, model, provider, ready, sessionId, submitState, surface],
  );

  // If we got a 202 and are waiting for SSE, show a pending indicator
  const isPending = submitState === "pending";

  return (
    <div className="break-glass-page">
      <section className="break-glass-page-main">
        <div className="break-glass-page-head">
          <div className="break-glass-page-title">
            <Wand2Icon aria-hidden="true" />
            <div>
              <h2>Orchestrate</h2>
              <p>Launch a spoke session from this hub</p>
            </div>
          </div>
        </div>

        {runOptionsError ? (
          <div className="break-glass-empty" role="alert">
            Failed to load options: {runOptionsError}
          </div>
        ) : !runOptions ? (
          <div className="break-glass-empty" role="status">
            <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
            <span>Loading options…</span>
          </div>
        ) : (
          <form className="orchestrate-form" onSubmit={(e) => { void handleSubmit(e); }}>
            <fieldset className="orchestrate-fieldset" disabled={isPending || !ready}>
              <div className="orchestrate-row">
                <span className="orchestrate-label">Provider</span>
                <div className="orchestrate-radios">
                  {(["claude", "codex"] as OrchestrateProvider[]).map((p) => (
                    <label key={p} className="orchestrate-radio">
                      <input
                        type="radio"
                        name="provider"
                        value={p}
                        checked={provider === p}
                        onChange={() => setProvider(p)}
                      />
                      <span>{p === "claude" ? "Claude" : "Codex"}</span>
                    </label>
                  ))}
                </div>
              </div>

              <div className="orchestrate-row">
                <span className="orchestrate-label">Surface</span>
                <div className="orchestrate-radios">
                  {(["gui", "cli"] as OrchestrateSurface[]).map((s) => (
                    <label key={s} className="orchestrate-radio">
                      <input
                        type="radio"
                        name="surface"
                        value={s}
                        checked={surface === s}
                        onChange={() => setSurface(s)}
                      />
                      <span>{s === "gui" ? "GUI" : "CLI"}</span>
                    </label>
                  ))}
                </div>
              </div>

              {modelChoices.length > 0 && (
                <div className="orchestrate-row">
                  <label className="orchestrate-label" htmlFor={`orchestrate-model-${sessionId}`}>
                    Model
                  </label>
                  <select
                    id={`orchestrate-model-${sessionId}`}
                    className="orchestrate-select"
                    value={model}
                    onChange={(e) => setModel(e.target.value)}
                  >
                    {modelChoices.map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                  </select>
                </div>
              )}

              {effortChoices.length > 0 && (
                <div className="orchestrate-row">
                  <label className="orchestrate-label" htmlFor={`orchestrate-effort-${sessionId}`}>
                    Effort
                  </label>
                  <select
                    id={`orchestrate-effort-${sessionId}`}
                    className="orchestrate-select"
                    value={effort}
                    onChange={(e) => setEffort(e.target.value)}
                  >
                    {effortChoices.map((ef) => (
                      <option key={ef} value={ef}>
                        {ef}
                      </option>
                    ))}
                  </select>
                </div>
              )}
            </fieldset>

            <div className="break-glass-reason orchestrate-blast-radius">
              On confirm, this session is granted full git access to all
              repositories for 24 h.
            </div>

            {submitState === "error" && submitError && (
              <div className="break-glass-reason orchestrate-error" role="alert">
                {submitError}
              </div>
            )}

            <div className="break-glass-actions">
              <button
                type="submit"
                className="run-session-data-action"
                disabled={isPending || !ready || !model}
              >
                {isPending ? (
                  <>
                    <Loader2Icon size={14} className="run-spin" aria-hidden="true" />
                    Launching…
                  </>
                ) : (
                  "Launch spoke"
                )}
              </button>
            </div>
          </form>
        )}
      </section>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Status view (already a hub)
// ---------------------------------------------------------------------------

function OrchestrateStatusView({
  sessionId,
  spokeConfig,
  spawnedSessions,
}: {
  sessionId: string;
  spokeConfig: Record<string, unknown>;
  spawnedSessions: SpawnedSessionRef[];
}) {
  const provider = stringField(spokeConfig, "provider");
  const surface = stringField(spokeConfig, "surface");
  const model = stringField(spokeConfig, "model");
  const effort = stringField(spokeConfig, "effort");
  const configuredBy = stringField(spokeConfig, "configured_by");
  const configuredAt = stringField(spokeConfig, "configured_at");

  return (
    <div className="break-glass-page">
      <section className="break-glass-page-main">
        <div className="break-glass-page-head">
          <div className="break-glass-page-title">
            <Wand2Icon aria-hidden="true" />
            <div>
              <h2>Orchestration active</h2>
              <p>{sessionId}</p>
            </div>
          </div>
          <span className="admin-break-glass-status is-approved">hub</span>
        </div>

        <dl className="break-glass-facts">
          {provider && (
            <div>
              <dt>Provider</dt>
              <dd>{provider}</dd>
            </div>
          )}
          {surface && (
            <div>
              <dt>Surface</dt>
              <dd>{surface}</dd>
            </div>
          )}
          {model && (
            <div>
              <dt>Model</dt>
              <dd>{model}</dd>
            </div>
          )}
          {effort && (
            <div>
              <dt>Effort</dt>
              <dd>{effort}</dd>
            </div>
          )}
          {configuredBy && (
            <div>
              <dt>Configured by</dt>
              <dd>{configuredBy}</dd>
            </div>
          )}
          {configuredAt && (
            <div>
              <dt>Configured at</dt>
              <dd>{configuredAt}</dd>
            </div>
          )}
        </dl>

        {spawnedSessions.length > 0 && (
          <div className="orchestrate-spokes">
            <div className="break-glass-reason">
              <strong>Spoke sessions ({spawnedSessions.length})</strong>
            </div>
            <ul className="orchestrate-spoke-list">
              {spawnedSessions.map((spoke) => (
                <li key={spoke.id} className="orchestrate-spoke-item">
                  <a
                    href={spoke.url}
                    target="_blank"
                    rel="noreferrer"
                    className="run-pr-menu-item orchestrate-spoke-link"
                  >
                    <span className="orchestrate-spoke-name">{spoke.name}</span>
                    {(spoke.mode || spoke.model) && (
                      <span className="run-slash-desc">
                        {[spoke.mode, spoke.model].filter(Boolean).join(" · ")}
                      </span>
                    )}
                    <ExternalLinkIcon size={12} className="run-pr-menu-icon" aria-hidden="true" />
                  </a>
                </li>
              ))}
            </ul>
          </div>
        )}

        <div className="break-glass-reason orchestrate-blast-radius">
          This hub session holds an active git break-glass grant (all
          repositories, 24 h window). The grant was self-issued at orchestration
          launch.
        </div>
      </section>
    </div>
  );
}

// Re-export types for use in tests
export type { OrchestrateRunOptions, OrchestrateResponse };
export { normalizeOrchestrateRunOptions };
