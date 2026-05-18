# Diagnostic Discipline

This is a checklist for agents (and humans) investigating bugs and incidents in
this codebase. The other three policy docs in this directory
(`quality-timeframes.md`, `migration-policy.md`, `product-inspirations.md`)
describe the quality bar for *writing* code. This one describes how to
*investigate* code so the writing has the right target.

It is short on purpose. Read it before you form a hypothesis, not after.

## Source of truth

For any claim about product behavior (did this turn stop, did this user receive
this message, did this credential refresh, did this rollout converge), the
**durable ledger is the source of truth, not the live system.** Specifically:

- Run state, conversation events, stop/interrupt outcomes: Postgres
  `session_events` (`tank-operator-db.postgres.database.azure.com`,
  database `tank-operator`).
- Session registry and ownership: Postgres `session_registry`.
- Credentials and per-user profile: Postgres `profiles`.
- Image fingerprint deployed: `k8s/values.yaml` + ArgoCD application status.
- Auth role decisions: auth.romaine.life JWT contents + tank-operator's
  `/api/auth/me` response.

Logs, metrics, and live MCP queries are evidence about *something*, but not
necessarily about the user's claim. Cross-check against the durable source
before concluding.

## Investigation order

When a user reports "X didn't work":

1. **Restate the claim in falsifiable terms.** "The agent kept running tool
   calls after I clicked Stop" is a behavior claim. "The UI didn't show the
   stop chip" is a display claim. Different bugs, different fixes. Do not
   reframe the claim while answering it.

2. **Check the durable ledger first.** For the specific session / turn /
   resource in question, query the durable table directly. The first piece of
   evidence you write down should be from Postgres (or the equivalent durable
   surface for the area), not from logs or metrics.

3. **Then live signals.** Metrics, logs, JetStream consumer info,
   `kubectl describe`. These are how you explain *why* the durable ledger
   shows what it shows.

4. **Map evidence to the claim, not the other way around.** If the loudest
   signal (log spam, alert firing) doesn't directly contradict the user's
   claim, write down why before pursuing it as the root cause. Loud is not
   the same as load-bearing.

## When you find a likely cause

5. **Walk the three policy docs as a checklist.** Specifically:
   - `product-inspirations.md` — "A control such as Stop is only complete when
     the durable terminal event arrives." If the proposed cause involves a
     user-visible state that doesn't have a corresponding durable terminal,
     the cause description is incomplete.
   - `quality-timeframes.md` — Done Standard. If the proposed cause's fix is
     "add a fallback path" or "add a log," check that you aren't violating
     "Marking a control action successful before the durable system confirms
     it" or "Missing counters for user-trust failures."
   - `migration-policy.md` — If the proposed fix preserves an old behavior
     "for safety," it is not a fix.

6. **Read the closed `session-log` issues for the area.** Stop / interrupt /
   refresh / rollout / auth — each has a small library of post-mortems with
   tagged `session-log` label. The lessons-learned that didn't make it into
   the policy docs live there. Search for the area before concluding on a
   root cause.

## When the user pushes back

7. **A pushback is usually a frame correction, not a request for more
   evidence in the same frame.** "Look at the code first and then diagnose"
   means "your diagnosis order is wrong, start over." It does not mean "go
   produce code citations for your existing conclusion." Restart the
   investigation from step 1 with the new framing.

## Bus / event-fabric specifics

For any bug touching the NATS JetStream session bus (data plane vs. control
plane, command consumers, durable event ordering):

8. **The four-outcome contract for user-trust controls.** A durable
   `*_requested` event from the user MUST be followed by exactly one durable
   terminal: the success terminal, an explicit failure terminal, or an
   already-raced terminal. Silent strandings — no terminal, no counter, no
   alert — are a bug class, not a single bug. If your hypothesis can produce
   a `*_requested` with no following terminal, the hypothesis names a bug
   even if it isn't the immediate cause you're investigating.

9. **Backend persist + publish vs. runner consume + emit are different
   stages.** Backend metrics (`tank_turn_*_total`) say what the backend did.
   Runner metrics (`tank_runner_*_total`) say what the runner did. A
   discrepancy between them is "lost between the planes" and needs JetStream
   consumer-info to localize.

## Anti-patterns

10. Reaching for `kubectl logs` before querying the durable ledger.
11. Treating the loudest log line as the cause without testing it against the
    user's actual claim.
12. Restating a behavior claim as a display claim to make it match the
    available evidence.
13. Producing a confident root-cause writeup before walking the policy-doc
    checklist.
14. Treating closed post-mortem issues as historical curiosities rather than
    extensions of the policy docs.

These all share a shape: investigating the system from the live side first,
where evidence is loud and easy to gather, rather than from the durable
side first, where the contract lives.
