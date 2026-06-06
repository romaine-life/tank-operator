# App Chrome Capabilities

This ledger names top-level app chrome behavior. These entries are not a
backlog; they are stable handles for product surfaces that future agents should
recognize during planning, review, testing, and retirement.

## Help Menu

Status: active

Intent:
Expose support, documentation, diagnostics, and product guidance without
interrupting the active session.

Affected contracts:
- App Chrome
- Observability, when the menu exposes diagnostics or operator-facing state

Contract impact:
- Help actions must not mutate session state.
- Internal diagnostic/help routes must fail visibly when unavailable or
  unauthorized.
- Links and labels should remain stable enough for users and agents to find
  support paths during incidents.

Evidence:
- PRs changing Help menu behavior should verify links/actions resolve or fail
  visibly.
- PRs adding diagnostics from Help should cite the Observability contract and
  the metric, debug endpoint, log, or dashboard evidence involved.

## Settings Menu

Status: active

Intent:
Let the user inspect or modify account, application, and session preferences
from a predictable top-level surface.

Affected contracts:
- App Chrome
- Auth And Streams, when settings expose account/auth behavior
- Session Lifecycle, when settings affect session creation or runtime behavior

Contract impact:
- Product-affecting settings are durable when they are meant to survive reloads
  or apply across sessions/devices.
- Browser-local settings are intentionally local and do not masquerade as
  account or session policy.
- Mutating settings show pending, confirmed, and failure states based on the
  responsible backend or durable store.

Evidence:
- PRs adding durable settings should prove reload behavior and backend
  confirmation.
- PRs adding browser-local settings should state the local-only scope.
- PRs touching account/auth settings should cite Auth And Streams evidence.

## Shells Menu

Status: active

Intent:
Let the user discover, open, switch, or manage shell-oriented surfaces without
losing orientation in the active session.

Affected contracts:
- App Chrome
- Session Lifecycle, when shell availability follows session or pod state
- Agent Runners, terminal, or a future Shells contract when shell process
  lifecycle becomes its own contracted feature

Contract impact:
- Shell availability reflects current session and runtime state without
  requiring refresh.
- Shell open/switch/manage actions do not appear successful before the
  underlying surface confirms attachment or availability.
- Shell menu state must not contradict terminal/session lifecycle state.
- If Shells grows independent attach/detach, tab, process, or reconnect
  semantics, split it into its own feature contract.

Evidence:
- PRs changing Shells should prove current availability is reflected without
  refresh.
- PRs adding shell lifecycle actions should prove confirmed/failure states from
  the backend or terminal surface.
- PRs expanding shell process semantics should either update this ledger or add
  a dedicated Shells contract.

## Run Header Overflow Menu

Status: active

Intent:
Keep the run header a clean "title + ⋮" strip by collapsing every top-right
session control — the view tabs (Turns, Background, Files) and the auxiliary
actions (Settings, Help) — behind a single vertical overflow control instead of
a row of competing buttons.

Affected contracts:
- App Chrome
- Transcript Navigation, because the menu is the entry point to the Turns and
  Background side-pane views
- Observability, because the trigger carries the admin observability attention
  dot

Contract impact:
- The menu is the single entry point for these actions in the normal view; the
  old standalone Turns/Background/Files/Settings/Help tab buttons do not survive
  alongside it (no duplicate control paths).
- Availability gating stays on the row, not on header chrome: Turns is disabled
  until the agent has turn activity, Files until the session container is
  available. Disabled rows stay visible and non-selectable, never hidden.
- The collapse does not hide ambient state: live Turns/Background counts ride
  their rows, and a "needs attention" dot stays on the closed trigger so admin
  observability state is visible without opening the menu.
- The read-only public message-link view renders no overflow menu; it keeps the
  standalone Turns tab, which is that view's only session surface.

Evidence:
- PRs changing this surface should verify the header renders a single overflow
  trigger in the normal view, that every former tab is reachable inside the menu
  with its availability gating intact, and that the public view still exposes
  Turns.
- `frontend/src/migrationPolicy.test.ts` pins the menu-based
  Turns/Background/Files structure and the public-view standalone Turns tab.

