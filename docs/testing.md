# Testing tank-operator

## Glimmung Test Slots

Tank-operator test slots are provisioned by Glimmung. Before relying on
hardcoded slot paths or pod names, read the current hot-swap contract from
Glimmung and use its hot-swap tools when they cover the artifact being tested.

Slot hostnames such as `https://tank-operator-slot-N.tank.dev.romaine.life`
are trusted auth origins through Glimmung-managed auth origins, not through a
static auth.romaine.life allowlist.

## Test-Slot SPA Auth

Session pods can authenticate as service principals through the projected
Kubernetes service-account token and auth.romaine.life's
`/api/auth/exchange/k8s` flow. Those tokens carry `role=service` and an
`actor_email` claim for the human owner.

The SPA treats service principals as authenticated platform callers and does
not require a user-facing GitHub App installation. Do not install the GitHub
App for a service account just to run browser automation. If a test needs to
exercise the actual chat UI, keep the real auth token for session APIs; on
current tank-operator builds `/api/auth/me` should return `role=service` and
the UI will bypass the GitHub onboarding wall.

If you are validating an older deployed build that predates the service-role
bypass, the narrow temporary workaround is to stub only `/api/auth/me` in
Playwright so it reports a harmless non-null `installation_id`, while leaving
the real Authorization token and all session API calls untouched. Remove that
stub once the slot is running a build with the bypass.
