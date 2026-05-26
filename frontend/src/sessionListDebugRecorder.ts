import {
  captureSessionListDebugSnapshot,
  getSessionListDebugSnapshot,
  subscribeSessionListDebug,
  type SessionListDebugCaptureResponse,
  type SessionListDebugEvent,
} from "./sessionListDebug";

const DEFAULT_RECORD_DURATION_MS = 2 * 60 * 1000;
const DEFAULT_RECORD_SAMPLE_INTERVAL_MS = 10 * 1000;
const DEFAULT_EVENT_SAMPLE_DEBOUNCE_MS = 750;

export type SessionListDebugCaptureStatus = {
  state: "idle" | "sending" | "ok" | "error";
  message?: string;
  at?: string;
};

export type SessionListDebugRecordingState = {
  run_id: string;
  source: string;
  started_at: string;
  until: string;
  samples: number;
  last_capture_at: string | null;
};

export type SessionListDebugRecorderSnapshot = {
  captureStatus: SessionListDebugCaptureStatus;
  recording: SessionListDebugRecordingState | null;
};

type RecorderListener = () => void;

let recordDurationMs = DEFAULT_RECORD_DURATION_MS;
let recordSampleIntervalMs = DEFAULT_RECORD_SAMPLE_INTERVAL_MS;
let eventSampleDebounceMs = DEFAULT_EVENT_SAMPLE_DEBOUNCE_MS;

let captureStatus: SessionListDebugCaptureStatus = { state: "idle" };
let recording: SessionListDebugRecordingState | null = null;
let intervalTimer: ReturnType<typeof setInterval> | null = null;
let elapsedTimer: ReturnType<typeof setTimeout> | null = null;
let eventSampleTimer: ReturnType<typeof setTimeout> | null = null;
let eventSampleTrigger: SessionListDebugEvent | null = null;
let lastObservedSeq = 0;
let unsubscribeDebug: (() => void) | null = null;
const recorderListeners = new Set<RecorderListener>();

export function getSessionListDebugRecorderSnapshot(): SessionListDebugRecorderSnapshot {
  return {
    captureStatus,
    recording,
  };
}

export function subscribeSessionListDebugRecorder(listener: RecorderListener): () => void {
  recorderListeners.add(listener);
  ensureDebugSubscription();
  return () => recorderListeners.delete(listener);
}

export async function captureSessionListDebugNow(
  source = "SessionListDebugCaptureControls",
): Promise<SessionListDebugCaptureResponse | void> {
  return postDebugCapture("manual-capture", source, { phase: "capture-now" });
}

export function startSessionListDebugRecording(
  source = "SessionListDebugCaptureControls",
): void {
  if (recording) return;
  ensureDebugSubscription();
  const startedAt = new Date();
  const runID = debugRunID();
  recording = {
    run_id: runID,
    source,
    started_at: startedAt.toISOString(),
    until: new Date(startedAt.getTime() + recordDurationMs).toISOString(),
    samples: 0,
    last_capture_at: null,
  };
  lastObservedSeq = getSessionListDebugSnapshot().seq;
  notifyRecorderListeners();
  void postRecordingCapture("manual-record-start", {
    phase: "start",
    duration_ms: recordDurationMs,
    sample_interval_ms: recordSampleIntervalMs,
    event_sample_debounce_ms: eventSampleDebounceMs,
  }).catch(() => undefined);
  armRecordingTimers(runID);
}

export function stopSessionListDebugRecording(mode: "manual" | "elapsed" = "manual"): void {
  const current = recording;
  if (!current) return;
  recording = null;
  clearRecordingTimers();
  notifyRecorderListeners();
  void postDebugCapture("manual-record-stop", current.source, {
    run_id: current.run_id,
    phase: "stop",
    mode,
    samples: current.samples,
    started_at: current.started_at,
    until: current.until,
  }).catch(() => undefined);
}

export function resetSessionListDebugRecorderForTest(): void {
  clearRecordingTimers();
  recording = null;
  captureStatus = { state: "idle" };
  lastObservedSeq = 0;
  if (unsubscribeDebug) {
    unsubscribeDebug();
    unsubscribeDebug = null;
  }
  recordDurationMs = DEFAULT_RECORD_DURATION_MS;
  recordSampleIntervalMs = DEFAULT_RECORD_SAMPLE_INTERVAL_MS;
  eventSampleDebounceMs = DEFAULT_EVENT_SAMPLE_DEBOUNCE_MS;
  notifyRecorderListeners();
}

