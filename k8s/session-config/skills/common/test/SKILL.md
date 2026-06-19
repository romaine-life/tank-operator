---
name: test
description: Validate the current session's work in a Glimmung test environment that Tank provisions deterministically server-side
---

# /test

This skill is invoked to add a test surface for the issue being discussed. If it is invoked prior to code being written, this is your signal to make the code changes. If the code is already written, move to the next step.

Starting test means any rollout workflow marker for this session is no longer active. Tank keeps the test and rollout workflow states mutually exclusive in the durable session row; the test environment pill means the test workflow has started, not that a Glimmung slot still physically exists.

The invocation may include extra text after `/test` or `$test`. Treat that text as the user's immediate test request or issue context, and carry it into the validation plan. It is possible the start of the entire conversation is an invocation of the test skill and an issue statement.

The invocation of this skill is a signal to make sure code is written to address the user's stated objective. If you were asked to only diagnose previously, this skill is the signal to make code changes. There is no point to following this skill if there are no code changes.

## Provisioning is deterministic and server-side — you do not reserve or deploy by hand

Test-slot provisioning is now **deterministic and zero-LLM**. Tank's Test
button / `POST /api/sessions/{id}/test-workflow/start` endpoint validates
readiness (published + CI-green + mergeable + current-with-main) and then
provisions the slot **server-side**: it checks out a Glimmung slot, deploys the
branch's CI-built image, and lights the GUI test pill. This covers every
project, not just `tank-operator`.

You therefore do **not** check out a slot, deploy an image, or set the test pill
by hand. There are no agent-facing tools for that anymore. Your job is to (1)
get the branch into a provisionable state, then (2) validate the running
environment and return the slot when done.

### 1. Get the branch ready for the deterministic gate

The gate only provisions when the branch is legitimately deployable, so make
that true:

- Make sure the code addressing the user's objective is actually written.
- Commit and push the branch (the governed `publish_current_head` path runs on
  every commit) so GitHub CI builds the proof image for the head commit. The
  deploy step uses that CI-built image — unpushed working-tree code can never
  reach a slot.
- Open a draft PR from the branch immediately so CI runs and the PR exists for
  the readiness checks. After the PR exists, call the Tank MCP
  `set_pull_request_link` tool with the current session id and PR URL so the Tank
  UI can link to it from the test workflow controls.
- Get latest from `main` and merge it into your branch, resolve any conflicts,
  and confirm all checks are green. The gate refuses a branch that is not
  published, not CI-green, not mergeable, or not current with `main`; an
  unresolvable conflict is cause to stop and get input, because the proposed fix
  needs to be adapted to main.

Determine the Glimmung project from the user's request, the current git remote,
or the repo-specific guide below. Do not assume `tank-operator` unless the
current work is actually for that repo.

### 2. Validate the running environment

Once the slot is provisioned (the test pill is active with a slot URL), validate
the change against the real running surface:

- Use the Glimmung `inspect_browser_url` tool for browser validation and
  screenshots when appropriate. When you cite an inspection screenshot as
  evidence, do not cite only the Glimmung artifact URL. Also place a copy in the
  caller session workspace under `/workspace/screenshots/` and include that local
  path in the final answer or PR evidence. Prefer the tool's workspace screenshot
  option when available; if the tool cannot save a local copy, use a local
  download/copy workaround or state explicitly that the local copy failed.
- You are free to come up with a test case for the feature. This is not
  mandatory, but if you assess that you have tools to craft a test case, do so.
  It is also acceptable to say that a feature is not easy to test, or tools are
  missing.

The feature is not done when you deem it so. The test environment is explicitly
so the user can validate the changes to their satisfaction. This is a very
important step when collaborating with coding agents, because the code is not
always transparent to the user, but the user experience is.

If your tests provide important feedback, iterate: implement the improvement,
push the commit, let CI build the new image, and re-run the deterministic test
workflow so the slot is redeployed to the new build. Pushing commits to the
remote branch is also a backup in case the pod goes down.

## Repo-specific validation guidance

Before validating, look for a repo guide under this skill's local
`references/repos/` directory. Use the current repo name as the primary key
and, when needed, the owner/repo pair from `git remote -v` to disambiguate.
Examples:

- `references/repos/tank-operator.md`
- `references/repos/glimmung.md`

If a matching guide exists, read it before choosing the validation path. That
guide owns project-specific assumptions such as Glimmung project names, health
endpoints, and visible build markers.

If no repo guide exists, use the generic flow:

- infer or ask for the Glimmung project
- open or update the PR so CI builds the proof image for the commit, and get the
  branch published, CI-green, mergeable, and current with `main`
- let the deterministic Test workflow provision the slot from that CI-built image
- verify the slot's real surface serves that build
- return the slot when validation is complete

If the repo looks like it will need repeated testing, add a concise guide under
`references/repos/` rather than expanding this main skill with project-specific
details.

You should be checking if the PR needs to have merge conflicts resolved. If
they're unresolvable, that's cause to stop and get input. You also need to make
sure all checks are green before handing the test environment off.

When testing is complete or the user no longer needs the environment, call the
Glimmung MCP `return_test_slot` tool with the project and slot index or slot
name. Include `tank_session_id` so Tank clears the GUI test pill.
