import { useMemo, useState } from "react";
import {
  CheckIcon,
  CopyIcon,
  Loader2Icon,
  ShieldAlertIcon,
} from "lucide-react";
import { authedFetch } from "./auth";

type GrantKind = "github" | "azure" | "model";
type GrantStatus = {
  kind: GrantKind;
  ok: boolean;
  eventId?: string;
  expiresAt?: string;
  detail: string;
};

type Props = {
  initialSessionId?: string;
  sessionScope: string;
};

const ttlOptions = [
  { label: "15 min", value: 900 },
  { label: "1 hour", value: 3600 },
  { label: "4 hours", value: 14400 },
  { label: "24 hours", value: 86400 },
];

const effortOptions = ["low", "medium", "high", "xhigh"];

function grantLabel(kind: GrantKind): string {
  switch (kind) {
    case "github":
      return "GitHub";
    case "azure":
      return "Azure";
    case "model":
      return "Agent selection";
  }
}

function resultText(results: GrantStatus[]): string {
  return results
    .map((result) => {
      const status = result.ok ? "granted" : "failed";
      const expiry = result.expiresAt ? ` until ${result.expiresAt}` : "";
      return `${grantLabel(result.kind)}: ${status}${expiry}. ${result.detail}`;
    })
    .join("\n");
}

async function postGrant(path: string, body: unknown): Promise<Record<string, any>> {
  const res = await authedFetch(path, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  let parsed: Record<string, any> = {};
  if (text.trim()) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = { detail: text };
    }
  }
  if (!res.ok) {
    throw new Error(String(parsed.detail ?? `grant request returned ${res.status}`));
  }
  return parsed;
}

