---
name: test
description: Reserve and use a Glimmung test environment for validating the current session's work, using repo-specific hot-swap guidance when available
---

# /test

This skill is invoked to add a test surface for the issue being discussed. If it is invoked prior to code being written, this is your signal to make the code changes. If the code is already written, move to the next step.

Starting test means any rollout workflow marker for this session is no longer active. Tank keeps the test and rollout workflow states mutually exclusive in the durable session row; the test environment pill means the test workflow has started, not that a Glimmung slot still physically exists.

The invocation may include extra text after `/test` or `$test`. Treat that text as the user's immediate test request or issue context, and carry it into the environment setup plan. It is possible the start of the entire conversation is an invocation of the test skill and an issue statement.

The invocation of this skill is a signal to make sure code is written to address the user's stated objective. If you were asked to only diagnose previously, this skill is the signal to make code changes. There is no point to following this skill if there are no code changes.

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

Use the Glimmung `inspect_browser_url` tool for browser validation and screenshots when appropriate. When you cite an inspection screenshot as evidence, do not cite only the Glimmung artifact URL. Also place a copy in the caller session workspace under `/workspace/screenshots/` and include that local path in the final answer or PR evidence. Prefer the tool's workspace screenshot option when available; if the tool cannot save a local copy, use a local download/copy workaround or state explicitly that the local copy failed.

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

**`static` (frontend/webapp) changes are the common trap.**
`apply_test_slot_hot_swap` does **not** support `artifact_kind: static` — it
covers `backend`, `agent_runner`, and `codex_runner`. A `static` rejection is
**expected**; it is not a dead-end and is not a signal that a new Glimmung
feature is needed. The norm for a webapp is a manual `kubectl cp` of the built
assets into the app pod's static-override dir: build the frontend → clear the
override dir → `kubectl cp dist/. <pod>:<override-dir> -c <container>` on every
ready app pod → verify the served asset hash matches your local build (served
live, usually no restart). `references/repos/tank-operator.md` and
`references/repos/chess-tactics.md` are worked examples — mirror the closest one,
and confirm the override path / selector / container from the live contract
(`get_test_slot_hot_swap_contract`). Do not build or wrap the `glimmung-agent`
CLI for this; the raw `kubectl cp` is the accepted path.

If the repo looks like it will need repeated testing, add a concise guide under
`references/repos/` rather than expanding this main skill with project-specific
details.

If the user reviews the test site and has suggestions/improvements, be sure to continue collaborating with the user by implementing their changes and hot-swapping into the test env as default behavior. The user is counting on you to make this a collaboration, and making your code changes feel alive by making them accessible in the test environment is how we accomplish that.

As you hot swap, push commits to the remote branch as well. That's a backup in case the pod goes down. You should also get latest from main and merge it into your branch.

Open a draft PR from the branch immediately. After the PR exists, call the Tank
MCP `set_pull_request_link` tool with the current session id and PR URL so the
Tank UI can link to it from the test workflow controls. Opening the PR is also
the normal image-build path for same-repo work: GitHub CI builds the relevant
images and, when the repo's workflow is trusted to use registry credentials,
pushes reusable fingerprint/proof images to the registry. Those pushed images
are useful later for merge/deploy because the expensive build has already been
primed. Keep hot-swapping for the fast live-test loop, but do not treat
hot-swap as a substitute for opening the PR and letting CI build the image
artifacts.

You should be checking if PR needs to have merge conflicts resolved. If they're
unresolvable, that's cause to stop and get input, because the proposed fix
needs to be adapted to main. You also need to make sure all checks are green
before handing the test environment off.

When testing is complete or the user no longer needs the environment, call the Glimmung MCP `return_test_slot` tool with the project and slot index or slot name. Include `tank_session_id` so Tank clears the GUI test pill.
