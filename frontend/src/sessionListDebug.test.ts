import { beforeEach, test, expect } from "vitest";
import {
  analyzeSessionListDebugSnapshot,
  captureSessionListDebugSnapshot,
  getSessionListDebugSnapshot,
  recordSessionListDebugEvent,
  resetSessionListDebugForTest,
  setSessionListDebugCaptureReporterForTest,
  updateSessionListDebugStore,
  updateSessionListDebugRender,
  type SessionListDebugCapturePayload,
  type SessionListDebugRow,
} from "./sessionListDebug";
import {
  resetSessionListDebugRecorderForTest,
  setSessionListDebugRecorderOptionsForTest,
  startSessionListDebugRecording,
  stopSessionListDebugRecording,
} from "./sessionListDebugRecorder";

function debugRow(overrides: Partial<SessionListDebugRow> = {}): SessionListDebugRow {
  return {
    id: "223",
    name: null,
    display_name: "223",
    display_name_source: "generated",
    pod_name: "session-223",
    status: "Pending",
    visible: true,
    agent_avatar_id: "av_agent",
    system_avatar_id: "av_system",
    rendered_avatar_id: "av_agent",
    ...overrides,
  };
}

beforeEach(() => {
  resetSessionListDebugRecorderForTest();
  resetSessionListDebugForTest();
});

test("created session identity changes stay local until a manual capture", async () => {
  const reports: SessionListDebugCapturePayload[] = [];
  setSessionListDebugCaptureReporterForTest((payload) => {
    reports.push(payload);
  });

  recordSessionListDebugEvent({
    kind: "create-response",
    source: "createSession",
    session_id: "223",
    row: debugRow(),
  });
  updateSessionListDebugRender({
    active_id: "223",
    sessions: [debugRow({ name: "homepage", display_name: "homepage" })],
  });
  await Promise.resolve();

  expect(reports.length).toBe(0);
  expect(getSessionListDebugSnapshot().events.some((event) => event.kind === "render-state")).toBe(true);
});

test("manual capture posts the current debug snapshot", async () => {
  const reports: SessionListDebugCapturePayload[] = [];
  setSessionListDebugCaptureReporterForTest((payload) => {
    reports.push(payload);
    return { capture_id: "sldc_test", accepted: true };
  });

  recordSessionListDebugEvent({
    kind: "create-response",
    source: "createSession",
    session_id: "223",
    row: debugRow(),
  });
  updateSessionListDebugRender({
    active_id: "223",
    sessions: [debugRow({ status: "Active", display_name: "223" })],
  });
  const result = await captureSessionListDebugSnapshot({
    reason: "manual-capture",
    session_id: "223",
    source: "SessionListDebugPage",
    detail: { note: "bad render visible" },
  });

  expect(result?.capture_id).toBe("sldc_test");
  expect(reports.length).toBe(1);
  expect(reports[0]?.reason).toBe("manual-capture");
  expect(reports[0]?.session_id).toBe("223");
  expect(reports[0]?.source).toBe("SessionListDebugPage");
  expect(reports[0]?.snapshot.events.at(-1)?.kind).toBe("manual-capture-requested");
  const diagnostics = (reports[0]?.detail as {
    session_list_debug_diagnostics?: { issue_count?: number; generated_display_names?: string[] };
  }).session_list_debug_diagnostics;
  expect(diagnostics?.issue_count).toBe(0);
  expect(diagnostics?.generated_display_names).toEqual(["223"]);
});

test("session-list diagnostics flag missing and mismatched identity", () => {
  updateSessionListDebugStore({
    cursor: "10",
    rows: [debugRow({ agent_avatar_id: null, rendered_avatar_id: null })],
    tombstones: [],
  });
  updateSessionListDebugRender({
    active_id: "223",
    sessions: [
      debugRow({
        agent_avatar_id: null,
        rendered_avatar_id: "jp1-sattler",
        name: "wrong name",
        display_name: "wrong name",
        display_name_source: "durable",
      }),
    ],
  });

  const diagnostics = analyzeSessionListDebugSnapshot(getSessionListDebugSnapshot());
  expect(diagnostics.issues.some((issue) => issue.code === "rendered_avatar_without_assignment")).toBe(true);
  expect(diagnostics.issues.some(
          (issue) => issue.code === "store_render_identity_mismatch" && issue.field === "name",
        )).toBe(true);
});

