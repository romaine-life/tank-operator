# Live-preview sender (generic, repo-agnostic)

The **sender** half of Glimmung's live frontend preview lane. A dev session
builds its own repo's frontend, pushes the built `dist/` to its preview
environment's **edge**, and tells Glimmung — so a co-watchable preview URL
serves the freshly-built UI override-first over a **stable** backend, iterating
in seconds instead of a full CI image build + deploy.

This is the **live-preview** lane (scratch, *for seeing*) — never the faithful
image-deploy validation lane, and it shares **no vocabulary** with the retired
hot-swap path. The build is **sender-side**: the edge never builds.

- Plan / locked architecture: glimmung `docs/live-preview-plan.md`.
- Edge data plane + contracts: glimmung `cmd/live-preview-edge`,
  `internal/livepreview`, and the Stage 2a preview API in
  `internal/server/preview_api.go`.

## Where it lives / how it's invoked

Two scripts, delivered to every session pod through the session ConfigMap
(`tank-session-config`), mounted read-only at `/opt/tank/session-config/`:

| Script | Role |
| --- | --- |
| `live-preview-push.sh` | one-shot: build → resolve → push → receipt (→ optional wait-live) |
| `live-preview-watch.sh` | daemon: watch `dist/` and push on every settled change |

Invoke with `bash` (the ConfigMap mounts them non-executable, by design):

```sh
# one-shot, full convention, from inside the repo checkout:
bash /opt/tank/session-config/live-preview-push.sh --build

# push an already-built dist/ and wait for Glimmung to confirm it observed-live:
bash /opt/tank/session-config/live-preview-push.sh --no-build --wait-live

# daemon: dev runs their own `vite build --watch`; the daemon pushes each change:
bash /opt/tank/session-config/live-preview-watch.sh

# daemon that also drives the build tool itself:
bash /opt/tank/session-config/live-preview-watch.sh --watch-cmd 'npm run build -- --watch'

# revert the override (edge serves the stable backend again):
bash /opt/tank/session-config/live-preview-push.sh --revert
```

> The v1 tank-operator-specific sender (`push-frontend.sh` +
> `live-preview-daemon.sh`, which target the retired in-app static-override
> receiver) is **left untouched**; Stage 5 deletes it and cuts tank-operator
> over to these generic scripts. These are additive.

## Per-repo build convention (repo-agnostic)

Nothing is hardcoded to one app. Each setting is resolved
**flag > env > config file > convention default**:

| Setting | Flag | Env | Config key | Default |
| --- | --- | --- | --- | --- |
| repo root | `--repo` | `LIVE_PREVIEW_REPO_DIR` | — | `git rev-parse --show-toplevel` of CWD, else CWD |
| frontend dir | `--frontend-dir` | `LIVE_PREVIEW_FRONTEND_DIR` | `frontend_dir` | `frontend/` if it has `package.json`, else repo root |
| dist dir | `--dist` | `LIVE_PREVIEW_DIST_DIR` | `dist_dir` | `dist` (relative to frontend dir) |
| build command | `--build-cmd` | `LIVE_PREVIEW_BUILD_CMD` | `build_command` | `npm ci && npm run build` |
| build id | `--build-id` | `LIVE_PREVIEW_BUILD_ID` | — | content hash of `dist/` (`c-<16 hex>`) |
| watch command | `--watch-cmd` | `LIVE_PREVIEW_WATCH_CMD` | `watch_command` | none (dev runs their own watcher) |

Optional per-repo config file at `$REPO/.tank/live-preview.json`:

```json
{
  "frontend_dir": "frontend",
  "dist_dir": "dist",
  "build_command": "npm ci && npm run build",
  "watch_command": "npm run build -- --watch",
  "glimmung_url": "http://glimmung.glimmung.svc.cluster.local",
  "project": "myapp",
  "name": "p-1234"
}
```

**Build id = content hash of `dist/`.** The id reflects the exact bytes served,
so Glimmung's observed read-back compares real content and identical builds get
a stable id. The daemon also uses the content hash for change detection, so it
pushes only when the output actually changed.

## Preview-env resolution

The target preview env (its `{project}/{name}` + edge URL) is resolved, in
order:

1. Explicit `--preview-url URL --project P --name N` (no Glimmung lookup).
2. Glimmung `GET /v1/previews`, matched by `--project/--name`, else this
   session's `$SESSION_ID` (`session_id` field), else this token's verified
   subject (`authorized_subject` field). Ambiguous matches error and list the
   candidates; no match errors with "provision one first".

