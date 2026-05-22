# glimmung

Use `project: "glimmung"` when reserving Glimmung test slots for this repo.

Always read the current contract first with
`get_test_slot_hot_swap_contract(project: "glimmung")`. Do not rely on
hardcoded paths, pod selectors, container names, restart commands, or artifact
locations from this guide.

Glimmung's own test slots run the issue chart in hot mode. The app workload
lives in the slot namespace, uses the slot name as the app instance label, and
runs the backend under `/app/glimmung-supervisor`.

Run repo-local commands from the Glimmung repo root. Put the changed path list
in `/tmp/glimmung-changed-files` before invoking the hot-swap command.

For frontend/static changes:

- install from `frontend/package-lock.json` if dependencies are not already
  present
- run `npm run build` in `frontend/`
- hot-swap with `go run ./cmd/glimmung-agent test-slot-hot-swap --static-only`

For Go backend changes:

- use `go run ./cmd/glimmung-agent test-slot-hot-swap --backend-only`; the
  contract owns the backend build command, artifact path, target path, and
  health path

For combined frontend/backend changes, omit `--static-only` and
`--backend-only` so the repo-local tool applies both contract sections.

Use the assigned slot name from `checkout_test_slot` as the namespace and app
selector value:

```sh
go run ./cmd/glimmung-agent test-slot-hot-swap \
  --project glimmung \
  --namespace "$SLOT_NAME" \
  --selector "app.kubernetes.io/instance=$SLOT_NAME" \
  --container glimmung \
  --health-base-url "$VALIDATION_URL" \
  --changed-files-file /tmp/glimmung-changed-files
```

Use `--static-only` or `--backend-only` on that command when the change is
limited to one artifact class. If `GLIMMUNG_BASE_URL` is not available in the
session, pass `--glimmung-base-url https://glimmung.romaine.life`.

Do not routine-hot-swap Dockerfiles, image build inputs, lockfiles, Helm chart
wiring, or launcher/runtime configuration. Those changes need the normal PR CI
image build path and, when they affect the slot runtime itself, a fresh or
repaired test slot so the generated Kubernetes state is validated.

After any manual or repo-local hot-swap command, call
`record_test_slot_hot_swap` with concise diagnostics unless the tool used
already recorded history.
