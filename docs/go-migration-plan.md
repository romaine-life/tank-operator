# Go Migration Plan

This plan covers moving the Python backend/control-plane and pod sidecars to
Go without invalidating existing sessions. It is intentionally a migration plan,
not a rewrite plan. The current Python implementation remains the production
source of truth until each contract below has parity tests and an explicit
cutover gate.

## Current Boundary Map

The production system has four separable runtime surfaces:

1. **Orchestrator API**: `backend/src/tank_operator/api.py`, `sessions.py`,
   `profiles.py`, `auth.py`, `internal_api.py`, and `exec_proxy.py`.
   This serves the SPA, authenticates users, creates/adopts session pods,
   proxies terminal/run streams, stores session/run metadata, and exposes
   internal MCP-facing APIs.
2. **Anthropic API proxy**: `api-proxy/src/tank_api_proxy/server.py`.
   Envoy handles real HTTP/TLS/streaming; Python implements ext_proc header
   mutation plus single-flight OAuth refresh and Key Vault persistence.
3. **Session pod sidecars/binaries**:
   `sandbox-agent` owns pod-local interactive process terminals and replay.
   `claude-container/mcp-auth-proxy/src/.../server.py` injects fresh projected
   ServiceAccount bearer tokens into local MCP traffic.
4. **Shell/session config**: `k8s/session-config/*.sh` and `.mcp.json`.
   These are not Python, but they are part of the backend contract because the
   orchestrator execs them and depends on their filesystem markers.

The safest migration boundary is **process/API compatible replacement**, not
object-by-object translation. Keep URL paths, JSON shapes, WebSocket frame
shapes, pod labels/annotations, Cosmos document ids, and pod-local file paths
stable until all existing browser sessions and headless runs survive a backend
rollout.

## Target Go Module Boundaries

Use one Go module at repo root or `backend-go/` initially, with packages split
by runtime boundary rather than by Python file names:

- `cmd/tank-operator`: HTTP server, health/static serving, graceful shutdown.
- `internal/httpapi`: user-facing routes, request/response structs, middleware,
  SSE, WebSocket route glue.
- `internal/auth`: Entra JWT validation, app JWT mint/verify, cookie/query
  WebSocket auth, GitHub install state tokens, Kubernetes TokenReview auth.
- `internal/sessions`: session lifecycle service, pod manifest builder, idle
  reaper, session adoption, owner checks, mode normalization.
- `internal/kubeexec`: Kubernetes `pods/exec` websocket protocol, channel
  framing, stdin write, detached launch, tail/cancel stream helpers.
- `internal/store`: Cosmos implementations for profiles, session registry,
  active runs, and run events, plus in-memory fallbacks for local/test mode.
- `internal/internalapi`: MCP/internal route group and caller-pod resolution.
- `internal/static`: embedded SPA assets plus optional static override support.
- `cmd/tank-api-proxy-extproc`: Go replacement for the ext_proc sidecar only;
  Envoy stays as-is.
- `cmd/mcp-auth-proxy`: Go replacement for pod-local MCP auth injection.

Keep Dockerfiles able to build Python and Go variants side-by-side for at least
one release. The Helm chart should make the binary image/command selectable by
value, not by branch.

## Compatibility Contracts

### HTTP and WebSocket API

The Go orchestrator must be wire-compatible with all current routes:

- Auth/config: `/healthz`, `/api/config`, `/api/auth/microsoft/login`,
  `/api/auth/logout`, `/api/internal/auth/k8s`, `/api/auth/me`,
  `/api/github/install/url`, `/api/github/install/callback`.
- Session CRUD/events: `/api/sessions`, `/api/sessions/with-context`,
  `/api/sessions/events`, `/api/sessions/{id}`, `/touch`, `/test-state`,
  `/rollout-state`, `/save-credentials`, `/paste-image`.
- Session file/metadata APIs: `/skills`, `/mcp-servers`, `/files`,
  `/files/content`, `/files/upload`, `/files/walk`, `/files/raw`.
- Runs: `/run/active`, `/run/history`, `/runs/{run_id}/events`,
  `/api/sessions/run`, `/api/sessions/{id}/messages`,
  websocket `/api/sessions/{id}/run`.
