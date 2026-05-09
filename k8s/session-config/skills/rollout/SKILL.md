---
name: rollout
description: Land a change through PR merge, follow resulting builds, and watch ArgoCD until the new image is deployed
---

# /rollout — Land and Watch a Change

When the user invokes `/rollout`, carry the current change all the way through deployment.

## Workflow

1. **Finish the code change.** Make the requested code edits, keep the diff focused, and run the relevant local checks.
2. **Open a PR.** Commit the change on a branch and create a pull request with a concise summary and the tests or checks you ran.
3. **Merge the PR.** Once the PR is ready and checks allow it, merge it. Prefer the repo's normal merge strategy and do not bypass required checks.
4. **Follow the builds.** Watch the GitHub Actions workflows or other build jobs triggered by the merge. Identify the image tag or artifact produced by the build.
5. **Watch ArgoCD.** Argo usually syncs within seconds after the build is produced. Use the ArgoCD MCP/tools available in the session and poll about every 5 seconds until the new image lands in the target application.
6. **Notify the user.** Message the user when the image is deployed. Include the PR, merge commit, image tag, and Argo application or workload that received it. Include the URL of the site relevant to the project being worked on, and anything else relevant.

## Guardrails

- Do not merge unrelated local changes.
- Do not force-push or rewrite shared history unless the user explicitly asks.
- If checks fail, stop and fix the failure before merging when it is in scope. If the failure is unrelated or external, explain the blocker clearly.
- If ArgoCD does not sync within a reasonable window, keep polling while giving periodic status updates that include the current observed revision/image and health/sync state.

## Lease/Glimmung

- If you had the /test skill called previously, or if you took a lease using the glimmung mcp tool to utilize a test slot, return the lease and allow glimmung to handle the test environment cleanup.
