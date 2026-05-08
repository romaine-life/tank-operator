# Live-cluster verification: multi-user GitHub routing (#57 stage 3)

Covers the two PRs that completed #57 stage 3 in `mcp-github` and the
companion tank-operator fix:

| PR | Repo | What it does |
|----|------|--------------|
| mcp-github#6 | mcp-github | Cross-installation fallback: non-host callers can reach host-owned repos |
| mcp-github#7 | mcp-github | Audience-scoped SA token for `resolve-caller` calls |
| tank-operator#231 | tank-operator | `audiences=["tank-operator"]` in resolve-caller TokenReview |

---

## Prerequisites

All three PRs must be merged and the ArgoCD sync must have rolled out the new
images. Check with:

```bash
kubectl rollout status deployment/mcp-github -n mcp-github
kubectl rollout status deployment/tank-operator -n tank-operator
```

---

## 1  Audience-scoped projected SA token (mcp-github#7 + tank-operator#231)

### Confirm the volume is mounted

```bash
kubectl exec -n mcp-github deploy/mcp-github -c mcp-github -- \
  cat /var/run/secrets/tank-operator/token | cut -c1-40
```

You should see a JWT prefix (`eyJ…`). An `No such file` error means the
chart was not redeployed yet.

### Confirm the token's audience

The projected token has `audience: tank-operator`. Decode the payload without
verification:

```bash
kubectl exec -n mcp-github deploy/mcp-github -c mcp-github -- \
  python3 -c "
import base64, json, sys
token = open('/var/run/secrets/tank-operator/token').read().strip()
payload = token.split('.')[1]
payload += '=' * (4 - len(payload) % 4)
print(json.dumps(json.loads(base64.b64decode(payload)), indent=2))
"
```

Look for `"aud": ["tank-operator"]` (or `"aud": "tank-operator"`) in the
output. Any other audience means the volume spec or chart rollout is wrong.

### Confirm the orchestrator accepts the token

```bash
TANK_TOKEN=$(kubectl exec -n mcp-github deploy/mcp-github -c mcp-github -- \
  cat /var/run/secrets/tank-operator/token)

kubectl exec -n mcp-github deploy/mcp-github -c mcp-github -- \
  python3 -c "
import httpx, os
token = open('/var/run/secrets/tank-operator/token').read().strip()
r = httpx.get(
    'http://tank-operator.tank-operator.svc/api/internal/resolve-caller',
    params={'pod_ip': '0.0.0.0'},
    headers={'Authorization': f'Bearer {token}'},
)
print(r.status_code, r.text[:300])
"
```

Expected: `404` with body `"no session pod with IP 0.0.0.0"`. That confirms
the orchestrator accepted the SA identity (the token passed TokenReview and
the `mcp-github/mcp-github` allowlist check) before returning 404 because no
pod has that IP. A `401` or `403` means the token or allowlist is wrong.

---

## 2  Cross-installation fallback (mcp-github#6)

This verifies that a non-host user can successfully call a tool against a
host-owned repo (`nelsong6/*`) even though their `tank-operator-romaine-life`
installation has no access to it.

### Start a session as the non-host user

Log in to the SPA as `gantonski@gmail.com` (or any account that has completed
the GitHub App install flow but does **not** own `nelsong6/*`). Open a terminal
session and confirm the session pod IP:

```bash
# Inside the session pod terminal
hostname -i
```

Note the pod IP (e.g. `10.244.0.42`).

### Trigger a cross-install operation

From the Claude Code agent running inside that session, call any mcp-github
tool that targets a host-owned repo. For example:

```
list_issues owner=nelsong6 name=tank-operator
```

or via the MCP REPL:

```python
# In any agent context inside the session
mcp__github__list_issues(owner="nelsong6", name="tank-operator")
```

The call should succeed and return issues rather than a 404 or 403.

### Confirm the fallback fired

Stream mcp-github logs during the call:

```bash
kubectl logs -n mcp-github deploy/mcp-github -c mcp-github --follow | \
  grep -E "cross-install|inaccessible"
```

On the first call from that session you should see:

```
INFO cross-install fallback: installation <id> cannot serve nelsong6/tank-operator (caching 1800s)
```

### Confirm the result is cached

Make the same call a second time. The log line should **not** reappear (the
negative-access cache is hit and `for_caller_repo` returns the host minter
directly). You can also check the mcp-github logs for the absence of a repeat
`cross-install fallback` message within the 30-minute window.

### Confirm host attribution

Because the fallback uses the host's `romaine-life-app` installation, any
write (e.g. `create_branch`) on a host-owned repo made from the non-host
session will be attributed to `romaine-life-app[bot]`, not to the caller's
user-facing App. This is the expected behaviour for cross-install writes to
host repos; the caller's own installation is used for repos they own.

---

## Rollback checklist

If either feature is misbehaving after deployment:

1. **Cross-install fallback loop**: if the host-minter fallback also 404s,
   mcp-github raises the original error (not the fallback's). Check the
   caller's installation_id is non-null via
   `GET /api/internal/resolve-caller?pod_ip=<ip>` from inside the mcp-github
   pod.

2. **TokenReview 401**: check the orchestrator's `INTERNAL_API_ALLOWED_SUBJECTS`
   env var includes `mcp-github/mcp-github`; check the projected volume is
   mounted with the correct audience.

3. **No pod annotation**: `resolve-caller` returns 404 when the session pod is
   missing the `tank-operator/owner-email` annotation — this is a tank-operator
   sessions.py regression, not an mcp-github one.