- CLI terminal: `/api/sessions/{id}/cli-process` and websocket
  `/api/sessions/{id}/sandbox-agent/v1/processes/{process_id}/terminal/ws`.
- Internal MCP APIs under `/api/internal/*`.

Route compatibility means preserving status codes, close codes, response JSON
field names, SSE event names, WebSocket first-frame semantics, keepalive frames,
and current error strings where the frontend or MCP tools may display them.

### Kubernetes Session Objects

Existing pods must remain adoptable. The Go implementation must read and write:

- Pod name: `session-{session_id}`.
- Labels: `app.kubernetes.io/managed-by=tank-operator`,
  `app.kubernetes.io/instance`, `tank-operator/owner`,
  `tank-operator/session-id`, `tank-operator/mode`,
  `azure.workload.identity/use=true`.
- Annotations: `tank-operator/owner-email`,
  `tank-operator/display-name`, `tank-operator/glimmung-context`,
  `tank-operator/test-state`, `tank-operator/rollout-state`,
  ArgoCD tracking id.
- Container names: especially `claude` and `mcp-auth-proxy`.
  All `pods/exec` calls must continue to specify `container=claude`.
- Session status vocabulary: `Pending`, `Active`, `Failed`.
- Legacy adoption: pods without a `sandbox-agent` port and pods owned by
  legacy Deployments must retain the current conservative behavior.

### Durable Data

Do not change Cosmos document shapes during the first Go cutover:

- Profiles partition by normalized email.
- Session registry records keep ids `session:{id}` or `session:{scope}:{id}`.
- Session counter records keep `session-counter` / `session-counter:{scope}`.
- Active run records remain partitioned by `session_id`.
- Run events remain partitioned by `run_id`, with event ids usable as
  `Last-Event-ID` cursors.

If schema changes become necessary later, add read-old/write-new support first,
ship it in Python or Go, wait one release, then migrate.

### Pod-Local Run Contract

The run plane has fragile compatibility requirements:

- Prompt staging remains `/tmp/tank-prompt-*`.
- Stream files remain `/tmp/tank-run-{run_id}.stream`.
- PID files remain `/tmp/tank-run-{run_id}.pid`.
- History fallback remains `/tmp/tank-run-history.ndjson` plus the latest
  Claude JSONL project file.
- Exit marker remains `__TANK_RUN_EXIT__:{rc}`.
- Cancellation continues to kill the recorded process and its children.
- Browser disconnect must not cancel a live headless run; explicit cancel must.

These paths are the adoption mechanism after an orchestrator restart or rollout.

## Session Adoption Strategy

Go should start by adopting existing runtime state rather than creating a new
source of truth:

1. On startup, list managed session pods and build no persistent in-memory
   session table. Treat Kubernetes plus Cosmos as source of truth.
2. For every user list request, follow the current algorithm: list owner-labeled
   pods, backfill missing visible session registry records, then merge registry
   records with live pods.
3. Reaper behavior must preserve the current restart grace: if no local
   activity is known for a pod after process startup, adopt it as active "now"
   and only reap on a later sweep.
4. Active runs must be verified against pod-local PID files. A Cosmos active-run
   row is only a hint; if the process is gone, mark it stale.
5. Go rollout must run with `maxUnavailable: 0` and enough `preStop` delay that
   existing WebSockets drain naturally while new HTTP requests land on the new
   pod.

The current chart has more than one orchestrator replica configured, while the
Python comments describe single-replica assumptions for in-memory WebSocket
tracking. The Go migration should not deepen that coupling. Either make the
reaper leader-elected before cutover, or disable reaping in non-leaders and
document which pod is leader.

## WebSocket and Exec Risks

The highest-risk work is not CRUD; it is stream semantics.

- Kubernetes exec protocol is channel-prefixed binary frames. Go must correctly
  handle channels 0 stdin, 1 stdout, 2 stderr, 3 status/error, and 4 resize,
  including final `v1.Status` parsing.
- Multi-container pods require `container=claude`. Missing this still produces
  browser reconnect failures.
- `exec_write_file` currently streams exact byte counts without relying on a
  stdin-close control. The Go implementation must prove this works for 0-byte,
  small, and multi-chunk writes.
