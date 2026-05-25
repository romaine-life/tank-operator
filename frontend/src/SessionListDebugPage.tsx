import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { CameraIcon, LogInIcon, RadioIcon, RefreshCwIcon, StopCircleIcon } from "lucide-react";
import { authedFetch, bootstrapAuth, startLogin } from "./auth";
import {
  captureSessionListDebugSnapshot,
  getSessionListDebugSnapshot,
  subscribeSessionListDebug,
  type SessionListDebugEvent,
  type SessionListDebugRow,
  type SessionListDebugSnapshot,
} from "./sessionListDebug";

const RECORD_DURATION_MS = 2 * 60 * 1000;
const RECORD_SAMPLE_INTERVAL_MS = 10 * 1000;

type ServerStatus = "checking" | "signed-out" | "forbidden" | "loading" | "ok" | "error";
type CaptureStatus = {
  state: "idle" | "sending" | "ok" | "error";
  message?: string;
  at?: string;
};

type RecordingState = {
  run_id: string;
  started_at: string;
  until: string;
  samples: number;
  last_capture_at: string | null;
};

type DebugUser = {
  email: string;
  is_admin: boolean;
};

type ServerDebugState = {
  owner?: string;
  scope?: string;
  cursor?: string;
  row_count?: number;
  rows?: Record<string, unknown>[];
  fetched_at?: string;
  note?: string;
};

