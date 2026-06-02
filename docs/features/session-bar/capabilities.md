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
  `sessions.repos`.

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
    list at 64 entries, and replaces the caller owner's profile row value.
  - UI: the picker initializes from the authenticated profile and updates the
    visible pin state only from the server response. The retired
    `tank.homePinnedRepos` localStorage key is not allowlisted on boot.

- **Observability:**
  - `tank_github_pinned_repos_update_total{result=ok|invalid|unavailable|error}`
    distinguishes contract drift, profile-store availability, and database
    write failures.

- **Evidence:**
  - `cmd/tank-operator` `TestValidatePinnedRepoSlugs` covers validation,
    deduping, the distinct pin cap, and rejection of malformed slugs.
  - `main.tsx does not allowlist retired local repo pins` prevents the
    browser-local key from being treated as live state again.
  - `repos.ts` `pinnedRepoSlugs caps profile metadata` keeps the SPA cap in
    lockstep with the backend metadata boundary.
