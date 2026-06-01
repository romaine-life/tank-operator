# Session Bar Capabilities

Named behaviors in the session-bar surface. See
[contract.md](contract.md) for the durable invariants and
[../README.md](../README.md) for how capability ledgers are used.

## session-repo-attribution

- **Status:** shipped
- **Intent:** Make a session's GitHub repo activity durable and queryable
  later, without changing the sidebar UI. Two sources feed the stored set:
  1. **Create-time selection** — the `owner/name` slugs the user staged on
     the splash page, persisted write-once on `sessions.repos` (this already
     existed; it drives the repo-cloner init container).
  2. **Runtime discovery** — the repos a session's pod actually checked out
     under `/workspace`, observed by a pod-side reporter and folded into the
     new `sessions.discovered_repos text[]` column. This captures repos the
     agent cloned on demand mid-session (`mint_clone_token` + `git clone`, or
     a plain public clone) — the "no durable record" shape migration 0035
     called out.

  The repo metadata is intentionally not rendered in each compact session row
  and does not add sidebar controls; it remains durable and queryable through
  the API/database.

- **Affected contracts:** Session Bar (this surface). The durable source of
  truth stays `session_registry` ("…repositories, and clone state"); the new
  column is more of that same registry-owned repository metadata, delivered
  through the existing per-row SSE update path.

- **Mechanism:**
  - Column + read/serialize: `sessions.discovered_repos` (migration 0078),
    threaded through `SessionRecord`, the `List`/`Get` queries, `Info`
    (`discovered_repos`, always-emit array), and the row-update wire
    (`rowWireShape` / `MarshalRowUpdate`).
  - Write path: `POST /api/internal/sessions/{id}/discovered-repos`
    (`handleInternalSetDiscoveredRepos`, service-principal gated like
    clone-state) → `Manager.MergeDiscoveredRepos` →
    `Store.MergeDiscoveredRepos` (monotonic set union with a `@>` containment
    guard so a re-report of a known set is a no-op — no `row_version` bump, no
    SSE fan-out) → `publishRow`.
  - Reporter: `k8s/session-config/workspace-repo-reporter.sh`, launched in the
    background by the runner launch scripts (the SDK-runner shapes are the only
    ones with a `/workspace` emptyDir). It scans git remotes under
    `/workspace`, strips any embedded credentials, and POSTs the observed set
    only when it grows. Best-effort: never blocks the runner, retries next
    tick on transient failure.
  - UI: no visible surface in this PR. The SPA still normalizes
    `discovered_repos` from the shared row payload for wire compatibility, but
    it does not render, filter, or otherwise expose repo attribution.

- **Observability:**
  - `tank_session_discovered_repos_reported_total{result=merged|noop|empty|error}`
    — the merged-vs-noop split is the health signal (a healthy reporter is
    mostly noop; a flood of merges means slugs are churning).
  - `tank_session_discovered_repos_dropped_total` — slugs dropped in
    normalization (malformed / over cap); steady-state zero.

- **Evidence:**
  - `cmd/tank-operator` `TestNormalizeDiscoveredRepoSlugs` (input boundary:
    drop-not-reject, dedup, cap, credential-junk rejected).
  - `sessioncontroller` `TestMarshalRowUpdateIncludesDiscoveredRepos` (wire
    contract: rides every row payload, empty serializes as `[]`).
  - End-to-end clone→report→query validated on a Glimmung test slot (real
    Postgres exercises the `MergeDiscoveredRepos` SQL, which the pure-function
    registry unit tests do not cover).

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
    /api/github/pinned-repos` returns the same durable list.
  - Write path: `PUT /api/github/pinned-repos` validates the shared
    `owner/name` slug contract, dedups case-insensitively, caps the metadata
    list at 64 entries, and replaces the profile row value.
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
