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
