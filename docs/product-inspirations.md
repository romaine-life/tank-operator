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
- User-visible run state comes from durable turn events, not local optimism.
  A control such as Stop is only complete when the durable terminal event
  arrives.
- Provider-specific event streams are adapter inputs. The frontend renders the
  Tank conversation protocol, not raw provider wire formats.
- Work delivery should use a real command/event fabric. Browser polling,
  process memory fanout, and database polling are not the normal live path for
  app-managed GUI chat.
- Session-pod death is a session lifecycle boundary. Durable messaging covers
  browser disconnects, orchestrator rollouts, and runner-process restarts while
  the same session pod is alive; it does not promise to resurrect an `emptyDir`
  workspace after the pod is gone.

When these constraints conflict with a quick local fix, prefer the
inspiration-aligned architecture and delete the old path in the same migration.
