import { beforeEach, test } from "node:test";
import assert from "node:assert/strict";

import {
  recordSessionListDebugEvent,
  resetSessionListDebugForTest,
  setSessionListDebugCaptureReporterForTest,
  updateSessionListDebugRender,
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

test("auto capture reports a created session name mutation", async () => {
  const reports: unknown[] = [];
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

  assert.equal(reports.length, 1);
  assert.equal((reports[0] as { reason: string }).reason, "created-session-name-mutated");
  assert.equal((reports[0] as { session_id: string }).session_id, "223");
});

test("auto capture does not report the default display name for an unnamed created session", async () => {
  const reports: unknown[] = [];
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
    sessions: [debugRow({ status: "Active", display_name: "223" })],
  });
  await Promise.resolve();

  assert.equal(reports.length, 0);
});

test("auto capture treats an explicit rename response as the new expected name", async () => {
  const reports: unknown[] = [];
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
    sessions: [debugRow({ name: "research", display_name: "research" })],
  });
  await Promise.resolve();

  assert.equal(reports.length, 0);
});
