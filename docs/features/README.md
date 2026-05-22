# Feature Contracts

The repo-wide policy docs describe the quality bar:

- [migration-policy.md](../migration-policy.md)
- [product-inspirations.md](../product-inspirations.md)
- [quality-timeframes.md](../quality-timeframes.md)
- [diagnostic-discipline.md](../diagnostic-discipline.md)

Feature contracts translate those rules into concrete, reviewable invariants
for the parts of Tank that can visibly lie to the user.

Read the relevant contract before planning or implementing substantial work in
that area. A PR that touches a contracted feature should name the contract and
show the evidence that the contract still holds. Evidence can be a unit test,
integration test, browser test, metric, alert, migration guard, or direct
runtime observation, depending on the risk.

## Contracts

- [Agent Runners](agent-runners/contract.md)
- [Artifacts And Files](artifacts-and-files/contract.md)
- [Auth And Streams](auth-and-streams/contract.md)
- [Session Bar](session-bar/contract.md)
- [Session Lifecycle](session-lifecycle/contract.md)
- [Transcript](transcript/contract.md)
- [Transcript Navigation](transcript-navigation/contract.md)

## When To Add A Contract

Add or expand a feature contract when a feature:

- can show state that contradicts the durable system;
- crosses browser, orchestrator, runner, database, or Kubernetes boundaries;
- depends on live streams, reconnect, retry, or rollout behavior;
- has controls where "appeared to work" is different from "durably worked";
- has already produced a user-trust bug.

Small static UI, isolated copy changes, and one-off internal helpers usually do
not need their own contract. They inherit the global policy docs.

## PR Review Rule

For any contracted feature, reviewers should ask:

- Which contract does this PR touch?
- Which invariant could the PR break?
- What evidence proves the invariant still holds?
- Does refresh, reconnect, restart, or rollout reveal state the live UI missed?
- Is any old path, fallback, or browser-local source of truth being kept alive?

If the answer depends on "it should probably work," the PR is not complete by
the repo quality standard.
