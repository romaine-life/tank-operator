# glimmung test-slot validation

Project: `glimmung`

Provisioning is deterministic and server-side: once the branch is pushed,
CI-green, mergeable, and current with `main`, Tank's Test workflow checks out a
slot and deploys the branch's CI-built image for you. Do not check out or deploy
by hand. Verify the deployed API or dashboard surface directly from the slot URL
and capture browser evidence when the change is visible.
