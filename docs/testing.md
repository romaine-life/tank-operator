# Testing tank-operator

## Glimmung Test Slots

Tank-operator test slots are provisioned by Glimmung. Before relying on
hardcoded slot paths or pod names, read the current hot-swap contract from
Glimmung and use its hot-swap tools when they cover the artifact being tested.

Slot hostnames such as `https://tank-operator-slot-N.tank.dev.romaine.life`
are trusted auth origins through Glimmung-managed auth origins, not through a
static auth.romaine.life allowlist.

## Test-Slot SPA Auth

Session pods authenticate as service principals through the projected
Kubernetes service-account token and auth.romaine.life's
`/api/auth/exchange/k8s` flow. Those tokens carry `role=service` and an
`actor_email` claim for the human owner. The SPA treats service principals as
authenticated platform callers and does not require a user-facing GitHub App
installation — the OnboardingWall is skipped for `role=service`. Do not
install the GitHub App for a service account just to run browser automation.

End-to-end exchange from a session pod:

```sh
SA=$(cat /var/run/secrets/auth.romaine.life/token)
AUTH_JWT=$(curl -sS -X POST https://auth.romaine.life/api/auth/exchange/k8s \
  -H "Authorization: Bearer $SA" -H 'Content-Type: application/json' -d '{}' \
  | jq -r .token)                                       # role=service + actor_email
SESSION_JWT=$(curl -sS -X POST https://tank-operator-slot-1.tank.dev.romaine.life/api/auth/exchange \
  -H 'Content-Type: application/json' -d "{\"auth_jwt\":\"$AUTH_JWT\"}" \
  | jq -r .token)                                       # tank-operator session JWT
curl -sS https://tank-operator-slot-1.tank.dev.romaine.life/api/auth/me \
  -H "Authorization: Bearer $SESSION_JWT"               # 200, role=service
```

The same minted JWT is what powers authenticated browser automation against
slot URLs — see the next section.

## Authenticated browser automation via inspect_browser_url

`inspect_browser_url` (in [`mcp-glimmung`](https://github.com/nelsong6/mcp-glimmung))
drives the slot's `slot-playwright` pod against a URL. The playwright pod
itself holds no credentials, so anything signed-in has to come from the
caller. The tool exposes three injection knobs that map directly to
Playwright's `BrowserContext` configuration:

| Param | Forwarded to | Use |
|---|---|---|
| `cookies` | `context.addCookies(cookies)` | Session cookies; the tank-operator `auth_token` cookie is the dominant case |
| `extra_http_headers` | `context.setExtraHTTPHeaders(headers)` | `Authorization: Bearer …` on slot URLs that hit JSON APIs |
| `local_storage` | `addInitScript` running before every page script | SPAs that boot from `localStorage[tank-operator-jwt]` |

Recommended pattern for the chat UI: produce the session JWT with the
exchange above, then pass it as the `auth_token` cookie. Playwright lands on
the slot URL already signed in as the service principal, and the SPA's
bootstrap path validates the existing JWT via `/api/auth/me` instead of
hitting the silent-exchange flow.

```python
inspect_browser_url(
    url="https://tank-operator-slot-1.tank.dev.romaine.life/",
    tank_session_id="<your session id>",
    cookies=[{
        "name": "auth_token",
        "value": SESSION_JWT,                # minted via /api/auth/exchange above
        "url": "https://tank-operator-slot-1.tank.dev.romaine.life",
        "httpOnly": True,
        "secure": True,
        "sameSite": "Lax",
    }],
)
```

This is the production-correct path. Do not work around an old "stub
`/api/auth/me` in Playwright" pattern; the backend bypass for
`role=service` is live and the inspector now plumbs the cookie through, so
the real auth path is always available.