export function setSessionListDebugRecorderOptionsForTest(options: {
  duration_ms?: number;
  sample_interval_ms?: number;
  event_sample_debounce_ms?: number;
}): void {
  recordDurationMs = options.duration_ms ?? recordDurationMs;
  recordSampleIntervalMs = options.sample_interval_ms ?? recordSampleIntervalMs;
  eventSampleDebounceMs = options.event_sample_debounce_ms ?? eventSampleDebounceMs;
}

function ensureDebugSubscription(): void {
  if (unsubscribeDebug) return;
  unsubscribeDebug = subscribeSessionListDebug(handleDebugSnapshotChanged);
}

function handleDebugSnapshotChanged(): void {
  if (!recording) return;
  const snapshot = getSessionListDebugSnapshot();
  if (snapshot.seq <= lastObservedSeq) return;
  lastObservedSeq = snapshot.seq;
  const latestEvent = snapshot.events.at(-1);
  if (!latestEvent || latestEvent.kind.startsWith("manual-capture")) return;
  eventSampleTrigger = latestEvent;
  if (eventSampleTimer) clearTimeout(eventSampleTimer);
  eventSampleTimer = setTimeout(() => {
    eventSampleTimer = null;
    const trigger = eventSampleTrigger;
    eventSampleTrigger = null;
    if (!trigger) return;
    void postRecordingCapture("manual-record-sample", {
      phase: "event-sample",
      trigger_seq: trigger.seq,
      trigger_kind: trigger.kind,
      trigger_source: trigger.source ?? null,
      trigger_session_id: trigger.session_id ?? null,
    }).catch(() => undefined);
  }, eventSampleDebounceMs);
}

function armRecordingTimers(runID: string): void {
  clearRecordingTimers();
  intervalTimer = setInterval(() => {
    if (!recording || recording.run_id !== runID) return;
    void postRecordingCapture("manual-record-sample", {
      phase: "interval-sample",
    }).catch(() => undefined);
  }, recordSampleIntervalMs);
  elapsedTimer = setTimeout(() => {
    if (recording?.run_id === runID) stopSessionListDebugRecording("elapsed");
  }, recordDurationMs);
}

function clearRecordingTimers(): void {
  if (intervalTimer) clearInterval(intervalTimer);
  if (elapsedTimer) clearTimeout(elapsedTimer);
  if (eventSampleTimer) clearTimeout(eventSampleTimer);
  intervalTimer = null;
  elapsedTimer = null;
  eventSampleTimer = null;
  eventSampleTrigger = null;
}

async function postRecordingCapture(
  reason: string,
  detail: Record<string, unknown>,
): Promise<SessionListDebugCaptureResponse | void> {
  const current = recording;
  if (!current) return;
  const sampleIndex =
    reason === "manual-record-start" ? current.samples : current.samples + 1;
  if (reason !== "manual-record-start") {
    recording = {
      ...current,
      samples: sampleIndex,
      last_capture_at: new Date().toISOString(),
    };
    notifyRecorderListeners();
  }
  const result = await postDebugCapture(reason, current.source, {
    run_id: current.run_id,
    started_at: current.started_at,
    until: current.until,
    sample_index: sampleIndex,
    ...detail,
  });
  const capturedAt = new Date().toISOString();
  if (recording?.run_id === current.run_id) {
    recording = {
      ...recording,
      last_capture_at: capturedAt,
    };
    notifyRecorderListeners();
  }
  return result;
}

async function postDebugCapture(
  reason: string,
  source: string,
  detail: Record<string, unknown>,
): Promise<SessionListDebugCaptureResponse | void> {
  captureStatus = { state: "sending", message: reason };
  notifyRecorderListeners();
  try {
    const result = await captureSessionListDebugSnapshot({
      reason,
      source,
      detail,
    });
    captureStatus = {
      state: "ok",
      message: result?.capture_id ? `saved ${result.capture_id}` : "saved",
      at: new Date().toISOString(),
    };
    notifyRecorderListeners();
    return result;
  } catch (error) {
    captureStatus = {
      state: "error",
      message: error instanceof Error ? error.message : String(error),
      at: new Date().toISOString(),
    };
    notifyRecorderListeners();
    throw error;
  }
}

function notifyRecorderListeners(): void {
  for (const listener of recorderListeners) listener();
}

function debugRunID(): string {
  const random = Math.random().toString(16).slice(2, 10);
  return `sldr_${Date.now().toString(36)}_${random}`;
}
