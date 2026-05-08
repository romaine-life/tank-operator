# mcp-tank — agent notes

This repo's tools are how a session pod talks to its sibling sessions.
See [README.md](README.md) for what each tool does.

When extending: keep the surface small and orchestration-focused. Anything
that feels like "the agent's own work" (running a build, reading code,
posting a PR comment) belongs in another MCP server, not here.
