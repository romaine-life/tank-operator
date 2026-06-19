# Agentic flows: split LLM work into discrete stages

The platform's autonomous agent flows split a single conceptual task
into multiple, narrowly-scoped LLM invocations. Each stage runs as its
own LLM call with its own prompt, context, tool permissions, timeout,
budget, and structured handoff artifacts.

## Why split

The cost of letting a single LLM run end-to-end on an autonomous task
is **context burden**:

1. **Context dilutes attention.** A monolithic run carries irrelevant
   exploration, build noise, and earlier-tool-call output forward into
   every subsequent decision. Verification reasoning competes with
   implementation reasoning.
2. **Skill-mixing degrades quality.** Code authoring rewards different
   habits than test design or evidence verification. A test-design
   prompt without code-edit tools forces the model to plan instead of
   jumping straight to a fix.
3. **Failure modes mix.** A failure during evidence capture in a
   monolithic run can corrupt the implementation context — tools left
   in the wrong state, time burned, retries on bad assumptions. With
   stages, the verification phase fails *clean*; the implementation
   phase's artifact is already on disk and unaffected.
4. **Cost is lumpier.** A single 30-minute LLM call is harder to retry
   surgically than three 10-minute stages with handoff artifacts.

## Stage shape

Stages typically are:

1. **Plan / devise tests** — read the issue, identify the change
   target, write down the validation plan and `required_evidence`
   contract. **No code-edit tools.**
2. **Implement code change** — consume the plan, edit code only.
   **No GitHub tools, no live-validation tools.**
3. **Verify** — consume the prior artifacts, run tests / live
   validation / screenshots, write the verification result.
   **No code-edit tools, no GitHub-write tools.**

Each stage's tool permissions enforce the contract — the LLM cannot
silently expand its scope into an adjacent stage's responsibility.

Stages communicate through structured artifacts: each writes both
machine-readable JSON (consumed by the wrapper to gate transitions
and by the next stage as input context) and human-readable Markdown
(appended to the run summary).

## Canonical shape

Tank's default Glimmung workflow runs as native Kubernetes jobs and
keeps each stage explicit:

- `prepare` establishes the validation target.
- `implement` makes and pushes the code change.
- `verify` records the validation result.

The wrapper should enforce the verifier's claimed pass before opening
a PR. The important contract is the phase boundary and the evidence
handoff, not whether a project uses Kubernetes Jobs or GitHub Actions
underneath.

## When *not* to split

Single-LLM is fine when the entire task is one tight loop with no
genuine planning, implementation, or verification distinction — for
example, "answer this question" or "rephrase this sentence". The
split is for autonomous flows that produce code + evidence; one-shot
operator commands don't need the overhead.

**Interactive sessions are not this doc's subject.** A human-driven
session that diagnoses an incident and then implements the fix is one
continuous task with one owner; the accumulated diagnosis context
(evidence, file locations, rejected hypotheses, design decisions) is
an asset there, not a burden. Do not cite this doc to propose
abandoning a live session and restarting implementation in a fresh
context — context resets in interactive work have repeatedly cost
real time re-deriving state this doc was never meant to discard. The
context-burden argument above applies to *unattended* runs, where
per-stage tool permissions and handoff artifacts substitute for the
human who would otherwise catch drift.

## What goes into a project's CLAUDE.md / AGENTS.md

When introducing a new agentic flow for a project, **either** reuse a
stage-split workflow shape **or** explicitly document why a single
LLM is appropriate for that flow. Drift toward "let one LLM do
everything" is the failure mode this principle exists to prevent.

Glimmung-driven projects using runner-k8s should split LLM
work across multiple `k8s_job` steps (each invoking claude-code with
a narrowed prompt and tool set), the same way spirelens splits across
GitHub Actions jobs. Per-stage tool permissions and per-stage handoff
artifacts are the load-bearing parts; the runner technology
underneath is secondary.
