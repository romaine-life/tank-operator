---
name: test-drive
description: Reserve a Glimmung test environment, apply the change, and actively validate the feature with repo tests plus Playwright evidence
---

# /test-drive

Use this when the user wants the full test workflow driven by the agent, not just a held test slot.

Follow the `test` skill workflow first: reserve the right Glimmung slot, mark Tank's test environment state if needed, hot-swap the current change according to the repo-specific test contract, open a draft PR, link it back to Tank, and keep the slot available for user review.

Then drive a feature validation pass yourself:

- Read the repo's testing contract (`docs/testing.md`, package scripts, make targets, or repo-specific skill references) and run the narrowest faithful automated checks before broader suites.
- Identify the changed behavior and exercise it through realistic API calls, UI interactions, background jobs, or CLI commands as appropriate for the repo.
- Use Playwright for browser-visible behavior. Capture screenshots or traces that show the important before/after or final verified state.
- Store evidence under `/workspace/screenshots/` or another obvious workspace path when possible, and mention those paths in the final answer or PR evidence.
- If API and UI state both matter, verify both. Do not treat a passing unit test as enough when the feature is user-visible.
- Iterate on failures, hot-swap fixes back into the test slot, and rerun the relevant checks.

Be explicit about residual risk. If the feature cannot be faithfully driven because required credentials, data, browsers, or MCP tools are missing, say exactly what is missing and still run the best local or API-level checks available.
