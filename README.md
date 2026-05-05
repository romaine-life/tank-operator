# tank-operator

Web frontend over a thin K8s orchestrator that spawns ephemeral agent pods on
demand. "+ button → fresh agent shell, terminal opens in a browser tab, killed when the tab
closes." See [issue #1](https://github.com/nelsong6/tank-operator/issues/1) for the full
design and rationale.

The Claude, Codex, and Pi session images are built from `claude-container/`
in this repo (`Dockerfile`, plus bundled `mcp-auth-proxy` and `terminald`
Python packages). `platform-mcp` is installed from its standalone
[`nelsong6/platform-mcp`](https://github.com/nelsong6/platform-mcp) repo.
Session-facing MCP config, AGENTS/CLAUDE primers, the
bootstrap shell script, and bundled skill docs live in `k8s/session-config/` and are mounted
through the chart's `tank-session-config` ConfigMap. [claude-container-build.yml](.github/workflows/claude-container-build.yml)
pushes SHA-pinned `romainecr.azurecr.io/claude-container:<sha>` and
`romainecr.azurecr.io/codex-container:<sha>` /
`romainecr.azurecr.io/pi-container:<sha>` images, then rewrites the Helm chart
to point each session mode at the right image.

The HTTP MCP servers it talks to live in standalone repos:

- [`mcp-azure-personal`](https://github.com/nelsong6/mcp-azure-personal) — first-party personal Azure MCP server and chart.
- [`mcp-github`](https://github.com/nelsong6/mcp-github) — custom GitHub App-backed MCP server.
- [`mcp-k8s`](https://github.com/nelsong6/mcp-k8s) — read-only kubectl/helm MCP server.
- [`mcp-argocd`](https://github.com/nelsong6/mcp-argocd) — read-only ArgoCD MCP server.

Runtime UAMIs (e.g. `mcp.tf`, `mcp-server/`) live under `infra/`. CI auth
(image-push to ACR) for those standalone MCP repos is managed by
infra-bootstrap. Shared cluster infrastructure (the AKS cluster itself, the
ACR, the Key Vault) also lives in
[infra-bootstrap](https://github.com/nelsong6/infra-bootstrap) and is
referenced here as data sources.

## Repo layout

```
backend/                      FastAPI + kubernetes-asyncio orchestrator
frontend/                     Vite + React UI (xterm.js arrives in Phase 2)
Dockerfile                    multi-stage: vite build → python runtime
k8s/                          Helm chart: deployment, RBAC, HTTPRoute, ExternalSecret
.github/workflows/build.yml   OIDC az login → build → push to ACR
```

## Phases

1. **Skeleton** (this commit) — orchestrator Deployment up; `POST /api/sessions` creates a
   Job; `GET`/`DELETE` work; frontend `+` button hits the API and lists sessions. No exec.
2. **Exec** — WebSocket proxy + xterm.js. End-to-end terminal in browser.
3. **Polish** — tab UI, sidebar, idle reaper, optional per-session PVC.

## Local dev

```bash
# Backend (needs a kube context with access to the sessions namespace, or run --dry-run)
cd backend && pip install -e . && python -m tank_operator

# Frontend
cd frontend && npm install && npm run dev
# Vite dev server proxies /api → http://localhost:8000.
# Sign in via MSAL: the dev server uses the same Entra app registration as prod
# (redirect URI registered for https://tank.romaine.life/), so you'll need to
# either tunnel localhost behind that hostname or add a dev redirect URI.
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
- Microsoft sign-in and the GitHub onboarding wall still work.
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

Auth: the SPA uses MSAL.js to obtain an Entra ID token, POSTs it to
`/api/auth/microsoft/login`, and the backend mints its own short-lived JWT
(see [auth.py](backend/src/tank_operator/auth.py)). Sessions are scoped by
SHA-256 of the signed-in user's email. Allowlist is the comma-separated
`ALLOWED_EMAILS` env var, sourced from KV via ExternalSecret.
