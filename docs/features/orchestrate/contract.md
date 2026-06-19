# Orchestrate Contract

This contract translates the repo-wide policy docs into feature-specific rules
for **Orchestrate** — the LLM-in-the-loop hub-and-spoke surface where a GUI chat
session becomes the hub of a fleet of spoke sessions it spawns one slice at a
time. It is distinct from the dormant, deterministic merge-driven
"Orchestrations" DAG engine; the two are intentionally separate systems.

Named behavior, status, and evidence live in the capability ledgers:
- Session Lifecycle → "Orchestrate Hub Launch (Self-Grant + Durable Spoke Config)"
- App Chrome → "Orchestrate Wand + Routed Surface"

## Product Model

Big multi-slice tasks otherwise degrade into ship-a-slice-then-ask-the-user,
one human turn per slice. Orchestrate replaces that loop: the hub delegates each
slice to a fresh spoke session, and the spoke reports back to the **hub**, not
the user. The human is consulted once — to approve the plan — then the fleet
runs hub-to-spoke. User trust depends on two things being true: the hub really
has the git authority it claims, and a spoke's report really wakes the hub.

## Sources Of Truth

- `sessions.spoke_config jsonb` — the durable hub flag and fleet run config. Set
  only by `POST /api/sessions/{id}/orchestrate`. NULL means "not a hub" (the form
  state); non-NULL means "hub" (the status state). No pod-annotation source — hub
  state is server-owned, never runtime-reported.
- `control_action_events` rows with `action = github.break_glass.grant` and
  payload `source = orchestrate-self-grant` — the durable record of the hub's
  self-granted git authority. The agent-side git break-glass MCP reads active
  grants from here.
- The session command/event bus — the kickoff turn and every spoke ping-back
  ride the same `enqueueSDKTurn` path; the bus is a delivery mechanism, not a
  source of truth.

## Migration Rules

- Orchestrate must not be conflated with the deterministic DAG "Orchestrations"
  engine. No shared columns, routes, or types; the DAG engine is left untouched.
- The spoke config has exactly one validation choke point — the same provider
  allowlists session create uses (`validateModelArg`/`validateEffort`). No
  orchestrate-only model/mode enum may appear in the browser or the handler.
- `spoke_config` is threaded everywhere `rollout_state` is read (registry
  List/Get, `Info`, `RowPublisher`, session-list-events). A read path that knows
  `rollout_state` but not `spoke_config` is unmigrated.

## Live Behavior

- The wand button's lit state and the form↔status flip are driven by durable
  `spoke_config` arriving over SSE — never by local optimism on the POST
  response. A reload mid-run lands on the correct state from the durable row.
- A spoke's `send_prompt` to the hub session id arrives as a new turn that wakes
  the hub. The hub does not poll GUI spokes.

## Failure And Recovery

- Owner-only: service principals are rejected (`403`); ownership is the
  write-class `GetByOwner` gate, not admin-liftable. The hub must be a GUI SDK
  chat session (`400` otherwise) so ping-backs can wake it.
- The launch sequence is persist `spoke_config` → self-grant break-glass →
  enqueue kickoff. A failure after the grant is surfaced as a non-2xx with detail
  and logged; the grant is TTL-bounded and harmless if the kickoff never fired.
- The 24h grant ceiling is hard. A run longer than 24h needs a human re-confirm
  (re-POST), which appends a fresh grant. There is no renewal model.
- Session-pod death is terminal (per Session Lifecycle): a dead hub or spoke is
  not resurrected. Durability stops at the durable row + control-action ledger.
- CLI spokes are supported but second-class: they cannot ride the SDK turn
  channel, so the hub falls back to polling them (per the skill). GUI is the
  first-class ping-back path.

## Observability

- `tank_orchestrate_launch_total{result}` — launch outcomes, including the
  `service_rejected` / `not_owner` / `not_hub_mode` / `invalid_spoke_config`
  gates and the `store_error` / `grant_error` / `kickoff_error` partial-failure
  modes.
- `tank_session_run_config_rejected_total{surface="orchestrate"}` — spoke-config
  allowlist rejections.
- `tank_control_action_events_total` — the self-grant, with the durable grant
  row carrying the `orchestrate-self-grant` marker for audit.

## Acceptance Checks

- Backend: the launch endpoint's gate, validation, full-power grant shape +
  marker, persisted spoke_config, single kickoff turn, and re-confirm-appends-grant
  are unit-tested (`orchestrate_launch_test.go`); `spoke_config` round-trips the
  durable path (`spoke_config_test.go`).
- Frontend: the `orchestrate` route round-trips, the wand gates on active GUI
  sessions, the form POSTs the launch endpoint, and the panel renders status from
  a durable snapshot.
- Owed before "done": a live test-slot exercise of wand → form → confirm →
  status flip, and specifically a real spoke→hub `send_prompt` ping-back proving
  the hub wakes.
