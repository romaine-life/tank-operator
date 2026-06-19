# Session Bar Capabilities

Named behaviors in the session-bar surface. See
[contract.md](contract.md) for the durable invariants and
[../README.md](../README.md) for how capability ledgers are used.

## session-repo-selection

- **Status:** shipped
- **Intent:** Persist the `owner/name` GitHub repo slugs the user selected at
  session creation so existing sessions, recent-repo shortcuts, clone state,
  and reporting all read the same durable source.
- **Durable source:** `sessions.repos text[]`, written once during session
  creation after handler-side slug validation and mode gating.
- **Runtime behavior:** for workspace-backed SDK sessions, the `repo-cloner`
  init container clones the selected repos into `/workspace` and writes
  per-repo outcomes to `sessions.clone_state jsonb`.
- **Non-goal:** Tank does not discover later ad-hoc clones by polling the
  workspace. If later-cloned repo attribution becomes necessary, route it
  through an explicit MCP-owned clone/report path instead of workspace scans.
- **Observability:** `tank_session_repos_selected_total{count_bucket}` counts
  session creates by selected-repo bucket (`none`, `one`, `many`).

## profile-backed-repo-pins

- **Status:** shipped
- **Intent:** Make the splash repo picker's explicit pinned-repository list a
  durable per-user preference instead of browser-local state. Pins are
  shortcuts across sessions and devices; create-time repo selection remains
  `sessions.repos`. Pin *order* is user-controlled: the `pinned_repos text[]`
  order is the canonical pin order and the splash numbered-shortcut order, so
  rearranging pins is a durable preference, not a view-local sort.

- **Affected contracts:** Session Bar. The picker is part of the session
  creation surface and must not show a pin state that only exists in one
  browser's `localStorage`.

- **Mechanism:**
  - Column: `profiles.pinned_repos text[] NOT NULL DEFAULT '{}'`.
  - Read path: `/api/auth/me` includes `pinned_repos`; `GET
    /api/github/pinned-repos` returns the same durable list. Browser callers
    read their own profile row; service-principal callers read the
    `actor_email` owner row through the same `OwnerEmail()` boundary used by
    session-owned state.
  - Write path: `PUT /api/github/pinned-repos` validates the shared
    `owner/name` slug contract, dedups case-insensitively, caps the metadata
    list at 64 entries, replaces the caller owner's profile row value, and
    publishes a per-owner low-latency wake after the durable write succeeds.
  - Live path: `GET /api/github/pinned-repos/events` is a browser-native SSE
    stream authenticated through the same short-lived `stream_ticket` boundary
    as session streams. The stream subscribes first, then emits a durable
    `profiles.pinned_repos` snapshot; each NATS wake causes another profile
    snapshot read. The wake payload carries no state.
  - UI: the picker initializes from the authenticated profile, refreshes from
    `GET /api/github/pinned-repos` at authenticated boot, picker open, window
    focus, and visible-tab return, and applies both PUT responses and SSE
    snapshots through the same normalization path. The retired
    `tank.homePinnedRepos` localStorage key is not allowlisted on boot.
  - Reorder: pin order is user-controlled by drag-and-drop on two surfaces —
    the always-visible numbered "Pinned" shortcut chips and the full "Pinned"
    list in the picker panel (the panel also supports keyboard reorder via a
    per-row grip handle + ArrowUp/ArrowDown). Both surfaces share one drag
    implementation and reorder the same durable list. A reorder issues the same
    `PUT /api/github/pinned-repos` write as pin/unpin with the reordered list —
    there is no separate order field and no order-only endpoint. The SPA applies the new order optimistically for
    drag responsiveness, reconciles against the authoritative PUT response, and
    reverts to the pre-drag order if the durable write fails, so local order
    never persistently contradicts the profile row. Correctness depends on the
    durable write preserving array order through `validatePinnedRepoSlugs` and
    `UpdatePinnedRepos`, which is guarded by a backend test.

- **Observability:**
  - `tank_github_pinned_repos_update_total{result=ok|invalid|unavailable|error}`
    distinguishes contract drift, profile-store availability, and database
    write failures.
  - `tank_github_pinned_repos_publish_total{result=ok|error}` surfaces NATS
    publish failures after successful durable writes.
  - `tank_github_pinned_repos_stream_open_total`,
    `tank_github_pinned_repos_stream_emit_total`,
    `tank_github_pinned_repos_stream_heartbeat_total`, and
    `tank_github_pinned_repos_stream_error_total{reason}` cover the browser
    stream's open/snapshot/keepalive/error behavior with bounded labels.

