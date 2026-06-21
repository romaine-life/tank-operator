# Session Transcript Capture & Conversation Resurrection

Status: **Stages 1–3 implemented** (capture, resurrection, contract
amendments); Stage 4 (Codex parity) + durable `resurrected_from` lineage are
follow-ups
Owner: TBD
Affected contracts: [Session Lifecycle](features/session-lifecycle/contract.md),
[Agent Runners](features/agent-runners/contract.md),
[Observability](features/observability/contract.md)

## 1. Problem

Session pods are `emptyDir`-backed and ephemeral. When a node drains
(AKS node-image upgrade, spot eviction, scale-down) every session pod on it
dies, and with it the agent's conversation. The durable chat ledger
(`session_events`) survives, but it is a **display projection**: the Claude
adapter persists `text`, `tool_use`/`tool_result` items, and a `kind:"reasoning"`
display summary (the human-readable Turn-activity "Thinking..." text), but it
does NOT persist the provider-faithful `thinking`/`redacted_thinking` blocks or
their cryptographic signatures. The reasoning display text is a lossy summary for
the UI; it is not resume-faithful. The provider-faithful transcript the SDK needs
to *resume* a conversation — the raw thinking blocks and signatures — lives only
in the SDK's on-disk JSONL inside the dead pod, which is why capture (§3+) is a
separate snapshot sink and not derivable from `session_events`.

Result: today a node roll is total conversation loss for every live session,
and there is no path back even though the user does **not** care about
uncommitted workspace bytes — they care about the **conversation** continuing.

## 2. Goal and non-goals

**Goal.** Make a session's *conversation* survivable across pod death by
durably capturing the SDK transcript artifact off the pod, and add an explicit
user-initiated **resurrection** that creates a new pod-backed session whose
agent resumes the captured conversation.

**In scope**
- Byte-faithful capture of the Claude SDK JSONL transcript to durable storage.
- An explicit resurrection flow: new pod → re-clone repos → materialize the
  transcript → `resume`.
- The Codex analogue (thread/`~/.codex/sessions` state) as a follow-up stage;
  the architecture is symmetric but the capture target differs.

**Explicitly out of scope**
- Preserving the `/workspace` filesystem (uncommitted edits, build artifacts,
  downloads). Committed code is re-cloned from `sessions.repos`; everything
  else is regenerated or accepted as lost. This is a deliberate product
  decision by the owner.
- Automatic / silent continuation across pod death. Resurrection is an
  explicit action that produces a *new* session lifecycle (see §4).
- Resurrecting across an arbitrary SDK-format gap as a hard guarantee (best
  effort across SDK majors — see §8).

## 3. Why path A (capture the file), not reconstruct from `session_events`

Two candidate sources of truth for resume were considered:

- **Reconstruct the JSONL from `session_events` + saved thinking blocks.**
  Rejected as the primary path. `session_events` is a *lossy, transformed*
  display projection, and the SDK JSONL is an **undocumented, versioned**
  internal format (record `uuid`/`parentUuid` threading, message envelopes,
  block ordering, `cwd`/`gitBranch`/`version` metadata). Reconstruction means
  reversing a lossy projection *and* re-emitting a moving internal format —
  exactly the "build on an unstable internal contract / lossy fallback path"
  anti-pattern the policy docs forbid. It also trips the documented
  `thinking_block_modified` 400 if any signature/order is off.

- **Capture the real JSONL file (path A).** Chosen. We store the bytes the SDK
  itself wrote, so capture and restore never parse or rebuild the format —
  they copy. Fidelity (thinking blocks, signatures, ordering) is byte-exact by
  construction, which makes the whole `thinking_block_modified` class moot.

The cost is storing the transcript content roughly a second time (it overlaps
`session_events` on tool I/O bytes), but on **object storage** that is
economically irrelevant (see §7), and it is the bulk we *don't* otherwise have
in resume-faithful form.

## 4. Contract impact (must be resolved before merge)

### Session Lifecycle Contract — amend
Current text: the product must not pretend "a dead pod can be resurrected" and
must "not silently continue a session after the pod-death boundary," because
"the `emptyDir` workspace is gone."

This feature does **not** violate the spirit, but the wording must be updated
so a future agent does not read it as a prohibition:

- Pod death remains **terminal for the running session**. The dead session's
  lifecycle state still moves to terminal with durable evidence.
- Resurrection is a **new, explicit lifecycle**: a new `session_registry` row
  and a new pod, linked to the dead session by a `resurrected_from` lineage
  field. We are not reviving the dead pod or pretending its workspace returned.
- The workspace is still gone; only the **conversation** is restored, and only
  by replaying a durably-captured artifact, never by silent continuation.

