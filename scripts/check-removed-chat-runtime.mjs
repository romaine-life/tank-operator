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
  // The session-list redesign plan names the retired packages as
  // migration targets — it is documentation of the deletion, not a
  // resurrection. Same exemption shape as docs/tank-conversation-protocol.md.
  "docs/session-list-redesign.md",
  // pgstore migrations.go carries the idempotent `DROP TABLE IF
  // EXISTS session_lifecycle_events` cleanup statement — the name has
  // to appear in the SQL for the DROP to mean anything. Excluded so
  // the migration guard doesn't fire on the deletion statement
  // itself.
  "backend-go/internal/pgstore/migrations.go",
  // sessionBusSubjects.test.ts pins the post-ea70777 wire shape and
  // asserts the legacy 3-token tank.session.<storage_token>.events
  // form is NOT what eventSubject() returns. The legacy literal
  // appears as a notEqual assertion fixture — it's the negative
  // confirmation that the cutover held. Same exemption shape as
  // backend-go/internal/sessionbus/subjects_test.go (which uses string
  // concat and doesn't trip the guard) — TS string concat isn't as
  // idiomatic, so the test pins the literal directly.
  "claude-runner/src/sessionbus/sessionBusSubjects.test.ts",
  // navigationMode module — names the retired symbols in the
  // explanatory module header (the comment block that documents
  // which bug class the module was created to retire). Same
  // exemption shape as docs/tank-conversation-protocol.md — prose,
  // not live code reintroducing the retired path.
  "frontend/src/navigationMode.ts",
  "frontend/src/navigationMode.test.ts",
  // Transcript-navigation contract — the Observability section names
  // the retired symbols explicitly so future readers can find the
  // migration guard from the contract.
  "docs/features/transcript-navigation/contract.md",
  // The new admin debug endpoint's source carries a header comment
  // explaining the bug class it diagnoses, naming the retired
  // userScrolledUp identifier. Same prose-not-code shape.
  "backend-go/cmd/tank-operator/handlers_debug_conversation_read_state.go",
  // The new alert + dashboard panel cite the retired constant by
  // name in runbook/description prose so an operator following the
  // runbook understands what was retired and why.
  "k8s/templates/observability.yaml",
  "k8s/templates/grafana-dashboard.yaml",
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
  // Mid-session model/effort re-pin replaced the pod-lifetime seal that
  // silently ignored later model/effort changes. The claude-runner now tears
  // down + rebuilds query() with provider-session resume; codex re-resumes its
  // thread. Block the retired "ignore the override" metric/identifier so a
  // future change cannot quietly restore the silent-divergence path. See
  // docs/features/agent-runners/contract.md.
  {
    name: "retired pod-lifetime model seal (silent override-ignore)",
    pattern: /optionsOverrideIgnoredTotal|tank_runner_options_override_ignored_total/,
  },
  { name: "retired frontend activity polling loop", pattern: /setInterval\(\s*refreshSessionActivity/ },
  // Break-glass approvals are still available through the admin/deep-link
  // flow, but the composer approval chip was retired on 2026-06-17. Block
  // the old row-to-chip reducer, menu symbols, settings shortcut, and CSS
  // hook so persisted break-glass control-action rows cannot reappear as a
  // composer chip.
  { name: "retired composer break-glass pending reducer", pattern: /\bpendingBreakGlassRequests\b/ },
  { name: "retired composer break-glass menu button", pattern: /\bBreakGlassApprovalMenuButton\b/ },
  { name: "retired composer break-glass menu item", pattern: /\bBreakGlassApprovalMenuItem\b/ },
  { name: "retired composer break-glass menu icon", pattern: /\bBreakGlassMenuIcon\b/ },
  { name: "retired composer break-glass menu builder", pattern: /\bbreakGlassApprovalMenuItemsForSession\b/ },
  { name: "retired composer break-glass quick approve", pattern: /\bquickApproveBreakGlassMenuItem\b/ },
  { name: "retired composer break-glass CSS hook", pattern: /\brun-break-glass-action-btn\b/ },
  {
    name: "retired composer break-glass settings shortcut",
    pattern: /appRouteUrl\(\s*["']settings["']\s*,\s*["']admin["']\s*,\s*["']break-glass["']\s*\)/,
  },
  {
    name: "retired composer break-glass approval kind",
    pattern: /type\s+ApprovalMenuKind\s*=[^;\n]*(?:["']github["']|["']azure["'])/,
  },
  { name: "retired composer break-glass prop", pattern: /\bbreakGlass=\{\{/ },
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
  // Durable per-session turn numbers replaced the browser-render "Turn N"
  // ordinal and the raw turn_<uuid> public route. The label must come from the
  // durable session_turns number, never the array index; the public route must
  // carry the number, never the turn_id (the turn_id-keyed /api/.../turns/${turnId}/...
  // builders use `turns/` + slash and are intentionally not matched).
  { name: "retired array-position turn label", pattern: /`Turn \$\{[^}]*\bindex\b[^}]*\}`/ },
  { name: "retired turn_<uuid> public route segment", pattern: /\/turns\$\{turnId/ },
  // The Turns selector must source its turn set from the durable turn directory
  // (turnDirectoryEntries via /turns/directory), never from the bounded
  // transcript window. buildTurnViewItems(renderedEntries, …) was the window
  // derivation that hid every turn older than the ~24-row tail; the cutover
  // feeds it turnViewSourceEntries (directory-owned set, live-overlaid).
  { name: "window-derived Turns selector", pattern: /buildTurnViewItems\(\s*renderedEntries\b/ },
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
  { name: "duplicate claude-runner conversation contract", pattern: /claude-runner\/src\/conversation\.ts\b/ },
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
  // in the claude-runner during the #457 session bus refactor. The old
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
  // Tank-local JWT minting was retired after auth.romaine.life became the
  // only token issuer. The SPA stores/presents the upstream JWT directly,
  // GitHub install state is an opaque Postgres nonce, and service principals
  // use auth.romaine.life's /api/auth/exchange/* endpoints directly.
  // Keep blocking the retired bare Tank-owned route while allowing the
  // IdP subroutes used by service JWTs, external federation, and SSH certs.
  { name: "removed Tank auth exchange route", pattern: /\/api\/auth\/exchange(?!\/(?:k8s|federation|ssh-cert)\b)\b/ },
  { name: "removed Tank internal k8s auth route", pattern: /\/api\/internal\/auth\/k8s\b/ },
  { name: "removed Tank local auth storage key", pattern: /\btank-operator-jwt\b/ },
  { name: "removed Tank auth cookie", pattern: /\bauth_token\b/ },
  { name: "removed Tank JWT signing config", pattern: /\btank-operator-jwt-signing\b|\bJWT_KV_(?:VAULT|KEY_NAME)\b/ },
  { name: "removed Tank JWT minter", pattern: /\bNewMinter\b|\bMintSession\b|\bMintInstallState\b|\bVerifyInstallState\b|\bKeyVaultJWT\b|\bhandleAuthExchange\b|\bhandleK8sAuth\b/ },
  // Browser EventSource cannot send Authorization headers. The retired
  // interim fix placed the full auth.romaine.life JWT in SSE query strings;
  // the durable path is POST /api/auth/stream-ticket + opaque stream_ticket.
  // WebSocket upgrades still use access_token, so block only the EventSource
  // helper names and frontend authedEventSourceURL query-JWT shape.
  { name: "removed EventSource access_token carrier", pattern: /\bTokenForBrowserStream\b|\bCurrentUserFromBrowserStream\b|\bauthedEventSourceURL\b[\s\S]{0,500}\baccess_token\b/ },
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
  // auth.romaine.life service-principal JWT path in romaine-life/tank-operator#486
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
  // tank-operator#1128 stage 3: GUI session runner sidecars authenticate
  // to NATS through auth_callout with NATS_USER=<storage key> and
  // NATS_PASSWORD_FILE=<projected auth.romaine.life SA token>. The shared
  // tank-nats-auth remains only as the orchestrator static-user password.
  {
    name: "removed shared NATS token injection into session runner env",
    pattern: /(runnerEnv|codexRunnerEnv)[\s\S]{0,600}"name":\s*"NATS_TOKEN"/,
  },
  {
    name: "removed NATS callout shared-token grant env",
    pattern: /\bNATS_CALLOUT_LEGACY_TOKEN\b/,
  },
  {
    name: "removed NATS callout unrestricted shared-token user",
    pattern: /\blegacy-shared-token\b/,
  },
  // Turn-complete sound was per-pane (declared inside ChatPane in
  // App.tsx) before the cutover that moved it onto the always-on
  // /api/sessions/events SSE consumer. The per-pane shape was the
  // reason the sound only fired on session-return: ChatPane's
  // /api/sessions/{id}/events stream closes when the pane is hidden,
  // so background turn-complete events never reached the per-pane
  // listener until the user came back. The App-level canonical site
  // is a single `useCallback` per helper; ChatPane gets the play /
  // prime functions as props. Block the per-pane `function`-keyword
  // form so a future refactor can't quietly re-declare them inside
  // ChatPane (or any other per-session component) and rebuild the
  // dual-listener path.
  { name: "per-pane playTurnCompleteSound declaration", pattern: /\bfunction\s+playTurnCompleteSound\s*\(/ },
  { name: "per-pane primeTurnCompleteSound declaration", pattern: /\bfunction\s+primeTurnCompleteSound\s*\(/ },
  { name: "per-pane getTurnCompleteAudio declaration", pattern: /\bfunction\s+getTurnCompleteAudio\s*\(/ },
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
  // Stage 2 chat-windowing cutover (PR #503): the SPA used to fetch the
  // entire ledger forward from order_key=0 in a 50-page loop of 1000-event
  // pages. The DOM extended under the user's eyes mid-load and produced
  // the "scroll down, scroll bar learns there's more, repeat 3-4×"
  // dance. Replaced with one tail read (anchor=newest) for normal
  // navigation plus explicit message-link anchored reads and bounded
  // back-paginate via before_order_key. Block re-introduction of the old
  // shapes so a future refactor can't quietly rebuild the forward walk.
  { name: "removed 50-page forward-walk loop", pattern: /for\s*\(\s*let\s+page\s*=\s*0\s*;\s*page\s*<\s*50\b/ },
  { name: "removed 1000-event-per-page timeline fetch", pattern: /limit:\s*["']1000["']/ },
  { name: "removed unread-centered default transcript anchor", pattern: /\bfirst_unread\b/ },
  { name: "removed localStorage transcript position anchor", pattern: /\btank\.transcript\.position\b|\bSDK_TRANSCRIPT_POSITION\b|\breadSdkTranscriptPosition\b|\bwriteSdkTranscriptPosition\b|\bclearSdkTranscriptPosition\b/ },
  { name: "removed legacy forward transcript timeline read", pattern: /\blegacy_forward\b|\bsessionEventReadLegacyForward\b/ },
  // Transcript-row projection-version catch-up is a per-request,
  // per-session materialization concern. Serving pods must not run a
  // fleet-wide backfill scan at startup, and test slots must not backfill
  // prod/default just because their Postgres pool can read it.
  { name: "retired startup transcript row backfill launcher", pattern: /\bstartTranscriptRowBackfills\b|\btranscriptBackfillScopes\b/ },
  { name: "retired fleet transcript row backfill selector", pattern: /\bBackfillSessionIDs\b/ },
  // Stage 3 (PR #503): hand-rolled scroll-detect hysteresis. Replaced
  // by react-virtuoso's atBottomStateChange callback, which is the
  // durable boolean source for "is the user viewing the live tail." The
  // 24px threshold was the smoking-gun signature of the prior listener —
  // ban the literal so a future refactor can't reintroduce it.
  { name: "removed 24px scroll hysteresis listener", pattern: /distanceFromBottom\s*>\s*24/ },
  // Transcript-navigation contract — the DOM-distance heuristic that
  // latched the old userScrolledUp boolean during react-virtuoso's
  // followOutput smooth-scroll catch-up window was retired (session
  // 269 case, 2026-05-27). The replacement is an explicit
  // NavigationMode state machine driven by user-gesture events; see
  // ./frontend/src/navigationMode.ts and
  // ./docs/features/transcript-navigation/contract.md. Block every
  // symbol that participated in the retired DOM-override fallback so
  // a future refactor can't quietly bring the layout-state-as-
  // navigation-state pattern back.
  { name: "retired userScrolledUp boolean", pattern: /\buserScrolledUp\b/ },
  { name: "retired setUserScrolledUp setter", pattern: /\bsetUserScrolledUp\b/ },
  { name: "retired sdkAtBottomRef bottom mirror", pattern: /\bsdkAtBottomRef\b/ },
  { name: "retired syncSdkVisualTailState DOM check", pattern: /\bsyncSdkVisualTailState\b/ },
  { name: "retired transcriptVisuallyAtBottom predicate", pattern: /\btranscriptVisuallyAtBottom\b/ },
  { name: "retired transcriptBottomDistance helper", pattern: /\btranscriptBottomDistance\b/ },
  { name: "retired TRANSCRIPT_VISUAL_BOTTOM_THRESHOLD_PX constant", pattern: /\bTRANSCRIPT_VISUAL_BOTTOM_THRESHOLD_PX\b/ },
  { name: "retired transcriptScroll module file", pattern: /\bfrontend\/src\/transcriptScroll(?:\.test)?\.ts\b/ },
  // Transcript-navigation contract — the hardcoded `followOutput="smooth"`
  // on the transcript Virtuoso was retired. Smooth-animating the live-tail
  // follow on every row-length change made the open/load/resync row storm
  // read as the transcript "zipping around" before it settled, violating
  // the contract's "Load, ready, reconnect, and resync do not introduce
  // scroll jumps" acceptance check. The follow is now gated on the durable
  // NavigationMode (live-tail → instant "auto"; historical-anchor →
  // disabled) via the followLiveTail prop. Explicit user gestures still
  // animate through the scrollToLatest signal. Block the hardcoded smooth
  // follow so a refactor can't quietly bring the animated chase back.
  { name: "retired hardcoded smooth followOutput", pattern: /followOutput\s*=\s*["']smooth["']|followOutput\s*=\s*\{\s*["']smooth["']\s*\}/ },
  // tank-operator#83 follow-up — the session-list typed-event surface
  // shipped with session_scope in the row + index but the read path,
  // NATS subject, and frontend reducer all keyed on email alone. Prod
  // and slot orchestrators share one Postgres + NATS broker, so
  // cross-scope events leaked into each other's sidebars (deletes that
  // came back, slot sessions showing up in prod). The fix made scope a
  // first-class read dimension: (email, scope, order_key) is the
  // partition everywhere. Block the specific call shapes that would
  // resurrect the email-only path so a future PR can't quietly rebuild
  // the leak the type signatures otherwise enforce against.
  //
  // The Go signature change is its own compile-time guard for
  // ListByOwner/HasOrderKey/PublishSessionListEvent/SubscribeSessionList
  // Events. The patterns below cover the wire-shape and reducer-shape
  // regressions the compiler doesn't catch.
  {
    name: "retired email-only SessionListEventSubject call",
    // Pattern matches a single-argument call like
    // SessionListEventSubject(email). The new shape is two args
    // (email, scope); a one-arg call is the pre-#83-follow-up signature
    // that conflated scopes on the wire.
    pattern: /\bSessionListEventSubject\(\s*[A-Za-z_][\w.]*\s*\)/,
  },
  {
    name: "retired email-only session-list NATS subject literal",
    // Old subject: tank.live.sessions.<email_token>.events
    // New subject: tank.live.sessions.<email_token>.<scope_token>.events
    // A literal matching the old 4-segment shape (no scope token
    // between email and `.events`) is the old wire shape coming back.
    pattern: /tank\.live\.sessions\.[A-Za-z0-9_\-]+\.events\b/,
  },
  {
    name: "retired frontend session_scope default fallback",
    // The reducer used to default-fill "default" when session_scope was
    // missing on the wire; that turned malformed payloads into silent
    // state mutations. The wire shape now requires scope to be present;
    // any code that re-adds a ?? "default" fallback near session_scope
    // is the regression.
    pattern: /session_scope[\s\S]{0,40}\?\?\s*["']default["']/,
  },
  // tank-operator#525 — the session-list cold-open replay path was
  // resurrecting deleted sessions because (a) Reader.List's pod-fallback
  // re-appended pods whose registry row was visible=false during the
  // ~75s pod-termination window, and (b) the SSE handler replayed
  // session_lifecycle_events from order_key=0 when the client opened
  // with an empty cursor, letting pod-status events landing after
  // session.deleted in the ledger re-add the row via the reducer's
  // placeholder-synthesis branch. The fix made registry.List return
  // visible+invisible rows (the Reader filters output by Visible but
  // uses every id for `seen`), and made the SSE handler fast-forward
  // an empty cursor to current tip. Block reintroduction of the
  // visible-only registry SQL shape and the cursor="" loop-from-zero.
  {
    name: "retired registry visible-only SQL filter",
    // The sessions-table read query used to filter `AND visible = true`
    // at SQL time, which hid invisible rows from Reader.List's seen
    // set and let the pod-fallback re-append Terminating pods.
    // Reader is now responsible for filtering by SessionRecord.Visible
    // at output time; the SQL must return all rows for the partition.
    pattern: /FROM sessions[\s\S]{0,200}?AND\s+visible\s*=\s*true/,
  },
  {
    name: "retired SSE replay-from-zero on empty cursor",
    // Pre-#525 handleSessionsEvents looped writeSessionListStreamPage
    // when cursor.AfterOrderKey was empty; the loop emitted every
    // historical event for (email, scope) on cold open. Cold opens now
    // fast-forward the cursor to LatestOrderKey before any catch-up
    // emission. Block reintroduction of an explicit empty-cursor
    // catch-up entry point.
    pattern: /writeSessionListStreamPage\([\s\S]{0,200}?AfterOrderKey:\s*""/,
  },
  // docs/session-list-redesign.md Phase 3 — the typed-event wire was
  // replaced by the row-update wire. Block the entire pre-Phase-3
  // surface so a refactor can't quietly resurrect the event-type
  // discriminator on the wire (the failure pattern that produced
  // the placeholder-synthesis resurrection bug).
  {
    name: "removed PublishSessionListEvent typed-event publisher",
    pattern: /\bPublishSessionListEvent\b/,
  },
  {
    name: "removed SubscribeSessionListEvents typed-event subscriber",
    pattern: /\bSubscribeSessionListEvents\b/,
  },
  {
    name: "removed SessionListEventSubject helper",
    pattern: /\bSessionListEventSubject\b/,
  },
  {
    name: "removed sessionListEvents.ts frontend reducer module",
    // Block the file path so a future refactor can't quietly
    // recreate the typed-event reducer. The new wire shape lives
    // in sessionStore.ts.
    pattern: /sessionListEvents\.ts\b/,
  },
  {
    name: "removed applySessionListEvent reducer entry point",
    pattern: /\bapplySessionListEvent\b/,
  },
  {
    name: "removed applyPodStatusEvent placeholder-synthesis branch",
    pattern: /\bapplyPodStatusEvent\b/,
  },
  {
    name: "removed Tank-Lifecycle-Tip-Order-Key header",
    pattern: /\bTank-Lifecycle-Tip-Order-Key\b/,
  },
  {
    name: "removed notePlaceholderSynthesized telemetry beacon",
    pattern: /\bnotePlaceholderSynthesized\b/,
  },
  {
    name: "removed tank_session_list_client_placeholder_synthesized_total counter",
    pattern: /\btank_session_list_client_placeholder_synthesized_total\b/,
  },
  // docs/session-list-redesign.md Phase 1 — internal/podinformer and
  // cmd/tank-operator/lifecycle_emitter.go were consolidated into
  // internal/sessioncontroller so all three lifecycle producers
  // (K8s watch, chat activity, Manager user-actions) write through
  // one RowWriter. Block reintroduction so a future refactor can't
  // quietly fork the writers back apart and resurrect the three-
  // pipes-glued-at-the-SPA architecture the redesign retires.
  {
    name: "removed internal/podinformer package import",
    pattern: /\binternal\/podinformer\b/,
  },
  {
    name: "removed internal/podinformer package keyword",
    pattern: /^\s*package podinformer\b/m,
  },
  {
    name: "removed cmd/tank-operator/lifecycle_emitter.go file path",
    pattern: /cmd\/tank-operator\/lifecycle_emitter\.go\b/,
  },
  {
    name: "removed lifecycleEmitter private struct name",
    // The pre-#528 struct lived in cmd/tank-operator and held the
    // chat-activity hook. Phase 1 moved it into
    // internal/sessioncontroller as the exported ChatActivityEmitter.
    // Block the old lowercase name so a refactor can't shadow the
    // new exported type with a parallel implementation.
    pattern: /\blifecycleEmitter\b/,
  },
  // docs/session-list-redesign.md Phase 2 — Reader.List cut over to
  // reading every sidebar-visible field from the sessions row, dropping
  // the K8s pod list and the lifecycle-store hydration entirely. The
  // hydrate path was the read-side of the multi-pipe architecture the
  // redesign retires; reintroducing it would resurrect the merge-
  // boundary bug class.
  {
    name: "removed LifecycleStatusSource interface",
    pattern: /\bLifecycleStatusSource\b/,
  },
  {
    name: "removed Reader.hydrateLifecycle method",
    pattern: /\bhydrateLifecycle\b/,
  },
  // docs/session-list-redesign.md Phase 4 — the durable
  // session_lifecycle_events ledger was deleted. The sessions row is
  // now the only persistent state on the sidebar path; every transition
  // lands as a column update + row-update fan-out via RowWriter, with
  // in-memory dedup in the K8s watch's transitionTracker rather than a
  // unique-constraint dedup at the ledger. Block reintroduction of the
  // package, the table, every exported symbol, the dedup contract, and
  // the deleted metric / alert names so a future refactor can't quietly
  // resurrect the multi-pipe shape the redesign retires.
  {
    name: "removed internal/lifecycleevents package import",
    pattern: /\binternal\/lifecycleevents\b/,
  },
  {
    name: "removed internal/lifecycleevents package keyword",
    pattern: /^\s*package lifecycleevents\b/m,
  },
  {
    name: "removed session_lifecycle_events CREATE TABLE",
    // Block the literal SQL that would recreate the table. Historical
    // comments referencing the old name are fine — the regression
    // vector is bringing back the table itself or a query against it.
    // The DROP TABLE in migrations.go is exempted via
    // ignoredRelativePaths.
    pattern: /CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?session_lifecycle_events\b/i,
  },
  {
    name: "removed session_lifecycle_events SQL query",
    // INSERT/UPDATE/DELETE/SELECT against the retired table. Same
    // structural guard as the CREATE TABLE rule above.
    pattern: /(?:FROM|INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+session_lifecycle_events\b/i,
  },
  {
    name: "removed lifecycleevents.Event type",
    pattern: /\blifecycleevents\.Event\b/,
  },
  {
    name: "removed lifecycleevents.Store interface",
    pattern: /\blifecycleevents\.Store\b/,
  },
  {
    name: "removed lifecycleevents.ActivitySummary alias",
    pattern: /\blifecycleevents\.ActivitySummary\b/,
  },
  {
    name: "removed lifecycleevents.PodStatusSummary type",
    pattern: /\blifecycleevents\.PodStatusSummary\b/,
  },
  {
    name: "removed lifecycleevents.NewPostgresStore constructor",
    pattern: /\blifecycleevents\.NewPostgresStore\b/,
  },
  {
    name: "removed lifecycleevents.StubStore stand-in",
    pattern: /\blifecycleevents\.StubStore\b/,
  },
  {
    name: "removed lifecycleevents.Cursor type",
    pattern: /\blifecycleevents\.Cursor\b/,
  },
  {
    name: "removed lifecycleevents.Page type",
    pattern: /\blifecycleevents\.Page\b/,
  },
  {
    name: "removed LifecycleAppender interface (sessions.Manager dep)",
    pattern: /\bLifecycleAppender\b/,
  },
  {
    name: "removed buildLifecycleEventStore wiring helper",
    pattern: /\bbuildLifecycleEventStore\b/,
  },
  {
    name: "removed lifecycleEvents appServer field",
    pattern: /\blifecycleEvents\s+lifecycleevents\.Store\b/,
  },
  {
    name: "removed TransitionDeduped outcome (ledger dedup is gone)",
    pattern: /\bTransitionDeduped\b/,
  },
  {
    name: "removed RowWriter ledger Store param",
    // Pre-Phase-4 RowWriter constructor took (store, emitter, pool,
    // metrics). The store param is gone; this pattern matches the
    // four-arg shape so a refactor can't quietly bring it back.
    pattern: /\bNewRowWriter\(\s*[^)]*?lifecycleevents\b/,
  },
  {
    name: "removed Event.EventID field (ledger uniqueness contract)",
    pattern: /\bEvent\{[\s\S]{0,300}?EventID:/,
  },
  {
    name: "removed Event.OrderKey field (ledger ordering)",
    pattern: /\bEvent\{[\s\S]{0,300}?OrderKey:/,
  },
  {
    name: "removed LatestActivity ledger reader",
    // The lifecycle ledger's per-session activity fold. Now lives on
    // the sessions row's activity_summary column; the row read path
    // unmarshals the bytes directly. No legitimate use elsewhere.
    pattern: /\bLatestActivity\(/,
  },
  {
    name: "removed LatestPodStatus ledger reader",
    // The lifecycle ledger's per-session pod-status fold. Now sourced
    // from the sessions row's status / ready_at / terminating_at
    // columns.
    pattern: /\bLatestPodStatus\(/,
  },
  {
    name: "removed tank_session_lifecycle_event_writes_total metric",
    pattern: /\btank_session_lifecycle_event_writes_total\b/,
  },
  {
    name: "removed tank_session_pod_informer_lag_seconds metric",
    pattern: /\btank_session_pod_informer_lag_seconds\b/,
  },
  {
    name: "removed tank_session_lifecycle_activity_emit_total metric",
    pattern: /\btank_session_lifecycle_activity_emit_total\b/,
  },
  {
    name: "removed tank_session_lifecycle_activity_failure_total metric",
    pattern: /\btank_session_lifecycle_activity_failure_total\b/,
  },
  // Interrupt-on-data-plane (the retired single-consumer architecture).
  // Until this PR, both runners dispatched isInterruptCommand →
  // acceptInterrupt from inside the data-plane command consumer. That's
  // the exact shape that produced the "Stop doesn't interrupt deep
  // tool-use loops" regression: JetStream max_ack_pending=1 on the
  // command consumer held interrupt_turn behind submit_turn for the
  // duration of the turn. Interrupts now arrive via the control-plane
  // consumer (startControlConsumer); reintroducing the data-plane
  // dispatch breaks the split. The runner-shared sessionBus.js
  // legitimately keeps `isInterruptCommand` as a defensive ack-and-drop
  // (cutover hygiene for in-flight stragglers); the pattern below
  // matches only the dispatch-to-acceptInterrupt shape, not the
  // defensive drop.
  // Provider-credential health surface — the durable session.status:failed
  // banner pipeline replaces every prior "infer codex auth state from
  // turn outcomes" path the SPA might have grown. The Layer 1 row
  // (provider_credential_health) is the source of truth; the orchestrator
  // poller writes it; the SPA reads it through session.status events.
  //
  // The guards below catch the most likely regression shape: a future
  // refactor that adds an SPA-side fetch of the proxy's /health endpoint
  // (which would bypass the durable transcript surface and the debouncer)
  // or a frontend.tsx file referencing Layer 1 row state by name. We
  // intentionally do NOT block the unqualified "provider_credential_health"
  // string because legitimate backend code references the table name in
  // SQL, struct names, and counter help strings.
  {
    name: "in-SPA proxy /health/codex polling (must go via session.status banner)",
    // Block fetch("/health/codex") / new EventSource("/health/codex") /
    // similar from anywhere — the path is internal to the cluster and
    // not exposed at the orchestrator HTTP boundary anyway, but a
    // regression where a developer wires the SPA at it directly would
    // bypass the debouncer. /health/codex appears nowhere in legitimate
    // tank-operator frontend code today; the proxy and orchestrator
    // reference it as a path literal in their own internal client.
    pattern: /fetch\([\s\S]{0,80}["'`]\/health\/codex\b/,
  },

  // Run-status pill retired in favor of routing per-turn status through
  // the durable transcript + composer (docs/features/transcript/contract.md
  // names session_events as the source of truth; quality-timeframes.md's
  // review heuristic "local UI state that can contradict durable state"
  // describes the pill exactly). Per-turn failures now produce
  // ConversationViewEntry meta entries from the reducer's turn.failed /
  // turn.command_failed / turn.interrupted cases; the composer's
  // PromptInputSubmit handles Submit↔Stop. Block reintroduction of the
  // pill JSX, its state machinery, and the helper functions that only
  // existed to fill the pill's rotating verb / elapsed counter slots.
  {
    name: "removed run-status-bar CSS class",
    pattern: /\brun-status-bar\b/,
  },
  {
    name: "removed run-status-avatar CSS class",
    pattern: /\brun-status-avatar\b/,
  },
  {
    name: "removed lastStatusText pill mirror state",
    pattern: /\b(?:setLastStatusText|lastStatusText)\b/,
  },
  {
    name: "removed STREAM_VERBS pill verb cycle",
    pattern: /\bSTREAM_VERBS\b/,
  },
  {
    name: "removed formatStreamElapsed pill elapsed helper",
    pattern: /\bformatStreamElapsed\b/,
  },
  {
    name: "removed formatToolLabel pill verb helper",
    pattern: /\bformatToolLabel\b/,
  },
  {
    name: "retired interrupt-on-data-plane dispatch",
    // Anchored on `.startCommandConsumer(...)` so the control-plane
    // dispatch inside `.startControlConsumer(...)` (which legitimately
    // routes isInterruptCommand → acceptInterrupt) does not trip this
    // guard. The regression we are blocking is specifically: the same
    // handler that processes submit_turn also tries to handle interrupts,
    // which is the shape JetStream max_ack_pending=1 turns into a stuck
    // interrupt.
    pattern: /\.startCommandConsumer\(async\s*\(record\)[\s\S]{0,800}?isInterruptCommand\(record\)\s*\)\s*\{\s*(?:commandsConsumedTotal[\s\S]{0,200}?)?await\s+this\.acceptInterrupt/,
  },
  // ea70777 (romaine-life/tank-operator#652) retired the 4-token chat
  // command/control subject shape and the 3-token chat event subject
  // shape in favor of scope-partitioned 5-token / 4-token shapes that
  // partition cleanly across (scope, session_id). The cutover gap left
  // every pre-deploy session pod silently stranded on the OLD filter
  // (see CLAUDE.md → "Migration audit checklist" for the durable-
  // consumer wire-format gate). Block reintroduction of complete-
  // literal OLD subjects on the wire so a future refactor can't quietly
  // revert and silently re-strand chat. Test fixtures that construct
  // the legacy shape via string concatenation (subjects_test.go's
  // legacySlotSubject := "tank.session." + StorageToken(...) + ".events")
  // do NOT contain a complete literal and don't trip the guard. The
  // post-cutover shape is tank.session.<scope_token>.<session_token>.…
  // (one more segment between tank.session and the boundary keyword).
  {
    name: "retired 4-token chat command subject literal",
    pattern: /tank\.session\.[A-Za-z0-9_\-]+\.commands\.[a-z_]+\b/,
  },
  {
    name: "retired 4-token chat control subject literal",
    pattern: /tank\.session\.[A-Za-z0-9_\-]+\.control\.[a-z_]+\b/,
  },
  {
    name: "retired 3-token chat event subject literal",
    // tank.session.<token>.events is the pre-cutover shape; the
    // post-cutover shape is tank.session.<scope>.<token>.events. The
    // regex's [A-Za-z0-9_\-]+ stops at a dot, so the 4-token form
    // (with an extra dot before .events) does not match this pattern.
    pattern: /tank\.session\.[A-Za-z0-9_\-]+\.events\b/,
  },
  // Hermes retirement: block reintroduction of the session mode, bridge,
  // deployment environment, and metrics.
  {
    name: "retired Hermes session mode",
    pattern: /\bhermes_gui\b/,
  },
  {
    name: "retired Hermes API environment",
    pattern: /\bHERMES_[A-Z0-9_]+\b/,
  },
  {
    name: "retired Hermes bridge metrics",
    pattern: /\btank_hermes_[a-z0-9_]+\b/,
  },
  // Pi agent retirement: block reintroduction of the Pi runtime modes,
  // image tags, and build settings.
  {
    name: "retired Pi session mode pi_cli",
    pattern: /\bpi_cli\b/,
  },
  {
    name: "retired Pi session mode pi_config",
    pattern: /\bpi_config\b/,
  },
  {
    name: "retired Pi image configuration piImage",
    pattern: /\bpiImage\b/,
  },
  {
    name: "retired Pi image environment variable PI_SESSION_IMAGE",
    pattern: /\bPI_SESSION_IMAGE\b/,
  },
  {
    name: "retired pi-container image identifier",
    pattern: /\bpi-container\b/,
  },
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
  console.error("Removed chat runtime surface or architecture violations detected:");
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log("No removed chat runtime surfaces or architecture violations found.");

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
