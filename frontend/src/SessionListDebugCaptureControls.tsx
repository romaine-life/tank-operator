import { useCallback, useEffect, useRef, useState } from "react";
import { CameraIcon, RadioIcon, StopCircleIcon } from "lucide-react";
import { captureSessionListDebugSnapshot } from "./sessionListDebug";

const RECORD_DURATION_MS = 2 * 60 * 1000;
const RECORD_SAMPLE_INTERVAL_MS = 10 * 1000;

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

export function SessionListDebugCaptureControls({
  source = "SessionListDebugCaptureControls",
}: {
  source?: string;
}) {
  const [captureStatus, setCaptureStatus] = useState<CaptureStatus>({ state: "idle" });
  const [recording, setRecording] = useState<RecordingState | null>(null);
  const recordingRef = useRef<RecordingState | null>(null);

  useEffect(() => {
    recordingRef.current = recording;
  }, [recording]);

  const postDebugCapture = useCallback(
    async (reason: string, detail: Record<string, unknown> = {}) => {
      setCaptureStatus({ state: "sending", message: reason });
      try {
        const result = await captureSessionListDebugSnapshot({
          reason,
          source,
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
    [source],
  );

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

  return (
    <div className="session-list-capture-controls">
      <div className="session-list-capture-actions">
        <button
          type="button"
          className="session-list-capture-btn"
          title="Capture current session-list diagnostics"
          onClick={() =>
            void postDebugCapture("manual-capture", { phase: "capture-now" }).catch(
              () => undefined,
            )
          }
        >
          <CameraIcon aria-hidden="true" />
          <span>Capture Now</span>
        </button>
        {recording ? (
          <button
            type="button"
            className="session-list-capture-btn session-list-capture-danger"
            title="Stop session-list diagnostic recording"
            onClick={() => stopRecording("manual")}
          >
            <StopCircleIcon aria-hidden="true" />
            <span>Stop</span>
          </button>
        ) : (
          <button
            type="button"
            className="session-list-capture-btn"
            title="Record session-list diagnostics for two minutes"
            onClick={startRecording}
          >
            <RadioIcon aria-hidden="true" />
            <span>Record 2m</span>
          </button>
        )}
      </div>
      <div className="session-list-capture-meta" aria-live="polite">
        <span>{recording ? `recording ${recording.samples} samples` : "recording idle"}</span>
        {captureStatus.state !== "idle" ? (
          <span>{`capture ${captureStatusLabel(captureStatus)}`}</span>
        ) : null}
      </div>
    </div>
  );
}

function captureStatusLabel(status: CaptureStatus): string {
  if (status.state === "sending") return status.message ? `sending ${status.message}` : "sending";
  if (status.state === "ok") return status.message ?? "saved";
  if (status.state === "error") return status.message ?? "error";
  return "idle";
}

function debugRunID(): string {
  const random = Math.random().toString(16).slice(2, 10);
  return `sldr_${Date.now().toString(36)}_${random}`;
}
