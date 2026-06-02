# tank-operator design system

This document is the repo-native design brief for tank-operator. It distills the
older "Tank Operator Design System" bundle into the current app reality.

The live visual catalog is `/_styleguide`. When UI work changes a reusable
surface, update `frontend/src/StyleguideView.tsx` in the same change.

## Product posture

tank-operator is a web frontend over a Kubernetes orchestrator. The product is
the session: a browser UI that starts an agent pod, streams its work, exposes
files and settings, and keeps rollout/test state visible.

The interface should feel like an operator console:

- dark-only
- dense, scan-friendly, and technical
- calm at rest
- explicit about pod/session state
- no marketing composition, decorative illustration, or emoji in chrome

## Voice

Use terse, technical copy. Prefer concrete identifiers over generic labels:
session ids, pod names, paths, model ids, and hostnames are useful information.

Rules:

- Use `tank-operator` lowercase in body copy.
- Use sentence case for headings and controls.
- Use lowercase transient states: `loading...`, `signing in...`, `no sessions`.
- Use state nouns for persistent state labels: `Active`, `Pending`, `Failed`.
- Expose diagnostic errors directly when possible.
- Avoid apology copy, excitement, emoji, and consumer-app warmth.

## Foundations

The current tokens live in `frontend/src/index.css`. The source bundle this was
based on included `colors_and_type.css`, but the current app has moved forward:
additional modes exist, sidebar collapse exists, and run-pane header tabs exist.

Key token roles:

- `--bg-app`: main canvas.
- `--bg-sidebar`: sidebar layer.
- `--bg-sidebar-control`: launcher and row rest state.
- `--bg-sidebar-hover`: row hover, darker than rest.
- `--bg-sidebar-active`: open/active state, lighter and accented.
- `--text-primary`, `--text-body`, `--text-muted`, `--text-faint`: chrome text ladder.
- `--accent-soft`, `--accent-fg`: default provider/mode accent.
- `--cyan*`: reserved for remote-control/live-link affordances.

Typography:

- `--font-primary`: chrome labels, sidebar, buttons, cards, tabs.
- `--font-sans`: longer body copy and app text.
- `--font-mono`: terminal contents and literal inline code only.
- Default chrome/body size is 14px, not 16px.

Layout:

- Desktop sidebar width is 260px.
- Collapsed sidebar width is 56px.
- Fixed-format controls should have stable dimensions.
- Text must not overlap or resize its parent unexpectedly.
- The chat composer is part of the bounded run-pane chrome, not page content
  that disappears below the viewport. At high browser zoom or narrow effective
  widths, the transcript body must yield space, secondary composer controls
  must wrap or compact inside the composer, and the textarea must remain
  reachable without requiring the user to zoom out.

Motion:

- 120ms for hover/color.
- 180ms for small menu entrance.
- 320ms for larger panel/welcome fades.
- No bounce, scale press, glow, or decorative motion.

## Components to keep represented in `/_styleguide`

Foundations:

- neutral and semantic color swatches
- type scale
- spacing and radii

Core components:

- buttons
- new-session launcher
- provider dropdown
- status dots
- mode chips
- session rows
- run header tabs
- error pill
- onboarding/welcome card

Portfolio scenes:

- session workspace: sidebar plus active run pane
- onboarding/empty state
- narrow run header tab state

These scenes are more important than isolated atoms when the issue is layout,
label alignment, density, or visual hierarchy.

## Frontman and visual selection

Frontman is useful for point-and-select DOM context, but tank-operator should
own its agent/runtime workflow. The preferred direction is:

1. Keep `/_styleguide` as the stable design portfolio.
2. Validate relevant UI states with browser inspection and screenshots.
3. Add a small Tank-native element inspector later if click-to-show becomes
   valuable enough.

Do not adopt Frontman's LLM environment just to get DOM selection.

## Drift policy

The old design-system zip is useful as prior art, not authority. Before copying
from it, verify against current production code:

- `frontend/src/index.css`
- `frontend/src/App.tsx`
- `frontend/src/StyleguideView.tsx`
- `frontend/src/providerIcons.tsx`
- `frontend/src/sessionAvatars.tsx`

When current code and the old bundle disagree, current code wins unless the user
explicitly asks to revive an older direction.