## Session Connection Indicator

Status: active

Intent:
Surface only user-relevant live-stream degradation in the run title chrome,
without turning routine SSE setup after tab/session reactivation into a warning
the user learns to ignore.

Affected contracts:
- App Chrome
- Auth And Streams
- Transcript

Contract impact:
- Raw stream lifecycle remains telemetry-owned: open, ready, close, retry,
  resync, and stream-error events stay observable even when they do not render
  app chrome.
- Routine `connecting` is telemetry-only until it outlasts the short display
  threshold. The title pill then reads `reconnecting`, matching the user's
  visible situation rather than the implementation's initial handshake.
- `connection lost` and `resyncing` remain immediately visible because they
  affect trust in whether the live tail is current.
- The indicator is scoped to the visible chat pane and remains outside the
  transcript/composer flow, so reconnect, resync, and retry state cannot move
  transcript content or steal input focus.

Evidence:
- `frontend/src/sessionConnectionIndicator.ts` owns the pure display policy;
  `frontend/src/sessionConnectionIndicator.test.ts` proves routine connecting
  is suppressed below threshold, slow reconnect/failure/resync are visible, and
  labels do not bleed onto hidden or non-chat panes.
- `frontend/src/sessionEventStreamTelemetry.ts` continues to emit the raw
  browser stream events consumed by the Auth And Streams and Transcript
  observability contracts.
- `frontend/src/migrationPolicy.test.ts` pins that the pill remains in title
  chrome rather than the transcript/composer flow.

## Cluster Health Sidebar Surface

Status: active

Intent:
Expose cluster-level causes of Tank instability directly in the persistent
sidebar: Kubernetes node readiness/pressure, Tank session pod readiness, and
NATS JetStream pressure/quorum risk.

Affected contracts:
- App Chrome
- Observability
- Session Lifecycle, when session pod readiness explains launch/runtime failures

Contract impact:
- The surface reads live backend-owned health state, not browser-local
  inference.
- Failure to load health is visible in the sidebar and retryable without
  devtools.
- NATS health includes both transport reachability and JetStream pressure
  signals so the 2026-05-25 "publish dies when NATS stalls" shape is visible
  before a user has to infer it from failed turns.
- JetStream stream health distinguishes configured replicas from current
  replicas. The sidebar must not treat the server-local `/jsz`
  `cluster.replicas` array length as the configured stream count, because that
  array omits the local raft participant and can make a healthy stream look
  like `2/3`.

Evidence:
- PRs changing this surface should verify `GET /api/cluster-health`, the
  sidebar render path, and Helm RBAC/env wiring.
- PRs adding or removing health dimensions should cite the Observability
  contract and explain which failure mode remains visible.

## Avatar Admin Console

Status: active

Intent:
Let administrators curate the durable avatar catalog used by app chrome and
transcript surfaces from the Settings -> Admin pane without relying on
automatic face detection or code edits.

Affected contracts:
- App Chrome
- Auth And Streams, because avatar image reads and writes are authenticated
- Observability, because admin mutations affect user-visible identity surfaces

Contract impact:
- Avatar additions, deletions, and kind reassignments (agent <-> system) are
  confirmed by the backend durable store.
- Uploaded backing photos are not exposed as unauthenticated static assets.
- Non-admin callers can read the active catalog for rendering but cannot mutate
  it.
- A kind reassignment is atomic with cleanup of the avatar's unused entries in
  the old kind's per-owner deck cycles; used entries stay as a historical
  record of which avatar was drawn for which session.
- Failure states for auth, upload validation, image reads, deletes, and kind
  reassignments are visible in the Settings -> Admin avatar pane.

Evidence:
- PRs changing avatar admin behavior should verify the Settings -> Admin pane,
  admin-only writes, authenticated image reads, and reload-safe catalog
  rendering.
- PRs changing avatar storage should cite the migration and bounded metric
  evidence for avatar create/read/delete/update_kind outcomes.
- PRs touching kind reassignment should prove the unused-deck-entry cleanup
  is atomic with the kind flip and that used entries are preserved.

