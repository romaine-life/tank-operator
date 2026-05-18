#!/usr/bin/env node

// Completion manifest for the durable turn.interrupt_requested migration.
//
// This script is the spec for the migration. It is intentionally committed to
// the branch BEFORE any feature code so its expectations are auditable
// independently of whatever an agent later writes. "Done" for the migration
// means this script exits 0. There is no other measure of done — agent
// self-reports are not load-bearing.
//
// Workflow:
//   1. This file lands as commit 1 of the branch.
//   2. Reviewer reads the plan and this manifest side-by-side; pushes back on
//      anything missing before any feature code is written.
//   3. Agent implements. Goal: all FAIL rows become PASS.
//   4. After merge, this file stays in scripts/ as a regression guard, exactly
//      like scripts/check-removed-chat-runtime.mjs.
//
// Each check has a `from:` field naming the plan section it derives from, so
// "did the manifest cover every plan paragraph?" is itself answerable by grep.
//
// Skip the slow exec gates during structural iteration with:
//   SKIP_EXEC=1 node scripts/check-stop-request-migration.mjs

import { spawnSync } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const skipExec = process.env.SKIP_EXEC === "1";

const CHECKS = [
  // ────────────────────────── Schema, contract, fixtures ──────────────────────────
  {
    id: "schema-type-enum",
    from: "Schema, contract, fixtures",
    file: "schemas/tank-conversation-event.schema.json",
    description: "schema /properties/type/enum contains turn.interrupt_requested",
    kind: "json-enum-includes",
    pointer: ["properties", "type", "enum"],
    value: "turn.interrupt_requested",
  },
  {
    id: "schema-allof-clause",
    from: "Schema, contract, fixtures",
    file: "schemas/tank-conversation-event.schema.json",
    description: "schema allOf clause for new type pins actor=system, source=tank, requires turn_id",
    kind: "json-allof-clause",
    targetType: "turn.interrupt_requested",
    requireActor: "system",
    requireSource: "tank",
    requireFields: ["turn_id"],
  },
  {
    id: "fixture-canonical",
    from: "Schema, contract, fixtures",
    file: "schemas/tank-conversation-event.fixtures.json",
    description: "canonical fixture for new type exists (mandatory per check-tank-conversation-contract.mjs)",
    kind: "json-array-has-event",
    arrayPath: ["events"],
    eventTypePath: ["event", "type"],
    value: "turn.interrupt_requested",
  },
  {
    id: "shared-js-enum",
    from: "Schema, contract, fixtures",
    file: "runner-shared/conversation.js",
    description: "TANK_EVENT_TYPES (JS) lists the new type",
    kind: "grep-present",
    pattern: /"turn\.interrupt_requested"/,
  },
  {
    id: "shared-js-validator-case",
    from: "Schema, contract, fixtures",
    file: "runner-shared/conversation.js",
    description: "isValidEventByType enforces actor=system, source=tank for new type",
    kind: "grep-present",
    pattern: /case "turn\.interrupt_requested":[\s\S]{0,400}?actor === "system"[\s\S]{0,400}?source === "tank"/,
  },
  {
    id: "shared-dts-enum",
    from: "Schema, contract, fixtures",
    file: "runner-shared/conversation.d.ts",
    description: "TANK_EVENT_TYPES (.d.ts) tuple lists the new type",
    kind: "grep-present",
    pattern: /"turn\.interrupt_requested"/,
  },
  {
    id: "go-event-const",
    from: "Schema, contract, fixtures",
    file: "backend-go/internal/conversation/types.go",
    description: "EventTurnInterruptRequested EventType const declared with literal value",
    kind: "grep-present",
    pattern: /EventTurnInterruptRequested\s+EventType\s*=\s*"turn\.interrupt_requested"/,
  },
  {
    id: "go-valid-event-type-includes",
    from: "Schema, contract, fixtures",
    file: "backend-go/internal/conversation/types.go",
    description: "validEventType switch references EventTurnInterruptRequested",
    kind: "grep-present",
    pattern: /func validEventType[\s\S]{0,800}?EventTurnInterruptRequested/,
  },
  {
    id: "go-validate-case",
    from: "Schema, contract, fixtures",
    file: "backend-go/internal/conversation/types.go",
    description: "validateEventMap pins actor=system, source=tank for new type",
    kind: "grep-present",
    pattern: /case EventTurnInterruptRequested:[\s\S]{0,400}?ActorSystem[\s\S]{0,400}?SourceTank/,
  },

  // ────────────────────────── Backend builder + handler ──────────────────────────
  {
    id: "go-builder-fn",
    from: "Backend writer",
    file: "backend-go/internal/conversation/builders.go",
    description: "TurnInterruptRequestedEventMap function exists",
    kind: "grep-present",
    pattern: /func TurnInterruptRequestedEventMap\(/,
  },
  {
    id: "go-builder-args-struct",
    from: "Backend writer",
    file: "backend-go/internal/conversation/builders.go",
    description: "TurnInterruptRequestedArgs struct exists",
    kind: "grep-present",
    pattern: /type TurnInterruptRequestedArgs struct/,
  },
  {
    id: "go-builder-deterministic-event-id",
    from: "Backend writer",
    file: "backend-go/internal/conversation/builders.go",
    description: "builder uses deterministic event_id of form `<turnID>:turn.interrupt_requested` (so duplicate POSTs dedupe at Postgres UNIQUE)",
    kind: "grep-present",
    pattern: /TurnID\s*\+\s*":turn\.interrupt_requested"/,
  },
  {
    id: "handler-uses-builder",
    from: "Backend writer",
    file: "backend-go/cmd/tank-operator/handlers_turns.go",
    description: "handleInterruptSessionTurn invokes TurnInterruptRequestedEventMap",
    kind: "grep-present",
    pattern: /TurnInterruptRequestedEventMap\(/,
  },
  {
    id: "handler-persist-before-publish",
    from: "Backend writer",
    file: "backend-go/cmd/tank-operator/handlers_turns.go",
    description: "in handleInterruptSessionTurn body, persistBackendEvent appears before PublishCommand",
    kind: "order-in-function",
    functionName: "handleInterruptSessionTurn",
    earlier: /persistBackendEvent\(/,
    later: /sessionBus\.PublishCommand\(/,
  },

  // ────────────────────────── Activity + lifecycle ──────────────────────────
  {
    id: "lifecycle-types-includes",
    from: "Read path: lifecycle fold",
    file: "backend-go/internal/store/session_events.go",
    description: "LifecycleEventTypes contains the new type",
    kind: "grep-present",
    pattern: /LifecycleEventTypes\s*=\s*\[\]string\{[\s\S]*?"turn\.interrupt_requested"/,
  },
  {
    id: "unread-output-turn-excludes",
    from: "Read path: lifecycle fold",
    file: "backend-go/internal/store/session_events.go",
    description: "UnreadOutputTurnTypes does NOT include the new type (it is a control marker, not unread output)",
    kind: "block-absent",
    blockPattern: /UnreadOutputTurnTypes\s*=\s*\[\]string\{[\s\S]*?\}/,
    absentPattern: /"turn\.interrupt_requested"/,
  },
  {
    id: "unread-output-item-excludes",
    from: "Read path: lifecycle fold",
    file: "backend-go/internal/store/session_events.go",
    description: "UnreadOutputItemTypes does NOT include the new type",
    kind: "block-absent",
    blockPattern: /UnreadOutputItemTypes\s*=\s*\[\]string\{[\s\S]*?\}/,
    absentPattern: /"turn\.interrupt_requested"/,
  },
  {
    id: "find-turn-terminal-excludes",
    from: "Read path: lifecycle fold",
    file: "backend-go/internal/store/session_events.go",
    description: "FindTurnTerminal query does NOT treat the new type as terminal (interrupt_requested is mid-flight)",
    kind: "block-absent",
    blockPattern: /func \(s \*postgresSessionEventStore\) FindTurnTerminal\([\s\S]*?\)\.Scan\(&payload\)/,
    absentPattern: /turn\.interrupt_requested|EventTurnInterruptRequested/,
  },
  {
    id: "activity-fold-case-stopping",
    from: "Read path: lifecycle fold",
    file: "backend-go/internal/lifecycleevents/activity.go",
    description: "DeriveActivitySummary maps new event to Status = \"stopping\"",
    kind: "grep-present",
    pattern: /case "turn\.interrupt_requested":[\s\S]{0,400}?Status\s*=\s*"stopping"/,
  },
  {
    id: "activity-fold-preserves-active-turn",
    from: "Read path: lifecycle fold",
    file: "backend-go/internal/lifecycleevents/activity.go",
    description: "stopping case does NOT null out ActiveTurnID (turn is still mid-flight)",
    kind: "block-absent",
    blockPattern: /case "turn\.interrupt_requested":[\s\S]{0,400}?(?=case "|\}\s*$)/,
    absentPattern: /ActiveTurnID\s*=\s*nil/,
  },

  // ────────────────────────── Frontend reducer + projection ──────────────────────────
  {
    id: "reducer-runstatus-stopping",
    from: "Frontend reducer + projection",
    file: "frontend/src/conversationReducer.ts",
    description: "ConversationRunStatus union includes \"stopping\"",
    kind: "grep-present",
    pattern: /export type ConversationRunStatus[\s\S]{0,400}?"stopping"/,
  },
  {
    id: "reducer-interrupt-requests-state-field",
    from: "Frontend reducer + projection",
    file: "frontend/src/conversationReducer.ts",
    description: "ConversationReducerState declares interruptRequests",
    kind: "grep-present",
    pattern: /interruptRequests\s*:/,
  },
  {
    id: "reducer-case-present",
    from: "Frontend reducer + projection",
    file: "frontend/src/conversationReducer.ts",
    description: "reducer has case \"turn.interrupt_requested\"",
    kind: "grep-present",
    pattern: /case "turn\.interrupt_requested"/,
  },
  {
    id: "reducer-transition-gated",
    from: "Frontend reducer + projection",
    file: "frontend/src/conversationReducer.ts",
    description: "stopping transition is gated on active states (submitted, streaming, needs_input)",
    kind: "grep-present",
    // The transition logic may be inlined at the case or factored into a
    // helper named applyInterrupt*. Either way, all three active-state
    // literals must appear within a contiguous block originating at one of
    // those anchors. Order between the three is intentionally unspecified.
    pattern: /(?:applyInterrupt|case "turn\.interrupt_requested")[\s\S]{0,1500}?"submitted"[\s\S]{0,400}?"streaming"[\s\S]{0,400}?"needs_input"/,
  },
  {
    id: "projection-stopping-field",
    from: "Frontend reducer + projection",
    file: "frontend/src/conversationProjection.ts",
    description: "ConversationProjection has stopping: boolean",
    kind: "grep-present",
    pattern: /stopping\s*:\s*boolean/,
  },
  {
    id: "projection-merges-interrupt-requests",
    from: "Frontend reducer + projection",
    file: "frontend/src/conversationProjection.ts",
    description: "projection reads interruptRequests from state",
    kind: "grep-present",
    pattern: /interruptRequests/,
  },

  // ────────────────────────── Frontend activity ──────────────────────────
  {
    id: "activity-type-stopping",
    from: "Frontend reducer + projection",
    file: "frontend/src/sessionActivity.ts",
    description: "ConversationActivityStatus union includes \"stopping\"",
    kind: "grep-present",
    pattern: /export type ConversationActivityStatus[\s\S]{0,400}?"stopping"/,
  },
  {
    id: "activity-label-stopping",
    from: "Frontend reducer + projection",
    file: "frontend/src/sessionActivity.ts",
    description: "sessionActivityStatusLabel returns \"Stopping\" for the new status",
    kind: "grep-present",
    pattern: /activity\?\.status === "stopping"[\s\S]{0,200}?"Stopping"/,
  },
  {
    id: "activity-chip-stopping",
    from: "Frontend reducer + projection",
    file: "frontend/src/sessionActivity.ts",
    description: "sessionActivityChips emits a chip with tone \"stopping\"",
    kind: "grep-present",
    pattern: /tone:\s*"stopping"/,
  },
  {
    id: "activity-dot-stopping",
    from: "Frontend reducer + projection",
    file: "frontend/src/sessionActivity.ts",
    description: "sessionActivityDotStatus returns \"agent-stopping\" for the new status",
    kind: "grep-present",
    pattern: /"agent-stopping"/,
  },

  // ────────────────────────── Frontend deletions (migration policy) ──────────────────────────
  {
    id: "app-no-stopRequested",
    from: "Frontend deletions",
    file: "frontend/src/App.tsx",
    description: "App.tsx does NOT reference stopRequested (deleted local flag)",
    kind: "grep-absent",
    pattern: /\bstopRequested\b/,
  },
  {
    id: "app-no-stoppingTargetRef",
    from: "Frontend deletions",
    file: "frontend/src/App.tsx",
    description: "App.tsx does NOT reference stoppingTargetRef (deleted local ref)",
    kind: "grep-absent",
    pattern: /\bstoppingTargetRef\b/,
  },
  {
    id: "app-projection-drives-stopping",
    from: "Frontend deletions",
    file: "frontend/src/App.tsx",
    description: "applySdkProjectionToUi maps projection.runStatus === \"stopping\" to the local stopping run status (single, projection-driven path)",
    kind: "grep-present",
    // setRunStatus(...) wraps a comparison-to-stopping that resolves to the
    // "stopping" literal — either inline ternary or branched. The match is
    // intentionally loose so a future refactor can split the branch out.
    pattern: /setRunStatus\([\s\S]{0,400}?projection\.runStatus\s*===\s*"stopping"[\s\S]{0,200}?"stopping"/,
  },
  {
    id: "app-cancelrun-no-imperative-stopping",
    from: "Frontend deletions",
    file: "frontend/src/App.tsx",
    description: "cancelRun does NOT call setRunStatus(\"stopping\") — that path is now projection-driven only",
    kind: "order-in-function-absent",
    functionName: "cancelRun",
    absentPattern: /setRunStatus\(\s*["']stopping["']\s*\)/,
  },
  {
    id: "prompt-input-isStopping-prop",
    from: "Frontend deletions",
    file: "frontend/src/components/ai-elements/prompt-input.tsx",
    description: "PromptInputSubmit accepts isStopping prop (so button can render \"Stopping…\" disabled)",
    kind: "grep-present",
    pattern: /isStopping/,
  },

  // ────────────────────────── Migration guard ──────────────────────────
  {
    id: "guard-stopRequested",
    from: "Migration guard",
    file: "scripts/check-removed-chat-runtime.mjs",
    description: "migration guard rule blocks reintroduction of stopRequested",
    kind: "grep-present",
    pattern: /retired local stop-request flag/,
  },
  {
    id: "guard-stoppingTargetRef",
    from: "Migration guard",
    file: "scripts/check-removed-chat-runtime.mjs",
    description: "migration guard rule blocks reintroduction of stoppingTargetRef",
    kind: "grep-present",
    pattern: /retired stopping-target ref/,
  },

  // ────────────────────────── Test conversions ──────────────────────────
  {
    id: "migration-test-flipped-to-absent",
    from: "Test conversions",
    file: "frontend/src/migrationPolicy.test.ts",
    description: "assertion now expects setRunStatus(\"stopping\") to be ABSENT in cancelRun body",
    kind: "grep-present",
    pattern: /cancelRunBody\.includes\(\s*'setRunStatus\("stopping"\)'\s*\)\s*,\s*false/,
  },
  {
    id: "migration-test-no-stale-true-assertion",
    from: "Test conversions",
    file: "frontend/src/migrationPolicy.test.ts",
    description: "the old assertion that REQUIRED setRunStatus(\"stopping\") in cancelRun is gone",
    kind: "grep-absent",
    pattern: /cancelRunBody\.includes\(\s*'setRunStatus\("stopping"\)'\s*\)\s*,\s*true/,
  },
  {
    id: "migration-test-no-stopRequested",
    from: "Test conversions",
    file: "frontend/src/migrationPolicy.test.ts",
    description: "complementary assertion: stopRequested absent from cancelRun body",
    kind: "grep-present",
    pattern: /cancelRunBody\.includes\(\s*['"]stopRequested['"]\s*\)\s*,\s*false/,
  },
  {
    id: "migration-test-no-stoppingTargetRef",
    from: "Test conversions",
    file: "frontend/src/migrationPolicy.test.ts",
    description: "complementary assertion: stoppingTargetRef absent from cancelRun body",
    kind: "grep-present",
    pattern: /cancelRunBody\.includes\(\s*['"]stoppingTargetRef['"]\s*\)\s*,\s*false/,
  },

  // ────────────────────────── Observability ──────────────────────────
  {
    id: "metric-counter-declared",
    from: "Observability",
    file: "backend-go/cmd/tank-operator/observability.go",
    description: "tank_turn_interrupt_request_total counter declared",
    kind: "grep-present",
    pattern: /tank_turn_interrupt_request_total/,
  },
  {
    id: "metric-outcome-label",
    from: "Observability",
    file: "backend-go/cmd/tank-operator/observability.go",
    description: "counter carries outcome label (single label, bounded cardinality)",
    kind: "grep-present",
    pattern: /tank_turn_interrupt_request_total[\s\S]{0,500}?\[\]string\{\s*"outcome"\s*\}/,
  },
  {
    id: "handler-records-metric",
    from: "Observability",
    file: "backend-go/cmd/tank-operator/handlers_turns.go",
    description: "handleInterruptSessionTurn calls the metric (at minimum, at one exit point)",
    kind: "grep-present",
    pattern: /turnInterruptRequestTotal\b/,
  },

  // ────────────────────────── Tests by name ──────────────────────────
  {
    id: "test-handler-persist-before-publish",
    from: "Tests",
    file: "backend-go/cmd/tank-operator/handlers_turns_test.go",
    description: "TestInterruptPersistsRequestedEventBeforeCommand exists",
    kind: "grep-present",
    pattern: /func TestInterruptPersistsRequestedEventBeforeCommand\b/,
  },
  {
    id: "test-handler-persist-failure",
    from: "Tests",
    file: "backend-go/cmd/tank-operator/handlers_turns_test.go",
    description: "TestInterruptPersistFailureBlocksCommand exists",
    kind: "grep-present",
    pattern: /func TestInterruptPersistFailureBlocksCommand\b/,
  },
  {
    id: "test-handler-publish-failure",
    from: "Tests",
    file: "backend-go/cmd/tank-operator/handlers_turns_test.go",
    description: "TestInterruptPublishFailureLeavesRequestedEventAndCommandFailed exists",
    kind: "grep-present",
    pattern: /func TestInterruptPublishFailureLeavesRequestedEventAndCommandFailed\b/,
  },
  {
    id: "test-handler-idempotent",
    from: "Tests",
    file: "backend-go/cmd/tank-operator/handlers_turns_test.go",
    description: "TestInterruptIsIdempotentByEventID exists",
    kind: "grep-present",
    pattern: /func TestInterruptIsIdempotentByEventID\b/,
  },
  {
    id: "test-activity-fold-stopping",
    from: "Tests",
    file: "backend-go/internal/lifecycleevents/activity_test.go",
    description: "activity fold test exercises turn.interrupt_requested → stopping",
    kind: "grep-present",
    pattern: /turn\.interrupt_requested/,
  },
  {
    id: "test-reducer-stopping",
    from: "Tests",
    file: "frontend/src/conversationReducer.test.ts",
    description: "reducer tests cover the new event + stopping transitions",
    kind: "grep-present",
    pattern: /turn\.interrupt_requested/,
  },
  {
    id: "test-projection-chip",
    from: "Tests",
    file: "frontend/src/conversationProjection.test.ts",
    description: "projection tests cover the interrupt-request chip rendering",
    kind: "grep-present",
    pattern: /turn\.interrupt_requested|Stop requested/,
  },
  {
    id: "test-activity-frontend-stopping",
    from: "Tests",
    file: "frontend/src/sessionActivity.test.ts",
    description: "sessionActivity tests cover stopping label/dot/chip",
    kind: "grep-present",
    pattern: /stopping/i,
  },
  {
    id: "test-replay-vs-live-stop",
    from: "Tests",
    file: "frontend/src/sdkDurableStreamRegression.test.ts",
    description: "replay-equals-live regression covers a stop scenario",
    kind: "grep-present",
    pattern: /turn\.interrupt_requested/,
  },

  // ────────────────────────── Docs ──────────────────────────
  {
    id: "docs-protocol-event-listed",
    from: "Docs",
    file: "docs/tank-conversation-protocol.md",
    description: "tank-conversation-protocol.md mentions turn.interrupt_requested",
    kind: "grep-present",
    pattern: /turn\.interrupt_requested/,
  },
  {
    id: "docs-protocol-state-machine-stopping",
    from: "Docs",
    file: "docs/tank-conversation-protocol.md",
    description: "state machine doc lists `stopping`",
    kind: "grep-present",
    pattern: /`stopping`/,
  },
  {
    id: "docs-protocol-no-stale-ui-rule",
    from: "Docs",
    file: "docs/tank-conversation-protocol.md",
    description: "the old \"UI may show stopping after publish succeeds\" sentence is gone",
    kind: "grep-absent",
    pattern: /UI may show `stopping` after publish succeeds, but it must not mark the run\s+stopped or clear the active turn until the durable `turn\.interrupted` event/,
  },
  {
    id: "docs-observability",
    from: "Docs",
    file: "docs/observability.md",
    description: "observability.md registers the new counter",
    kind: "grep-present",
    pattern: /tank_turn_interrupt_request_total/,
  },

  // ────────────────────────── Control plane (interrupt delivery) ──────────────────────────
  //
  // The durable stop *boundary* (turn.interrupt_requested) shipped in PR
  // #481 made the request observable. It did not make the interrupt
  // *effective* during long tool-use turns: both runners' JetStream
  // command consumer is configured with max_ack_pending=1 so the
  // in-flight submit_turn's ack window (sustained by working() heartbeats
  // for the duration of the turn) held the queued interrupt_turn
  // command behind it. The fix splits data plane (submit_turn,
  // input_reply — serial, max_ack_pending=1) from control plane
  // (interrupt_turn — low-latency, separate durable consumer) onto
  // distinct JetStream subjects. The checks below pin the load-bearing
  // invariants so a future refactor can't merge the planes back.
  {
    id: "go-control-subject-helper",
    from: "Control plane",
    file: "backend-go/internal/sessionbus/subjects.go",
    description: "ControlSubject helper exists and is distinct from CommandSubject",
    kind: "grep-present",
    pattern: /func ControlSubject\(sessionStorageKey, provider string\) string \{[\s\S]{0,400}?\.control\./,
  },
  {
    id: "go-subject-for-command-routes-interrupt",
    from: "Control plane",
    file: "backend-go/internal/sessionbus/subjects.go",
    description: "SubjectForCommand routes CommandInterrupt to ControlSubject",
    kind: "grep-present",
    pattern: /func SubjectForCommand\([\s\S]{0,400}?command\.Type == CommandInterrupt[\s\S]{0,200}?return ControlSubject\(/,
  },
  {
    id: "go-publish-uses-subject-for-command",
    from: "Control plane",
    file: "backend-go/internal/sessionbus/bus.go",
    description: "PublishCommand selects subject via SubjectForCommand (not CommandSubject directly)",
    kind: "grep-present",
    pattern: /b\.js\.Publish\([\s\S]{0,200}?SubjectForCommand\(command\)/,
  },
  {
    id: "go-publish-does-not-direct-route-interrupt",
    from: "Control plane",
    file: "backend-go/internal/sessionbus/bus.go",
    description: "PublishCommand body does NOT call CommandSubject directly (routing is the single decision in SubjectForCommand)",
    kind: "order-in-function-absent",
    functionName: "PublishCommand",
    absentPattern: /CommandSubject\(/,
  },
  {
    id: "js-control-subject-helper",
    from: "Control plane",
    file: "runner-shared/sessionBus.js",
    description: "controlSubject helper is exported and mirrors the Go ControlSubject wire shape",
    kind: "grep-present",
    pattern: /export function controlSubject\([\s\S]{0,200}?\.control\./,
  },
  {
    id: "js-start-control-consumer-method",
    from: "Control plane",
    file: "runner-shared/sessionBus.js",
    description: "SharedSessionBus exposes startControlConsumer",
    kind: "grep-present",
    pattern: /async startControlConsumer\(handler, signal\)/,
  },
  {
    id: "js-ensure-control-consumer-method",
    from: "Control plane",
    file: "runner-shared/sessionBus.js",
    description: "ensureControlConsumer registers a JetStream consumer on the control filter_subject",
    kind: "grep-present",
    pattern: /async ensureControlConsumer\(\)[\s\S]{0,1200}?filter_subject:\s*controlSubject\(/,
  },
  {
    id: "js-control-consumer-name-distinct",
    from: "Control plane",
    file: "runner-shared/sessionBus.js",
    description: "controlConsumerName carries a `_control_` segment so it doesn't collide with the data-plane consumer",
    kind: "grep-present",
    pattern: /controlConsumerName\(\)[\s\S]{0,400}?_control_/,
  },
  {
    id: "js-control-max-ack-pending-not-one",
    from: "Control plane",
    file: "runner-shared/sessionBus.js",
    description: "SESSION_CONTROL_MAX_ACK_PENDING default is greater than 1 (the data-plane budget that caused the regression)",
    kind: "grep-present",
    pattern: /SESSION_CONTROL_MAX_ACK_PENDING\s*=\s*parsePositiveInt\(\s*process\.env\.SESSION_CONTROL_MAX_ACK_PENDING\s*,\s*(?:[2-9]|\d{2,})/,
  },
  {
    id: "js-data-plane-drops-stray-interrupt",
    from: "Control plane",
    file: "runner-shared/sessionBus.js",
    description: "data-plane consumer ack-and-drops stray interrupts (cutover hygiene; handler is never invoked)",
    kind: "grep-present",
    pattern: /isInterruptCommand\(command\)[\s\S]{0,400}?record\.ack\(\)/,
  },
  {
    id: "agent-runner-starts-control-consumer",
    from: "Control plane",
    file: "agent-runner/src/runner.ts",
    description: "agent-runner Runner.run() starts the control consumer alongside the command consumer",
    kind: "grep-present",
    pattern: /this\.startControlConsumer\(signal\)/,
  },
  {
    id: "codex-runner-starts-control-consumer",
    from: "Control plane",
    file: "codex-runner/src/runner.ts",
    description: "codex-runner Runner.run() starts the control consumer alongside the command consumer",
    kind: "grep-present",
    pattern: /this\.startControlConsumer\(signal\)/,
  },
  {
    id: "agent-runner-command-consumer-no-interrupt-dispatch",
    from: "Control plane",
    file: "agent-runner/src/runner.ts",
    description: "agent-runner startCommandConsumer body does NOT dispatch interrupts (control plane is the only path)",
    kind: "order-in-function-absent",
    functionName: "startCommandConsumer",
    absentPattern: /acceptInterrupt/,
  },
  {
    id: "codex-runner-command-consumer-no-interrupt-dispatch",
    from: "Control plane",
    file: "codex-runner/src/runner.ts",
    description: "codex-runner startCommandConsumer body does NOT dispatch interrupts (control plane is the only path)",
    kind: "order-in-function-absent",
    functionName: "startCommandConsumer",
    absentPattern: /acceptInterrupt/,
  },
  {
    id: "agent-runner-control-consumer-dispatches-interrupt",
    from: "Control plane",
    file: "agent-runner/src/runner.ts",
    description: "agent-runner startControlConsumer body dispatches interrupts to acceptInterrupt",
    kind: "grep-present",
    pattern: /private startControlConsumer\([\s\S]{0,1200}?isInterruptCommand\(record\)[\s\S]{0,400}?acceptInterrupt\(record\)/,
  },
  {
    id: "codex-runner-control-consumer-dispatches-interrupt",
    from: "Control plane",
    file: "codex-runner/src/runner.ts",
    description: "codex-runner startControlConsumer body dispatches interrupts to acceptInterrupt",
    kind: "grep-present",
    pattern: /private startControlConsumer\([\s\S]{0,1200}?isInterruptCommand\(record\)[\s\S]{0,400}?acceptInterrupt\(record\)/,
  },

  // ────────────────────────── Self-telling observability ──────────────────────────
  {
    id: "alert-stop-not-delivered",
    from: "Self-telling observability",
    file: "k8s/templates/observability.yaml",
    description: "TankStopNotDelivered alert is declared (backend persisted > runner consumed)",
    kind: "grep-present",
    pattern: /alert:\s*TankStopNotDelivered/,
  },
  {
    id: "alert-stop-not-terminated",
    from: "Self-telling observability",
    file: "k8s/templates/observability.yaml",
    description: "TankStopNotTerminated alert is declared (runner consumed > terminal interrupted)",
    kind: "grep-present",
    pattern: /alert:\s*TankStopNotTerminated/,
  },

  // ────────────────────────── Tests by name (control plane) ──────────────────────────
  {
    id: "test-go-subject-routes-interrupt",
    from: "Tests",
    file: "backend-go/internal/sessionbus/bus_test.go",
    description: "TestSubjectForCommandRoutesInterruptToControlPlane exists",
    kind: "grep-present",
    pattern: /func TestSubjectForCommandRoutesInterruptToControlPlane\b/,
  },
  {
    id: "test-go-subject-routes-data-plane",
    from: "Tests",
    file: "backend-go/internal/sessionbus/bus_test.go",
    description: "TestSubjectForCommandRoutesDataPlane exists",
    kind: "grep-present",
    pattern: /func TestSubjectForCommandRoutesDataPlane\b/,
  },
  {
    id: "test-agent-runner-interrupt-during-submit",
    from: "Tests",
    file: "agent-runner/src/runner.test.ts",
    description: "agent-runner has a regression test that interrupt dispatch is independent of an in-flight submit (the load-bearing property the split establishes)",
    kind: "grep-present",
    pattern: /interrupt.*(?:during|while).*submit|dispatchInterruptIndependentlyOfSubmit/i,
  },

  // ────────────────────────── Executable gates ──────────────────────────
  {
    id: "exec-contract-checker",
    from: "Executable gates",
    description: "scripts/check-tank-conversation-contract.mjs exits 0",
    kind: "exec",
    command: ["node", "scripts/check-tank-conversation-contract.mjs"],
  },
  {
    id: "exec-migration-guard",
    from: "Executable gates",
    description: "scripts/check-removed-chat-runtime.mjs exits 0",
    kind: "exec",
    command: ["node", "scripts/check-removed-chat-runtime.mjs"],
  },
  {
    id: "exec-go-conversation",
    from: "Executable gates",
    description: "go test ./internal/conversation/... passes",
    kind: "exec",
    command: ["go", "test", "./internal/conversation/..."],
    cwd: "backend-go",
  },
  {
    id: "exec-go-store",
    from: "Executable gates",
    description: "go test ./internal/store/... passes",
    kind: "exec",
    command: ["go", "test", "./internal/store/..."],
    cwd: "backend-go",
  },
  {
    id: "exec-go-cmd",
    from: "Executable gates",
    description: "go test ./cmd/tank-operator/... passes",
    kind: "exec",
    command: ["go", "test", "./cmd/tank-operator/..."],
    cwd: "backend-go",
  },
  {
    id: "exec-frontend-tests",
    from: "Executable gates",
    description: "frontend npm test passes",
    kind: "exec",
    command: ["npm", "test"],
    cwd: "frontend",
  },

  // ────────────────────── Four-outcome contract (post-#532) ──────────────────────
  //
  // #532 closed two silent-stranding paths in the runner's interrupt
  // acceptance (the pre-submit race and the publish-gated SDK interrupt).
  // The replacement contract is: every accepted interrupt_turn produces
  // exactly one terminal-outcome increment on
  // tank_runner_interrupt_outcome_total. The checks below pin the load-
  // bearing pieces of that contract so a future refactor can't reintroduce
  // the silent-return shapes. See nelsong6/tank-operator#532 and
  // docs/tank-conversation-protocol.md → "Four-outcome contract on the
  // runner side" for the prose contract.
  {
    id: "runner-interrupt-outcome-counter",
    from: "Four-outcome contract (post-#532)",
    file: "agent-runner/src/metrics.ts",
    description: "tank_runner_interrupt_outcome_total counter is registered with the outcome label",
    kind: "grep-present",
    pattern: /name:\s*"tank_runner_interrupt_outcome_total"[\s\S]{0,400}?labelNames:\s*\[\s*"outcome"\s*\]/,
  },
  {
    id: "runner-pending-interrupts-buffer",
    from: "Four-outcome contract (post-#532)",
    file: "agent-runner/src/runner.ts",
    description: "pendingInterrupts buffer exists so an interrupt arriving before its submit_turn is not silently dropped",
    kind: "grep-present",
    pattern: /pendingInterrupts:\s*BufferedInterrupt\[\]/,
  },
  {
    id: "runner-buffer-drain-helper",
    from: "Four-outcome contract (post-#532)",
    file: "agent-runner/src/runner.ts",
    description: "drainPendingInterruptsFor applies pre-arrived stops at submit_turn dispatch time",
    kind: "grep-present",
    pattern: /drainPendingInterruptsFor\(turn:\s*PendingTurn\)/,
  },
  {
    id: "runner-sdk-interrupt-first",
    from: "Four-outcome contract (post-#532)",
    file: "agent-runner/src/runner.ts",
    description: "applyInterruptToTurn calls sdkQuery.interrupt() BEFORE publishing the durable terminal (load-bearing ordering)",
    kind: "grep-present",
    // sdkQuery.interrupt() must appear before publishTerminalWithRetry
    // inside applyInterruptToTurn. The match is intentionally loose so
    // formatting / comments / try-catch can vary.
    pattern: /applyInterruptToTurn[\s\S]{0,2000}?this\.sdkQuery\?\.\s*interrupt\(\)[\s\S]{0,1200}?publishTerminalWithRetry/,
  },
  {
    id: "runner-publish-retry-fallback",
    from: "Four-outcome contract (post-#532)",
    file: "agent-runner/src/runner.ts",
    description: "applyInterruptToTurn falls back to turn.failed{publish_interrupt_failed} when turn.interrupted publish exhausts retries",
    kind: "grep-present",
    pattern: /publish_interrupt_failed/,
  },
  {
    id: "runner-orphan-terminal",
    from: "Four-outcome contract (post-#532)",
    file: "agent-runner/src/runner.ts",
    description: "expireBufferedInterrupt emits turn.failed{interrupt_orphaned} so a buffered interrupt without a matching submit_turn resolves to a durable terminal",
    kind: "grep-present",
    pattern: /interrupt_orphaned/,
  },
  {
    id: "runner-no-silent-not-found",
    from: "Four-outcome contract (post-#532)",
    file: "agent-runner/src/runner.ts",
    description: "acceptInterrupt does NOT return silently with 'not_found' (the pre-#532 silent-stranding path)",
    kind: "grep-absent",
    // The shutdown-path interruptActiveTurn still uses InterruptOutcome
    // and may return "not_found"; what's forbidden is acceptInterrupt
    // (the client-driven path) silently early-exiting on no-match.
    // Pin the regression by forbidding `return "not_found"` inside
    // acceptInterrupt's body.
    pattern: /private\s+async\s+acceptInterrupt[\s\S]{0,2500}?return\s+"not_found"/,
  },
  {
    id: "observability-stop-outcome-stranded",
    from: "Four-outcome contract (post-#532)",
    file: "k8s/templates/observability.yaml",
    description: "TankStopOutcomeStranded alert composes the new counter buckets",
    kind: "grep-present",
    pattern: /alert:\s*TankStopOutcomeStranded[\s\S]{0,1200}?tank_runner_interrupt_outcome_total\{outcome="buffered"\}/,
  },
  {
    id: "observability-stop-publish-failed",
    from: "Four-outcome contract (post-#532)",
    file: "k8s/templates/observability.yaml",
    description: "TankStopPublishFailed alert fires on tank_runner_interrupt_outcome_total{outcome=\"publish_failed\"}",
    kind: "grep-present",
    pattern: /alert:\s*TankStopPublishFailed[\s\S]{0,800}?outcome="publish_failed"/,
  },
  {
    id: "docs-protocol-four-outcome-contract",
    from: "Four-outcome contract (post-#532)",
    file: "docs/tank-conversation-protocol.md",
    description: "tank-conversation-protocol.md documents the four-outcome contract by name",
    kind: "grep-present",
    pattern: /Four-outcome contract on the runner side/,
  },
  {
    id: "docs-diagnostic-discipline",
    from: "Four-outcome contract (post-#532)",
    file: "docs/diagnostic-discipline.md",
    description: "diagnostic-discipline doc exists (load-bearing investigation policy that should have prevented #532's misdiagnosis)",
    kind: "grep-present",
    pattern: /durable ledger is the source of truth/,
  },
  {
    id: "claudemd-diagnostic-pointer",
    from: "Four-outcome contract (post-#532)",
    file: "CLAUDE.md",
    description: "CLAUDE.md references the new diagnostic-discipline doc",
    kind: "grep-present",
    pattern: /docs\/diagnostic-discipline\.md/,
  },

  // ────────────────────── Four-outcome contract: codex-runner side (PR 2 of #532) ──────────────────────
  //
  // codex-runner mirrors agent-runner's buffer-and-apply shape. The codex
  // runner's interrupt flow uses two buffers (pendingInterrupts for the
  // case where the submit_turn is already tracked; orphanInterrupts for
  // the pre-submit race that pre-#532 silently ack'd). The checks below
  // pin every load-bearing piece on the codex side.
  {
    id: "codex-interrupt-outcome-counter",
    from: "Four-outcome contract (post-#532)",
    file: "codex-runner/src/metrics.ts",
    description: "codex-runner exposes tank_runner_interrupt_outcome_total (mode=codex via setDefaultLabels)",
    kind: "grep-present",
    pattern: /name:\s*"tank_runner_interrupt_outcome_total"[\s\S]{0,400}?labelNames:\s*\[\s*"outcome"\s*\]/,
  },
  {
    id: "codex-orphan-interrupts-buffer",
    from: "Four-outcome contract (post-#532)",
    file: "codex-runner/src/runner.ts",
    description: "orphanInterrupts buffer exists so a pre-submit-race stop is not silently dropped",
    kind: "grep-present",
    pattern: /orphanInterrupts:\s*OrphanInterrupt\[\]/,
  },
  {
    id: "codex-orphan-drain-on-track",
    from: "Four-outcome contract (post-#532)",
    file: "codex-runner/src/runner.ts",
    description: "trackCommandTurnTarget drains orphan-buffered interrupts when the matching submit_turn arrives",
    kind: "grep-present",
    pattern: /trackCommandTurnTarget[\s\S]{0,800}?this\.drainOrphanInterruptsFor/,
  },
  {
    id: "codex-publish-retry-helper",
    from: "Four-outcome contract (post-#532)",
    file: "codex-runner/src/runner.ts",
    description: "publishTerminalWithRetry exists and retries dispatch with backoff",
    kind: "grep-present",
    pattern: /publishTerminalWithRetry[\s\S]{0,400}?TERMINAL_PUBLISH_ATTEMPTS/,
  },
  {
    id: "codex-publish-fallback-on-interrupt",
    from: "Four-outcome contract (post-#532)",
    file: "codex-runner/src/runner.ts",
    description: "run-loop catch branch falls back to turn.failed{publish_interrupt_failed} when turn.interrupted publish exhausts retries",
    kind: "grep-present",
    pattern: /publish_interrupt_failed/,
  },
  {
    id: "codex-orphan-terminal",
    from: "Four-outcome contract (post-#532)",
    file: "codex-runner/src/runner.ts",
    description: "expireOrphanInterrupt emits turn.failed{interrupt_orphaned} so an orphan resolves the UI to a durable terminal",
    kind: "grep-present",
    pattern: /interrupt_orphaned/,
  },
  {
    id: "codex-no-silent-ack",
    from: "Four-outcome contract (post-#532)",
    file: "codex-runner/src/runner.ts",
    description: "codex-runner acceptInterrupt does NOT silently ack the no-match case (the pre-#532 silent-stranding path)",
    kind: "grep-absent",
    // Pre-#532 shape ended with `await this.commandBus.markCompleted(record);`
    // as the no-match fallback inside acceptInterrupt. Post-#532 the
    // fallback is bufferOrphanInterrupt; this guard forbids the
    // silent-ack regression.
    pattern: /acceptInterrupt[\s\S]{0,2500}?\}\s*\n\s*await\s+this\.commandBus\.markCompleted\(record\);\s*\n\s*\}/,
  },

  // ────────────────────── Oversized-event truncation (PR 3 of #532) ──────────────────────
  //
  // Pre-#532 a Tank conversation event whose JSON-encoded body exceeded
  // NATS's max_payload (1 MiB by default) silently went into the void:
  // dispatch() caught the synchronous throw from the protocol handler
  // and the runner moved on. Session 19's 7 publish failures across the
  // pod's lifetime were exactly this shape. PR 3 adds a transport-budget
  // truncation utility in runner-shared that replaces oversized strings
  // with a typed marker; both runners' dispatch wrappers call it before
  // every publish and increment tank_runner_event_truncated_total when
  // it fires. The persister-side ValidateEventMap accepts the marker as
  // an ordinary string, so the durable ledger never loses an event due
  // to size.
  {
    id: "shared-truncate-utility",
    from: "Oversized-event truncation (PR 3 of #532)",
    file: "runner-shared/sessionBus.js",
    description: "truncateEventIfOversized exists in runner-shared/sessionBus.js",
    kind: "grep-present",
    pattern: /export function truncateEventIfOversized/,
  },
  {
    id: "shared-truncate-dts",
    from: "Oversized-event truncation (PR 3 of #532)",
    file: "runner-shared/sessionBus.d.ts",
    description: "truncateEventIfOversized type declaration exported alongside the implementation",
    kind: "grep-present",
    pattern: /export function truncateEventIfOversized/,
  },
  {
    id: "shared-publishevent-uses-truncation",
    from: "Oversized-event truncation (PR 3 of #532)",
    file: "runner-shared/sessionBus.js",
    description: "publishEvent applies truncateEventIfOversized defensively before js.publish",
    kind: "grep-present",
    pattern: /publishEvent\(event[\s\S]{0,800}?truncateEventIfOversized/,
  },
  {
    id: "agent-runner-dispatch-truncates",
    from: "Oversized-event truncation (PR 3 of #532)",
    file: "agent-runner/src/runner.ts",
    description: "agent-runner's dispatch() runs truncateEventIfOversized before sink.upsert and increments the counter",
    kind: "grep-present",
    pattern: /dispatch[\s\S]{0,1200}?truncateEventIfOversized[\s\S]{0,400}?eventTruncatedTotal/,
  },
  {
    id: "codex-runner-dispatch-truncates",
    from: "Oversized-event truncation (PR 3 of #532)",
    file: "codex-runner/src/runner.ts",
    description: "codex-runner's dispatch() runs truncateEventIfOversized before sink.upsert and increments the counter",
    kind: "grep-present",
    pattern: /dispatch[\s\S]{0,1200}?truncateEventIfOversized[\s\S]{0,400}?eventTruncatedTotal/,
  },
  {
    id: "agent-runner-truncated-counter",
    from: "Oversized-event truncation (PR 3 of #532)",
    file: "agent-runner/src/metrics.ts",
    description: "tank_runner_event_truncated_total counter is registered with event_type + severity labels",
    kind: "grep-present",
    pattern: /name:\s*"tank_runner_event_truncated_total"[\s\S]{0,400}?labelNames:\s*\[\s*"event_type",\s*"severity"\s*\]/,
  },
  {
    id: "codex-runner-truncated-counter",
    from: "Oversized-event truncation (PR 3 of #532)",
    file: "codex-runner/src/metrics.ts",
    description: "codex-runner exposes the matching tank_runner_event_truncated_total counter",
    kind: "grep-present",
    pattern: /name:\s*"tank_runner_event_truncated_total"[\s\S]{0,400}?labelNames:\s*\[\s*"event_type",\s*"severity"\s*\]/,
  },
];

// ─────────────────────────────────────────────────────────────────────────────
// Runner
// ─────────────────────────────────────────────────────────────────────────────

printHeader();

const results = [];
for (const check of CHECKS) {
  if (check.kind === "exec" && skipExec) {
    results.push({ check, pass: true, skipped: true, evidence: "SKIP_EXEC=1" });
    printResult(results[results.length - 1]);
    continue;
  }
  const result = await runCheck(check);
  results.push(result);
  printResult(result);
}

printSummary(results);
const failed = results.filter((r) => !r.pass);
process.exit(failed.length === 0 ? 0 : 1);

// ─────────────────────────────────────────────────────────────────────────────
// Dispatch
// ─────────────────────────────────────────────────────────────────────────────

async function runCheck(check) {
  try {
    const result = await dispatch(check);
    return { check, ...result };
  } catch (err) {
    return { check, pass: false, evidence: `error: ${err.message}` };
  }
}

async function dispatch(check) {
  switch (check.kind) {
    case "grep-present":      return await grepPresent(check);
    case "grep-absent":       return await grepAbsent(check);
    case "block-absent":      return await blockAbsent(check);
    case "order-in-function": return await orderInFunction(check);
    case "order-in-function-absent": return await absentInFunction(check);
    case "json-enum-includes":return await jsonEnumIncludes(check);
    case "json-allof-clause": return await jsonAllOfClause(check);
    case "json-array-has-event": return await jsonArrayHasEvent(check);
    case "exec":              return execCheck(check);
    default: return { pass: false, evidence: `unknown kind: ${check.kind}` };
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Check implementations
// ─────────────────────────────────────────────────────────────────────────────

async function grepPresent({ file, pattern }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const content = await readRel(file);
  const match = pattern.exec(content);
  if (!match) return { pass: false, evidence: `pattern not found in ${file}: ${pattern}` };
  const { line } = locate(content, match.index);
  return { pass: true, evidence: `${file}:${line}` };
}

async function grepAbsent({ file, pattern }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const content = await readRel(file);
  const match = pattern.exec(content);
  if (match) {
    const { line, column } = locate(content, match.index);
    const preview = match[0].replace(/\s+/g, " ").slice(0, 80);
    return { pass: false, evidence: `${file}:${line}:${column} present but should be absent: ${JSON.stringify(preview)}` };
  }
  return { pass: true, evidence: `${file}: pattern absent` };
}

async function blockAbsent({ file, blockPattern, absentPattern }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const content = await readRel(file);
  const blockMatch = blockPattern.exec(content);
  if (!blockMatch) return { pass: false, evidence: `enclosing block not found in ${file}` };
  const insideMatch = absentPattern.exec(blockMatch[0]);
  if (insideMatch) {
    const absLine = locate(content, blockMatch.index + insideMatch.index).line;
    const preview = insideMatch[0].replace(/\s+/g, " ").slice(0, 80);
    return { pass: false, evidence: `${file}:${absLine} found inside enclosing block: ${JSON.stringify(preview)}` };
  }
  return { pass: true, evidence: `${file}: block found, target absent inside` };
}

async function absentInFunction({ file, functionName, absentPattern }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const content = await readRel(file);
  // Match Go-style "func name(", JS-style "function name(", OR
  // TS-class-method-style "  private name(" / "  async name(" so the
  // function-body checks apply to runner-class methods like
  // `private startCommandConsumer(signal: AbortSignal)`. Anchored to
  // start-of-line to avoid matching call sites like `this.name(`.
  const sigRe = new RegExp(
    `(?:func\\s+(?:\\([^)]*\\)\\s+)?${functionName}\\s*\\(` +
    `|function\\s+${functionName}\\s*\\(` +
    `|(?:^|\\n)\\s*(?:private|public|protected|export)?\\s*(?:async\\s+)?${functionName}\\s*\\()`,
  );
  const sig = sigRe.exec(content);
  if (!sig) return { pass: false, evidence: `function ${functionName} not found in ${file}` };
  const bodyStart = content.indexOf("{", sig.index);
  if (bodyStart < 0) return { pass: false, evidence: `function body of ${functionName} not found` };
  let depth = 0;
  let i = bodyStart;
  for (; i < content.length; i++) {
    const ch = content[i];
    if (ch === "{") depth++;
    else if (ch === "}") {
      depth--;
      if (depth === 0) { i++; break; }
    }
  }
  const body = content.slice(bodyStart, i);
  const m = absentPattern.exec(body);
  if (m) {
    const line = locate(content, bodyStart + m.index).line;
    const preview = m[0].replace(/\s+/g, " ").slice(0, 80);
    return { pass: false, evidence: `${file}:${line} inside ${functionName}: ${JSON.stringify(preview)}` };
  }
  return { pass: true, evidence: `${file}: ${functionName} body free of pattern` };
}

async function orderInFunction({ file, functionName, earlier, later }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const content = await readRel(file);
  const sigRe = new RegExp(`func\\s+(?:\\([^)]*\\)\\s+)?${functionName}\\s*\\(`);
  const sig = sigRe.exec(content);
  if (!sig) return { pass: false, evidence: `function ${functionName} not found in ${file}` };
  const bodyStart = content.indexOf("{", sig.index);
  if (bodyStart < 0) return { pass: false, evidence: `function body of ${functionName} not found` };
  let depth = 0;
  let i = bodyStart;
  for (; i < content.length; i++) {
    const ch = content[i];
    if (ch === "{") depth++;
    else if (ch === "}") {
      depth--;
      if (depth === 0) { i++; break; }
    }
  }
  const body = content.slice(bodyStart, i);
  const eM = earlier.exec(body);
  const lM = later.exec(body);
  if (!eM) return { pass: false, evidence: `earlier pattern not found in ${functionName} body` };
  if (!lM) return { pass: false, evidence: `later pattern not found in ${functionName} body` };
  if (eM.index >= lM.index) {
    const el = locate(content, bodyStart + eM.index).line;
    const ll = locate(content, bodyStart + lM.index).line;
    return { pass: false, evidence: `${file}: earlier at line ${el} should precede later at line ${ll}` };
  }
  const el = locate(content, bodyStart + eM.index).line;
  const ll = locate(content, bodyStart + lM.index).line;
  return { pass: true, evidence: `${file}: earlier@${el} < later@${ll} in ${functionName}` };
}

async function jsonEnumIncludes({ file, pointer, value }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const json = JSON.parse(await readRel(file));
  let node = json;
  for (const k of pointer) node = node?.[k];
  if (!Array.isArray(node)) return { pass: false, evidence: `path ${pointer.join(".")} is not an array in ${file}` };
  if (!node.includes(value)) return { pass: false, evidence: `${file}: ${pointer.join(".")} does not include ${JSON.stringify(value)}` };
  return { pass: true, evidence: `${file}: enum includes ${JSON.stringify(value)}` };
}

async function jsonAllOfClause({ file, targetType, requireActor, requireSource, requireFields }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const json = JSON.parse(await readRel(file));
  const clauses = Array.isArray(json.allOf) ? json.allOf : [];
  const clause = clauses.find((c) => c?.if?.properties?.type?.const === targetType);
  if (!clause) return { pass: false, evidence: `${file}: no allOf clause where if.type.const = ${JSON.stringify(targetType)}` };
  const then = clause.then ?? {};
  const actor = then?.properties?.actor?.const;
  const source = then?.properties?.source?.const;
  const required = then?.required ?? [];
  const problems = [];
  if (requireActor && actor !== requireActor) problems.push(`actor=${JSON.stringify(actor)} (want ${JSON.stringify(requireActor)})`);
  if (requireSource && source !== requireSource) problems.push(`source=${JSON.stringify(source)} (want ${JSON.stringify(requireSource)})`);
  for (const f of requireFields ?? []) {
    if (!required.includes(f)) problems.push(`missing required ${f}`);
  }
  if (problems.length) return { pass: false, evidence: `${file}: ${problems.join("; ")}` };
  return { pass: true, evidence: `${file}: allOf clause for ${targetType} satisfies actor/source/required` };
}

async function jsonArrayHasEvent({ file, arrayPath, eventTypePath, value }) {
  if (!(await fileExists(file))) return { pass: false, evidence: `file missing: ${file}` };
  const json = JSON.parse(await readRel(file));
  let arr = json;
  for (const k of arrayPath) arr = arr?.[k];
  if (!Array.isArray(arr)) return { pass: false, evidence: `path ${arrayPath.join(".")} is not an array in ${file}` };
  const hit = arr.some((entry) => {
    let n = entry;
    for (const k of eventTypePath) n = n?.[k];
    return n === value;
  });
  if (!hit) return { pass: false, evidence: `${file}: no entry where ${eventTypePath.join(".")} = ${JSON.stringify(value)}` };
  return { pass: true, evidence: `${file}: fixture for ${value} found` };
}

function execCheck({ command, cwd }) {
  const cwdAbs = cwd ? path.join(repoRoot, cwd) : repoRoot;
  const result = spawnSync(command[0], command.slice(1), {
    cwd: cwdAbs,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
  if (result.error) return { pass: false, evidence: `spawn error: ${result.error.message}` };
  if (result.status !== 0) {
    const stream = (result.stderr && result.stderr.trim()) || (result.stdout && result.stdout.trim()) || "";
    const tail = stream.split("\n").slice(-3).join(" ¶ ").slice(0, 240);
    return { pass: false, evidence: `exit ${result.status}: ${tail}` };
  }
  return { pass: true, evidence: `exit 0` };
}

// ─────────────────────────────────────────────────────────────────────────────
// Output
// ─────────────────────────────────────────────────────────────────────────────

function printHeader() {
  const byCategory = new Map();
  for (const check of CHECKS) {
    byCategory.set(check.from, (byCategory.get(check.from) ?? 0) + 1);
  }
  console.log(`Stop-request migration manifest: ${CHECKS.length} checks across ${byCategory.size} categories`);
  for (const [cat, n] of byCategory) console.log(`  ${String(n).padStart(2)} ${cat}`);
  if (skipExec) console.log("  (SKIP_EXEC=1 — exec gates will be marked PASS without running)");
  console.log("");
}

function printResult(r) {
  const sym = r.skipped ? "SKIP" : r.pass ? "PASS" : "FAIL";
  console.log(`${sym}  ${r.check.id.padEnd(46)}  ${r.check.description}`);
  if (!r.pass || r.skipped) {
    if (r.evidence) console.log(`      ↳ ${r.evidence}`);
  }
}

function printSummary(results) {
  const passed = results.filter((r) => r.pass && !r.skipped).length;
  const skipped = results.filter((r) => r.skipped).length;
  const failed = results.filter((r) => !r.pass);
  console.log("");
  console.log(`${passed}/${results.length} pass${skipped ? `, ${skipped} skipped` : ""}${failed.length ? `, ${failed.length} fail` : ""}`);
  if (failed.length) {
    console.log("");
    console.log("Failing checks:");
    for (const r of failed) {
      console.log(`  ${r.check.id}  [${r.check.from}]`);
      console.log(`      ${r.evidence}`);
    }
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

async function fileExists(rel) {
  try {
    await fs.access(path.join(repoRoot, rel));
    return true;
  } catch {
    return false;
  }
}

async function readRel(rel) {
  return await fs.readFile(path.join(repoRoot, rel), "utf8");
}

function locate(content, index) {
  const before = content.slice(0, index);
  const lines = before.split(/\r\n|\r|\n/);
  return { line: lines.length, column: lines[lines.length - 1].length + 1 };
}