Proposed contract edit: add a "Resurrection" subsection under Failure And
Recovery stating the above, and qualify "the session is terminal because the
`emptyDir` workspace is gone" with "the *workspace* is terminal; the
*conversation* may be re-seeded into a new session when a captured transcript
exists."

### Agent Runners Contract — extend
- Add: "The Claude SDK transcript is captured to durable storage as the
  resume-faithful record. `session_events` remains the display projection;
  neither is derived from the other." This is consistent with the existing
  "Runner process memory ... must not be the only record of user-visible
  completed work" line — today the *resume-faithful* record violates that
  spirit (it lives only on the pod), and this closes it.
- Add an acceptance check: a captured transcript exists and is current for any
  Active session with at least one completed turn.

### Observability Contract — extend
- New counters/alerts for capture freshness and restore outcomes (§9).

### Transcript Contract — display-only addition; capture unaffected
The display transcript now carries reasoning DISPLAY text: a populated
`kind:"reasoning"` Turn-activity item rendered in the turn's activity disclosure
and the Turns view (see the "Reasoning Display In Turn Activity" capability in
`features/transcript/capabilities.md`). That text is a lossy summary for the UI,
not a resume artifact: the resume-faithful `thinking`/`redacted_thinking` blocks
and their signatures are still NOT in `session_events` and remain snapshot-only,
captured byte-faithfully to the blob. Capture stays a parallel sink — it is not
derived from the reasoning display text, and the reasoning display text is not
derived from the capture.

## 5. Architecture

```
 CAPTURE (per live session, continuous)
   claude-runner (in-process)
     fsnotify watch ~/.claude/projects/<enc-cwd>/<sdkSessionId>.jsonl
       -> debounce -> whole-file snapshot -> Azure Blob
          key: <ownerToken>/<tankSessionId>/transcript.jsonl
          + sidecar metadata: sdkSessionId, relPathFromHome, sdkVersion, turnSeq

 RESURRECT (explicit, on demand)
   POST /api/sessions/{id}/resurrect
     -> new session_registry row (resurrected_from=id, same repos[], same mode)
     -> new pod
        repo-cloner re-clones repos[]            (existing)
        claude-runner on boot, if RESUME set:
           download blob -> write to <HOME>/<relPathFromHome>
           construct query({ options: { resume: sdkSessionId, ... } })
```

The runner owns **both** capture and restore. Rationale: the JSONL lives on the
runner container's own writable layer (`/home/node/.claude/...`), not a shared
volume, so a sidecar or init container cannot read/write it without a pod-spec
change. Keeping both ends in the runner needs no new volumes and mirrors the
existing `antigravity-runner` fsnotify-tail-of-`agy`-transcript pattern.

## 6. Capture details

- **Location.** `cwd` is `/workspace` but the SDK writes to
  `$HOME/.claude/projects/<encoded /workspace>/<sdkSessionId>.jsonl`, with
  `HOME=/home/node` (claude-container Dockerfile, user `node`). Confirmed: the
  runner container mounts only `/workspace` + token/CA mounts — the JSONL is on
  the **container writable layer**.
- **Snapshot, not append.** The SDK rewrites the file (context compaction =
  `compact_boundary`, in-place edits), so byte-append shipping would capture a
  torn transcript. Capture = **whole-file snapshot on each write event**,
  debounced (e.g. 1–2s quiet window, plus a hard flush at every durable turn
  terminal so a crash right after a turn still has that turn).
- **Key by Tank session id**, store the SDK session id and the
  **path-relative-to-HOME verbatim** alongside the blob, so restore writes to
  the identical path without recomputing the SDK's cwd-encoding scheme.
- **Store the SDK version** (`@anthropic-ai/claude-agent-sdk`) with each
  snapshot for the restore-compatibility gate (§8).
- **Auth.** Runner already has a projected SA token + workload identity shape;
  capture writes via the orchestrator-namespace UAMI to a dedicated Blob
  container (or via a thin orchestrator-internal upload endpoint if we want to
  keep blob creds out of session pods — decision in §11).

## 7. Storage

- **Azure Blob**, not Postgres. The artifact is opaque, written append-mostly,
  read whole exactly once (on resurrection), never queried relationally — the
  textbook object-storage profile, and it keeps the B1ms Postgres flat (the
  same reasoning that drove the Cosmos→Postgres move).
- **Size.** Single-digit MB typical, tens of MB for monster sessions; total
  across all sessions is single-digit-to-low-tens of GB. At ~$0.02/GB-month
  this is cents even storing every transcript.
- **Layout.** `tank-transcripts/<ownerToken>/<tankSessionId>/transcript.jsonl`
  + `.../meta.json`. Versioned blobs optional (keep last N snapshots) for
  debugging; the live one is the resume source.
- **Retention.** Tie to session deletion + a TTL (e.g. delete transcript blob
  when the session row is hard-deleted; TTL sweep for orphans). Provisioned in
  `infra/` (tofu) alongside the existing storage accounts.

