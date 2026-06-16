---
name: rollout
description: Land a change by handing CI off to Tank, fixing what it reports, and letting the human merge
---

# /rollout — Land a Change (hand off, don't babysit)

When the user invokes `/rollout`, carry the current change to a clean, merge-ready PR. You do **not** watch CI yourself and you do **not** merge — Tank watches CI for you and wakes you if something needs fixing; the human merges. See `docs/event-driven-rollout.md`.

## Workflow

1. **Finish the code change.** Make the requested edits, keep the diff focused, and run the relevant local checks.
2. **Publish.** Commit on the session branch — the post-commit hook auto-publishes through `publish_current_head`, which records the commit and starts Tank's CI/mergeability watch. Once the PR exists, call `set_pull_request_link` with the session id and PR URL so the Tank UI can show its status.
3. **Hand CI off to Tank.** Call the `watch_current_session_pr` tool. It performs the authoritative read for you — resolving GitHub's *asynchronous* `mergeable_state`, which is the exact thing agents get wrong — and returns one of:
   - **`conflict`** — rebase the session branch onto its base, resolve, re-publish, then call `watch_current_session_pr` again.
   - **`failed`** — a required check is already red; fix the cause, re-publish, then call `watch_current_session_pr` again.
   - **`ready`** — green and mergeable; tell the user it is ready for them to merge in Tank.
   - **`watching`** — CI is in flight. **You are done.** End your turn with a one-line status (e.g. "pushed, CI watching"). Do **not** set a timer, estimate a duration, poll CI, or pick up other work. Tank will wake you if CI fails or the PR conflicts. It will not wake you on success — the human merges.

4. **If Tank wakes you** (a `ci-failure` or `ci-conflict` turn), it hands you the specific check and what to do. Fix the cause, re-publish, and call `watch_current_session_pr` again to resume the watch. If the failure was unrelated or flaky and the PR is actually fine, just call `watch_current_session_pr` to re-verify and resume waiting.

## Guardrails

- **Do not merge.** Merging is the human's decision, made through Tank. Do not call merge tools and do not merge on GitHub.
- **Do not poll** CI (or ArgoCD) on a timer or busy-loop. Watching is Tank's job now; your responsibility ends at the hand-off in step 3.
- A `conflict` or `failed` result is unfinished work — fix it before handing back, or, if it is genuinely external/unrelated, explain the blocker clearly.
- Do not merge unrelated local changes; do not force-push or rewrite shared history unless the user explicitly asks.

## Lease/Glimmung

- Starting rollout means the test workflow is over. If you held a Glimmung test slot lease via the /test skill, return the lease so Glimmung can clean up the test environment.
