# tank-operator

Use `project: "tank-operator"` when reserving Glimmung test slots for this
repo.

Always read the current contract first with
`get_test_slot_hot_swap_contract(project: "tank-operator")`. Do not rely on
hardcoded paths, pod selectors, container names, restart commands, or artifact
locations from this guide.

Prefer Glimmung MCP hot-swap tools when they support the artifact being tested.
Use `apply_test_slot_hot_swap` for supported artifact kinds, passing the pushed
git ref and checked-out slot. Record the result with
`record_test_slot_hot_swap` when the tool does not already do so.

## PR CI and ACR proof images

Open a draft PR early for normal tank-operator work. For same-repo PRs,
`.github/workflows/docker-build-check.yaml` logs into ACR, checks whether each
fingerprinted proof image already exists, and pushes the missing proof images.
This is the ordinary branch image-build path. The PR is not just bookkeeping:
it primes ACR while you keep using hot-swap for fast slot validation.

Keep these image paths distinct:

- PR CI proof images: build validation and ACR priming for every repo-owned
  image in `docker-build-check.yaml`.
- Hot-swap: fastest way to update an already-running Glimmung slot.
- `session-images-build.yml`: publishes the actual `claude-container` and
  `codex-container` session image tags used when a slot must stamp newly
  created sessions with branch code.

If a validation target requires a fresh session pod to boot branch runner code,
use the session-image repoint path after the branch image exists; do not confuse
that with the generic PR proof-image build.

Before treating a hot-swap as validation evidence, run the repo classifier with
the validation target you intend to prove:

```sh
node scripts/classify-tank-test-fidelity.mjs --artifact-kind <kind> --validation-target <existing_session|new_session|full_runtime> --enforce
```

Tank's backend app pods and session runner pods are one distributed runtime.
Runner hot-swap updates existing session pods only. If the classifier returns
`hot_swap_partial` or `branch_image_required`, do not cite that single hot-swap
as proof for the target; use the listed artifact hot-swaps, a future-pod runner
override, or a branch image plus a fresh-session smoke according to the result.

Use the MCP hot-swap tool for the changed artifact:

- frontend/static change: `apply_test_slot_hot_swap` with
  `artifact_kind: "static"` -- see "Frontend (static) hot-swap" below.
- runner change: `apply_test_slot_hot_swap` with the runner `artifact_kind`
  (`agent_runner` | `codex_runner`).
- ConfigMap/chart/session-launcher change: patch or redeploy the slot resource
  that actually feeds newly created pods, then create a fresh pod/session to
  verify the generated runtime state.

Raw `kubectl` writes/exec into slot pods are removed; the apply endpoint -- run
by Glimmung under its own identity, gated on pushed + CI-green code -- is the
path for artifact hot-swaps.

## Frontend (static) hot-swap

Use `apply_test_slot_hot_swap` with `artifact_kind: "static"` -- the same MCP
tool as the runner kinds. Glimmung builds the frontend from the pushed git ref
in a Job (`npm ci && npm run build` in `node:20-alpine`), clears the app pod's
static-override dir, and copies the built `frontend/dist` into every ready app
replica. Static is served live, so there is no restart.

```
apply_test_slot_hot_swap(
  project: "tank-operator",
  artifact_kind: "static",
  git_ref: "<your pushed branch HEAD>",
  validation_target: "existing_session",
  slot_name: "tank-operator-slot-N",
)
```

The registered static contract supplies the rest -- `source: frontend/dist`,
`target: /var/run/tank-operator-static-override`,
`pod_selector: app.kubernetes.io/name=tank-operator`, `container: tank-operator`,
`builder_image: node:20-alpine`. Read it live with
`get_test_slot_hot_swap_contract(project: "tank-operator")` instead of
hardcoding, and never hardcode the ephemeral slot/pod names.

The endpoint refuses a `git_ref` that isn't pushed and CI-green on an open PR --
by design, a slot only ever runs reviewable, CI-passed code. Do **not** hand-copy
assets with `kubectl cp`: raw write/exec into slot pods is removed, and copying
local un-CI'd build output is exactly what this path replaces.

Verify (history is recorded automatically):

- Confirm the served `index.html` references the hashed JS from the build:
  `curl -sk https://tank-operator-slot-N.tank.dev.romaine.life/ | grep -oE 'index-[A-Za-z0-9_]+\.js'`, and `/healthz` returns 200.
- For visual proof, `inspect_browser_url` against the slot URL (auth cookie per
  [docs/testing.md](../../../../../../../docs/testing.md)) with
  `save_screenshot_to_workspace=True` so a copy lands in `/workspace/screenshots/`.
- The apply endpoint appends a hot-swap history entry to the lease on every outcome.

Runner artifacts use the same tool (`artifact_kind` = `agent_runner` |
`codex_runner`).
