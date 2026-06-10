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
2. **Interactive Bypasses**: The launcher (`antigravity-container/antigravity-runner-launch.sh`) seeds `onboarding.json` and theme settings to both the legacy and new config directories during pod bootstrap, so `agy` never presents onboarding/consent screens at runtime. The runner does not script the TUI: the PTY reader only drains output (agy blocks if the PTY buffer fills) and mirrors it to pod logs. If a new interactive screen appears, extend the seeded config files — do not add keystroke replay. The retired ToS auto-accept (PTY-stdout sniffing + replayed arrow/enter keys) raced real turn input and broke on TUI copy changes; its reintroduction is blocked by `TestPTYRunnerArchitectureConstraint` and `scripts/check-removed-chat-runtime.mjs`.
3. **Transcript Scraping**: `agy` writes JSON-lines steps to `transcript_full.jsonl`. The runner tails this file via `fsnotify`.
4. **Completion**: When the runner parses a completed `PLANNER_RESPONSE` step, it extracts the final answer, publishes `assistant_message.created` and `turn.completed` events back to NATS, and waits for the next turn.

---

## Why a Persistent Process (not `agy -p` per turn)

`agy` has a one-shot print mode (`agy -p`), and Tank's first chat runtime (the
May 2026 Python `exec_proxy`, removed in #437 "Remove legacy chat runtime") ran
Claude in the equivalent shape: spawn one headless process per run, read its
output, let it exit. The Antigravity GUI runner deliberately did not repeat
that shape. One `agy` process starts per session pod and stays alive across
turns, because:

1. **Per-spawn bootstrap cost.** Every `agy` start re-pays CLI init plus the
   auth handshake; first-ready takes up to 2 minutes (`waitForCliReady`), and
   `agy -p` enforces a ~30s auth timeout (see the antigravity notes in
   `backend-go/cmd/tank-operator/handlers_terminal.go`). Per-turn spawning
   turns that into per-turn latency and a per-turn failure mode.
2. **Conversation continuity.** The resident process holds the conversation.
   One-shot turns would depend on `agy` resume semantics that have no
   documented contract.
3. **Mid-turn control.** Tank's Stop path delivers SIGINT to the live process
   (`activeProcess.interrupt`) and must resolve in a durable
   `turn.interrupted`. A spawn-per-turn model has no stable target for
   interrupts, and the Tank Agent Runners contract requires exactly one
   durable terminal per turn.
4. **Native long-lived behaviors.** `agy` parks native timers/background work
   between turns (see schedule parking in
   `docs/features/agent-runners/capabilities.md`). Those behaviors only exist
   while the process lives.
5. **One-shot mode buys nothing here.** Even in print mode, `agy` performs the
   same auth/bootstrap dance (the 30s auth timeout above), the no-TTY hang
   risk documented above has not been cleared for `-p`, and the transcript
   files would still be the output contract. One-shot mode gives up the
   warm-state and control benefits without removing any of the PTY/seeding
   machinery.

This mirrors where the other providers landed: `claude-runner` holds one
SDK-spawned `claude` process (stream-json stdio) for the pod lifetime, and
`codex-runner` holds one `codex app-server` (JSON-RPC stdio). Antigravity sits
one rung lower on the interface ladder — PTY-driven input plus structured
transcript-file output — only because the structured front door
(`localharness`) is closed-source and GCP-Service-Account-only. Note that
`localharness` is not a cleverer way to drive the `agy` TUI: it is the same
agent loop compiled as a headless server with an RPC front end, so no TUI
exists in its path at all.

Revisit this if any of the following ship: consumer-OAuth support in
`localharness`, an open-source harness, or a documented headless/structured
mode in `agy`. Any of those should replace the PTY harness the same way the
codex exec transport was retired once the app-server transport could field
`request_user_input`.

---

## Process Death Is Session-Terminal (No Revival)

When the `agy` process exits — crash, OOM, or a Stop whose SIGINT turned out
to be fatal — **the session is done by design**. This is a deliberate product
decision (2026-06-10): restarting agy in place would resume the chat with a
fresh process that has lost the conversation (silent amnesia), and a
container restart via kubelet does the same thing implicitly. Neither is
acceptable as silent behavior, and revival is explicitly not part of the
architecture. If process death turns out to be frequent,
`tank_antigravity_runner_process_exit_total` and
`tank_session_provider_fatal_total` are the "how often" — design revival
deliberately at that point instead of inheriting it from a restart policy.

Mechanics, in order:

1. A `cmd.Wait()` supervisor goroutine observes the exit and closes the
   `activeProcess.exited` channel.
2. An in-flight turn resolves through the `exitedChan()` arm of
   `handleSubmitTurn`'s select: durable `turn.interrupted` if a Stop was in
   flight, otherwise `turn.failed{reason:"provider_process_exited"}`. The
   command is acked only after the terminal publishes.
3. The runner reports `POST /api/internal/sessions/{id}/provider-fatal`
   (projected SA token, self-session only). The orchestrator moves the
   session row to `Failed` through the same RowWriter transition pod death
   uses, so sidebar/activity/UI gating behave identically.
4. The runner stays alive but **inert**: subsequent `submit_turn` commands
   drain immediately to durable
   `turn.failed{reason:"provider_process_unavailable"}` instead of stranding
   un-acked in JetStream. The runner must NOT exit — a container exit would
   trigger kubelet's restart policy and relaunch agy with amnesia, which is
   exactly the revival this design forbids.

Two related liveness bounds share the same select:

- **Submit-ack watchdog** (`ANTIGRAVITY_SUBMIT_ACK_TIMEOUT_MS`, default 60s):
  the prompt write into the PTY is fire-and-forget, so if no transcript
  record at all appears within the window (the `USER_EXPLICIT` echo is the
  usual first signal), the turn fails durably as `prompt_not_accepted`.
  Deliberately no auto-retry: re-writing the prompt double-executes if agy
  did receive the first write.
- **Interrupt grace** (`ANTIGRAVITY_INTERRUPT_GRACE_MS`, default 10s): a Stop
  SIGINTs agy, but if agy neither settles a DONE planner response nor exits
  within the grace window, the runner forces the durable `turn.interrupted`
  anyway (mirroring codex-runner's "continue with durable Stop terminal").
  Interrupts with no active turn are ignored — SIGINTing an idle agy would
  be a session-terminal event for no reason.

---

## WARNING FOR FUTURE DEVELOPERS (AND AGENTS)

* **DO NOT** attempt to remove the PTY wrapper.
* **DO NOT** attempt to send binary Protobuf messages directly to `agy`'s standard input. `agy` does not speak Protobuf on stdin.
* **DO NOT** assume `localharness` is present or can be used. We must run the raw `agy` binary and act as the harness ourselves.
* **DO NOT** re-add PTY-stdout sniffing or keystroke replay for onboarding/consent screens. Seed the config files in the launcher instead.
