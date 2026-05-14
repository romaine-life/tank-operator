# tank-operator design skill

Use this note before making tank-operator UI changes.

## Quick orientation

- Product: a browser UI for managing agent sessions running in Kubernetes pods.
- Posture: operator console, not marketing surface.
- Theme: dark-only, restrained, dense, technical.
- Primary design surface: `/_styleguide`.
- Design brief: `docs/design-system.md`.

## Defaults

- Prefer existing CSS tokens and component classes in `frontend/src/index.css`.
- Add or update `frontend/src/StyleguideView.tsx` whenever reusable UI changes.
- Use `lucide-react` icons for new controls unless a local provider/brand icon
  already exists.
- Keep cards scarce. Use cards for repeated items, modals, and framed tools.
- Keep text inside controls short and stable. If a label can drift or overflow,
  add a styleguide state for it.
- Validate with browser inspection or screenshots when layout is the point.

## Voice

- Lowercase `tank-operator`.
- Sentence case labels.
- Diagnostic errors.
- Concrete identifiers.
- No emoji in chrome.
- No marketing copy.

## Visual rules

- Hover can darken; active/open can lighten.
- No decorative gradients, orbs, glass blur, illustration, or hero layout.
- Use neutral surfaces and small semantic accents.
- Reserve cyan for remote-control/live-link affordances.
- Use mono only for terminal content and literal code/path snippets.

## Checklist

- Does the changed component appear in `/_styleguide`?
- Does it have active, hover-relevant, disabled, narrow, or long-label states if
  those are realistic?
- Did `npm test` and `npm run build` pass in `frontend/`?
- If testing in a Glimmung slot, did the updated `frontend/dist` get hot-swapped
  and inspected?