## Admin Useful Files

Status: active

Intent:
Give administrators one-click access, from the "Useful files" list in the
Settings -> Admin controls pane, to the canonical session-config and policy
documents that govern how session pods behave: the default session primer, the
quality/migration/product policy docs, the whole session-config bundle, and the
repo developer guide.

Affected contracts:
- App Chrome

Contract impact:
- These links are content references, not product state. The canonical list is a
  typed frontend module (`frontend/src/adminReferenceLinks.ts`), not a durable
  store, consistent with this contract's rule that external documentation URLs
  are content references.
- The section is admin-only: it renders inside the Settings -> Admin controls
  view, which is gated on `is_admin` (`adminControls.visible`).
- Links open canonical sources on the repository's default branch in a new tab.
  They are external navigations, not durable actions, so they carry no
  outcome telemetry; if a future entry points at an internal/authenticated
  route, it must adopt this contract's "fail visibly" rule and the matching
  telemetry.

Evidence:
- `frontend/src/adminReferenceLinks.test.ts` pins the curated set, asserts every
  href is an absolute https URL on the tank-operator repo, and forbids
  duplicate ids/hrefs.
- PRs changing the list should keep that test green and update this entry if the
  set's intent changes.

## Mobile Session Triage (Compact Shell)

Status: active

Intent:
Make the session product usable on a phone for *triage* — list sessions, read a
transcript, send a turn, answer an AskUserQuestion, and stop a run — without
reproducing the desktop multi-pane operator console. At <= BP_COMPACT (768px)
the persistent 260px sidebar moves into an off-canvas navigation drawer, a
compact top bar carries the drawer trigger plus current-session context, and the
work pane takes the full width.

Affected contracts:
- App Chrome
- Session Bar / Transcript Navigation, because the drawer is the compact entry
  point to session switching and the run-pane chrome reflows beneath the top bar
- Session Lifecycle, because session rows still surface live pod/turn status in
  the drawer and top bar

Contract impact:
- Compact vs. desktop shell is browser UI state derived from one source
  (`useViewport()` over `frontend/src/breakpoints.ts`), not durable product
  state; it resets on reload by design and never overrides server-owned
  session/auth state.
- The sidebar has a single source of truth: the same `sidebarBody` fragment
  renders inline on desktop and inside the drawer on compact. No parallel mobile
  scaffold, no duplicate session-list implementation.
- The drawer is a vetted radix Dialog (focus trap, scroll lock, Escape, outside
  dismiss, aria-modal). It closes on every navigation and when the viewport grows
  back to desktop, so it cannot strand focus or scroll-lock — satisfying the App
  Chrome rule that chrome "open/close predictably, preserve focus."
- Touch parity for triage-critical affordances: the session delete/close control
  is visible (not hover-only) in the drawer; reorder-by-drag is a deliberate
  desktop-only enhancement and the row is non-draggable on compact, so there is
  no dead gesture on touch. The persisted order is still honored on mobile.
- Genuinely desktop-only surfaces (terminal attach, the drag/crop avatar editor)
  render an honest `DesktopOnly` "open on a larger screen" card on compact rather
  than a broken surface. This is a stated product boundary, not a fallback path.

Evidence:
- `frontend/src/breakpoints.test.ts` pins the canonical breakpoints and the
  derived media queries so JS and CSS cannot drift.
- `frontend/src/useViewport.test.ts` proves the matchMedia adapter (modern +
  legacy listener APIs; SSR/no-matchMedia defaults to the desktop shell).
- `frontend/src/mobileShell.test.ts` pins the compact shell CSS (single column +
  top-bar row), the BP_COMPACT/CSS alignment, the drawer/top-bar/desktop-only
  wiring, the non-draggable-on-compact invariant, and the visible delete
  affordance.
- Owed before "done" by docs/quality-timeframes.md: real-device validation (iOS
  Safari + Android Chrome) of the sign-in bounce, drawer focus/scroll-lock
  behavior, and the keyboard-aware composer at 390/768px. This is named scope,
  not optional robustness.