- **Evidence:**
  - `cmd/tank-operator` `TestValidatePinnedRepoSlugs` covers validation,
    deduping, the distinct pin cap, and rejection of malformed slugs.
  - `cmd/tank-operator` pinned-repos handler tests cover owner-profile wake
    publish and the stream's initial durable owner snapshot.
  - `auth.ts` stream-ticket tests cover the `pinned-repos` stream kind without
    a session id.
  - `migrationPolicy.test.ts` guards the EventSource stream and focus/visible
    durable refresh paths.
  - `main.tsx does not allowlist retired local repo pins` prevents the
    browser-local key from being treated as live state again.
  - `repos.ts` `pinnedRepoSlugs caps profile metadata` keeps the SPA cap in
    lockstep with the backend metadata boundary.
  - `repos.ts` `reorderPinnedRepoSlugs` tests pin the drag/keyboard reorder
    semantics (direction-aware insert, both ends reachable, adjacent keyboard
    steps, case-insensitive match, normalized no-op for unknown/equal slugs).
  - `cmd/tank-operator` `TestValidatePinnedRepoSlugs` "reorder is preserved
    through validation" guards the array-order preservation the SPA reorder
    relies on end-to-end.
  - `migrationPolicy.test.ts` "pin reorder writes through the durable
    pinned-repos endpoint" pins the reorder to the durable PUT path and forbids
    a browser-local order shadow.

## splash-repo-selection-gesture

- **Status:** shipped
- **Intent:** Give the splash repo picker a single-select-by-default,
  multi-select-on-intent model so a user can pick one repo fast or build a set
  deliberately, matching how OS file lists behave. A bare number shortcut (1-9)
  or a plain click on a repo name selects that repo *exclusively* — the staged
  set becomes exactly that repo. Shift+number, Shift-click, and an explicit "+"
  affordance on each chip are *additive* — the repo joins the current selection.
  This supersedes the prior additive-only behavior where a number/click always
  appended and an already-staged suggestion was a dimmed, disabled no-op.

- **Affected contracts:** Session Bar. The picker is part of the session
  creation surface; this entry governs how a gesture maps to the staged
  selection, not how the selection is persisted.

- **Mechanism:**
  - The exclusive/additive decision is a pure function,
    `applyRepoSelection(current, slug, mode)` in `frontend/src/repos.ts`.
    Exclusive returns `[slug]`; additive unions `slug` into `current`. Additive
    is idempotent (re-adding a staged repo is a no-op success, never an error)
    and stays bounded by `MAX_REPOS_PER_SESSION` (5); exclusive cannot exceed
    the cap because its result is a single slug.
  - The number-shortcut handler in `App.tsx` matches `event.code`
    (`Digit1..9` / `Numpad1..9`) instead of `event.key`, so `Shift+1` — which
    reports key `"!"` — still resolves to shortcut 1 in additive mode. Shift is
    no longer in the bail-out guard; Alt/Ctrl/Meta still are.
  - `RepoPicker.tsx` maps a plain click to exclusive and a Shift-click to
    additive via `repoSelectModeFromEvent`; the per-chip `AdditiveAddButton`
    ("+") is always additive. A staged repo stays clickable (a plain click
    narrows the selection back down to just it) and reads as selected (accent
    fill + `aria-pressed`) instead of a disabled no-op; its "+" is disabled
    because additive-adding a staged repo is a no-op.
  - The manual typed-entry Add button keeps `addRepoSlug` (additive with a
    duplicate error) so a typed duplicate is still surfaced rather than silently
    swallowed; it is a deliberately distinct path from the gesture `onSelect`.
  - This is client gesture mapping only: the durable create path
    (`sessions.repos`), the shared slug regex, the 5-repo cap, and the
    `profile-backed-repo-pins` durable behavior are unchanged, and the picker
    remains a pure render of parent state — no new browser-local source of truth.

- **Evidence:**
  - `repos.ts` `applyRepoSelection` tests: exclusive replace and narrow,
    cap-exempt exclusive, additive union, idempotent-duplicate additive,
    additive 5-cap enforcement, slug validation in both modes, and trimming.
  - `components/RepoPicker.test.tsx`: a plain click → `exclusive`, a Shift-click
    → `additive`, the "+" affordance → `additive`, a staged shortcut stays
    enabled (narrows) with a disabled "+", and the manual Add stays a distinct
    additive action that does not fire the gesture handler.
  - Live validation on a Glimmung `tank-operator` test slot via `static`
    hot-swap (keyboard + click + "+" exercised in the browser).

## splash-launch-defaults

