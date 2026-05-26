import { useEffect, useState } from "react";
import { CameraIcon, RadioIcon, StopCircleIcon } from "lucide-react";
import {
  captureSessionListDebugNow,
  getSessionListDebugRecorderSnapshot,
  startSessionListDebugRecording,
  stopSessionListDebugRecording,
  subscribeSessionListDebugRecorder,
  type SessionListDebugCaptureStatus,
} from "./sessionListDebugRecorder";

export function SessionListDebugCaptureControls({
  source = "SessionListDebugCaptureControls",
}: {
  source?: string;
}) {
  const [recorder, setRecorder] = useState(getSessionListDebugRecorderSnapshot);
  const { captureStatus, recording } = recorder;

  useEffect(() => {
    return subscribeSessionListDebugRecorder(() => {
      setRecorder(getSessionListDebugRecorderSnapshot());
    });
  }, []);

  return (
    <div className="session-list-capture-controls">
      <div className="session-list-capture-actions">
        <button
          type="button"
          className="session-list-capture-btn"
          title="Capture current session-list diagnostics"
          onClick={() =>
            void captureSessionListDebugNow(source).catch(() => undefined)
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
            onClick={() => stopSessionListDebugRecording("manual")}
          >
            <StopCircleIcon aria-hidden="true" />
            <span>Stop</span>
          </button>
        ) : (
          <button
            type="button"
            className="session-list-capture-btn"
            title="Record session-list diagnostics for two minutes"
            onClick={() => startSessionListDebugRecording(source)}
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

function captureStatusLabel(status: SessionListDebugCaptureStatus): string {
  if (status.state === "sending") return status.message ? `sending ${status.message}` : "sending";
  if (status.state === "ok") return status.message ?? "saved";
  if (status.state === "error") return status.message ?? "error";
  return "idle";
}
