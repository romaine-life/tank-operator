# chess-tactics test-slot validation

Project: `chess-tactics`

Provisioning is deterministic and server-side: once the branch is pushed,
CI-green, mergeable, and current with `main`, Tank's Test workflow checks out a
slot and deploys the branch's CI-built image for you. Do not check out or deploy
by hand. Verify the slot's browser-visible app surface with `inspect_browser_url`.
