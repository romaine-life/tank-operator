# tank-operator test-slot validation

Project: `tank-operator`

Use Glimmung `deploy_image_to_test_slot` after the branch is pushed and CI has
built the proof image. Pass the checked-out slot and the pushed branch or
commit ref.

After deployment, verify the slot's public health endpoint:

```text
https://tank-operator-slot-N.tank.dev.romaine.life/healthz
```

For UI changes, use `inspect_browser_url` against the slot URL and keep the
workspace screenshot path as evidence.
