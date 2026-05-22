# App Chrome Contract

This contract applies to the persistent application chrome around the active
work surface: top bar controls, top-right menus, account/help/settings entry
points, shell-discovery entry points, and global actions that remain visible
while the user works inside a session.

## Product Model

App chrome is the stable frame around Tank's session product. It should help
the user orient, inspect account or environment state, and reach global
actions without interrupting the active session or hiding product-critical
state behind ambiguous controls.

Top-level controls are product commitments. If a button or menu can mutate
durable state, attach to a live surface, or expose operational diagnostics, it
must behave like the rest of Tank: explicit state, durable confirmation where
needed, visible failure, and no browser-only source of truth for product
behavior.

## Sources Of Truth

- Auth state comes from auth.romaine.life JWT validation and `/api/auth/me`.
- Durable user or account settings live in server-owned storage, not only
  browser memory, when they affect product behavior across reloads, devices, or
  sessions.
- Session and shell availability come from session registry, pod/session
  lifecycle, and the relevant terminal or runner surfaces.
- External help/documentation URLs are content references; they are not product
  state.
- Menu open/closed state, hover state, and focus state are browser UI state
  only.

## Migration Rules

- Do not preserve old top-level controls, menu entries, or keyboard paths after
  a global action moves.
- Do not hide critical session, auth, or lifecycle state only inside a menu.
- Do not store product-affecting settings only in `localStorage` unless the
  setting is explicitly scoped as browser-local.
- Do not let multiple menu entries trigger subtly different implementations of
  the same global action.
- Do not add a top-level button for a complex behavior without naming the
  capability in this feature area's ledger or a more specific feature ledger.

## Live Behavior

- Top-right menus open predictably, close predictably, preserve focus, and do
  not steal focus from active transcript or terminal input except during direct
  user interaction.
- Menu contents reflect current auth, session, and shell availability without
  requiring a page refresh.
- Actions that mutate durable state show pending, success, and failure states
  based on backend confirmation.
- Links to help, diagnostics, account, and settings surfaces fail visibly when
  unavailable.
- App chrome should remain visually stable when live session state changes; it
  should not cause transcript or terminal layout jumps unrelated to the user's
  action.

## Failure And Recovery

- Browser reload reconstructs app chrome from durable auth/session/settings
  state and known route state.
- Stale auth must produce explicit auth recovery or signed-out state, not
  broken menus.
- If shell or session availability cannot be loaded, the shell/menu affordance
  should show unavailable or retryable state instead of acting on stale local
  assumptions.
- External help links may fail outside Tank's control, but internal help and
  diagnostic actions should report route or authorization failures clearly.

## Observability

- User-trust actions launched from app chrome need the same outcome telemetry
  as the underlying feature they call.
- Settings changes that affect product behavior should be observable as
  requested, confirmed, failed, or reverted.
- Shell menu actions should emit enough telemetry to distinguish unavailable
  shell surface, stale session state, auth failure, and frontend display lag.
- A report that a top-right menu showed stale or contradictory state should be
  diagnosable from durable auth/session/settings state plus client telemetry,
  not only browser devtools.

## Acceptance Checks

- Help, Settings, and Shells menu entries are named in
  [capabilities.md](capabilities.md) while they remain top-level product
  surfaces.
- Menu open/close behavior does not disrupt active transcript or terminal
  input outside direct user interaction.
- Product-affecting settings persist through reload when they are intended to
  be durable, and browser-local settings are labeled as local-only in the
  implementation/docs.
- Shells menu state reflects current session/shell availability without
  refresh.
- Any app-chrome action that delegates to another contracted feature cites that
  feature's evidence in the PR body.
