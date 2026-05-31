# Product Inspirations

Tank borrows product and architecture ideas from these open source projects.

## Messaging

- Mattermost: <https://github.com/mattermost/mattermost>
- Zulip: <https://github.com/zulip/zulip>
- Element Web: <https://github.com/element-hq/element-web>
- Matrix Synapse: <https://github.com/element-hq/synapse>
- Rocket.Chat: <https://github.com/RocketChat/Rocket.Chat>

## Cloud Development Environments

- Coder: <https://github.com/coder/coder>

## Applied Architecture Constraints

Tank follows the same split used by mature messaging and cloud-environment
systems: durable history is separate from live delivery, and client connection
state is separate from work state.

- Durable conversation history must be replayable from the server without an
  open browser connection.
- Live transport should wake clients and runners; it should not be the only
  place product state exists.
- Reconnect resumes from a cursor over persisted events. Unknown cursors force
  an explicit resync instead of silently skipping a gap.
- Normal session navigation lands at the live tail. Historical positions are
  explicit user intent only: copied message links and manual back-pagination
  resolve through durable cursors, never through browser-local saved scroll
  position.
- User-visible run state comes from durable turn events, not local optimism.
  A control such as Stop is only complete when the durable terminal event
  arrives.
- Session startup status shown in the chat transcript is stored as durable
  `session.status` entries in `session_events`. The `sessions` row drives
  sidebar/session state, but transcript rows are rendered only from the
  conversation ledger; browser-local startup drafts are not a transcript
  source.
- A first prompt typed on the splash screen is written to the durable
  conversation ledger before startup status, so the visible transcript begins
  with the user's message instead of client-only optimism. If attachments need
  pod-local paths, the user row is still written at creation time and the
  executable turn submission reuses that durable row after upload.
- Provider-specific event streams are adapter inputs. The frontend renders the
  Tank conversation protocol, not raw provider wire formats.
- The main transcript is a settled conversation projection, not the default sink
  for provider events. Assistant prose enters it only through explicit
  final-answer promotion on a successful turn; tool output, reasoning, progress,
  failed work, and stopped work stay in Turn activity unless the protocol
  explicitly promotes them.
- Transcript deep links resolve through the durable conversation ledger. A
  copied message URL may name the rendered transcript `timeline_id`, but the
  server translates it to an `order_key` and returns a bounded page around
  that persisted cursor; the browser DOM is never the source of truth. Sidebar
  visibility is not a transcript-history boundary: if the durable session row
  and ledger remain, owned/admin copied links stay resolvable.
- Work delivery should use a real command/event fabric. Browser polling,
  process memory fanout, and database polling are not the normal live path for
  app-managed GUI chat.
- Session-pod death is a session lifecycle boundary. Durable messaging covers
  browser disconnects, orchestrator rollouts, and runner-process restarts while
  the same session pod is alive; it does not promise to resurrect an `emptyDir`
  workspace after the pod is gone.

When these constraints conflict with a quick local fix, prefer the
inspiration-aligned architecture and delete the old path in the same migration.
