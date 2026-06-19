# Bundled Session Skills

Add bundled skills by placing files under one target directory:

- `common/<skill>/` installs into Claude and Codex sessions.
- `claude/<skill>/` installs only into Claude sessions.
- `codex/<skill>/` installs only into Codex sessions.

The Helm chart packages every file below this directory into the
`tank-session-config` ConfigMap. Session bootstrap rehydrates those files into
the agent-native skill directories before launching the agent.
