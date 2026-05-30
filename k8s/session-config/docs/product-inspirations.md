# Product Inspirations

This is a taste document, not a links page. It names external systems worth
studying and, for each, says what to **borrow** and what **not to inherit**.
The point is to copy proven primitives without importing another product's
boundary or its compromises.

Read it the way the source projects were read: steal the idea, keep your own
model.

## The Stance That Recurs

Across every project in this hub — an orchestrator, a workflow engine, a
pixel-art server, a game mod — the same north star keeps reappearing, and it is
the lens for everything below:

- **Durable state is the source of truth.** A registration in Postgres, a row
  in an event ledger, an authority clock — not browser-local optimism, not
  process memory, not a re-scrape of logs.
- **Live transport wakes clients; it does not own product state.** Reconnect
  resumes from a durable cursor. An unknown cursor forces an explicit resync,
  never a silent gap.
- **Observed outcomes beat claimed intent.** Record what actually happened, not
  what a control or a card text said would happen. A control is complete only
  when the durable terminal event confirms it.
- **Borrow primitives, not boundaries.** Another system can sit *under* a phase
  later; it should not become the user-facing model or the owner of identity.

When an inspiration below conflicts with a quick local fix, prefer the
inspiration-aligned architecture and delete the old path in the same change.

## Workflow & Pipeline Systems

| System | Read for | Borrow | Do not inherit |
| --- | --- | --- | --- |
| Argo Workflows | k8s-native execution, node status, artifacts, DAG/steps vocabulary | Executor ideas, status projection, pod/job lifecycle | Argo as the source of truth for run identity, retries, or UI graph semantics |
| Tekton | `PipelineRun`/`TaskRun` records, task-result contracts | Clear per-task result and status boundaries | A CI pipeline as the product model |
| Temporal | Durable orchestration, signals, cancellation, replay-safe decisions | State-machine rigor for long-running work | Hiding workflow shape inside SDK code; the graph must stay data-defined and inspectable |
| Kueue | k8s admission control for scarce batch capacity | Queue/admission concepts, fair sharing | Treating capacity admission as the whole product |
| Nomad | Evaluations vs. allocations vs. placement | The split between desired work, scheduler decisions, and concrete allocations | A generic cluster-scheduler UI |
| Prow/Tide, Zuul | PR gating, retest loops, merge pools, speculative queues | Review-gate discipline, visible merge readiness | GitHub PRs as the canonical run/issue loop |
| Buildkite, Concourse | Agent hooks, per-build annotations, strict task input/output | Hook points and human-readable evidence snippets | Pipeline ownership or annotations as the primary review object |
| SWE-agent, OpenHands | Issue→patch agent loops, trajectories, sandboxed evidence | Agent work loops, issue-oriented repair, evidence from the working session | A single-agent benchmark harness as the platform boundary |

**Negative reference — GitLab CI `needs`.** Job-level DAGs are powerful and
become unreadable once every job can point at every other job. Prefer
left-to-right phases with parallel-but-independent jobs inside a phase. If a job
needs another job's output, put it in a later phase.

## Execution & Run-Graph UI

| System | Read for | Borrow | Do not inherit |
| --- | --- | --- | --- |
| Azure DevOps / generic CI | Step visibility | The **step-list ▸ terminal-log split pane**: compact step rail on the left, monospace log on the right | Forcing every step into the global graph |
| GitHub Actions | Familiar run/job/step mental model | Recognizable status vocabulary at the job level | GitHub Check Runs as the canonical owner of run state |

Run-graph principles worth holding regardless of source:

- **Click-to-pin inspectors, not hover.** Durable selection survives sharing,
  deep links, and debugging; hover-only state loses information.
- **Breadcrumb navigation** over deep tab nesting once a surface is a primary
  work area.
- A run graph is **explanatory** (what happened, why); a decision/review
  surface is **separate** (what to inspect or decide now). Don't overload one
  with the other.

## Navigation Shell & Operator Consoles

| System | Read for | Borrow | Do not inherit |
| --- | --- | --- | --- |
| Grafana | Dense sidebar shell, scan-friendly dashboards | Information density, calm-at-rest chrome | Marketing composition or decorative illustration |
| GitLab | Project/area sidebar, count affordances | Left-nav structure, right-aligned counts | Feature sprawl as a default |
| k9s / Lens | Terminal-grade k8s consoles | The "personal infra control panel" posture | Treating raw cluster objects as the product |
| Linear | Command palette, keyboard-first quick actions | Fast, low-chrome action surfaces | Consumer-app warmth where an operator console is wanted |

House-style cues these encourage (adapt per repo's own design system):

- terse, lowercase, technical voice; concrete identifiers (ids, paths,
  hostnames) over generic labels
- dark, dense, single-family type with tabular numerals on numeric chrome
- explicit empty/loading/failure states; no apology copy, no emoji in chrome
- selection communicated by a consistent accent rail, not ad-hoc fills

## Design-Review Surface

| System | Read for | Borrow | Do not inherit |
| --- | --- | --- | --- |
| Storybook (as prior art only) | Component cataloging | The idea of a single page that shows every component in every state | A separate process/build; prefer an in-app `/_styleguide` or `/_design-portfolio` route rendering live DOM |

The diff says *what changed*, not *what it looks like*. A live styleguide route
plus a per-change environment is the review surface. Keep specimens grouped by
workflow, with passive review state (`unreviewed`, `needs_review`, `approved`,
`needs_work`) that does not by itself dispatch work.

## Durable Messaging & Conversation

| System | Read for | Borrow | Do not inherit |
| --- | --- | --- | --- |
| Mattermost, Zulip, Element/Matrix Synapse, Rocket.Chat | Replayable history separate from live delivery | Server-replayable history, reconnect-from-cursor, explicit resync on gaps | Live transport as the only place product state exists |

## Cloud Development Environments

| System | Read for | Borrow | Do not inherit |
| --- | --- | --- | --- |
| Coder, Gitpod, Codespaces | Ephemeral per-task workspaces | Workspace lifecycle as a lifecycle boundary | The promise of resurrecting a dead ephemeral workspace; pod/workspace death is a real boundary |

## How To Use This Doc

When a change claims to follow an established pattern, check it against the
"do not inherit" column, not just the "borrow" column. The taste is in the line
you choose **not** to cross.