test("manual recording samples can share a run id", async () => {
  const reports: SessionListDebugCapturePayload[] = [];
  setSessionListDebugCaptureReporterForTest((payload) => {
    reports.push(payload);
  });

  recordSessionListDebugEvent({
    kind: "create-response",
    source: "createSession",
    session_id: "223",
    row: debugRow(),
  });
  recordSessionListDebugEvent({
    kind: "rename-response",
    source: "renameSession",
    session_id: "223",
    row: debugRow({ name: "research", display_name: "research" }),
  });
  updateSessionListDebugRender({
    active_id: "223",
    sessions: [debugRow({ status: "Active", display_name: "223" })],
  });
  await captureSessionListDebugSnapshot({
    reason: "manual-record-start",
    source: "SessionListDebugPage",
    detail: { run_id: "sldr_test", phase: "start" },
  });
  await captureSessionListDebugSnapshot({
    reason: "manual-record-sample",
    source: "SessionListDebugPage",
    detail: { run_id: "sldr_test", phase: "sample", sample_index: 1 },
  });

  expect(reports.length).toBe(2);
  expect(reports[0]?.session_id).toBe("223");
  expect(reports[1]?.reason).toBe("manual-record-sample");
  expect((reports[1]?.detail as { run_id?: string }).run_id).toEqual("sldr_test");
});

test("manual recording keeps sampling after controls unmount", async () => {
  const reports: SessionListDebugCapturePayload[] = [];
  setSessionListDebugCaptureReporterForTest((payload) => {
    reports.push(payload);
  });
  setSessionListDebugRecorderOptionsForTest({
    duration_ms: 500,
    sample_interval_ms: 200,
    event_sample_debounce_ms: 10,
  });

  startSessionListDebugRecording("SettingsAdmin");
  await waitFor(() => reports.some((report) => report.reason === "manual-record-start"));

  recordSessionListDebugEvent({
    kind: "create-response",
    source: "createSession",
    session_id: "223",
    row: debugRow(),
  });
  updateSessionListDebugRender({
    active_id: "223",
    sessions: [debugRow({ name: "wrong name", display_name: "wrong name" })],
  });

  await waitFor(() =>
    reports.some(
      (report) =>
        report.reason === "manual-record-sample" &&
        (report.detail as { phase?: string }).phase === "event-sample",
    ),
  );
  stopSessionListDebugRecording("manual");

  const sample = reports.find(
    (report) =>
      report.reason === "manual-record-sample" &&
      (report.detail as { phase?: string }).phase === "event-sample",
  );
  expect(sample?.source).toBe("SettingsAdmin");
  expect(sample?.session_id).toBe("223");
  expect(sample?.snapshot.events.some(
          (event) =>
            event.kind === "render-state" &&
            event.rows?.some((row) => row.id === "223" && row.display_name === "wrong name"),
        )).toBe(true);
});

test("manual capture failures are retained in the local debug ring", async () => {
  setSessionListDebugCaptureReporterForTest(() => {
    throw new Error("store unavailable");
  });

  await expect(captureSessionListDebugSnapshot({
        reason: "manual-capture",
        session_id: "223",
        source: "SessionListDebugPage",
      })).rejects.toThrow(/store unavailable/);

  expect(getSessionListDebugSnapshot().events.some(
          (event) => event.kind === "manual-capture-report-failed",
        )).toBe(true);
});

async function waitFor(predicate: () => boolean): Promise<void> {
  const deadline = Date.now() + 1000;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  expect(predicate()).toBe(true);
}
