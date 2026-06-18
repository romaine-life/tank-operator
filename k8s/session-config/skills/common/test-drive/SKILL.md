---
name: test-drive
description: Validate your changes against an already-provisioned Glimmung test slot — exercise the feature and capture evidence (the slot is created for you, you do NOT reserve one)
---

# /test-drive

Use this when a test environment for your changes is **already running** and you
need to validate against it. Tank provisions the slot deterministically
(zero-LLM) and then wakes you with the live URL — your job is only to validate,
not to provision.

**Do NOT reserve, check out, lease, or hot-swap a slot.** A slot already exists,
is deployed with your branch, and is reachable at the URL you were given. The
old "reserve a slot yourself" behavior is retired: provisioning is owned by
Tank's test-slot gate (the "Create test slot and test" button), and you re-enter
only after it succeeds. If you believe no slot exists, say so and stop — do not
fall back to provisioning one.

If you were woken with a URL, use it. Otherwise the running slot URL is on the
session's test-state pill; if you genuinely cannot find one, report that you
have no slot to drive instead of creating one.

Drive a feature validation pass against the live slot:

- Read the repo's testing contract (`docs/testing.md`, package scripts, make
  targets, or repo-specific skill references) and run the narrowest faithful
  automated checks before broader suites.
- Identify the behavior you changed and exercise it end to end against the
  running slot through realistic API calls, UI interactions, background jobs, or
  CLI commands as appropriate for the repo.
- Use Playwright for browser-visible behavior, pointed at the slot URL. Capture
  screenshots or traces that show the important before/after or final verified
  state.
- Store evidence under `/workspace/screenshots/` or another obvious workspace
  path when possible, and mention those paths in the final answer or PR
  evidence.
- If API and UI state both matter, verify both. Do not treat a passing unit test
  as enough when the feature is user-visible.
- Iterate on failures: fix the code, let the governed publish flow push it, and
  re-run the relevant checks against the slot once the new commit is deployed.
  Do not provision a fresh slot to pick up a fix.

Report concrete findings: what you checked, what worked, what didn't, with
evidence. Be explicit about residual risk. If the feature cannot be faithfully
driven because required credentials, data, browsers, or MCP tools are missing,
say exactly what is missing and still run the best local or API-level checks
available against the running slot.