- **Status:** shipped
- **Intent:** Keep the pre-session splash from turning the last created
  session's launch settings into durable defaults. Fresh splash state defaults
  to Claude GUI, direct initial message, server-owned best models, max reasoning
  for Claude, Codex's highest supported reasoning (`xhigh`), and restricted
  Git. User edits remain a tab-scoped draft while they navigate away and back,
  but successful session creation clears the draft back to those defaults.
- **Affected contracts:** Session Bar, Session Lifecycle. The session-create
  payload still owns the durable session row; this capability only governs how
  the browser stages the next create.
- **Mechanism:** splash mode/interaction, model, effort, initial-message mode,
  and Restricted Git opt-out state use `sessionStorage` draft keys, not
  profile-backed or `localStorage` defaults. `resetHomeLaunchDefaults()` runs
  after a successful `POST /api/sessions`, clears those draft keys, resets the
  in-memory splash state, and writes durable run prefs without launch-model or
  launch-effort keys so stale profile values retire on the next sync. The
  boot-time `main.tsx` localStorage reaper no longer allowlists retired
  durable splash keys (`tank.defaultSessionMode`, `tank.defaultInteraction`,
  or the old Restricted Git key).
- **Evidence:** `frontend/src/modelEffortDefaults.test.ts` pins launch
  model/effort as tab-scoped prefs, default Claude/Codex reasoning, and
  reset-on-create. `frontend/src/homePreferences.test.tsx` covers the
  Restricted Git draft key and clear behavior. `frontend/src/main.test.ts`
  guards that retired durable splash keys are not allowlisted. Full frontend
  Vitest and production build cover the App integration.

## session-bug-labels

- **Status:** introduced
- **Intent:** Let a user attach one loose Tank-native `bug: …` label to a
  session without creating or managing a GitHub issue. Labels are for repeated
  follow-up work on the same defect across several sessions/PRs.
- **Durable source:** `bug_labels` stores normalized per-owner/per-scope label
  names and slugs; `session_bug_labels` stores the label attachments for each
  `(owner_email, session_scope, session_id)`.
- **Runtime behavior:** the splash setup panel stages bug labels before
  `POST /api/sessions`. Opening it lists existing owner/scope labels and
  accepts a new typed value. Create-time saves are included in the
  session-create request through `bug_labels`, with `bug_label` retained as the
  compatibility first-label field. Active-session editing is not exposed in the
  chat composer.
- **Reporting:** session repo reports include `bug_label` on each session row,
  a `bug_labels` summary, and `totals.bug_label_count`. Repo attribution remains
  unchanged; bug labels are a separate grouping.
- **Non-goal:** labels are not task state, ownership queues, or GitHub issue
  mirrors. They intentionally do not imply open/closed ordering semantics.

## bounded-activity-derivation