## 8. Restore details and fidelity gotchas

- **`continue` → `resume`.** The runner currently launches with
  `continue: true` (resume latest-in-cwd, fine for in-pod re-exec). For
  cross-pod resurrection it must use `resume: <sdkSessionId>` against the
  materialized file. Add a `RESUME_SDK_SESSION_ID` (or command-carried) input;
  when present, download + materialize before `query()`, then pass `resume`.
- **Materialize before `query()`.** The runner writes the blob to
  `<HOME>/<relPathFromHome>` on boot, ahead of the first `submit_turn` /
  `ensureSdkQuery`. First-boot-without-resume keeps today's behavior.
- **SDK-version coupling.** A JSONL captured under SDK vX resumed under vY may
  break (the `^0.3.158` history proves format sensitivity). Gate restore on
  recorded vs running SDK version: exact match = resume; mismatch = either
  refuse with a clear terminal ("transcript captured under an older engine;
  resurrection unavailable") or attempt best-effort behind a flag. Never
  silently produce a corrupt resume.
- **Repos re-clone** is unchanged (`repo-cloner` reads `sessions.repos`). The
  conversation references file paths that re-exist after clone; uncommitted
  state referenced in the transcript will simply be absent — acceptable per §2.

## 9. Observability

Per the lifecycle/observability contracts, capture and restore are user-trust
surfaces (an uncaptured transcript = a silent inability to resurrect):

- `tank_runner_transcript_capture_total{result}` — snapshot uploads
  (ok/failed/skipped).
- `tank_runner_transcript_capture_lag_seconds` — gauge: age of the last
  successful snapshot vs latest durable turn terminal. Alert when an Active
  session's transcript is stale beyond a threshold (capture regressed).
- `tank_session_resurrect_total{result}` — requested / restored /
  refused_version_mismatch / failed.
- `tank_session_resurrect_resume_outcome_total{outcome}` — first post-resume
  turn reached a durable terminal vs failed (catches a JSONL that materialized
  but won't actually drive the SDK).
- Alert `TankTranscriptCaptureStalled` (active session, no fresh snapshot) and
  `TankResurrectResumeFailed` (resume materialized but first turn failed),
  each with a runbook entry in `observability.md`.

## 10. Phasing (each stage coherent on its own)

1. **Capture only.** In-runner fsnotify whole-file snapshotter → Blob + meta,
   counters, freshness alert. No user-visible behavior; proves the artifact is
   durable and current. Infra: Blob container + retention.
2. **Resurrection flow.** `resurrect` endpoint, `resurrected_from` lineage,
   runner `resume` path + materialize-on-boot, version gate, restore counters.
   SPA action ("Resurrect session") on terminal sessions that have a transcript.
3. **Contract + docs.** Amend Session Lifecycle / Agent Runners / Observability
   contracts; add a `capabilities.md` entry ("conversation-resurrection") in
   the session-lifecycle feature folder.
4. **Codex parity (follow-up).** Capture `~/.codex/sessions` / thread state;
   same blob+restore shape; symmetric resume.

## 11. Open decisions

- **Blob creds in session pods or not?** Runner writes blob directly (needs
  blob scope on the session pod's identity) vs. runner POSTs snapshots to an
  orchestrator-internal upload endpoint (keeps blob creds orchestrator-side,
  one more hop). Leaning orchestrator-internal upload to preserve the
  session-pod credential-minimization posture.
- **Relocate `~/.claude/projects` onto the `/workspace` emptyDir?** Optional,
  orthogonal robustness: it would make the *container-restart* resume case
  (currently container-layer, so fragile) survive a kubelet container restart,
  and would let a sidecar do capture. Does **not** help pod death (emptyDir
  dies too). Recommend deferring; the blob is what addresses pod death.
- **Resurrect = new session row vs reuse the terminal row.** Plan assumes a new
  row with `resurrected_from` lineage (cleaner against the lifecycle contract).
  Confirm the SPA/session-list UX for the lineage.
- **Snapshot cadence** (debounce window + force-flush points) vs blob write
  volume.

## 12. Acceptance evidence

- Capture: an Active session with ≥1 completed turn has a fresh blob; killing
  the pod and inspecting the blob shows the full transcript including thinking
  blocks + signatures.
- Resurrection on a **pre-deploy** pod path: the validation must exercise a
  session created before the change and resurrected after — not only a freshly
  created one (per the CLAUDE.md migration-audit note; new-session-only
  validation has shipped silent regressions twice before).
- First post-resume turn reaches a durable terminal (`resume_outcome=ok`).
- Version-mismatch path produces the explicit refusal terminal, never a corrupt
  resume.
- Contracts name this work and cite the above as evidence.
</content>
</invoke>
