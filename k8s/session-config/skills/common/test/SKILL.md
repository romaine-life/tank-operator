---
name: test
description: Reserve and use a Glimmung test environment for validating the current Tank session's work
---

# /test

This skill is invoked when you have finished writing code, and it is time to test it. Usually the user wants to utilize nelsong6/glimmung for this, but some features need to be done ad-hoc.

The invocation may include extra text after `/test` or `$test`. Treat that text as the user's immediate test request or issue context, and carry it into the environment setup plan.

Reserve a test slot with the Glimmung MCP `checkout_test_slot` tool:

- Use `project: "tank-operator"` unless the user explicitly names a different project.
- Pass the current Tank session id as `tank_session_id`. In SDK runner sessions this is available as the `SESSION_ID` environment variable.
- Use `mode: "provision"` by default. Use `mode: "clean_slate"` only when the user asks for a reset or when the existing slot state is clearly invalid.
- Put any invocation details, branch name, validation target, or issue context into `phase_inputs`.

The checkout response provides the assigned slot and validation URL. If Tank's UI did not update automatically, call the Tank MCP `set_test_environment` tool with the same session id, `active: true`, the slot index, and the URL.

When the environment is up, hot-swap code into the test environment. We want to save time, so don't do full builds if you can avoid it. Use the Glimmung `inspect_browser_url` tool for browser validation and screenshots when appropriate.

If the user reviews the test site and has suggestions/improvements, be sure to continue collaborating with the user by implementing their changes and hot-swapping into the test env as default behavior. The user is counting on you to make this a collaboration, and making your code changes feel alive by making them accessible in the test environment is how we accomplish that.

As you hot swap, push commits to the remote branch as well. That's a backup in case the pod goes down.

When testing is complete or the user no longer needs the environment, call the Glimmung MCP `return_test_slot` tool with the project and slot index or slot name. Include `tank_session_id` so Tank clears the GUI test pill.
