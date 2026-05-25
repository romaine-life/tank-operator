# Testing tank-operator

## Glimmung Test Slots

Tank-operator test slots are provisioned by Glimmung. Before relying on
hardcoded slot paths or pod names, read the current hot-swap contract from
Glimmung and use its hot-swap tools when they cover the artifact being tested.

Slot hostnames such as `https://tank-operator-slot-N.tank.dev.romaine.life`
are trusted auth origins through Glimmung-managed auth origins, not through a
static auth.romaine.life allowlist.

Those slot HTTPRoutes use concrete hostnames, but they attach to the shared
`tank-operator-wildcard` listener in the `tank-operator` namespace. That
single listener/certificate covers `tank.dev.romaine.life` and
`*.tank.dev.romaine.life`; slots must not create their own public cert-manager
`Certificate` or public `XListenerSet`.

## Test-Slot SPA Auth

Session pods authenticate as service principals through the projected
Kubernetes service-account token and auth.romaine.life's
`/api/auth/exchange/k8s` flow. Those tokens carry `role=service` and an
`actor_email` claim for the human owner. The SPA treats service principals as
authenticated platform callers and does not require a user-facing GitHub App
installation; the OnboardingWall is skipped for `role=service`. Do not install
the GitHub App for a service account just to run browser automation.

`role` is still the auth.romaine.life platform identity. Tank's local admin
decision is `/api/auth/me.is_admin`: a service-principal token owned by a
configured super admin keeps `role=service` and returns `is_admin=true`, so
admin browser automation sees the same Settings/Admin surfaces as that human
owner without mutating the upstream role claim.

End-to-end exchange from a session pod:

```sh
SA=$(cat /var/run/secrets/auth.romaine.life/token)
AUTH_JWT=$(curl -sS -X POST https://auth.romaine.life/api/auth/exchange/k8s \
  -H "Authorization: Bearer $SA" -H 'Content-Type: application/json' -d '{}' \
  | jq -r .token)                                       # role=service + actor_email
curl -sS https://tank-operator-slot-1.tank.dev.romaine.life/api/auth/me \
  -H "Authorization: Bearer $AUTH_JWT"                  # 200, role=service, is_admin mirrors actor
```

The same auth.romaine.life JWT powers authenticated browser automation against
slot URLs.

## Authenticated browser automation via inspect_browser_url

`inspect_browser_url` (in [`mcp-glimmung`](https://github.com/nelsong6/mcp-glimmung))
drives the slot's `slot-playwright` pod against a URL. The Playwright pod
itself holds no credentials, so anything signed-in has to come from the
caller. The tool exposes injection knobs that map directly to Playwright's
`BrowserContext` configuration:

| Param | Forwarded to | Use |
|---|---|---|
| `extra_http_headers` | `context.setExtraHTTPHeaders(headers)` | `Authorization: Bearer ...` on slot URLs that hit JSON APIs |
| `local_storage` | `addInitScript` running before every page script | SPAs that boot from `localStorage[auth-romaine-jwt]` |

Recommended pattern for the chat UI: mint the auth.romaine.life service token
above, then seed it into the SPA's localStorage. Playwright lands on the slot
URL already signed in as the service principal, and the SPA's bootstrap path
validates the token via `/api/auth/me`. Admin-only panes are available when
that response carries `is_admin=true`.

```python
inspect_browser_url(
    url="https://tank-operator-slot-1.tank.dev.romaine.life/",
    tank_session_id="<your session id>",
    local_storage={
        "https://tank-operator-slot-1.tank.dev.romaine.life": {
            "auth-romaine-jwt": AUTH_JWT,
        },
    },
)
```

This is the production-correct path. Do not work around an old "stub
`/api/auth/me` in Playwright" pattern; the backend bypass for `role=service`
is live, `is_admin` carries Tank's local admin-power decision, and the
inspector now plumbs localStorage through, so the real auth path is always
available.
