---
name: test
description: Placeholder test skill for validating session skill loading and invocation
---

# /test

This skill is invoked when you have finished writing code, and it is time to test it. Usually the user wants to utilize nelsong6/glimmung for this, but some features need to be done ad-hoc. As such, there is an mcp tool to grab a lease on a test environment slot, which grabs an available test environment number, automatically provisions it. It also gives you the URL of a container running playwright dedicated to testing.

When it's up, you are to hot-swap code into the test environment. We want to save time, so don't do full builds if you can avoid it.

If the user reviews the test site and has suggestions/improvements, be sure to continue collaborating with the user by implementing their changes and hot-swapping into the test env as default behavior. The user is counting on you to make this a collaboration, and making your code changes feel alive by making them accessible in the test environment is how we accomplish that.

As you hot swap, it's important you push commits to the remote branch as well. That's just as a backup in case the pod goes down.
