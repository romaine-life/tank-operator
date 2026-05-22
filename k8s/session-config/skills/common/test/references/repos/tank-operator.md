# tank-operator

Use `project: "tank-operator"` when reserving Glimmung test slots for this
repo.

Always read the current contract first with
`get_test_slot_hot_swap_contract(project: "tank-operator")`. Do not rely on
hardcoded paths, pod selectors, container names, restart commands, or artifact
locations from this guide.

Prefer Glimmung MCP hot-swap tools when they support the artifact being tested.
Use `apply_test_slot_hot_swap` for supported artifact kinds, passing the pushed
git ref and checked-out slot. Record the result with
`record_test_slot_hot_swap` when the tool does not already do so.

If the MCP hot-swap tools do not cover the artifact, choose the fastest faithful
slot update for the change:

- frontend/static change: build/copy static assets according to the current
  contract
- backend change: build/copy/restart according to the current contract
- runner change: use the contract's runner hot-swap path or MCP tool
- ConfigMap/chart/session-launcher change: patch or redeploy the slot resource
  that actually feeds newly created pods, then create a fresh pod/session to
  verify the generated runtime state

Manual `kubectl` copy/restart commands are fallback implementation details, not
the default workflow. If using manual commands, verify the result in the live
slot and record concise diagnostics.
