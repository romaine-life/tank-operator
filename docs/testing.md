# Testing tank-operator

The repo tests at three layers, cheapest first. Pick the lowest layer that can
actually prove the behavior:

1. **Pure logic** — extract a decision into a side-effect-free module and unit
   test it. Most of `frontend/src/*.ts` (`appRoutes`, `navigationMode`,
   `breadcrumb`, `conversation*`, `turnActivityPager`) and the bulk of
   `backend-go` live here. Fast, deterministic, the default.
2. **Component / interaction** — render a real React component in jsdom and
   drive it the way a user would (click, type, tab) to prove *does clicking
   actually navigate; does the right thing render for these props/state*. This
   is the [Frontend test layers](#frontend-test-layers) section below.
3. **Real browser / integration** — a Glimmung test slot driven by
   `inspect_browser_url`, for visual accuracy, real auth, and full
   cross-service flows. Everything from [Glimmung Test Slots](#glimmung-test-slots)
   down is this layer.

A render/interaction test is not a substitute for the slot tier (it can't prove
real layout, real auth, or a real NATS round-trip) and the slot tier is not a
substitute for it (it's slow, scarce, and a bad place to enumerate every
prop/state branch). Use both for what each is good at.

## Frontend test layers

The frontend test runner is **Vitest**, invoked by `npm test` (one-shot, used by
CI) and `npm run test:watch` (watch mode). There is exactly one runner — the
previous `tsx --test` / `node:test` pure-logic suite was migrated onto Vitest so
there is no second runner, config, or assertion dialect to keep in sync.

Vitest is configured in `frontend/vitest.config.ts` with two projects split by
file extension, so the boundary is self-documenting and the wrong environment
cannot leak in:

| File | Project | Environment | For |
|---|---|---|---|
| `*.test.ts`  | `unit` | `node`  | pure logic — no DOM |
| `*.test.tsx` | `dom`  | `jsdom` | component / interaction — React + DOM |

Vitest reuses the project's Vite resolution, so the `@/` alias and the React
plugin match the app build — there is no separate transform pipeline. The
`dom` project loads `frontend/vitest.setup.ts`, which registers
`@testing-library/jest-dom` matchers and an `afterEach(cleanup)` so DOM state
never leaks across tests.

### Why this stack (and not Vitest browser mode)

- **Vitest + @testing-library/react + @testing-library/user-event + jsdom** is
  the Vite-native fit. RTL drives the testing-trophy idea: assert on what a user
  perceives (accessible roles/names, rendered text), not implementation detail.
- **jsdom, not Vitest browser mode.** Browser mode (real Chromium via Playwright)
  buys real layout/CSS and true focus/clipboard accuracy, but at the cost of
  browser binaries in CI and slower, flakier runs. This repo already owns the
  real-browser tier — Glimmung slots + `inspect_browser_url` — and
  `product-inspirations.md` names a live styleguide route + per-change
  environment as the *visual* review surface. So jsdom covers interaction
  *logic* here, and real-browser accuracy stays in the slot tier. Reconsider
  browser mode only for a specific behavior jsdom genuinely cannot model (e.g.
  real focus-ring/measurement-dependent logic); add it as a third Vitest project
  rather than swapping jsdom out.

### Conventions

- **Co-locate** tests next to the source (`Foo.tsx` → `Foo.test.tsx`). Extension
  picks the environment; don't fight it.
- **Query by accessibility, in priority order:** `getByRole` (with a `name`) >
  `getByLabelText` > `getByText` / `getByTitle` > `getByTestId` (last resort).
  If a role query is awkward, that's usually a hint the markup needs a real
  label, which helps real users too. Never assert on classnames.
- **`userEvent` over `fireEvent`.** `userEvent.setup()` simulates real user
  input (focus, key sequences, a working clipboard). `fireEvent` is the
  deliberate exception for two narrow cases the exemplars show:
  (a) **timer-isolation** tests under fake timers, where user-event's async
  pipeline fights faked time (`CopyButton.test.tsx`); and (b) needing precise
  **event-init / dispatch-result** control — the modifier/middle-click matrix
  and reading whether `preventDefault` fired (`TurnViewButton.test.tsx`).
- **Assert observable outcomes.** A successful copy is proven by reading the
  clipboard back and the accessible name flipping to "Copied", not by spying on
  a private call. A failure must render a *visible* affordance, not be swallowed.
- **Async:** prefer `await screen.findBy…` / `await user.…`; don't poke at
  component state. **Fake timers** only for genuinely time-based behavior, and
  pass user-event the bridge: `userEvent.setup({ advanceTimers: vi.advanceTimersByTime })`.

### Rendering components that live in `App.tsx`

`frontend/src/App.tsx` is a large module; most components are local. To test one,
**export it** (a minimal, intentional `export function`) — see `CopyButton`,
`LinkButton`, `TurnViewButton`. For a component that reads app context, also
export the context (`RunContext` is exported) and wrap the component in a real
provider with injected fakes; `LinkButton.test.tsx` shows a reusable
`renderWithRunContext` helper.

### Testing navigation (the breadcrumb pattern)

Tank's SPA navigates by `history.pushState` + a synthetic `popstate` that each
visible pane's route listener resolves (`applyCurrentSessionRoute` /
`applyCurrentHomeRoute` in `App.tsx`). Two halves are worth testing separately:

- **A control navigates in-app.** A plain left click on a link-style control
  should call the in-app navigate (assert the callback / resulting route) *and*
  suppress the browser's full-page navigation (`preventDefault`), while
  ⌘/Ctrl/Shift/Alt/middle clicks are left entirely to the browser so
  open-in-new-tab still works. Render real `href`s so links stay deep-linkable.
  `TurnViewButton.test.tsx` is the worked example and the template a breadcrumb
  crumb should follow.
- **A pane re-resolves on `popstate`.** To prove a pane re-renders the right
  trail/route when history changes, render it, call
  `window.history.pushState({}, "", "/sessions/42/turns/7")`, dispatch
  `window.dispatchEvent(new PopStateEvent("popstate"))`, and assert the pane now
  shows the new route — exercising the same listener real navigation drives.

### Exemplars

`CopyButton.test.tsx`, `TurnViewButton.test.tsx`, and `LinkButton.test.tsx` are
seeded as the canonical patterns (stateful async + fake timers; navigation +
modifier-key semantics; context injection + success/error paths). Read them
before adding a new component test; copy their shape.

### CI

`npm test --prefix frontend` runs in `.github/workflows/conversation-contract.yml`
on any `frontend/src/**` change, and `npm ci` there installs the test devDeps —
so this whole layer runs in CI with no extra workflow wiring.

## Glimmung Test Slots

Tank-operator test slots are provisioned by Glimmung. Before relying on
hardcoded slot paths or pod names, check the current slot lease and deploy the
CI-built image for pushed refs with Glimmung `deploy_image_to_test_slot`.

For `/test-drive`, user-visible Tank UI behavior must be proven in a real
session created inside the checked-out test slot. Use `spawn_test_slot_session`
so the slot orchestrator owns session creation, then drive the actual slot UI
with browser automation and record the session URL plus a screenshot or trace.
This is mandatory for transcript, Turns, composer, session-list, session-bar,
auth/onboarding, and other browser-visible flows where the product surface is a
Tank session. Static fixtures, styleguide pages, direct CSS checks, and API
reads are useful supplemental evidence, but they do not satisfy `/test-drive`
for those flows by themselves. If a real slot session cannot be driven, record
the exact blocker (for example runner crash, auth failure, unavailable browser,
or missing data) in the PR evidence and still run the best lower-level checks
available.

Slot hostnames such as `https://tank-operator-slot-N.tank.dev.romaine.life`
are trusted auth origins through Glimmung-managed auth origins, not through a
static auth.romaine.life allowlist.

Those slot HTTPRoutes use concrete hostnames, but they attach to the shared
`tank-operator-wildcard` listener in the `tank-operator` namespace. That
single listener/certificate covers `tank.dev.romaine.life` and
`*.tank.dev.romaine.life`; slots must not create their own public cert-manager
`Certificate` or public `XListenerSet`.

## Test-Slot SPA Auth

Session pods authenticate as service principals through the projected
Kubernetes service-account token and auth.romaine.life's
`/api/auth/exchange/k8s` flow. Those tokens carry `role=service` and an
`actor_email` claim for the human owner. The SPA treats service principals as
authenticated platform callers and does not require a user-facing GitHub App
installation; the OnboardingWall is skipped for `role=service`. Do not install
the GitHub App for a service account just to run browser automation.

`role` is still the auth.romaine.life platform identity. Tank's local admin
decision is `/api/auth/me.is_admin`: a service-principal token owned by a
configured super admin keeps `role=service` and returns `is_admin=true`, so
admin browser automation sees the same Settings/Admin surfaces as that human
owner without mutating the upstream role claim.

End-to-end exchange from a session pod:

```sh
SA=$(cat /var/run/secrets/auth.romaine.life/token)
AUTH_JWT=$(curl -sS -X POST https://auth.romaine.life/api/auth/exchange/k8s \
  -H "Authorization: Bearer $SA" -H 'Content-Type: application/json' -d '{}' \
  | jq -r .token)                                       # role=service + actor_email
curl -sS https://tank-operator-slot-1.tank.dev.romaine.life/api/auth/me \
  -H "Authorization: Bearer $AUTH_JWT"                  # 200, role=service, is_admin mirrors actor
```

The same auth.romaine.life JWT powers authenticated browser automation against
slot URLs.

## Authenticated browser automation via inspect_browser_url

`inspect_browser_url` (in [`mcp-glimmung`](https://github.com/romaine-life/mcp-glimmung))
drives the slot's `slot-playwright` pod against a URL. The Playwright pod
itself holds no credentials, so anything signed-in has to come from the
caller. The tool exposes injection knobs that map directly to Playwright's
`BrowserContext` configuration:

| Param | Forwarded to | Use |
|---|---|---|
| `extra_http_headers` | `context.setExtraHTTPHeaders(headers)` | `Authorization: Bearer ...` on slot URLs that hit JSON APIs |
| `local_storage` | `addInitScript` running before every page script | SPAs that boot from `localStorage[auth-romaine-jwt]` |

Recommended pattern for the chat UI: mint the auth.romaine.life service token
above, then seed it into the SPA's localStorage. Playwright lands on the slot
URL already signed in as the service principal, and the SPA's bootstrap path
validates the token via `/api/auth/me`. Admin-only panes are available when
that response carries `is_admin=true`.

```python
inspect_browser_url(
    url="https://tank-operator-slot-1.tank.dev.romaine.life/",
    tank_session_id="<your session id>",
    local_storage={
        "https://tank-operator-slot-1.tank.dev.romaine.life": {
            "auth-romaine-jwt": AUTH_JWT,
        },
    },
)
```

This is the production-correct path. Do not work around an old "stub
`/api/auth/me` in Playwright" pattern; the backend bypass for `role=service`
is live, `is_admin` carries Tank's local admin-power decision, and the
inspector now plumbs localStorage through, so the real auth path is always
available.

## PR CI and ACR proof images

Open a draft PR early when validating branch work. The PR is not only a review
surface: `.github/workflows/docker-build-check.yaml` is the normal image-build
gate for branches. For normal same-repo PRs it logs into ACR, computes each
repo-owned image fingerprint, reuses an existing proof image when present, and
pushes the missing proof image to ACR.

Authenticated image-build workflows use the ACR registry cache as the canonical
BuildKit cache. Do not add a second `type=gha,mode=max` export to those paths:
large session-image layers make GitHub Actions cache export dominate the job
after the proof image is already built and pushed. Fork PRs that cannot log into
ACR may still use GHA cache because it is their only cache backend.

That proof-image path is the input for slot deploys:

- Use Glimmung `deploy_image_to_test_slot` to deploy the CI-built image for a
  pushed ref into a checked-out slot. Glimmung resolves the ref to the
  fingerprint tag CI produced; the registry contract is not a commit-SHA image
  alias.
- Use PR CI proof images to prove buildability and prime ACR for slot
  validation and merge/deploy.
- Use `session-images-build.yml` when newly-created Tank session pods must boot
  branch `claude-container` / `codex-container` images.

## Making new slot sessions inherit a change (session-image repoint)

Newly-created sessions boot the image their orchestrator stamps. To make new
sessions inherit a branch change, point the slot's session image at the branch
build — the same lever production uses
(`CODEX_SESSION_IMAGE` / `SESSION_IMAGE`), not a runtime overlay. New sessions
then boot the branch code natively, exactly like prod boots its pinned image.

Two steps:

1. **Build the branch session image.** Session images are fingerprint-tagged
   from their build inputs by `.github/workflows/session-images-build.yml`.
   Compute the tag and, if it isn't already in ACR, dispatch the build at your
   branch (the workflow pushes the fingerprint tag on `workflow_dispatch`):

   ```sh
   # the codex-container tag your branch will produce
   scripts/image-fingerprint.sh --image codex --dockerfile claude-container/Dockerfile \
     --context . --paths 'claude-container/Dockerfile .dockerignore claude-container/mcp-auth-proxy claude-runner codex-runner runner-shared'
   # build it for the branch (no-op/cache hit if the fingerprint already exists)
   mcp__github__.dispatch_workflow(
     owner="romaine-life",
     name="tank-operator",
     workflow="session-images-build.yml",
     ref="<branch>",
     inputs={"git_ref":"<branch>"},
   )
   ```

   From a session, use the GitHub MCP `dispatch_workflow` tool for this step.
   A token minted by `mint_clone_token(workflows=True)` is for pushing edits to
   `.github/workflows/*`; it does not grant GitHub Actions workflow dispatch
   permission.

2. **Point the slot at it.** Set the durable per-scope override on the slot's
   orchestrator (the scope is the slot name, e.g. `tank-operator-slot-1`). New
   sessions in that slot then stamp the override image:

   ```sh
   curl -X PUT \
     https://tank-operator-slot-1.tank.dev.romaine.life/api/internal/session-scopes/tank-operator-slot-1/image-override \
     -H "Authorization: Bearer $AUTH_JWT" -H 'Content-Type: application/json' \
   ```

   `GET` the same path to see what new sessions will inherit; `DELETE` it to
   revert to the chart-pinned image. `PUT` replaces the scoped override row, so
   include every image family you want to keep pointed at branch images. The
   override is durable (survives the slot orchestrator restarting), refuses the
   production scope, and is honored only by test-env orchestrators — production
   sessions are never repointed.

For app-level validation, prefer Glimmung `deploy_image_to_test_slot`. For
session-image validation, build the session image and repoint the slot so newly
created sessions boot the branch image.