export function AdminBreakGlassPanel({ initialSessionId = "", sessionScope }: Props) {
  const [sessionId, setSessionId] = useState(initialSessionId);
  const [reason, setReason] = useState("Unblock stuck session break-glass access");
  const [ttlSeconds, setTTLSeconds] = useState(3600);
  const [includeGitHub, setIncludeGitHub] = useState(true);
  const [includeAzure, setIncludeAzure] = useState(false);
  const [includeModel, setIncludeModel] = useState(false);
  const [repoScope, setRepoScope] = useState<"current_repo" | "all_repos">("current_repo");
  const [repo, setRepo] = useState("romaine-life/tank-operator");
  const [mode, setMode] = useState("codex_gui");
  const [model, setModel] = useState("gpt-5.5");
  const [effort, setEffort] = useState("xhigh");
  const [pending, setPending] = useState(false);
  const [results, setResults] = useState<GrantStatus[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const requestEventId = useMemo(() => {
    const cleaned = sessionId.trim().replace(/[^A-Za-z0-9_.-]/g, "-");
    return cleaned ? `admin-break-glass-${cleaned}` : "admin-break-glass";
  }, [sessionId]);

  const instructions = useMemo(() => {
    const lines = [
      `Session ${sessionId.trim() || "<session_id>"} has break-glass grants in ${sessionScope}.`,
    ];
    if (includeGitHub) {
      lines.push(
        "GitHub: call request_git_break_glass again to activate tank-git-break-glass, then use mint_full_git_token or push_current_head for the approved work.",
      );
    }
    if (includeAzure) {
      lines.push(
        "Azure: azure-personal should activate from the approval turn; use its MCP tools directly until the grant expires.",
      );
    }
    if (includeModel) {
      lines.push(
        "Agent selection: retry the blocked test-slot session/model selection with the approved mode, model, and effort.",
      );
    }
    return lines.join("\n");
  }, [includeAzure, includeGitHub, includeModel, sessionId, sessionScope]);

  const canSubmit =
    sessionId.trim() !== "" &&
    reason.trim() !== "" &&
    (includeGitHub || includeAzure || includeModel) &&
    (!includeGitHub || repoScope === "all_repos" || repo.trim() !== "") &&
    (!includeModel || (mode.trim() !== "" && model.trim() !== "" && effort.trim() !== ""));

  async function generate() {
    if (!canSubmit || pending) return;
    setPending(true);
    setError(null);
    setResults([]);
    setCopied(false);
    const target = encodeURIComponent(sessionId.trim());
    const scopeSuffix = `?session_scope=${encodeURIComponent(sessionScope)}`;
    const common = {
      ttl_seconds: ttlSeconds,
      request_event_id: requestEventId,
      reason: reason.trim(),
    };
    const nextResults: GrantStatus[] = [];
    const run = async (kind: GrantKind, path: string, body: unknown) => {
      try {
        const response = await postGrant(path, body);
        nextResults.push({
          kind,
          ok: true,
          eventId: String(response.event_id ?? ""),
          expiresAt: String(response.expires_at ?? ""),
          detail:
            kind === "github"
              ? "Agent was notified to activate GitHub break glass."
              : kind === "azure"
                ? "Azure MCP grant was recorded and activation was requested."
                : "Model selection grant was recorded.",
        });
      } catch (err) {
        nextResults.push({
          kind,
          ok: false,
          detail: err instanceof Error ? err.message : "grant failed",
        });
      }
    };

    if (includeGitHub) {
      await run(
        "github",
        `/api/admin/sessions/${target}/git-break-glass/grants${scopeSuffix}`,
        {
          ...common,
          repo_scope:
            repoScope === "all_repos"
              ? { kind: "all_repos" }
              : { kind: "current_repo", repo: repo.trim() },
          branch_scope: { kind: "unlimited" },
          operations: ["mint_full_git_token", "push_current_head"],
        },
      );
    }
    if (includeAzure) {
      await run("azure", `/api/admin/sessions/${target}/azure-break-glass/grants${scopeSuffix}`, {
        ...common,
        operations: ["use_azure_personal_mcp"],
      });
    }
    if (includeModel) {
      await run(
        "model",
        `/api/admin/sessions/${target}/test-slot-model-approvals/grants${scopeSuffix}`,
        {
          ...common,
          mode: mode.trim(),
          model: model.trim(),
          effort: effort.trim(),
        },
      );
    }
    setResults(nextResults);
    if (nextResults.some((result) => !result.ok)) {
      setError("One or more grants failed.");
    }
    setPending(false);
  }

  async function copyInstructions() {
    await navigator.clipboard.writeText(`${instructions}\n\n${resultText(results)}`.trim());
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1800);
  }

  return (
    <div className="run-settings-diagnostics break-glass-admin">
      <div className="run-settings-diagnostics-head">
        <span className="run-settings-link-label">
          <ShieldAlertIcon className="run-settings-link-icon" aria-hidden="true" />
          <span>Grant scoped emergency access</span>
        </span>
        <span className="run-settings-scope-value">{sessionScope}</span>
      </div>
      <div className="break-glass-grid">
        <label className="run-settings-label">
          Session ID
          <input
            className="break-glass-input"
            value={sessionId}
            onChange={(event) => setSessionId(event.target.value)}
            placeholder="47"
          />
        </label>
        <label className="run-settings-label">
          TTL
          <select
            className="break-glass-input"
            value={ttlSeconds}
            onChange={(event) => setTTLSeconds(Number(event.target.value))}
          >
            {ttlOptions.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
      </div>
      <label className="run-settings-label">
        Reason
        <textarea
          className="break-glass-input break-glass-reason"
          value={reason}
          onChange={(event) => setReason(event.target.value)}
        />
      </label>
      <div className="break-glass-options" aria-label="Break-glass grant types">
        <label className="run-settings-toggle break-glass-check">
          <input
            type="checkbox"
            checked={includeGitHub}
            onChange={(event) => setIncludeGitHub(event.target.checked)}
          />
          <span>GitHub break glass</span>
        </label>
        <label className="run-settings-toggle break-glass-check">
          <input
            type="checkbox"
            checked={includeAzure}
            onChange={(event) => setIncludeAzure(event.target.checked)}
          />
          <span>Azure break glass</span>
        </label>
        <label className="run-settings-toggle break-glass-check">
          <input
            type="checkbox"
            checked={includeModel}
            onChange={(event) => setIncludeModel(event.target.checked)}
          />
          <span>Agent selection break glass</span>
        </label>
      </div>
      {includeGitHub && (
        <div className="break-glass-subform">
          <div className="break-glass-grid">
            <label className="run-settings-label">
              Repo scope
              <select
                className="break-glass-input"
                value={repoScope}
                onChange={(event) =>
                  setRepoScope(event.target.value as "current_repo" | "all_repos")
                }
              >
                <option value="current_repo">One repo</option>
                <option value="all_repos">All repos</option>
              </select>
            </label>
            {repoScope === "current_repo" && (
              <label className="run-settings-label">
                Repo
                <input
                  className="break-glass-input"
                  value={repo}
                  onChange={(event) => setRepo(event.target.value)}
                  placeholder="owner/name"
                />
              </label>
            )}
          </div>
        </div>
      )}
      {includeModel && (
        <div className="break-glass-subform">
          <div className="break-glass-grid break-glass-grid-three">
            <label className="run-settings-label">
              Mode
              <select
                className="break-glass-input"
                value={mode}
                onChange={(event) => setMode(event.target.value)}
              >
                <option value="codex_gui">Codex GUI</option>
                <option value="claude_gui">Claude GUI</option>
              </select>
            </label>
            <label className="run-settings-label">
              Model
              <input
                className="break-glass-input"
                value={model}
                onChange={(event) => setModel(event.target.value)}
              />
            </label>
            <label className="run-settings-label">
              Effort
              <select
                className="break-glass-input"
                value={effort}
                onChange={(event) => setEffort(event.target.value)}
              >
                {effortOptions.map((option) => (
                  <option key={option} value={option}>
                    {option}
                  </option>
                ))}
              </select>
            </label>
          </div>
        </div>
      )}
      <div className="break-glass-actions">
        <button
          type="button"
          className="btn-primary run-settings-admin-save"
          disabled={!canSubmit || pending}
          onClick={generate}
        >
          {pending ? <Loader2Icon aria-hidden="true" /> : <ShieldAlertIcon aria-hidden="true" />}
          <span>Generate grants</span>
        </button>
        <button
          type="button"
          className="run-settings-test-btn"
          disabled={results.length === 0}
          onClick={copyInstructions}
        >
          {copied ? <CheckIcon aria-hidden="true" /> : <CopyIcon aria-hidden="true" />}
          <span>{copied ? "Copied" : "Copy agent note"}</span>
        </button>
      </div>
      {error && <div className="run-settings-observability-note is-critical">{error}</div>}
      {results.length > 0 && (
        <div className="break-glass-results" role="status">
          {results.map((result) => (
            <div
              key={result.kind}
              className={`run-settings-observability-row ${result.ok ? "" : "is-critical"}`}
            >
              <span
                className={`run-settings-observability-chip ${
                  result.ok ? "is-healthy" : "is-critical"
                }`}
              >
                {grantLabel(result.kind)}
              </span>
              <span className="run-settings-observability-text">
                {result.ok ? `Granted until ${result.expiresAt}` : result.detail}
                {result.eventId ? <span> · {result.eventId}</span> : null}
              </span>
            </div>
          ))}
        </div>
      )}
      <pre className="break-glass-agent-note">{instructions}</pre>
    </div>
  );
}
