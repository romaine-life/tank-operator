# tank-operator test-slot validation

Project: `tank-operator`

Provisioning is deterministic and server-side: once the branch is pushed,
CI-green, mergeable, and current with `main`, Tank's Test workflow checks out a
slot and deploys the CI-built image for the pushed commit ref for you. Do not
check out or deploy by hand.

After the slot is provisioned, verify the slot's public health endpoint:

```text
https://tank-operator-slot-N.tank.dev.romaine.life/healthz
```

For UI changes, use `inspect_browser_url` against the slot URL and keep the
workspace screenshot path as evidence.
