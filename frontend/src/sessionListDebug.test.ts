import { beforeEach, test } from "node:test";
import assert from "node:assert/strict";

import {
  captureSessionListDebugSnapshot,
  getSessionListDebugSnapshot,
  recordSessionListDebugEvent,
  resetSessionListDebugForTest,
  setSessionListDebugCaptureReporterForTest,
  updateSessionListDebugRender,
  type SessionListDebugCapturePayload,
  type SessionListDebugRow,
} from "./sessionListDebug";

function debugRow(overrides: Partial<SessionListDebugRow> = {}): SessionListDebugRow {
  return {
    id: "223",
    name: null,
    display_name: "223",
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

  assert.equal(reports.length, 0);
  assert.equal(
    getSessionListDebugSnapshot().events.some((event) => event.kind === "render-state"),
    true,
  );
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

  assert.equal(result?.capture_id, "sldc_test");
  assert.equal(reports.length, 1);
  assert.equal(reports[0]?.reason, "manual-capture");
  assert.equal(reports[0]?.session_id, "223");
  assert.equal(reports[0]?.source, "SessionListDebugPage");
  assert.equal(reports[0]?.snapshot.events.at(-1)?.kind, "manual-capture-requested");
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

  assert.equal(reports.length, 2);
  assert.equal(reports[0]?.session_id, "223");
  assert.equal(reports[1]?.reason, "manual-record-sample");
  assert.deepEqual((reports[1]?.detail as { run_id?: string }).run_id, "sldr_test");
});

test("manual capture failures are retained in the local debug ring", async () => {
  setSessionListDebugCaptureReporterForTest(() => {
    throw new Error("store unavailable");
  });

  await assert.rejects(
    captureSessionListDebugSnapshot({
      reason: "manual-capture",
      session_id: "223",
      source: "SessionListDebugPage",
    }),
    /store unavailable/,
  );

  assert.equal(
    getSessionListDebugSnapshot().events.some(
      (event) => event.kind === "manual-capture-report-failed",
    ),
    true,
  );
});
