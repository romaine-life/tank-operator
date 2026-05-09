---
name: test
description: Placeholder test skill for validating session skill loading and invocation
---

# /test

This skill is invoked when you have finished writing code, and it is time to test it. Usually the user wants to utilize nelsong6/glimmung for this, but some features need to be done ad-hoc. As such, there is an mcp tool to grab a lease on a test environment slot, and provision it.

When it's up, you are to hot-swap code into the test environment. We want to save time, so don't do full builds. You'll want to let the user know what the url is of the test environment you set up for them. And you have access to playwright if you want to confirm visually what the feature is attempting to deliver.

For Tank-owned app repos, first reserve a Glimmung slot with the `glimmung.checkout_test_slot` MCP tool. Use the project name that matches the repo, usually `tank-operator` for `nelsong6/tank-operator`. After the lease is acquired and you know the slot index and test URL, call the `tank-operator.set_test_environment` MCP tool so the Tank GUI can light up its Test pill with the slot number and link. If the checkout response does not include a URL, infer the standby DNS URL from the project metadata when it is unambiguous, e.g. `https://tank-slot-N.tank.dev.romaine.life` for tank-operator slot `N`, and tell the user when you are making that inference.
