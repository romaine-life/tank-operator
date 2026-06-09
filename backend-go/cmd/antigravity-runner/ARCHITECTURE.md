# Antigravity Runner Architecture (Our Custom localharness)

This directory implements the runner (harness) that hosts and executes the Google `agy` CLI binary for `antigravity_gui` chat sessions.

## The Core Constraint: Why a PTY is Required

1. **No direct API**: The `agy` CLI binary does not expose a WebSocket or Protobuf server on its own.
2. **Closed-Source `localharness`**: The official `localharness` binary shipped inside the `google-antigravity` Python SDK is closed-source and only supports GCP Service Accounts. It cannot be used because our sessions require consumer OAuth authentication (Google One) to bypass billing requirements.
3. **Terminal Emulation (PTY)**: Since we must run the `agy` CLI binary, we are forced to emulate a human operator. `agy` uses interactive terminal features (e.g. bubbletea UI) and will hang or fail if run via standard piped I/O. We wrap it in a Pseudo-Terminal (PTY) using `github.com/creack/pty` to fool it into believing a real human is running it in a terminal.

---

## Architectural Event Flow

Our runner acts as the adapter layer (the harness) between the cluster's NATS JetStream session bus and the interactive `agy` process:

```
                  +--------------------------+
                  |   Tank Go Orchestrator   |
                  +-------------+------------+
                                | NATS Command
                                v
               +----------------+----------------+
               |  Go Harness (antigravity-runner) |
               +---|--------------------------^--+
                   | PTY stdin                | fsnotify
                   v                          | transcript_full.jsonl
               +---|--------------------------+--+
               |            agy CLI              |
               +---------------------------------+
```

1. **Prompt Ingestion**: The runner consumes `CommandSubmitTurn` from NATS, writes the prompt followed by a carriage return (`\r`) to `agy`'s PTY standard input.
2. **Interactive Bypasses**: The runner seeds `onboarding.json` and theme settings to both the legacy and new config directories during pod bootstrap. It monitors PTY stdout to automatically click through any unexpected Terms of Service screen.
3. **Transcript Scraping**: `agy` writes JSON-lines steps to `transcript_full.jsonl`. The runner tails this file via `fsnotify`.
4. **Completion**: When the runner parses a completed `PLANNER_RESPONSE` step, it extracts the final answer, publishes `assistant_message.created` and `turn.completed` events back to NATS, and waits for the next turn.

---

## WARNING FOR FUTURE DEVELOPERS (AND AGENTS)

* **DO NOT** attempt to remove the PTY wrapper.
* **DO NOT** attempt to send binary Protobuf messages directly to `agy`'s standard input. `agy` does not speak Protobuf on stdin.
* **DO NOT** assume `localharness` is present or can be used. We must run the raw `agy` binary and act as the harness ourselves.