export function SessionListDebugPage() {
  const ownerParam = useMemo(readOwnerParam, []);
  const [snapshot, setSnapshot] = useState<SessionListDebugSnapshot>(() =>
    getSessionListDebugSnapshot(),
  );
  const [user, setUser] = useState<DebugUser | null>(null);
  const [serverStatus, setServerStatus] = useState<ServerStatus>("checking");
  const [serverState, setServerState] = useState<ServerDebugState | null>(null);
  const [serverError, setServerError] = useState<string | null>(null);
  const [captureStatus, setCaptureStatus] = useState<CaptureStatus>({ state: "idle" });
  const [recording, setRecording] = useState<RecordingState | null>(null);
  const recordingRef = useRef<RecordingState | null>(null);

  const loadServerState = useCallback(async () => {
    setServerStatus("loading");
    setServerError(null);
    try {
      const nextUser = await bootstrapAuth();
      if (!nextUser) {
        setUser(null);
        setServerState(null);
        setServerStatus("signed-out");
        return;
      }
      setUser({ email: nextUser.email, is_admin: nextUser.is_admin });
      if (!nextUser.is_admin) {
        setServerState(null);
        setServerStatus("forbidden");
        return;
      }
      const query = ownerParam ? `?owner=${encodeURIComponent(ownerParam)}` : "";
      const res = await authedFetch(`/api/debug/session-list-state${query}`);
      if (res.status === 403) {
        setServerState(null);
        setServerStatus("forbidden");
        return;
      }
      if (!res.ok) throw new Error(`debug state fetch failed: ${res.status}`);
      const body = (await res.json()) as ServerDebugState;
      setServerState({
        ...body,
        rows: Array.isArray(body.rows)
          ? body.rows.filter(isRecord).map((row) => ({ ...row }))
          : [],
      });
      setServerStatus("ok");
    } catch (error) {
      setServerError(error instanceof Error ? error.message : String(error));
      setServerStatus("error");
    }
  }, [ownerParam]);

  useEffect(() => {
    const unsubscribe = subscribeSessionListDebug(() => {
      setSnapshot(getSessionListDebugSnapshot());
    });
    const timer = window.setInterval(() => {
      setSnapshot(getSessionListDebugSnapshot());
    }, 1000);
    return () => {
      unsubscribe();
      window.clearInterval(timer);
    };
  }, []);

  useEffect(() => {
    void loadServerState();
  }, [loadServerState]);

  useEffect(() => {
    recordingRef.current = recording;
  }, [recording]);

  const postDebugCapture = useCallback(
    async (reason: string, detail: Record<string, unknown> = {}) => {
      setCaptureStatus({ state: "sending", message: reason });
      try {
        const result = await captureSessionListDebugSnapshot({
          reason,
          source: "SessionListDebugPage",
          detail,
        });
        setCaptureStatus({
          state: "ok",
          message: result?.capture_id ? `saved ${result.capture_id}` : "saved",
          at: new Date().toISOString(),
        });
        return result;
      } catch (error) {
        setCaptureStatus({
          state: "error",
          message: error instanceof Error ? error.message : String(error),
          at: new Date().toISOString(),
        });
        throw error;
      }
    },
    [],
  );

  const handleCaptureNow = useCallback(() => {
    void postDebugCapture("manual-capture", { phase: "capture-now" }).catch(() => undefined);
  }, [postDebugCapture]);

  const stopRecording = useCallback(
    (mode: "manual" | "elapsed" = "manual") => {
      const current = recordingRef.current;
      if (!current) return;
      recordingRef.current = null;
      setRecording(null);
      void postDebugCapture("manual-record-stop", {
        run_id: current.run_id,
        phase: "stop",
        mode,
        samples: current.samples,
        started_at: current.started_at,
        until: current.until,
      }).catch(() => undefined);
    },
    [postDebugCapture],
  );

  const startRecording = useCallback(() => {
    if (recordingRef.current) return;
    const startedAt = new Date();
    const runID = debugRunID();
    const next: RecordingState = {
      run_id: runID,
      started_at: startedAt.toISOString(),
      until: new Date(startedAt.getTime() + RECORD_DURATION_MS).toISOString(),
      samples: 0,
      last_capture_at: null,
    };
    recordingRef.current = next;
    setRecording(next);
    void postDebugCapture("manual-record-start", {
      run_id: runID,
      phase: "start",
      duration_ms: RECORD_DURATION_MS,
      sample_interval_ms: RECORD_SAMPLE_INTERVAL_MS,
    })
      .then(() => {
        const capturedAt = new Date().toISOString();
        if (recordingRef.current?.run_id === runID) {
          recordingRef.current = { ...recordingRef.current, last_capture_at: capturedAt };
        }
        setRecording((prev) =>
          prev?.run_id === runID ? { ...prev, last_capture_at: capturedAt } : prev,
        );
      })
      .catch(() => undefined);
  }, [postDebugCapture]);

  useEffect(() => {
    if (!recording) return;
    const runID = recording.run_id;
    let sampleIndex = recording.samples;
    const interval = window.setInterval(() => {
      const current = recordingRef.current;
      if (!current || current.run_id !== runID) return;
      sampleIndex += 1;
      const capturedAt = new Date().toISOString();
      recordingRef.current = { ...current, samples: sampleIndex, last_capture_at: capturedAt };
      setRecording((prev) =>
        prev?.run_id === runID
          ? { ...prev, samples: sampleIndex, last_capture_at: capturedAt }
          : prev,
      );
      void postDebugCapture("manual-record-sample", {
        run_id: runID,
        phase: "sample",
        sample_index: sampleIndex,
        started_at: current.started_at,
        until: current.until,
      }).catch(() => undefined);
    }, RECORD_SAMPLE_INTERVAL_MS);
    const remainingMs = Math.max(0, new Date(recording.until).getTime() - Date.now());
    const timeout = window.setTimeout(() => stopRecording("elapsed"), remainingMs);
    return () => {
      window.clearInterval(interval);
      window.clearTimeout(timeout);
    };
  }, [recording?.run_id, postDebugCapture, stopRecording]);

  const events = [...snapshot.events].reverse();
  const storeRows = snapshot.store?.rows ?? [];
  const renderRows = snapshot.render?.sessions ?? [];
  const serverRows = serverState?.rows ?? [];

  return (
    <main className="debug-session-list">
      <header className="debug-session-list-head">
        <div className="debug-session-list-title">
          <h1>Session List Debug</h1>
          <div className="debug-session-list-meta">
            <span>client {snapshot.updated_at ?? "not recorded"}</span>
            <span>server {serverStatusLabel(serverStatus, serverState, serverError)}</span>
            {user ? <span>{user.email}</span> : null}
            {ownerParam ? <span>owner {ownerParam}</span> : null}
            {recording ? <span>recording {recordingLabel(recording)}</span> : null}
            {captureStatus.state !== "idle" ? (
              <span>capture {captureStatusLabel(captureStatus)}</span>
            ) : null}
          </div>
        </div>
        <div className="debug-session-list-actions">
          {serverStatus === "signed-out" ? (
            <button type="button" className="debug-action-btn" onClick={() => void startLogin()}>
              <LogInIcon aria-hidden="true" />
              <span>Sign in</span>
            </button>
          ) : null}
          <button
            type="button"
            className="debug-action-btn"
            title="Capture current session-list diagnostics"
            onClick={handleCaptureNow}
          >
            <CameraIcon aria-hidden="true" />
            <span>Capture Now</span>
          </button>
          {recording ? (
            <button
              type="button"
              className="debug-action-btn debug-action-danger"
              title="Stop session-list diagnostic recording"
              onClick={() => stopRecording("manual")}
            >
              <StopCircleIcon aria-hidden="true" />
              <span>Stop</span>
            </button>
          ) : (
            <button
              type="button"
              className="debug-action-btn"
              title="Record session-list diagnostics for two minutes"
              onClick={startRecording}
            >
              <RadioIcon aria-hidden="true" />
              <span>Record 2m</span>
            </button>
          )}
          <button type="button" className="debug-action-btn" onClick={() => void loadServerState()}>
            <RefreshCwIcon aria-hidden="true" />
            <span>Refresh</span>
          </button>
        </div>
      </header>

      <section className="debug-session-list-summary" aria-label="Summary">
        <SummaryTile label="active" value={snapshot.render?.active_id ?? "none"} />
        <SummaryTile label="cursor" value={snapshot.store?.cursor ?? "none"} />
        <SummaryTile label="store rows" value={String(storeRows.length)} />
        <SummaryTile label="render rows" value={String(renderRows.length)} />
        <SummaryTile label="events" value={String(snapshot.events.length)} />
        <SummaryTile label="server rows" value={String(serverRows.length)} />
        <SummaryTile label="recording" value={recording ? `${recording.samples} samples` : "idle"} />
      </section>

      <section className="debug-session-list-grid">
        <DebugPanel
          title="Client Render"
          meta={snapshot.render ? `avatar catalog ${snapshot.render.avatar_catalog_version ?? 0}` : "empty"}
        >
          <SessionRowsTable rows={renderRows} />
        </DebugPanel>
        <DebugPanel
          title="SessionStore"
          meta={snapshot.store ? `updated ${snapshot.store.updated_at}` : "empty"}
        >
          <SessionRowsTable rows={storeRows} />
          {snapshot.store?.tombstones.length ? (
            <pre className="debug-json">
              {JSON.stringify({ tombstones: snapshot.store.tombstones }, null, 2)}
            </pre>
          ) : null}
        </DebugPanel>
        <DebugPanel title="Server Registry" meta={serverRegistryMeta(serverStatus, serverState)}>
          {serverStatus === "forbidden" ? (
            <div className="debug-empty">admin access required</div>
          ) : serverStatus === "signed-out" ? (
            <div className="debug-empty">signed out</div>
          ) : serverStatus === "error" ? (
            <div className="debug-empty">{serverError}</div>
          ) : (
            <GenericRowsTable rows={serverRows} />
          )}
        </DebugPanel>
        <DebugPanel title="Recent Events" meta={`${events.length} retained`}>
          <EventList events={events} />
        </DebugPanel>
      </section>
    </main>
  );
}

