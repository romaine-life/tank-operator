---
name: test
description: Placeholder test skill for validating session skill loading and invocation
---

# /test

This skill is invoked when you have finished writing code, and it is time to test it. Usually the user wants to utilize nelsong6/glimmung for this, but some features need to be done ad-hoc. As such, there is an mcp tool to grab a lease on a test environment slot, and provision it.

When it's up, you are to hot-swap code into the test environment. We want to save time, so don't do full builds. You'll want to let the user know what the url is of the test environment you set up for them. And you have access to playwright if you want to confirm visually what the feature is attempting to deliver.

