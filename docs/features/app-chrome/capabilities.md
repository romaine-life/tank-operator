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
