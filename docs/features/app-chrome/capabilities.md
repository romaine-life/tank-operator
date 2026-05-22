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
