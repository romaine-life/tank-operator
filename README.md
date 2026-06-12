# tank-operator

Web frontend over a thin K8s orchestrator that spawns ephemeral agent pods on
demand. The launcher creates GUI chat and terminal agent sessions backed by
Kubernetes pods.

The Claude and Codex session images are built from `claude-container/`
in this repo (`Dockerfile`, plus bundled `mcp-auth-proxy`).
Session-facing MCP config, AGENTS/CLAUDE primers, the
bootstrap shell script, and bundled skill docs live in `k8s/session-config/` and are mounted
through the chart's `tank-session-config` ConfigMap. [claude-container-build.yml](.github/workflows/claude-container-build.yml)
pushes SHA-pinned `romainecr.azurecr.io/claude-container:<sha>` and
`romainecr.azurecr.io/codex-container:<sha>` images, then rewrites the Helm chart
to point each session mode at the right image.

The HTTP MCP servers it talks to live in standalone repos:

- [`mcp-azure-personal`](https://github.com/romaine-life/mcp-azure-personal) â€” first-party personal Azure MCP server and chart.
- [`mcp-github`](https://github.com/romaine-life/mcp-github) â€” custom GitHub App-backed MCP server.
- [`mcp-k8s`](https://github.com/romaine-life/mcp-k8s) â€” read-only kubectl/helm MCP server.
- [`mcp-argocd`](https://github.com/romaine-life/mcp-argocd) â€” read-only ArgoCD MCP server.

Runtime UAMIs (e.g. `mcp.tf`, `mcp-server/`) live under `infra/`. CI auth
(image-push to ACR) for those standalone MCP repos is managed by
infra-bootstrap. Shared cluster infrastructure (the AKS cluster itself, the
ACR, the Key Vault) also lives in
[infra-bootstrap](https://github.com/romaine-life/infra-bootstrap) and is
referenced here as data sources.

## Messaging durability scope

For messaging docs, "session pod" means the Kubernetes pod backing one user
session, including its workspace `emptyDir` and the pod-side Claude/Codex
runner containers.

SDK GUI turns are durable across browser disconnects, frontend reloads,
orchestrator rollouts, and runner-process restarts inside the same still-live
session pod. The browser submits turns through
`POST /api/sessions/{session_id}/turns`; the backend publishes durable
Tank conversation events and a NATS JetStream session command, and pod-side
runners consume those commands before feeding the provider SDK. The UI renders
durable conversation events from `/timeline` and the
`/api/sessions/{session_id}/events` SSE stream. Stop is a durable session
command: it is not considered complete until the runner publishes
`turn.interrupted`. Claude AskUserQuestion pauses the active turn with durable
`turn.awaiting_input`; the user's answer is recorded through
`/turns/{turn_id}/answer` as `turn.input_answered` and delivered to the paused
runner as `input_reply`. The backend session-bus persister writes runner events
to the Postgres `session_events` ledger and wakes open SSE streams only after
that write commits, so live delivery is a notification layer over persisted
history rather than browser polling.

Session-pod deletion or death is intentionally outside the messaging
durability goal. A dead session pod means the session and its `emptyDir`
workspace are gone; Tank does not try to resurrect that pod or preserve
in-flight agent work after it. Do not treat that as a product gap unless the
session lifecycle goal changes.

## Repo layout

```
backend-go/                   Go orchestrator (Postgres + KV + k8s exec)
frontend/                     Vite + React UI
api-proxy/                    Envoy ext_proc (Python): injects provider OAuth, refreshes on 401
agent-container/              Long-lived pod-side runner (Go) â€” in progress, see CLAUDE.md
claude-container/             Claude session image bootstrap + Dockerfile
k8s/                          Helm chart: deployment, RBAC, HTTPRoute, ExternalSecret
infra/                        Tofu â€” Postgres, KV, UAMI, role assignments
Dockerfile                    multi-stage: vite build â†’ go build â†’ alpine runtime
.github/workflows/build.yml   OIDC az login â†’ build â†’ push to ACR
```

## Local dev

```bash
# Orchestrator
cd backend-go && go build ./... && go test ./...
# Run requires kube context (in-cluster or kubeconfig).

# Frontend
cd frontend && npm install && npm run dev
# Vite dev server proxies /api â†’ http://localhost:8000.
# Sign-in is delegated to auth.romaine.life; clicking Sign-in redirects you
# there, you complete Microsoft sign-in, and bounce back. For local dev the
# session cookie on .romaine.life makes the silent auth path "just work"
# if you're already signed into another romaine.life app in the same browser.
```

## Desktop app mode

Tank is still a hosted control plane; do not run the backend or Kubernetes
orchestration inside a desktop wrapper. For desktop ergonomics, validate the
hosted frontend in a standalone browser app window first:

```bash
google-chrome --app=https://tank.romaine.life
```

On macOS:

```bash
open -na "Google Chrome" --args --app=https://tank.romaine.life
```

On Windows PowerShell, either Chrome or Edge works if installed in the default
location:

```powershell
Start-Process "$env:ProgramFiles\Google\Chrome\Application\chrome.exe" "--app=https://tank.romaine.life"
Start-Process "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe" "--app=https://tank.romaine.life"
```

Chrome and Edge can also install Tank as a web app from the browser menu. The
frontend ships a web app manifest with standalone display metadata and
install-sized icons so the installed entry has Tank branding instead of a
generic browser shortcut.

Validation checklist before considering Electron:

- Tank opens in its own app-like window without regular browser tabs.
- Sign-in via auth.romaine.life and the GitHub onboarding wall still work.
- Session list, create, rename, refresh, and delete flows still work.
- Terminal WebSocket attach works across active session switches.
- Clipboard and image paste behave well enough for terminal use.
- Alt+Tab and window switching feel materially better than a normal browser tab.

Only prototype Electron if this app-mode path fails and the missing value is
specific to native shell features such as app identity, custom protocols,
notifications, tray/menu/global shortcuts, or deeper clipboard integration.

## Deploy

ArgoCD auto-syncs `k8s/` when changes hit `main`. Image is built and pushed to
`romainecr.azurecr.io/tank-operator:<sha>` (and `:latest`) by `.github/workflows/build.yml`.

## Glimmung Test-Slot Hot Swap

Tank validation slots run the orchestrator through `/app/tank-supervisor` when
`renderMode=hot`. The chart mounts `/var/run/tank-operator-hot` for
backend artifacts and `/var/run/tank-operator-static-override` for frontend
assets. Production keeps the normal `/app/tank-operator-go` command and image
rollout path.

Operational notes:

- Test slots mount `/var/run/tank-operator-static-override` read-write in the
  app container; write static overrides through the `tank-operator` container.
  Older slots may still include a `static-writer` sidecar, but the sidecar is
  not required for hot-swap.
- `/app/tank-supervisor` does not watch the hot backend artifact. After copying
  a new backend binary to `/var/run/tank-operator-hot/tank-operator-go`, send
  `SIGHUP` to PID 1 in the `tank-operator` container. Do not kill the child
  `tank-operator-go` process directly; the supervisor treats child exit as
  terminal and exits the container.
- Hot-swap every ready app pod unless you intentionally scaled the deployment
  down for a one-off diagnostic.

Project metadata for Glimmung:

```json
{
  "test_slot_hot_swap": {
    "enabled": true,
    "static": {
      "enabled": true,
      "source": "frontend/dist",
      "target": "/var/run/tank-operator-static-override"
    },
    "backend": {
      "enabled": true,
      "strategy": "supervisor",
      "build_command": "cd backend-go && go build -o /tmp/tank-operator-go ./cmd/tank-operator",
      "artifact": "/tmp/tank-operator-go",
      "target": "/var/run/tank-operator-hot/tank-operator-go",
      "health_path": "/healthz"
    },
    "fidelity_classifier": {
      "enabled": true,
      "command": "node scripts/classify-tank-test-fidelity.mjs"
    },
    "agent_runner": {
      "enabled": true,
      "strategy": "supervisor",
      "build_command": "cd claude-runner && npm ci && npm run build && rm -rf hot && mkdir -p hot && cp -R dist hot/dist && cp -R ../runner-shared hot/runner-shared && find hot/dist -name '*.js' -exec sed -i 's|\"\\.\\./\\.\\./runner-shared/|\"/var/run/claude-runner-hot/runner-shared/|g; s|\"\\.\\./\\.\\./\\.\\./runner-shared/|\"/var/run/claude-runner-hot/runner-shared/|g' {} +",
      "source": "claude-runner/hot",
      "target": "/var/run/claude-runner-hot",
      "restart": "SIGHUP",
      "container": "claude-runner",
      "pod_selector": "tank-operator/session-id,tank-operator/mode in (claude_gui,claude_secondary_gui)",
      "builder_image": "node:20-alpine"
    },
    "codex_runner": {
      "enabled": true,
      "strategy": "supervisor",
      "build_command": "cd codex-runner && npm ci && npm run build && rm -rf hot && mkdir -p hot && cp -R dist hot/dist && cp -R ../runner-shared hot/runner-shared && find hot/dist -name '*.js' -exec sed -i 's|\"\\.\\./\\.\\./runner-shared/|\"/var/run/codex-runner-hot/runner-shared/|g; s|\"\\.\\./\\.\\./\\.\\./runner-shared/|\"/var/run/codex-runner-hot/runner-shared/|g' {} +",
      "source": "codex-runner/hot",
      "target": "/var/run/codex-runner-hot",
      "restart": "SIGHUP",
      "container": "codex-runner",
      "pod_selector": "tank-operator/session-id,tank-operator/mode in (codex_gui,codex_exec_gui,codex_app_server)",
      "builder_image": "node:20-alpine"
    },
    "antigravity_runner": {
      "enabled": true,
      "strategy": "supervisor",
      "build_command": "export DEBIAN_FRONTEND=noninteractive && apt-get update -qq && apt-get install -y -qq --no-install-recommends curl ca-certificates && curl -fsSL https://go.dev/dl/go1.26.0.linux-amd64.tar.gz -o /tmp/go.tgz && rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz && export PATH=/usr/local/go/bin:$PATH && rm -rf antigravity-runner-hot && mkdir -p antigravity-runner-hot && cd backend-go && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o ../antigravity-runner-hot/antigravity-cli-runner ./cmd/antigravity-runner",
      "source": "antigravity-runner-hot",
      "target": "/var/run/antigravity-runner-hot",
      "restart": "SIGHUP",
      "container": "antigravity-runner",
      "pod_selector": "tank-operator/session-id,tank-operator/mode=antigravity_gui",
      "builder_image": "node:20-bookworm-slim"
    }
  }
}
```

The `antigravity_runner` builder is a Node image that installs the Go
toolchain at build time, not a stock `golang:` image. The shared
`fidelity_classifier` (`node scripts/classify-tank-test-fidelity.mjs`) runs in
the builder *before* the build command, so the builder image must carry `node`,
while compiling the Go runner needs `go` — and no stock image ships both. A
dedicated go+node builder image baked to ACR would be a cleaner future
replacement for the install-at-build-time step.

Auth: Microsoft sign-in is delegated to auth.romaine.life. The SPA fetches
an auth.romaine.life JWT (silent if the `.romaine.life` session cookie is
present, otherwise via a top-level redirect through Microsoft) and presents it
directly to tank-operator. The orchestrator verifies the RS256 signature
against auth.romaine.life/api/auth/jwks and gates on the `role` claim:
`admin` and `user` are the human
roles, `service` is reserved for k8s service principals (session pods
that exchange their projected SA token for an auth.romaine.life JWT via
`/api/auth/exchange/k8s` â€” see
[romaine-life/tank-operator#486](https://github.com/romaine-life/tank-operator/issues/486)).
`pending` (auth.romaine.life's default for fresh Microsoft sign-ups)
gets a 403 until an admin promotes the user via auth.romaine.life's
/admin console. No per-tank email allowlist.

Service-role tokens carry an `actor_email` claim with the human owner
of the calling pod's session. Every `/api/internal/sessions/*` handler
gates on this claim to scope writes to the actor's session tree â€” a
pod cannot create or mutate sessions for any other actor.
