import { useEffect, useMemo, useState } from "react";
import { GitBranchIcon, LinkIcon, RefreshCwIcon } from "lucide-react";
import { authedFetch } from "./auth";

type TokenUsage = {
  total_tokens: number;
  input_tokens: number;
  output_tokens: number;
  usage_events: number;
};

type RepoSummary = {
  repo: string;
  session_count: number;
  total_tokens: number;
  input_tokens: number;
  output_tokens: number;
  last_touched?: string;
};

type SessionSummary = {
  owner: string;
  session_id: string;
  name: string;
  mode: string;
  repos: string[];
  visible: boolean;
  created_at: string;
  updated_at: string;
  usage: TokenUsage;
};

type SessionReport = {
  scope: string;
  days?: number;
  range?: {
    mode: "last_days" | "custom";
    days?: number;
    starts_at: string;
    ends_at: string;
    label: string;
  };
  attribution: string;
  totals: TokenUsage & {
    session_count: number;
    repo_count: number;
  };
  repos: RepoSummary[];
  sessions: SessionSummary[];
  fetched_at: string;
};

type SessionRepoReportProps = {
  sessionScope?: string;
  publicShareToken?: string;
  publicView?: boolean;
};

export function SessionRepoReport({
  sessionScope = "",
  publicShareToken,
  publicView = false,
}: SessionRepoReportProps) {
  const [rangeMode, setRangeMode] = useState<"days" | "custom">("days");
  const [days, setDays] = useState(30);
  const [customFrom, setCustomFrom] = useState("");
  const [customTo, setCustomTo] = useState("");
  const [report, setReport] = useState<SessionReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [shareStatus, setShareStatus] = useState("");

  const reportURL = useMemo(() => {
    if (publicShareToken) {
      return `/api/public/session-report-shares/${encodeURIComponent(publicShareToken)}`;
    }
    const params = new URLSearchParams({
      session_scope: sessionScope,
    });
    if (rangeMode === "custom") {
      if (!customFrom || !customTo) return "";
      params.set("from", customFrom);
      params.set("to", customTo);
    } else {
      params.set("days", String(days));
    }
    return `/api/admin/session-report?${params.toString()}`;
  }, [customFrom, customTo, days, publicShareToken, rangeMode, sessionScope]);

  const loadReport = async () => {
    if (!reportURL) return;
    setLoading(true);
    setError("");
    try {
      const res = publicShareToken ? await fetch(reportURL) : await authedFetch(reportURL);
      if (!res.ok) {
        throw new Error(await res.text());
      }
      setReport(await res.json() as SessionReport);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadReport();
  }, [reportURL]);

  const topSessions = report?.sessions.slice(0, 12) ?? [];
  const rangeLabel = report?.range?.label ?? (rangeMode === "custom" ? "Custom range" : `Last ${days} days`);

  const selectDays = (value: number) => {
    setRangeMode("days");
    setDays(value);
    setShareStatus("");
  };

  const createShare = async () => {
    if (!reportURL || publicView) return;
    setLoading(true);
    setError("");
    setShareStatus("");
    try {
      const shareURL = reportURL.replace("/api/admin/session-report?", "/api/admin/session-report-shares?");
      const res = await authedFetch(shareURL, { method: "POST" });
      if (!res.ok) throw new Error(await res.text());
      const body = (await res.json()) as { browser_url?: unknown };
      const browserURL = typeof body.browser_url === "string" ? body.browser_url : "";
      if (!browserURL) throw new Error("share response did not include a browser_url");
      await navigator.clipboard.writeText(browserURL);
      setShareStatus("Copied shared report link");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className={`session-repo-report${publicView ? " is-public" : ""}`}>
      <section className="session-repo-report-toolbar" aria-label="Session report controls">
        <div className="session-repo-report-title">
          <GitBranchIcon aria-hidden="true" />
          <span>{publicView ? "Shared session repo report" : "Session repo report"}</span>
          <span className="session-repo-report-range-label">{rangeLabel}</span>
        </div>
        <div className="session-repo-report-actions">
          {!publicView && (
            <>
              {[1, 7, 30, 90].map((value) => (
                <button
                  key={value}
                  type="button"
                  className={`session-repo-report-range${rangeMode === "days" && days === value ? " is-active" : ""}`}
                  onClick={() => selectDays(value)}
                >
                  {value}d
                </button>
              ))}
              <label className="session-repo-report-date">
                <span>From</span>
                <input
                  type="date"
                  value={customFrom}
                  onChange={(event) => {
                    setRangeMode("custom");
                    setCustomFrom(event.target.value);
                    setShareStatus("");
                  }}
                />
              </label>
              <label className="session-repo-report-date">
                <span>To</span>
                <input
                  type="date"
                  value={customTo}
                  onChange={(event) => {
                    setRangeMode("custom");
                    setCustomTo(event.target.value);
                    setShareStatus("");
                  }}
                />
              </label>
              <button
                type="button"
                className="session-repo-report-refresh"
                onClick={() => void createShare()}
                disabled={loading || !reportURL}
                aria-label="Copy shared report link"
                title="Copy shared report link"
              >
                <LinkIcon aria-hidden="true" />
              </button>
            </>
          )}
          <button
            type="button"
            className="session-repo-report-refresh"
            onClick={() => void loadReport()}
            disabled={loading || !reportURL}
            aria-label="Refresh report"
            title="Refresh report"
          >
            <RefreshCwIcon aria-hidden="true" />
          </button>
        </div>
      </section>

      {error && <div className="session-repo-report-error">{error}</div>}
      {shareStatus && <div className="session-repo-report-success">{shareStatus}</div>}

      <section className="session-repo-report-metrics" aria-label="Session report totals">
        <ReportMetric label="Sessions" value={formatCount(report?.totals.session_count)} />
        <ReportMetric label="Repos" value={formatCount(report?.totals.repo_count)} />
        <ReportMetric label="Tokens" value={formatTokenCount(report?.totals.total_tokens)} />
        <ReportMetric label="Usage rows" value={formatCount(report?.totals.usage_events)} />
      </section>

      <section className="session-repo-report-grid" aria-label="Session report details">
        <div className="session-repo-report-table-wrap">
          <h3 className="session-repo-report-heading">Repos touched</h3>
          <table className="session-repo-report-table">
            <thead>
              <tr>
                <th>Repo</th>
                <th>Sessions</th>
                <th>Tokens</th>
              </tr>
            </thead>
            <tbody>
              {(report?.repos ?? []).map((repo) => (
                <tr key={repo.repo}>
                  <td>{repo.repo}</td>
                  <td>{repo.session_count}</td>
                  <td>{formatTokenCount(repo.total_tokens)}</td>
                </tr>
              ))}
              {report && report.repos.length === 0 && (
                <tr>
                  <td colSpan={3}>No sessions in this window.</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>

        <div className="session-repo-report-table-wrap">
          <h3 className="session-repo-report-heading">Recent sessions</h3>
          <table className="session-repo-report-table">
            <thead>
              <tr>
                <th>Session</th>
                <th>Repos</th>
                <th>Tokens</th>
              </tr>
            </thead>
            <tbody>
              {topSessions.map((session) => (
                <tr key={`${session.owner}:${session.session_id}`}>
                  <td>
                    <span className="session-repo-report-session-name">
                      {session.name || `Session ${session.session_id}`}
                    </span>
                    <span className="session-repo-report-muted">{session.mode}</span>
                  </td>
                  <td>{session.repos.length > 0 ? session.repos.join(", ") : "Unassigned"}</td>
                  <td>{formatTokenCount(session.usage.total_tokens)}</td>
                </tr>
              ))}
              {report && topSessions.length === 0 && (
                <tr>
                  <td colSpan={3}>No sessions in this window.</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <p className="session-repo-report-note">
        {report?.attribution ?? "Loading report..."}
      </p>
    </div>
  );
}

function ReportMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="session-repo-report-metric">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function formatCount(value: number | undefined): string {
  return typeof value === "number" ? new Intl.NumberFormat().format(value) : "-";
}

function formatTokenCount(value: number | undefined): string {
  if (typeof value !== "number") return "-";
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
  return new Intl.NumberFormat().format(value);
}
