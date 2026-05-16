#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const ignoredDirs = new Set([
  ".claude",
  ".git",
  ".terraform",
  ".vite",
  ".next",
  ".venv",
  "__pycache__",
  "build",
  "coverage",
  "dist",
  "node_modules",
  "target",
  "venv",
]);

const ignoredFiles = new Set([
  "package-lock.json",
  "pnpm-lock.yaml",
  "yarn.lock",
  "go.sum",
]);

const ignoredRelativePaths = new Set([
  "scripts/check-removed-chat-runtime.mjs",
  "scripts/check-tank-conversation-contract.mjs",
  // The stop-request migration's completion manifest catalogues the
  // retired symbols as grep targets. Excluded so this guard doesn't
  // fire on its sibling guard's expectations.
  "scripts/check-stop-request-migration.mjs",
  // The protocol doc explains the migration by naming the retired
  // symbols in prose ("the retired stopRequested / stoppingTargetRef
  // UI-mirror"). This is documentation, not live code — the guard is
  // there to block reintroduction in implementation files.
  "docs/tank-conversation-protocol.md",
  "backend-go/cmd/tank-operator/server_static_test.go",
  "frontend/src/migrationPolicy.test.ts",
  // The observability test asserts /debug/vars returns 404 (the negative
  // confirmation that the route is gone). Excluded so the migration
  // guard doesn't fire on its own enforcement.
  "backend-go/cmd/tank-operator/observability_test.go",
]);

