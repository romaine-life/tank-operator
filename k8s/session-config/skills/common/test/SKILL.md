---
name: test
description: Reserve and use a Glimmung test environment for validating the current session's work, using repo-specific hot-swap guidance when available
---

# /test

This skill is invoked when you have finished writing code, and it is time to
test it in a Glimmung environment.

Starting test means any rollout workflow marker for this session is no longer
active. Tank keeps the test and rollout workflow states mutually exclusive in
the durable session row; the test environment pill means the test workflow has
started, not that a Glimmung slot still physically exists.

The invocation may include extra text after `/test` or `$test`. Treat that text as the user's immediate test request or issue context, and carry it into the environment setup plan. It is possible the start of the entire conversation is an invocation of the test skill and an issue statement.

Reserve a test slot with the Glimmung MCP `checkout_test_slot` tool:

- Determine the Glimmung project from the user's request, the current git
  remote, or the repo-specific guide below. Do not default to `tank-operator`
  unless the current work is actually for that repo.
- Pass the current Tank session id as `tank_session_id`. In SDK runner sessions this is available as the `SESSION_ID` environment variable.
- Use `mode: "provision"` by default. Use `mode: "clean_slate"` only when the user asks for a reset or when the existing slot state is clearly invalid.
- Put any invocation details, branch name, validation target, or issue context into `phase_inputs`.

The checkout response provides the assigned slot and validation URL. If Tank's UI did not update automatically, call the Tank MCP `set_test_environment` tool with the same session id, `active: true`, the slot index, and the URL.

When the environment is up, hot-swap code into the test environment. We want to
save time, so don't do full image builds if you can avoid it.

Use the Glimmung `inspect_browser_url` tool for browser validation and screenshots when appropriate. If you place screenshots in the session, they should go under workspace/screenshots.

You are free to come up with a test case for the feature. This is not mandatory, but if you assess that you have tools to craft a test case, do so. It is also acceptable to say that a feature is not easy to test, or tools are missing.

Once the environment is up, if your tests provide important feedback, you can iterate on any improvements and test them as well. However, the feature is not done when you deem it so. The test environment is explicitly so the user can validate the changes to their satisfaction. This is a very important step when collaborating with coding agents, because the code is not always transparent to the user, but the user experience is.

## Repo-specific hot-swap guidance

Before hot-swapping, look for a repo guide under this skill's local
`references/repos/` directory. Use the current repo name as the primary key
and, when needed, the owner/repo pair from `git remote -v` to disambiguate.
Examples:

- `references/repos/tank-operator.md`
- `references/repos/glimmung.md`

If a matching guide exists, read it before choosing a hot-swap path. That guide
owns project-specific assumptions such as Glimmung project names, MCP contract
tools, artifact kinds, pod selectors, copy paths, restart strategy, and
diagnostics.

If no repo guide exists, use the generic Glimmung flow:

- infer or ask for the Glimmung project
- inspect any available hot-swap contract/tooling for that project
- prefer Glimmung MCP hot-swap tools over manual `kubectl`
- use the fastest faithful live update for the changed artifact
- record what was applied and how it was verified

If the repo looks like it will need repeated testing, add a concise guide under
`references/repos/` rather than expanding this main skill with project-specific
details.

If the user reviews the test site and has suggestions/improvements, be sure to continue collaborating with the user by implementing their changes and hot-swapping into the test env as default behavior. The user is counting on you to make this a collaboration, and making your code changes feel alive by making them accessible in the test environment is how we accomplish that.

As you hot swap, push commits to the remote branch as well. That's a backup in case the pod goes down. You should also get latest from main and merge it into your branch.

Open a draft PR from the branch immediately. This kicks off builds, and makes it so as we iterate and hot swap code while simultaneously pushing code remotely, github CI will create builds from the code. These builds are able to be used when the PR completes for prod, so having them ready early means the PR is much faster to deploy. You should be checking if PR needs to have merge conflicts resolved. If they're unresolvable, that's cause to stop and get input, because the proposed fix needs to be adapted to main.

When testing is complete or the user no longer needs the environment, call the Glimmung MCP `return_test_slot` tool with the project and slot index or slot name. Include `tank_session_id` so Tank clears the GUI test pill.