- The run WebSocket multiplexes browser control frames with pod output. A
  browser tab refresh should leave the detached pod process running, while a
  `{"cancel":true}` frame should terminate it.
- Keepalive frames are load-bearing for gateways and long pod readiness waits.
- The terminal path is currently proxied to sandbox-agent, not Kubernetes exec.
  Keep the `/cli-process` and sandbox-agent terminal WebSocket routes
  compatible.

Recommended Go libraries:

- `k8s.io/client-go/kubernetes` and `k8s.io/client-go/tools/remotecommand` for
  exec where possible. If `remotecommand` cannot expose the exact stream/control
  behavior needed for current browser framing, isolate a lower-level SPDY/WebSocket
  implementation behind `internal/kubeexec`.
- `github.com/coder/websocket` or `nhooyr.io/websocket` for browser/server
  WebSockets; keep binary/text behavior explicit.
- Azure SDK for Go for Key Vault, Cosmos, and Workload Identity.

## Rollout Strategy

Use a strangler rollout with explicit gates:

1. **Golden contract tests first**. Add request/response and WebSocket-frame
   tests that exercise the Python app and can be reused against Go. Include pod
   manifest snapshots, mode aliases, owner label hashing, Cosmos document ids,
   run script builders, and internal API auth failures.
2. **Read-only Go shadow server**. Implement `/healthz`, `/api/config`, auth
   token decode, session list/get, and internal `resolve-caller` in Go. Deploy
   behind a separate Service or hidden prefix, compare responses from Python
   and Go in validation without serving users.
3. **Sidecars with narrow blast radius**. Migrate `mcp-auth-proxy` first,
   controlled by session image tag and verified only on newly created sessions.
   Existing sessions keep their old sidecars because pods are immutable.
4. **API route groups behind flags**. Move low-streaming REST groups first:
   profile/auth-me, session list/get/patch, file list/read. Keep create/delete,
   exec/write, runs, and WebSockets on Python until late.
5. **Dual-image orchestrator rollout**. Add Helm values for Python vs Go command
   and/or image. Use validation slots and PR CI Docker build checks before
   production. Do not route both implementations to mutate the same session at
   the same time unless the route is proven idempotent.
6. **Cutover streaming last**. Move run WebSocket and kubeexec only after parity
   tests cover disconnect, resume, cancel, stderr filtering, prompt staging
   failure, detached launch retry, and active-run adoption.
7. **Remove Python only after one stable production window** where all new
   sessions are Go-created and old Python-created sessions have been adopted,
   listed, streamed, renamed, and deleted by Go.

## Safe First Slice

The first implementation slice should be small and useful:

1. Create a Go module and `cmd/tank-operator` that serves `/healthz`.
2. Implement shared pure functions and tests:
   mode normalization, owner label hashing, session id/name validation,
   run id validation, session document ids, active-run document ids, and pod
   manifest generation for one representative mode.
3. Add Python-vs-Go manifest golden tests. The output for a standard
   `claude_cli`, `codex_cli`, `codex_gui`, and `pi_cli` pod should match the
   current Python manifest after normalizing map order and generated session id.
4. Implement read-only session list/get in Go using Kubernetes labels and Cosmos
   registry reads, but do not expose it on production traffic yet.
5. Deploy the Go server as a non-public shadow Deployment in a validation slot
   and compare its `/api/sessions` response with Python for the same user.

This slice exercises the hardest non-streaming contracts: Kubernetes object
compatibility, registry compatibility, mode/image selection, and adoption. It
does not touch live exec, terminal WebSockets, credential harvesting, or
headless run launch.

## Migration Checklist

- Add contract tests before Go mutations.
- Keep Python and Go built in CI until final removal.
- Preserve current API and pod object contracts.
- Make reaper leadership explicit before Go cutover.
- Do not change Cosmos schemas in the first cutover.
- Do not move run WebSocket or kubeexec until low-risk REST and sidecars have
  already run in production.
- Treat existing session pods as immutable; only new sessions receive new
  sidecar binaries or manifest changes.
- Use validation slots and GitHub Actions image build checks for Docker feedback.
