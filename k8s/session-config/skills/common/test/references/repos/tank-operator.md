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

If the MCP hot-swap tools do not cover the artifact, choose the fastest faithful
slot update for the change:

- frontend/static change: build `frontend/dist` and copy it into every app
  replica's static-override dir -- see "Frontend (static) hot-swap" below. The
  `apply_test_slot_hot_swap` MCP tool does NOT cover `static`, so this is a raw
  `kubectl` path, not an MCP call.
- backend change: build/copy/restart according to the current contract
- runner change: use the contract's runner hot-swap path or MCP tool
- ConfigMap/chart/session-launcher change: patch or redeploy the slot resource
  that actually feeds newly created pods, then create a fresh pod/session to
  verify the generated runtime state

For backend and runner artifacts, manual `kubectl` is a fallback -- prefer the
MCP/CLI paths. For the **frontend**, manual `kubectl` is the default workflow
(see below), because no MCP tool covers `static`.

## Frontend (static) hot-swap

This is the most common hot-swap and the one the MCP tools do **not** cover:
`apply_test_slot_hot_swap` handles `backend`, `agent_runner`, and
`codex_runner`. Static assets are served live from an override dir, so the verified
workflow is a raw `kubectl` copy into every app replica -- no image build, no
restart.

Verified against tank-operator slots (sessions 330 / 334 / 338). The constants
below (container name, target path, selector) are current as of this writing;
still confirm them live per the "read the contract first" rule, and never
hardcode the ephemeral slot/pod names.

1. Build (install from the lockfile first if you just cloned):

   ```sh
   cd frontend && npm ci && npm run build   # produces frontend/dist
   ```

2. Resolve the slot namespace, app pods, and app container. The namespace is the
   slot name from `checkout_test_slot` (`tank-operator-slot-N`). Write through
   the `tank-operator` container -- it mounts the target read-write
   (`TANK_OPERATOR_STATIC_OVERRIDE_DIR`). App pods may *also* carry a
   `static-writer` sidecar (slot 3 does, and some past sessions copied through
   it), but per the README the sidecar is **not required** and current guidance
   is to write through `tank-operator`. Confirm the container list before
   copying:

   ```sh
   NS=tank-operator-slot-N
   kubectl -n "$NS" get pods -l app.kubernetes.io/name=tank-operator \
     -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.containers[*].name}{"\n"}{end}'
   ```

3. Copy `frontend/dist` -> `/var/run/tank-operator-static-override` in **every**
   ready app replica (the README requires swapping all ready app pods). Clears
   stale assets first:

   ```sh
   cd /workspace/tank-operator
   for POD in $(kubectl -n "$NS" get pods -l app.kubernetes.io/name=tank-operator -o name); do
     POD=${POD#pod/}
     echo "=== $POD ==="
     kubectl -n "$NS" exec "$POD" -c tank-operator -- \
       sh -c 'rm -rf /var/run/tank-operator-static-override/* 2>/dev/null; mkdir -p /var/run/tank-operator-static-override'
     kubectl cp frontend/dist/. "$NS/$POD:/var/run/tank-operator-static-override/" -c tank-operator
   done
   ```

   If `kubectl cp` fails because the container has no `tar`, stream it instead:

   ```sh
   tar -C frontend/dist -cf - . | \
     kubectl -n "$NS" exec -i "$POD" -c tank-operator -- \
       tar -C /var/run/tank-operator-static-override -xf -
   ```

   Static is served immediately -- no `SIGHUP`/restart. (Restart-on-`SIGHUP` is
   the **backend** supervisor path, not this one.)

4. Verify, then record:

   - `kubectl -n "$NS" exec "$POD" -c tank-operator -- sh -c 'ls /var/run/tank-operator-static-override | head; wc -c /var/run/tank-operator-static-override/index.html'`
   - Confirm the served `index.html` references the hashed JS from your local
     `frontend/dist` (e.g. `index-XXXX.js`) and that `/healthz` returns 200.
   - For visual proof, `inspect_browser_url` against the slot URL (auth cookie
     per [docs/testing.md](../../../../../../../docs/testing.md)).
   - Log it: `record_test_slot_hot_swap(project: "tank-operator",
     operation: "hot_swap", status: "persisted", slot_index: N, summary: ...)`.

Backend (`/var/run/tank-operator-hot/tank-operator-go`, `SIGHUP` PID 1 in the
`tank-operator` container) and the runners DO have supported paths -- use
`apply_test_slot_hot_swap` (`artifact_kind` = `backend` | `agent_runner` |
`codex_runner`) rather than hand-rolled kubectl for those.

Gap worth closing: `static` is not a supported `apply_test_slot_hot_swap`
`artifact_kind`, which is why the frontend stays manual. If that path is added
to the Glimmung MCP surface, prefer it and update this guide.