const blocked = [
  { name: "session run create API", pattern: /\/api\/sessions\/run\b/ },
  { name: "internal session run API", pattern: /\/api\/internal\/sessions\/run\b/ },
  { name: "run active API", pattern: /\/run\/active\b/ },
  { name: "run history API", pattern: /\/run\/history\b/ },
  { name: "latest run events API", pattern: /\/runs\/latest\/events(?:\.json)?\b/ },
  { name: "run events API", pattern: /\/runs\/\{run_id\}\/events\b/ },
  { name: "bare run route registration", pattern: /HandleFunc\(\s*["']\/run["']/ },
  { name: "headless dispatcher", pattern: /\bDispatchHeadless\b/ },
  { name: "headless run script", pattern: /headless-run\.sh\b/ },
  { name: "provider event adapter", pattern: /\bproviderEventAdapters\b/ },
  { name: "removed conversation source", pattern: /\blegacy-run\b/ },
  { name: "removed conversation source constant", pattern: /\bSourceLegacyRun\b/ },
  { name: "active run store", pattern: /\bactive[-_]runs\b/i },
  { name: "run event store", pattern: /\brun[-_]events\b/i },
  { name: "removed runtime discriminator", pattern: /runtime\??:\s*["']sdk["']\s*\|\s*["']legacy["']/ },
  { name: "session runtime branch", pattern: /\bsession\.runtime\b/ },
  { name: "old subscription mode alias", pattern: /\bsubscription_headless\b/ },
  { name: "old codex mode alias", pattern: /\bcodex_headless\b/ },
  { name: "old codex subscription alias", pattern: /\bcodex_subscription\b/ },
  { name: "old pi subscription alias", pattern: /\bpi_subscription\b/ },
  { name: "direct Codex credential mirror", pattern: /\bCodexCredsSecret\b/ },
  { name: "default direct Codex credential mirror", pattern: /\bDefaultCodexCredsSecret\b/ },
  { name: "retired agent runner websocket route", pattern: /\/agent-ws\b/ },
  { name: "retired agent runner websocket port", pattern: /\bAGENT_RUNNER_WS_PORT\b/ },
  { name: "retired websocket fanout", pattern: /\bWSFanout\b/ },
  { name: "retired websocket frame type", pattern: /\bClientFrame\b/ },
  { name: "retired Tank order key storage name", pattern: /\btank_order_key\b/ },
  { name: "retired Tank event sequence storage name", pattern: /\btank_event_seq\b/ },
  { name: "retired frontend activity poll interval", pattern: /\bPOLL_INTERVAL_MS\b/ },
  { name: "retired frontend activity polling loop", pattern: /setInterval\(\s*refreshSessionActivity/ },
  // tank-operator#83 — sidebar session-list moved from wake-and-refetch
  // polling onto a durable typed-event ledger + cursor-resumable SSE.
  // Block reintroduction of every name that participated in the prior
  // path so the next refactor can't quietly rebuild the parallel
  // architecture. The activity polling endpoint, its frontend twin,
  // the 1.5s pending-session loop, the visibility/focus refetch handlers,
  // the bus's opaque wake subject + helpers, the live-pod podStatus()
  // helper, and the prior SSE event name are all in the deletion set.
  { name: "removed session list wake subject", pattern: /\bSessionListWakeSubject\b/ },
  { name: "removed session list wake publisher", pattern: /\bPublishSessionListWake\b/ },
  { name: "removed session list wake subscriber", pattern: /\bSubscribeSessionListWake\b/ },
  { name: "removed session list wake counter", pattern: /\btank_session_list_wake_publish_failure_total\b/ },
  { name: "removed session list wake adapter method", pattern: /\bRecordSessionListWakePublishFailed\b/ },
  { name: "removed legacy sessions-changed SSE event", pattern: /["']sessions-changed["']/ },
  { name: "removed /api/sessions/activity HTTP route", pattern: /\/api\/sessions\/activity\b/ },
  { name: "removed handleSessionActivity backend handler", pattern: /\bhandleSessionActivity\b/ },
  { name: "removed frontend refreshSessionActivity helper", pattern: /\brefreshSessionActivity\b/ },
  { name: "removed pending-session polling constant", pattern: /\bPENDING_SESSION_REFRESH_INTERVAL_MS\b/ },
  { name: "removed live-pod status helper", pattern: /\bfunc podStatus\(/ },
  // Glob-broad: bare references to /api/sessions/activity in YAML/MD
  // docs would also re-introduce the contract. (Match the path
  // including trailing word-boundary so /api/sessions/activity/foo
  // still trips the guard for any future regression.)
  { name: "retired internal session event notify route", pattern: /\/events\/notify\b/ },
  { name: "retired in-memory session event broker", pattern: /\bsessionEventBroker\b/ },
  { name: "retired session event notifier", pattern: /\bSessionEventNotifier\b/ },
  { name: "retired runner session notify helper", pattern: /\bsessionNotify\b/ },
  { name: "retired turn queue name", pattern: /\bturn[-_ ]queue\b/i },
  { name: "retired TurnQueue type", pattern: /\bTurnQueue\b/ },
  { name: "retired turnQueue identifier", pattern: /\bturnQueue\b/ },
  { name: "retired turn queue env var", pattern: /\bCOSMOS_TURN_QUEUE_CONTAINER\b/ },
  { name: "retired turn queue env prefix", pattern: /\bTURN_QUEUE_/ },
  { name: "retired runner Cosmos event module", pattern: /\b(?:agent|codex)-runner\/src\/cosmos\.ts\b/ },
  { name: "retired runner Cosmos tests", pattern: /\bcosmos\.test\.ts\b/ },
  { name: "retired session Azure config secret", pattern: /\bSESSION_AZURE_CONFIG_SECRET\b/ },
  { name: "retired session Azure config option", pattern: /\bSessionAzureConfigSecret\b/ },
  { name: "retired session Azure config default", pattern: /\bDefaultSessionAzureConfigSecret\b/ },
  { name: "retired session workload identity resource", pattern: /\btank_session_identity\b/ },
  // Producer-permissive raw-provider event dispatch (the SDK migration's
  // hidden dual path). Tank events are the only thing on the session bus
  // now; isCanonical / CANONICAL_TYPES / stampEventID were the runner-side
  // filter that let raw provider events through. tank.user_message was a
  // phantom canonical type that never matched the schema. The conditional
  // stampers (stampEventID, stampTankEvent local copies) silently emitted
  // half-envelopes; the unconditional runner-shared stampTankEvent throws
  // instead.
  { name: "removed CANONICAL_TYPES producer-permissive filter", pattern: /\bCANONICAL_TYPES\b/ },
  { name: "removed isCanonical producer-side filter", pattern: /\bisCanonical\b/ },
  { name: "removed local stampEventID stamper", pattern: /\bstampEventID\b/ },
  { name: "removed phantom canonical tank.user_message type", pattern: /\btank\.user_message\b/ },
  { name: "duplicate codex-runner conversation contract", pattern: /codex-runner\/src\/conversation\.ts\b/ },
  { name: "duplicate agent-runner conversation contract", pattern: /agent-runner\/src\/conversation\.ts\b/ },
  { name: "duplicate frontend tankConversation contract", pattern: /frontend\/src\/tankConversation\.ts\b/ },
  // Phantom Tank event types: schema/code surface that no production code
  // ever emitted. They became maintenance debt with no live emitter, so
  // they were deleted per docs/migration-policy.md (no inactive surface
  // without a concrete plan to wire it up).
  { name: "phantom conversation.started event type", pattern: /["']conversation\.started["']|\bEventConversationStarted\b/ },
  { name: "phantom conversation.archived event type", pattern: /["']conversation\.archived["']|\bEventConversationArchived\b/ },
  { name: "phantom session.activity_updated event type", pattern: /["']session\.activity_updated["']|\bEventActivityUpdated\b/ },
  { name: "phantom read_state.updated event type", pattern: /["']read_state\.updated["']|\bEventReadStateUpdated\b/ },
  { name: "phantom audit-only visibility", pattern: /["']audit-only["']|\bVisibilityAudit\b/ },
  // Dead-after-cutover symbols. The producer-side cutover (PR #461)
  // removed every caller of Bus.PublishEvent, EventSubject, and
  // SessionEventSink.create — the cleanup PR deleted the now-unreachable
  // definitions. Block reintroduction so a future refactor doesn't
  // accidentally rebuild the dual-publish path.
  { name: "removed Bus.PublishEvent", pattern: /\bfunc \(b \*Bus\) PublishEvent\b|\bsessionBus\.PublishEvent\b|\bbus\.PublishEvent\b/ },
  { name: "removed EventSubject helper", pattern: /\bfunc EventSubject\b|\bEventSubject\(/ },
  { name: "removed SessionEventSink.create", pattern: /\bsink\.create\(|create\(message: StampedTankEvent|create\(event: StampedTankEvent/ },
  // Cosmos DB SQL API was retired in #466 in favor of Azure Postgres
  // Flexible Server. Block reintroduction of the storage-layer symbols
  // and the orphan migration scripts that talked to Cosmos REST. Note:
  // backend-go/internal/{auth,keyvault,pgstore} still import the
  // generic `azcore` package — that's the Azure SDK auth package, used
  // by Key Vault and Postgres workload identity. Cosmos-specific deps
  // (`azcosmos`, `x-ms-documentdb-*` headers, etc.) are gone.
  { name: "removed cosmosSessionEventStore", pattern: /\bcosmosSessionEventStore\b|\bNewCosmosSessionEventStore\b/ },
  { name: "removed Cosmos retry helpers", pattern: /\bretryOnCosmosThrottle\b|\bisCosmosThrottled\b|\bcosmosRetryAfter\b/ },
  { name: "removed Cosmos retry module", pattern: /backend-go\/internal\/store\/cosmos_retry\.go\b/ },
  { name: "removed Cosmos data-plane import", pattern: /azure-sdk-for-go\/sdk\/data\/azcosmos\b/ },
  { name: "removed Cosmos REST header", pattern: /\bx-ms-documentdb-/ },
  // mcp.tf legitimately grants the azure-personal MCP server data-plane
  // access to an UNRELATED Cosmos account (infra-cosmos-serverless,
  // not the migrated tank-operator app store). Block only the
  // tank-operator-side provisioning resources, not role-assignment
  // grants on Cosmos accounts that still exist.
  { name: "removed Cosmos tofu module", pattern: /\binfra\/cosmos\.tf\b|\bazurerm_cosmosdb_sql_(database|container)\b/ },
  { name: "removed Cosmos env var", pattern: /\bCOSMOS_ENDPOINT\b|\bCOSMOS_DATABASE\b|\bCOSMOS_SESSION_EVENTS_CONTAINER\b|\bCOSMOS_PROFILES_CONTAINER\b/ },
  { name: "removed Cosmos cleanup scripts", pattern: /scripts\/(audit-session-events|clean-session-events|migrate-session-events-timeline-id)\.py\b/ },
  // NATS-delayed ScheduleWakeup was replaced by a pod-local setTimeout
  // in the agent-runner during the #457 session bus refactor. The old
  // path published a scheduled command via JetStream's AllowMsgSchedules
  // feature; block reintroduction so a future refactor doesn't recreate
  // the durable scheduler the inspirations doc explicitly scoped out.
  { name: "removed NATS-delayed wakeup helpers", pattern: /\benqueueDelayed\b|\bbuildDelayedCommand\b/ },
  { name: "removed schedule subject helper", pattern: /\bscheduleSubject\(/ },
  // Match the actual config-field assignment, not the doc-of-deletion
  // comment in bus.go that explains why the field is gone.
  { name: "removed JetStream AllowMsgSchedules config", pattern: /AllowMsgSchedules:\s*true\b/ },
  // The Python → Go orchestrator rewrite (#373) left a Go package named
  // `internal/compat` that grew into the de-facto shared session-pod
  // model (modes, manifest builder, storage-key helpers). The name
  // violated migration-policy.md ("compat is a deletion target"); the
  // package was renamed to `internal/sessionmodel`. Block the old import
  // path and the package keyword so a future PR can't accidentally
  // resurrect the `compat` name. Allow the literal word "compat" in
  // comments and identifiers like "incompatible" — match only the
  // package qualifier and import path.
  { name: "removed compat package import", pattern: /\binternal\/compat\b/ },
  { name: "removed compat package keyword", pattern: /^\s*package compat\b/m },
  // Python-vs-Go sessions diff utility, also from #373. It had no
  // callers outside its own tests after the Python orchestrator
  // retirement and was deleted in the post-NATS audit. Block re-add.
  { name: "removed sessioncompare package", pattern: /\binternal\/sessioncompare\b|\bpackage sessioncompare\b/ },
  // `live-only` visibility and the paired `item.delta` event type were
  // retired together in the post-NATS audit. The codex adapter was the
  // only producer of item.delta (always emitted as visibility=live-only,
  // then dropped at the runner sink — no consumer ever subscribed).
  // Resurrect both together if a live-only side channel is ever wired
  // up; do not reintroduce one without the other.
  { name: "removed live-only visibility", pattern: /["']live-only["']|\bVisibilityLiveOnly\b/ },
  { name: "removed item.delta event type", pattern: /["']item\.delta["']|\bEventItemDelta\b/ },
  // `target_item_id` was renamed to `target_timeline_id` to match the
  // post-#448 `item_id → timeline_id` migration: the field has always
  // carried a `timeline_id` value; only the wire/struct name was stale.
  { name: "removed target_item_id wire field", pattern: /["']target_item_id["']|\bTargetItemID\b/ },
  // Schedule/Wakeup naming residue from the now-retired NATS-delayed
  // path. The runner-side helpers were renamed to drop the "Schedule"
  // word once ScheduleWakeup became a pod-local setTimeout (#457).
  { name: "removed Schedule/Wakeup helper names", pattern: /\benqueueWakeupCommand\b|\bbuildScheduleWakeupCommand\b/ },
  // Microsoft sign-in was migrated off MSAL-in-each-app onto the shared
  // auth.romaine.life service. Tank-operator now carries no Entra app
  // registration, no MSAL client (browser or Electron), no per-app
  // microsoft-login route, and no Microsoft JWKS verification code of
  // its own. Block reintroduction of every name that participated in
  // the old flow so a future refactor can't quietly rebuild a parallel
  // path beside the auth.romaine.life delegation.
  { name: "removed ENTRA_CLIENT_ID env var", pattern: /\bENTRA_CLIENT_ID\b/ },
  { name: "removed entra_client_id config key", pattern: /\bentra_client_id\b/ },
  { name: "removed entra_authority config key", pattern: /\bentra_authority\b/ },
  { name: "removed tank-operator-oauth-client-id KV key", pattern: /\btank-operator-oauth-client-id\b|\btank-operator-test-oauth-client-id\b/ },
  { name: "removed tank-operator-oauth Entra app reg name", pattern: /\btank-operator-oauth(?:-test)?\b/ },
  { name: "removed MSAL browser/node deps", pattern: /@azure\/msal-(?:browser|node)\b/ },
  { name: "removed ExchangeEntraToken function", pattern: /\bExchangeEntraToken\b/ },
  { name: "removed Microsoft JWKS constants", pattern: /\bentraJWKSURL\b|\bissuerPattern\b/ },
  { name: "removed /api/auth/microsoft/login route", pattern: /\/api\/auth\/microsoft\/login\b/ },
  { name: "removed handleMicrosoftLogin handler", pattern: /\bhandleMicrosoftLogin\b/ },
  { name: "removed desktop-auth IPC channel", pattern: /\bdesktop-auth:microsoft-login\b/ },
  { name: "removed tankOperatorDesktop window bridge", pattern: /\btankOperatorDesktop\b/ },
  { name: "removed tank-operator:// custom protocol", pattern: /\btank-operator:\/\/auth\b/ },
  // ALLOWED_EMAILS allowlist retired in favor of the auth.romaine.life
  // role claim. Block reintroduction of the env var, KV mount, and
  // emails-list constructor signature so a future PR can't quietly bring
  // the per-app gate back.
  { name: "removed ALLOWED_EMAILS env var", pattern: /\bALLOWED_EMAILS\b/ },
  { name: "removed romaine-life-admin-emails KV mount", pattern: /\bromaine-life-admin-emails\b/ },
  // expvar / /debug/vars deleted in the observability cutover that
  // replaced ad-hoc expvar counters with a real Prometheus surface
  // (docs/observability.md). The Go stdlib expvar package and its
  // /debug/vars HTTP handler are migration-policy.md deletion targets:
  // nothing in the cluster scraped /debug/vars, and reintroducing
  // expvar.NewInt next to promauto.NewCounter would split metrics
  // between two registries silently.
  { name: "removed expvar package import in backend-go", pattern: /^\s*"expvar"$/m },
  { name: "removed expvar.NewInt counter", pattern: /\bexpvar\.NewInt\b/ },
  { name: "removed expvar.NewFloat counter", pattern: /\bexpvar\.NewFloat\b/ },
  { name: "removed expvar.NewMap counter", pattern: /\bexpvar\.NewMap\b/ },
  { name: "removed expvar.NewString counter", pattern: /\bexpvar\.NewString\b/ },
  { name: "removed expvar.Handler mount", pattern: /\bexpvar\.Handler\b/ },
  { name: "removed /debug/vars HTTP route", pattern: /\/debug\/vars\b/ },
  { name: "removed expvarPersisterMetrics type", pattern: /\bexpvarPersisterMetrics\b/ },
  { name: "removed expvarWakeMetrics type", pattern: /\bexpvarWakeMetrics\b/ },
  // /api/internal/sessions/* identity was migrated from raw SA-TokenReview
  // + caller_pod_ip (IP-tail lookup against pod annotations) to the
  // auth.romaine.life service-principal JWT path in nelsong6/tank-operator#486
  // Stage 4. Both the gate function and every adjacent helper were deleted
  // in the same migration; block reintroduction so a future refactor
  // can't quietly rebuild the parallel auth path. The legacy env var
  // INTERNAL_API_ALLOWED_SUBJECTS that fed the (ns, sa) allowlist is
  // also retired. mcp-tank-operator's caller_pod_ip query param was the
  // only consumer; both ends were cut over in the same release.
  { name: "removed requireInternalCaller gate", pattern: /\brequireInternalCaller\b/ },
  { name: "removed resolveCallerEmail helper", pattern: /\bresolveCallerEmail\b/ },
  { name: "removed FindPodByIP identity lookup", pattern: /\bFindPodByIP\b/ },
  { name: "removed caller_pod_ip query param", pattern: /\bcaller_pod_ip\b/ },
  { name: "removed INTERNAL_API_ALLOWED_SUBJECTS env var", pattern: /\bINTERNAL_API_ALLOWED_SUBJECTS\b/ },
  { name: "removed parseInternalSubjects helper", pattern: /\bparseInternalSubjects\b/ },
  { name: "removed internalAllowedSubjects field", pattern: /\binternalAllowedSubjects\b/ },
  // /api/internal/sessions/spawn was an alias of POST /api/internal/sessions
  // during the #486 Stage 4 cutover. Both endpoints had identical semantics
  // post-cutover; the alias was retired in the API-cleanup follow-up. POST
  // /api/internal/sessions is now the canonical service-principal
  // session-create endpoint. Block reintroduction of the alias so a future
  // PR can't quietly resurrect the parallel surface.
  { name: "removed handleInternalSpawnSession alias", pattern: /\bhandleInternalSpawnSession\b/ },
  { name: "removed /api/internal/sessions/spawn URL", pattern: /\/api\/internal\/sessions\/spawn\b/ },
  // UI-local stop optimism retired in the turn.interrupt_requested
  // migration. `stopping` is now a projection-driven run status sourced
  // from the durable turn.interrupt_requested event; the local flag and
  // its paired ref were the UI mirror that contradicted durable state
  // (the smell named in docs/quality-timeframes.md's review heuristics).
  // The literal setRunStatus("stopping") imperative call is allowed in
  // applySdkProjectionToUi (projection-driven) but forbidden in cancelRun
  // — that boundary is pinned by frontend/src/migrationPolicy.test.ts,
  // which has finer-grained function-body awareness than this script.
  { name: "retired local stop-request flag", pattern: /\bstopRequested\b/ },
  { name: "retired stopping-target ref",     pattern: /\bstoppingTargetRef\b/ },
];

const failures = [];

for await (const filePath of walk(repoRoot)) {
  const relativePath = toRepoPath(filePath);
  if (ignoredRelativePaths.has(relativePath)) continue;
  const bytes = await fs.readFile(filePath);
  if (bytes.includes(0)) continue;
  const text = bytes.toString("utf8");
  for (const rule of blocked) {
    const match = rule.pattern.exec(text);
    if (!match) continue;
    const { line, column } = lineAndColumn(text, match.index);
    failures.push(`${relativePath}:${line}:${column} ${rule.name}: ${JSON.stringify(match[0])}`);
  }
}

if (failures.length > 0) {
  console.error("Removed chat runtime surface detected:");
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log("No removed chat runtime surfaces found.");

async function* walk(dir) {
  const entries = await fs.readdir(dir, { withFileTypes: true });
  for (const entry of entries) {
    const absolutePath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!ignoredDirs.has(entry.name)) yield* walk(absolutePath);
      continue;
    }
    if (!entry.isFile()) continue;
    if (ignoredFiles.has(entry.name)) continue;
    yield absolutePath;
  }
}

function toRepoPath(filePath) {
  return path.relative(repoRoot, filePath).split(path.sep).join("/");
}

function lineAndColumn(text, index) {
  const before = text.slice(0, index);
  const lines = before.split(/\r\n|\r|\n/);
  return {
    line: lines.length,
    column: lines[lines.length - 1].length + 1,
  };
}
