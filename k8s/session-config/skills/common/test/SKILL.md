---
name: test
description: Reserve and use a Glimmung test environment for validating the current Tank session's work
---

# /test

This skill is invoked when you have finished writing code, and it is time to test it.

The invocation may include extra text after `/test` or `$test`. Treat that text as the user's immediate test request or issue context, and carry it into the environment setup plan. It is possible the start of the entire conversation is an invocation of the test skill and an issue statement.

Reserve a test slot with the Glimmung MCP `checkout_test_slot` tool:

- Use `project: "tank-operator"` unless the user explicitly names a different project.
- Pass the current Tank session id as `tank_session_id`. In SDK runner sessions this is available as the `SESSION_ID` environment variable.
- Use `mode: "provision"` by default. Use `mode: "clean_slate"` only when the user asks for a reset or when the existing slot state is clearly invalid.
- Put any invocation details, branch name, validation target, or issue context into `phase_inputs`.

The checkout response provides the assigned slot and validation URL. If Tank's UI did not update automatically, call the Tank MCP `set_test_environment` tool with the same session id, `active: true`, the slot index, and the URL.

When the environment is up, hot-swap code into the test environment. We want to save time, so don't do full image builds if you can avoid it. Use the Glimmung `inspect_browser_url` tool for browser validation and screenshots when appropriate.

## tank-operator hot-swap details

Read the current contract first with `get_test_slot_hot_swap_contract(project: "tank-operator")`; do not assume paths or container names. As of this skill, tank-operator test slots use:

- static source: `frontend/dist`
- static target: `/var/run/tank-operator-static-override`
- static writer container: `tank-operator`
- backend artifact: `/tmp/tank-operator-go`
- backend target: `/var/run/tank-operator-hot/tank-operator-go`
- backend app container: `tank-operator`
- backend restart: send `SIGHUP` to PID 1 in the `tank-operator` container

Important operational constraints:

- The app container mounts the static override read-write in test slots. Write static files through the `tank-operator` container; the legacy `static-writer` sidecar may exist but should not be required for hot-swap.
- The supervisor does not watch the backend artifact. It restarts the child only on `SIGHUP`.
- Do not kill `tank-operator-go` directly. The supervisor treats child exit as terminal and exits the container.
- Copy static/backend into every ready app pod unless you have intentionally scaled the test deployment down for a narrow diagnostic.
- Prefer direct `kubectl exec -i ... cat > file` or tar streams with explicit verification over blind `kubectl cp` when websocket copy streams get flaky.

Static hot-swap pattern:

```sh
n=tank-operator-slot-1
for pod in $(kubectl -n "$n" get pods -l app.kubernetes.io/name=tank-operator -o name | sed 's#pod/##'); do
  kubectl -n "$n" exec "$pod" -c tank-operator -- sh -lc \
    'mkdir -p /var/run/tank-operator-static-override && rm -rf /var/run/tank-operator-static-override/*'
  tar cf - -C frontend/dist . |
    kubectl -n "$n" exec -i "$pod" -c tank-operator -- \
      tar xf - -C /var/run/tank-operator-static-override
  kubectl -n "$n" exec "$pod" -c tank-operator -- sh -lc \
    'test -f /var/run/tank-operator-static-override/index.html'
done
```

Backend hot-swap pattern:

```sh
cd backend-go && go build -o /tmp/tank-operator-go ./cmd/tank-operator
n=tank-operator-slot-1
for pod in $(kubectl -n "$n" get pods -l app.kubernetes.io/name=tank-operator -o name | sed 's#pod/##'); do
  kubectl -n "$n" exec -i "$pod" -c tank-operator -- sh -lc \
    'cat > /var/run/tank-operator-hot/tank-operator-go.tmp &&
     chmod 0755 /var/run/tank-operator-hot/tank-operator-go.tmp &&
     mv /var/run/tank-operator-hot/tank-operator-go.tmp /var/run/tank-operator-hot/tank-operator-go' \
    < /tmp/tank-operator-go
  kubectl -n "$n" exec "$pod" -c tank-operator -- kill -HUP 1
done
kubectl -n "$n" wait --for=condition=Ready pod -l app.kubernetes.io/name=tank-operator --timeout=90s
```

If the user reviews the test site and has suggestions/improvements, be sure to continue collaborating with the user by implementing their changes and hot-swapping into the test env as default behavior. The user is counting on you to make this a collaboration, and making your code changes feel alive by making them accessible in the test environment is how we accomplish that.

As you hot swap, push commits to the remote branch as well. That's a backup in case the pod goes down.

Open a draft PR from the branch immediately. This kicks off builds, and makes it so as we iterate and hot swap code while simultaneously pushing code remotely, github CI will create builds from the code. These builds are able to be used when the PR completes for prod, so having them ready early means the PR is much faster to deploy. You should be checking if PR needs to have merge conflicts resolved. If they're unresolvable, that's cause to stop and get input, because the proposed fix needs to be adapted to main.

When testing is complete or the user no longer needs the environment, call the Glimmung MCP `return_test_slot` tool with the project and slot index or slot name. Include `tank_session_id` so Tank clears the GUI test pill.