A preview env is provisioned out of band (Glimmung `POST /v1/previews`,
control-plane only); the sender never provisions — it pushes onto an existing
preview.

## The wire contracts it speaks (all landed on glimmung `main`)

| Call | Method + path | Auth | Body / headers |
| --- | --- | --- | --- |
| edge push | `PUT {previewURL}/__live-preview/push` | service JWT | gzip(tar(`dist/`)); `X-Live-Preview-Build: <build id>` |
| edge revert | `DELETE {previewURL}/__live-preview/push` | service JWT | — |
| edge status | `GET {previewURL}/__live-preview/status` | service JWT | (read by Glimmung's verifier) |
| list previews | `GET {glimmung}/v1/previews` | service JWT | — |
| push receipt | `POST {glimmung}/v1/previews/{project}/{name}/push-receipt` | service JWT | `{"build":"<build id>"}` |
| one preview | `GET {glimmung}/v1/previews/{project}/{name}` | service JWT | (polled by `--wait-live`) |

`{glimmung}` defaults to `http://glimmung.glimmung.svc.cluster.local`
(`GLIMMUNG_INTERNAL_URL` / `--glimmung-url` / config `glimmung_url`).

### Claimed vs observed (load-bearing)

The receipt records a **CLAIM** ("pushed `<build>`"). The sender reports
`pushed (claim recorded)`, **never** "live". Glimmung's observed verifier then
reads `GET /__live-preview/status` back and marks the env `live` only when the
edge is serving exactly that build (`stale` if not). `--wait-live` polls
Glimmung's durable row and reports `OBSERVED LIVE` / `OBSERVED STALE` / timeout —
it never trusts the local push's own optimism.

## Auth — the #1 footgun

The edge **and** Glimmung verify an **auth.romaine.life-signed** service-principal
JWT (RS256, `iss=https://auth.romaine.life`, `role=service`). The pod's projected
token at `/var/run/secrets/auth.romaine.life/token` is **cluster-signed** with
that *audience* — it is **not** itself the accepted JWT. The sender **exchanges**
it:

```
POST {auth}/api/auth/exchange/k8s   Authorization: Bearer <projected token>
  -> { "token": "<auth.romaine.life service JWT, sub=svc:tank:<session>>" }
```

That returned JWT is what is presented (`Authorization: Bearer <jwt>`) to the
edge and to Glimmung. This is the same exchange the v1 sender, the runner
launcher, and Glimmung's own verifier (`RomaineServiceTokenSource`) use
(`{auth}` defaults to `https://auth.romaine.life`,
`AUTH_ROMAINE_EXCHANGE_URL`/`AUTH_ROMAINE_TOKEN_PATH` override).

The edge additionally requires the JWT `sub` to equal the preview's
`AUTHORIZED_SUBJECT` (set by Glimmung at provision = the preview owner = this
session's subject) — **a pod may only write its own preview**. The projected
token is re-read fresh on every exchange (it rotates ~hourly).

## Operator output & failure states

`live-preview: …` progress on stderr: building → exchanging identity →
resolved preview → pushing → pushed (claim recorded) → (optional) OBSERVED LIVE.
Distinct, actionable exit codes:

| Code | Meaning |
| --- | --- |
| 0 | pushed (and, with `--wait-live`, observed live) |
| 2 | usage error |
| 3 | build failed / no `dist/index.html` |
| 4 | auth exchange returned no token |
| 5 | preview resolution failed (none / ambiguous / no URL yet) |
| 6 | edge push/revert failed (401/403 auth, 413 too large, 400 bad archive, 5xx) |
| 7 | Glimmung receipt failed |
| 8 | `--wait-live`: observed stale, or timed out |

A 401/403 push error explicitly names the `role=service` + `authorized_subject`
requirement. The daemon leaves the prior bundle live on a failed push and stops
after `--max-failures` consecutive failures.

## Tests

`k8s/session-config/tests/live-preview-sender-test.py` exercises the full
contract end to end against in-process fakes of auth.romaine.life, the edge, and
Glimmung — token exchange, build-by-convention, all three resolution paths,
content-hash build id, gzip-tar push with the build header, edge auth
enforcement (role + subject), receipt, `--wait-live` observed-live, `--revert`,
and the watch daemon. Run:

```sh
python3 k8s/session-config/tests/live-preview-sender-test.py
```
