# mcp-tank — agent notes

This legacy stdio server is not registered in default session pods. The
canonical sibling-session surface is the HTTP `tank-operator` MCP server from
`nelsong6/mcp-tank-operator`; see [README.md](README.md) before changing this
package.

When extending: keep the surface small and orchestration-focused. Anything
that feels like "the agent's own work" (running a build, reading code,
posting a PR comment) belongs in another MCP server, not here.
