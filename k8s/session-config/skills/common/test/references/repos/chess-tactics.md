# chess-tactics

Use `project: "chess-tactics"` when reserving Glimmung test slots for this repo.

Always read the current contract first with
`get_test_slot_hot_swap_contract(project: "chess-tactics")`. Do not rely on
hardcoded paths, selectors, container names, or target dirs from this guide —
confirm them live, and never hardcode the ephemeral slot/pod names.

chess-tactics is a single-replica webapp: a Node/Express backend
(`backend/server.js`) that serves the Vite-built frontend and, when the override
dir is populated, serves that instead. The slot app workload runs in the slot
namespace, uses the slot name as **both** the `app` label value and the
container name, listens on port 3000, and has health at `/health`.

## Frontend (static) hot-swap — the common case

`apply_test_slot_hot_swap` now supports `artifact_kind: static`, but
chess-tactics' static contract isn't configured for the apply endpoint yet — it
needs `build_command`, `pod_selector`, `container`, `builder_image`, and its
`source` corrected to `frontend/dist` (see "Known gaps"). Until that's
registered on the chess-tactics Glimmung project, the interim is the manual
`kubectl` copy below. **This manual path is being retired** — raw `kubectl`
write/exec into slot pods is going away cluster-wide — so the real fix is to
register the contract and switch to the MCP tool, mirroring
`references/repos/tank-operator.md`.

Verified live (session 909): after the copy the slot served the locally-built
asset hashes (`index-*.js` / `index-*.css`) and `/design/main-menu` returned 200
with no restart.

1. Build (install from the lockfile first if you just cloned):

   ```sh
   cd frontend && npm ci && npm run build   # produces frontend/dist
   ```

2. Resolve the slot namespace, app pod, and container. The namespace and the
   `app` label value are the slot name from `checkout_test_slot` (e.g.
   `chess-tactics-1`); the container shares that name. Confirm:

   ```sh
   NS=chess-tactics-N
   kubectl -n "$NS" get pods -l "app=$NS" \
     -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.containers[*].name}{"\n"}{end}'
   ```

3. Copy `frontend/dist` -> `/var/run/chess-tactics-static-override` in every
   ready app pod (clear stale assets first):

   ```sh
   cd /workspace/chess-tactics
   for POD in $(kubectl -n "$NS" get pods -l "app=$NS" -o name); do
     POD=${POD#pod/}
     echo "=== $POD ==="
     kubectl -n "$NS" exec "$POD" -c "$NS" -- \
       sh -c 'rm -rf /var/run/chess-tactics-static-override/* 2>/dev/null; mkdir -p /var/run/chess-tactics-static-override'
     kubectl cp frontend/dist/. "$NS/$POD:/var/run/chess-tactics-static-override/" -c "$NS"
   done
   ```

   If `kubectl cp` fails because the container lacks `tar`, stream instead:

   ```sh
   tar -C frontend/dist -cf - . | \
     kubectl -n "$NS" exec -i "$POD" -c "$NS" -- \
       tar -C /var/run/chess-tactics-static-override -xf -
   ```

   Served immediately — no `SIGHUP`/restart. (`server.js` resolves the override
   `index.html` per request via `STATIC_FRONTEND_DIR`.)

4. Verify, then record:

   - Confirm the slot serves the hashed assets from your local `frontend/dist`,
     and that `/health` returns 200:

     ```sh
     curl -sk "https://$NS.tank.dev.romaine.life/" | grep -oE 'index-[A-Za-z0-9_]+\.(js|css)'
     curl -sk -o /dev/null -w '%{http_code}\n' "https://$NS.tank.dev.romaine.life/health"
     ```

   - For visual proof, `inspect_browser_url` against the slot URL (needs an
     active lease) with `save_screenshot_to_workspace=True` so a copy lands in
     `/workspace/screenshots/`.
   - `record_test_slot_hot_swap(project: "chess-tactics", operation: "hot_swap",
     status: "persisted", slot_index: N, summary: ...)`.

## Backend hot-swap

The contract's `backend` builds `backend/server.js` ->
`/var/run/chess-tactics-hot/server.js` and restarts via `kill -HUP 1` in the app
container (supervisor strategy). `apply_test_slot_hot_swap` does not cover
chess-tactics' `backend` either (no runner artifacts registered), so use the
same manual build -> `kubectl cp` -> `kill -HUP 1` pattern through the `$NS`
container. Read the live contract for the current paths.

## Do not hot-swap

Dockerfile, image build inputs, lockfiles, Helm chart wiring (`k8s/`), or
launcher/runtime config — those need the normal PR CI image build and, when they
affect slot runtime, a fresh or repaired slot.

## Known gaps

- The registered `static` contract has `source: frontend` (the raw tree), not
  `frontend/dist`. The manual recipe above copies `frontend/dist` directly and
  is unaffected, but do **not** run `glimmung-agent test-slot-hot-swap
  --static-only --project chess-tactics` (it reads the contract `source`) until
  the contract is corrected to `frontend/dist`, or it copies an unbuilt tree.
- `static` is now supported by the `apply_test_slot_hot_swap` MCP surface
  (shipped for tank-operator). To use it here, register chess-tactics' static
  contract with the apply-endpoint fields (`build_command`, `pod_selector`,
  `container`, `builder_image`) and `source: frontend/dist`. Since raw `kubectl`
  into slots is being removed cluster-wide, register the contract rather than
  leaning on the manual recipe above.