function SummaryTile({ label, value }: { label: string; value: string }) {
  return (
    <div className="debug-summary-tile">
      <span>{label}</span>
      <code>{value}</code>
    </div>
  );
}

function DebugPanel({
  title,
  meta,
  children,
}: {
  title: string;
  meta?: string;
  children: ReactNode;
}) {
  return (
    <article className="debug-panel">
      <header className="debug-panel-head">
        <h2>{title}</h2>
        {meta ? <span>{meta}</span> : null}
      </header>
      <div className="debug-panel-body">{children}</div>
    </article>
  );
}

function SessionRowsTable({ rows }: { rows: SessionListDebugRow[] }) {
  if (rows.length === 0) return <div className="debug-empty">no rows</div>;
  return (
    <div className="debug-table-wrap">
      <table className="debug-table">
        <thead>
          <tr>
            <th>id</th>
            <th>name</th>
            <th>status</th>
            <th>avatar</th>
            <th>version</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.id}>
              <td><code>{row.id}</code></td>
              <td>{row.display_name ?? row.name ?? row.pod_name ?? ""}</td>
              <td>{row.status ?? ""}</td>
              <td>
                <code>{row.agent_avatar_id ?? "none"}</code>
                {row.rendered_avatar_id ? <span>{" -> "}{row.rendered_avatar_id}</span> : null}
              </td>
              <td>{row.row_version ?? row.sidebar_position ?? ""}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function GenericRowsTable({ rows }: { rows: Record<string, unknown>[] }) {
  if (rows.length === 0) return <div className="debug-empty">no rows</div>;
  return (
    <div className="debug-table-wrap">
      <table className="debug-table">
        <thead>
          <tr>
            <th>id</th>
            <th>name</th>
            <th>visible</th>
            <th>avatar</th>
            <th>version</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={`${String(row.id ?? "row")}-${index}`}>
              <td><code>{valueText(row.id)}</code></td>
              <td>{valueText(row.name ?? row.pod_name)}</td>
              <td>{valueText(row.visible)}</td>
              <td><code>{valueText(row.agent_avatar_id)}</code></td>
              <td>{valueText(row.row_version)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function EventList({ events }: { events: SessionListDebugEvent[] }) {
  if (events.length === 0) return <div className="debug-empty">no events</div>;
  return (
    <ol className="debug-event-list">
      {events.map((event) => (
        <li key={event.seq} className="debug-event">
          <div className="debug-event-head">
            <code>#{event.seq}</code>
            <strong>{event.kind}</strong>
            {event.session_id ? <code>{event.session_id}</code> : null}
            <span>{event.at}</span>
          </div>
          <pre className="debug-json">{JSON.stringify(event, null, 2)}</pre>
        </li>
      ))}
    </ol>
  );
}

function readOwnerParam(): string {
  if (typeof window === "undefined") return "";
  return new URLSearchParams(window.location.search).get("owner")?.trim() ?? "";
}

function serverStatusLabel(
  status: ServerStatus,
  state: ServerDebugState | null,
  error: string | null,
): string {
  if (status === "ok") return state?.fetched_at ?? "ok";
  if (status === "error") return error ?? "error";
  return status;
}

function serverRegistryMeta(status: ServerStatus, state: ServerDebugState | null): string {
  if (status !== "ok") return status;
  const parts = [
    state?.owner ? `owner ${state.owner}` : null,
    state?.scope ? `scope ${state.scope}` : null,
    state?.cursor ? `cursor ${state.cursor}` : null,
    state?.note ?? null,
  ];
  return parts.filter(Boolean).join(" | ");
}

function captureStatusLabel(status: CaptureStatus): string {
  if (status.state === "sending") return status.message ? `sending ${status.message}` : "sending";
  if (status.state === "ok") return status.message ?? "saved";
  if (status.state === "error") return status.message ?? "error";
  return "idle";
}

function recordingLabel(recording: RecordingState): string {
  return `${recording.run_id} until ${recording.until}`;
}

function debugRunID(): string {
  const random = Math.random().toString(16).slice(2, 10);
  return `sldr_${Date.now().toString(36)}_${random}`;
}

function valueText(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return JSON.stringify(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
