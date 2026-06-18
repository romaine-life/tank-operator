#!/usr/bin/env node

// Regression guard: control-action ledger writes authorize off the VERIFIED
// per-session service-principal subject, never a caller-asserted request header.
//
// Why this exists: PR #1207 gated POST /api/internal/sessions/{id}/control-actions
// on an X-Tank-Caller-Session-Id header *in addition to* the subject. Every
// already-running session pod's sidecar predated that header, so every governed-git
// control-action (publish / CI / mergeability / break-glass) 403'd and was silently
// swallowed by the sidecar — the control_action_events ledger froze system-wide for
// ~2.5h on 2026-06-16, stalling the hot-swap verify gate, break-glass, and ci-wait.
// The fix authorizes solely off the unforgeable subject (svc:tank:<id> /
// svc:tank:slot-<n>-session-<id>) that auth.romaine.life mints from the pod's
// tank-operator/session-id annotation. This guard keeps the caller-asserted-header
// factor from coming back. "Done" for that boundary means this script exits 0.

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (rel) => fs.readFileSync(path.join(repoRoot, rel), "utf8");

const controlActions = read("backend-go/cmd/tank-operator/control_actions.go");
const handlersInternal = read("backend-go/cmd/tank-operator/handlers_internal.go");
const observability = read("backend-go/cmd/tank-operator/observability.go");

const failures = [];
const must = (cond, msg) => {
  if (!cond) failures.push(msg);
};

// 1. The per-session authorization helper must not consume the request — a caller
//    header is a claim, not a verified identity, and must never gate the write.
must(
  /func \(s \*appServer\) internalCallerMatchesSession\(user \*auth\.User, sessionID string\) bool/.test(controlActions),
  "internalCallerMatchesSession must authorize off (user, sessionID) only — no *http.Request param.",
);
must(
  !/internalCallerMatchesSession\([^)]*\*http\.Request/.test(controlActions),
  "internalCallerMatchesSession must not take an *http.Request — control-action authz must not read caller-asserted headers.",
);

// 2. The caller-asserted session-identity headers must not gate control-action writes again.
for (const ghost of ["callerSessionIDHeader", "callerSessionScopeHeader"]) {
  must(
    !controlActions.includes(ghost) && !handlersInternal.includes(ghost),
    `${ghost} (X-Tank-Caller-Session-*) must not be reintroduced as a control-action authorization factor.`,
  );
}

// 3. Authorization must be the verified subject, scope-bound to this backend so a
//    production identity cannot write a slot session's ledger (or vice versa).
must(
  /serviceSubjectMatchesSession\(user\.Sub, sessionID\)/.test(controlActions),
  "handleInternalAppendControlAction must authorize via serviceSubjectMatchesSession(user.Sub, sessionID).",
);
must(
  /localSessionScope\(\)/.test(controlActions) && /prodSessionScope/.test(controlActions),
  "serviceSubjectMatchesSession must bind the subject to the backend scope (localSessionScope / prodSessionScope).",
);

// 4. Authorization denials must stay observable — a named result series, not "other" —
//    so this exact silent outage trips TankControlActionAuthorizationDenied next time.
must(
  /func controlActionResultLabel[\s\S]*?"forbidden"[\s\S]*?return result/.test(observability),
  'controlActionResultLabel must keep "forbidden" as a named series so authorization denials are alertable.',
);

if (failures.length) {
  console.error("check-control-action-authz: FAIL");
  for (const f of failures) console.error("  - " + f);
  process.exit(1);
}
console.log("check-control-action-authz: OK — control-action writes authorize off the verified per-session subject.");
