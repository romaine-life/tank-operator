import { useEffect, useMemo, useState } from "react";
import { GitBranchIcon, RefreshCwIcon } from "lucide-react";
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
  days: number;
  attribution: string;
  totals: TokenUsage & {
    session_count: number;
    repo_count: number;
  };
  repos: RepoSummary[];
  sessions: SessionSummary[];
  fetched_at: string;
};

export function SessionRepoReport({ sessionScope }: { sessionScope: string }) {
  const [days, setDays] = useState(30);
  const [report, setReport] = useState<SessionReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const reportURL = useMemo(() => {
    const params = new URLSearchParams({
      days: String(days),
      session_scope: sessionScope,
    });
    return `/api/admin/session-report?${params.toString()}`;
  }, [days, sessionScope]);

  const loadReport = async () => {
    setLoading(true);
    setError("");
    try {
      const res = await authedFetch(reportURL);
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

  return (
    <div className="session-repo-report">
      <section className="session-repo-report-toolbar" aria-label="Session report controls">
        <div className="session-repo-report-title">
          <GitBranchIcon aria-hidden="true" />
          <span>Session repo report</span>
        </div>
        <div className="session-repo-report-actions">
          {[7, 30, 90].map((value) => (
            <button
              key={value}
              type="button"
              className={`session-repo-report-range${days === value ? " is-active" : ""}`}
              onClick={() => setDays(value)}
            >
              {value}d
            </button>
          ))}
          <button
            type="button"
            className="session-repo-report-refresh"
            onClick={() => void loadReport()}
            disabled={loading}
            aria-label="Refresh report"
            title="Refresh report"
          >
            <RefreshCwIcon aria-hidden="true" />
          </button>
        </div>
      </section>

      {error && <div className="session-repo-report-error">{error}</div>}

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
