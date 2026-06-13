# Artifacts And Files Capabilities

Named behaviors in the artifacts-and-files surface. See
[contract.md](contract.md) for the durable invariants and
[../README.md](../README.md) for how capability ledgers are used.

## static-page-render

- **Status:** shipped
- **Intent:** Let a user render an agent-authored `.html` workspace file as a
  live page — the primary case is an LLM-generated diagram — from the files
  viewer, without exposing the auth token to that untrusted page.
- **Entry point:** an "Open as page" action in the file viewer for `.html` /
  `.htm` files, plus the session-scoped route
  `/sessions/{id}/static/<workspace-path>`.
- **Render model (load-bearing security invariant):** the page renders in a
  sandboxed `<iframe srcDoc>` **without** `allow-same-origin`, so it runs in an
  opaque origin and cannot read `localStorage` (the auth.romaine.life JWT) or
  script the surrounding app. Same-origin (`tank.romaine.life`) is therefore
  safe; the Tank chrome around the frame lives outside the sandbox and cannot
  be spoofed by the framed content. Never add `allow-same-origin`.
- **Durable source:** `static_page_snapshots` (Postgres `bytea`, keyed by
  `(session_scope, session_id, rel_path)`, 12h TTL with an inline expired-row
  sweep). Opening a page captures a fresh snapshot from the live pod
  (recapture-on-open) and returns the bytes inline; the read path serves the
  snapshot without touching the pod, so a rendered page outlives the ephemeral
  session for its TTL. Rows are scope-fenced like every other shared table.
- **Endpoints:** `POST /api/sessions/{id}/static-pages` (capture; owner/admin
  read gate; requires the live pod) and `GET /api/sessions/{id}/static-pages`
  (serve the snapshot; pod-independent). Non-HTML paths and `..` segments are
  rejected, and `safeWorkspacePath` re-validates the path server-side.
- **Non-goal (current):** no public / no-auth share link yet. A
  `session_scope`-fenced snapshot plus an opaque token is the intended later
  extension; until then reads stay owner/admin-gated and the render is in-app.
- **Observability:** `tank_static_page_total{operation,result}` counts capture
  and read attempts with bounded labels
  (`ok`, `not_found`, `pod_unavailable`, `store_error`, …).

## workspace-image-page-route

- **Status:** shipped
- **Intent:** Let a user open an image selected in the workspace files panel in
  a dedicated browser tab with a stable session-scoped app URL.
- **Entry point:** the browser's native image/link context menu on the image
  preview, backed by the session-scoped route
  `/sessions/{id}/files/<workspace-path>`.
- **Render model:** the route reconstructs the files panel selection on load
  and keeps the protected image bytes on the existing authenticated
  `files/raw` fetch-to-blob path. The URL names the app page and selected
  workspace path; it is not a raw protected image URL.
- **Durable boundary:** this is still a live workspace file. Reload works while
  the session pod and file exist; session-pod death makes the image unavailable
  like any other pod-local workspace file.
- **Evidence:** `frontend/src/FileImageViewer.test.tsx`,
  `frontend/src/appRoutes.test.ts`, and a browser validation of a
  `/sessions/{id}/files/<workspace-path>` image preview route.

## workspace-read-denylist

- **Status:** shipped
- **Intent:** Let the pod owner browse anything the (bypass-permissions) agent
  wrote in the session pod — `~/.claude` plans, agent state, `/opt/tank` bundled
  skills/docs, `/tmp` scratch, or wherever else the agent wandered — instead of
  being fenced to `/workspace`. Motivated by agents writing plans/output outside
  `/workspace` that the owner then could not view in the files panel.
- **Boundary (load-bearing security invariant):** the read/browse file API is
  default-allow; the ONLY fence is `sessionmodel.SecretMountDenyPrefixes`
  (`/var/run/secrets/`, its `/run/secrets` realpath form, `/proc/`, `/sys/`),
  applied by `sessionmodel.PathReadable` to the in-pod **realpath**. The realpath
  is resolved in-pod (`resolveInPodReadablePath`) and the allow/deny decision is
  made in Go — single source of truth — so a `/workspace/leak -> /var/run/secrets`
  symlink is refused. `safeReadablePath` is a lexical pre-filter only. Writes and
  uploads keep `safeWorkspacePath` (`/workspace`-fenced); reads outside
  `/workspace` are read-only.
- **Fail-safe:** `TestProjectedSecretMountsAreDenied` asserts every projected
  serviceAccountToken / Secret mountPath in the generated pod spec (plus the
  implicit kubelet-injected default-SA mount) is denied, so a future secret
  mounted at a novel path fails CI instead of silently becoming browsable. The
  denylist lives next to the mount construction in `sessionmodel.go`.
- **Surface:** absolute pod paths throughout the read API and files panel
  (`/api/sessions/{id}/files`, `/files/content`, `/files/raw`, `/files/walk`, the
  `/sessions/{id}/files/<abs-path>` route, and the file-raw stream-ticket resource
  id — keyed on the absolute path so it matches the raw read). The panel lands on
  `/workspace` with bookmark chips for `~`, `/opt/tank`, `/tmp`; 403 (denied) is
  distinct from 404.
- **Observability:** `tank_file_read_total{operation,path_class,result}`; the
  `denied` result is the token-probe signal.
- **Non-goals (v1):** writes outside `/workspace` via the UI; linkifying
  non-`/workspace` paths in chat prose; static-page "Open as page" outside
  `/workspace`.
- **Evidence:** `backend-go/internal/sessionmodel/sessionmodel_test.go`
  (`TestProjectedSecretMountsAreDenied`, `TestPathReadable`),
  `backend-go/cmd/tank-operator/handlers_files_test.go` (`TestSafeReadablePath`,
  `TestRealpathResolveAndDenylist`), `frontend/src/appRoutes.test.ts`,
  `frontend/src/workspaceRoots.test.ts`, and a test-slot browser validation
  (browse `~/.claude`, a `/var/tmp` agent-write, a denied `/var/run/secrets`
  probe, and a symlink-escape).