- **Status:** shipped (issue #1077 item 7)
- **Intent:** The sidebar's activity/unread derivation must cost a bounded
  amount of database work per durable batch, independent of flood size or
  unread-backlog depth — a flood session (the #1051 class) must not become a
  read-side DoS through its own status pill.
- **Runtime behavior:** the session-bus persister coalesces activity emits
  per refresh class per batch (`coalesceActivityEvents`: one
  `EmitChatActivityDelta` for the LAST inserted event of each class —
  lifecycle, compaction, user-message). Each emit's recompute reads durable
  state through partial indexes (migrations 0153/0154) whose predicates are
  kept in provable lockstep with `store.LifecycleEventTypes` /
  `UnreadOutput*Types` by inlining literal type lists
  (`TestActivityPartialIndexPredicatesLockstepWithStoreTypeLists`).
- **Saturation semantics:** the unread-output count scans at most
  `unreadScanCap` (2000) candidate rows per count and saturates there — the
  badge is a signal, not an audit. Read-state advancement (the cursor) is
  unaffected; only the displayed magnitude is capped.
- **Non-goal:** per-event emit accuracy. The last event of a class carries
  the whole batch by design because every class recomputes from durable
  state; restoring per-event emits restores the unbounded derivation cost.

## drawer-touch-targets

- **Status:** shipped
- **Intent:** Make the session list usable by touch when it renders inside the
  compact navigation drawer, where the desktop row density and the hover-revealed
  delete control are too small for a finger.
- **Render model:** same `sidebarBody` fragment as desktop — this is
  density/touch-target tuning only, scoped to the drawer (`.sidebar-in-drawer`,
  `App.tsx`) at <= BP_PHONE. The per-session delete/close control becomes a
  ~40px (2.5rem) target and the `.session-open` rows get `var(--space-2)` of
  vertical padding. No source-of-truth, gesture, or behavior change; desktop is
  unchanged.
- **Evidence:** `frontend/src/mobileShell.test.ts` ("drawer session rows are
  touch-sized on compact").

## unrestricted-git-row-indicator

- **Status:** shipped
- **Intent:** Restricted (Tank-governed) Git is the default for new sessions,
  so the noteworthy state is the *opt-out*. Give every session row a standing,
  glanceable marker of whether the session has **ungoverned** Git, so a user
  working across many sessions can spot the unrestricted exceptions without
  opening each one.
- **Durable source:** `sessions.capabilities text[]` (the `restricted_git`
  member), echoed to the SPA on every session-list row. The indicator reads
  durable session state only — it adds no browser-local flag and cannot
  contradict the registry. A GUI session *without* the `restricted_git` member
  is the flagged (unrestricted) case.
- **Runtime behavior:** for an unrestricted GUI session the session-row
  interaction chip renders a git glyph (lucide `GitBranchIcon`) in place of the
  GUI monitor glyph and tints itself red with the `--unrestricted-git-*` accent.
  Restricted GUI sessions (the default) keep the plain monitor glyph. The swap
  is gated on the `gui` interaction because `restricted_git` is only ever
  granted to repo-backed GUI modes (`REPO_SUPPORTED_MODES`); a non-gui row keeps
  its normal glyph regardless. The chip's `title`/`aria-label` read
  "unrestricted git" so hover and assistive tech carry the same signal.
- **Single source of truth:** the capability string, membership test, and the
  glyph-swap decision live in `frontend/src/sessionModes.ts`
  (`RESTRICTED_GIT_CAPABILITY`, `hasRestrictedGit`, `interactionIconKind`) and
  are unit-tested in `sessionModes.test.ts`. The string mirrors the backend
  constant `SessionCapabilityRestrictedGit`
  (`backend-go/internal/sessionmodel`); a test pins the literal so backend
  drift surfaces.
- **Non-goal:** the indicator does not change Git enforcement, gate any
  control, or imply the GUI/CLI surface of the session beyond what the
  capability already encodes. It is a read-only display affordance.

## spawned-sessions-chip

- **Status:** shipped
- **Intent:** When an agent spawns a session (the `spawn_run_session` /
  `spawn_test_slot_session` MCP tools), the human needs a durable, clickable
  handle to the new session — not a one-line "I started session 1132, boss"
  in the transcript that the agent may describe uselessly or never link. The
  composer toolbar gets a "tank operator" chip that lists the sessions spawned
  from the current one, each a working link, so the relationship survives
  reloads and is reachable without reading the conversation.
- **Durable source:** `sessions.spawned_sessions jsonb` on the **origin
  (parent)** row (migration `0178`). Each entry is a self-contained
  `{id,name,mode,model,repos,url,created_at}` ref. The absolute `url` is
  stamped server-side by whichever operator handled the spawn, so a
  cross-scope test-slot child carries its own slot host. The column is the
  snapshot/SSE-facing source of truth; the SPA never re-derives the
  parent→child relationship from the event ledger.
- **Write path:** `handleInternalCreateSession` appends to the origin row when
  the `X-Tank-Origin-Session-Id` header is present (the proxy stamps it on
  every MCP call), via `sessionregistry.AppendSpawnedSession`. The append is
  atomic and id-deduped (a spawn retry is idempotent) and matches the parent
  on its full `(email, session_scope, session_id)` key — session ids are only
  unique within a scope, so the scope is load-bearing. Same-scope spawns
  (`spawn_run_session`) record lineage; a cross-scope test-slot spawn needs
  the origin scope plumbed through to reach a prod parent, a tracked
  follow-up (the write never targets the wrong row in the meantime). It is
  **best-effort**: the child is already durably created, so a failed edge
  write never fails the spawn. Counter `tank_session_spawn_link_total{result}`.
- **Runtime behavior:** `SpawnedSessionsMenuButton` (`frontend/src/App.tsx`)
  is hidden until the session has spawned ≥1 child, then shows the tank glyph
  with a count badge and a popover of links (`name`, `mode·model·repos`,
  external-link to the child). It is a presence signal, not a permanently
  disabled affordance. The chip reads `session.spawned_sessions` straight off
  the durable row (like `rollout_state`/`repos`); it converges over the
  session-list SSE without refresh.
- **Icon ownership:** the tank glyph (`TankIcon`) moved here from the rollout
  chip — the tank belongs to "tank operator", and spawning sessions is the
  core tank-operator action. The rollout chip took a new `WheelsIcon`
  ("rolling out"). The two read as a matched pair.
- **Non-goal:** no transcript message, no live child *status* (the chip is a
  creation-time link list, not a status feed), no parent backlink rendered on
  the child, and no general PR/test-slot consolidation — PRs already have
  their own chip. The chip never gates a control; it is a read-only link
  affordance over durable lineage.

## nested-spawned-sessions

- **Status:** shipped
- **Intent:** A session can spawn other sessions (`spawn_run_session`), and a
  running sub-session is too easy to lose in a flat, position-sorted list. The
  session list groups each spawned child as **one indented tier directly under
  its origin**, with a ├─/└─ tree connector, so the parent→child relationship
  is visible at a glance and a working sub-session stays attached to the work
  that started it.
- **Durable source:** the child's own `sessions.parent_session_id` column
  (migration `0179`) — the id of the origin that spawned it, stamped in the
  **same INSERT that creates the child** (from the `X-Tank-Origin-Session-Id`
  header). This is deliberately the *child→parent* edge, not the parent's
  `spawned_sessions` array (which stays the chip's *parent→children* source).
  Reading the child's own pointer is what makes a child **born nested**: the
  pointer lands with the child row, so the first snapshot/row-update that
  carries the child already nests it — no appear-at-top-then-snap reflow.
  Inferring nesting from the parent's separate, later `spawned_sessions` append
  was the original implementation and *did* reflow; that inference has been
  removed (no dual path). `parent_session_id` is write-once: set on create,
  absent from the registry `Upsert` `ON CONFLICT` update set, so lifecycle
  re-upserts preserve it. Still pure presentation — no expand/collapse or
  ordering state.
- **Arrangement:** `frontend/src/sessionTree.ts` → `arrangeSessionTree()`
  builds the child→parent map from each row's `parent_session_id` (only when the
  origin is present in the same scoped list) and re-orders the
  already-`sidebar_position`-sorted list so each child follows its origin,
  annotating `{depth (0|1), parentId, isLastChild}`. Root order and within-group
  order are preserved from the durable sort (it layers nesting on top of the
  drag order without owning it). Every input session is emitted exactly once,
  including under malformed self-referential or cyclic lineage (a safety pass
  emits any cycle-trapped row as a root).
- **One tier only:** depth is clamped to 1 by design. If a sub-session itself
  spawns sessions, those grandchildren are grouped under the same top-level
  ancestor at the same single indented tier — the sidebar never indents twice.
  `parentId` still records the *direct* spawner for diagnostics.
- **Cross-scope exclusion:** test-slot children live in a different
  `session_scope` and never appear in this `(email, scope)`-scoped list. Even
  though such a child may carry a cross-scope `parent_session_id` (the prod
  origin id), `arrangeSessionTree` only nests when the referenced origin is
  present in the same list, so a test slot spawned from prod never nests — no
  phantom row, no need to gate the write on scope.
- **Rendering:** the nested `<li>` carries `is-nested` (+ `is-nested-last` for
  the elbow) and a `.session-nest-connector` gutter element
  (`frontend/src/App.tsx`, styled in `index.css`). The tab keeps its right edge
  pinned and steps its left edge in via `margin-left`, and reads slightly
  smaller (compact height + smaller avatar/name) so it is visibly subordinate.
  The collapsed icon-rail sidebar drops the indent and connector. The connector
  is an opaque, single-paint spine: each nested row's `::before` spans its own
  height plus the one row gap *above* it (`top: -var(--nest-row-gap); height:
  100% + var(--nest-row-gap)`), so the per-row segments butt-join into a
  continuous vertical without overshooting onto the tab above or double-painting
  the seam. `--nest-row-gap` is bound to the `.sessions` flex `gap` (it *is* that
  gap); the two must not drift, or the butt-join misses. (The original bridge was
  a fixed `4px` against a real `2px` gap — the 2px overshoot is what made the
  guide clip into the tabs and the translucent segments brighten where they
  overlapped.)
- **Observability:** the session-list debug projection
  (`sessionListDebugRow`/`SessionListDebugRow`) carries `parent_session_id` and
  `nest_depth` so a "my sub-session didn't nest" report is diagnosable from the
  capture without re-deriving lineage; each rendered row also exposes
  `data-depth`. No new metric: `parent_session_id` is written inside the child's
  create `Upsert`, so a failed write fails the create itself (surfaced like any
  create error) rather than silently dropping a best-effort edge — unlike the
  parent-side chip append, which stays best-effort and counted by
  `tank_session_spawn_link_total{result}`.
- **Non-goal:** no second indent level, no subtree collapse/expand (it would
  hide the very sub-sessions this surfaces), no parent backlink rendered on the
  child beyond the visual grouping, and no new durable ordering/state. Nesting a
  cross-scope test-slot child under a prod origin remains out of scope (they are
  different scopes and never share a list).
