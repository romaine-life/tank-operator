# Auth And Streams Contract

This contract applies to browser auth, protected fetches, EventSource streams,
terminal WebSocket auth carriers, stream tickets, token refresh, and
cross-service service-principal calls.

## Product Model

Authentication should be boring and recoverable. Streams should be durable
followers, not private state channels. A valid user should not need to refresh
the page to turn a stale credential, missed wake, or reconnect into correct
product state.

## Sources Of Truth

- auth.romaine.life JWTs own caller identity and role.
- `/api/auth/me` owns Tank's current acceptance decision for the browser.
- `profiles` owns per-user profile and GitHub installation state.
- Stream tickets are short-lived carriers for browser-native EventSource only.
- Durable feature tables and event ledgers own product state; streams deliver
  changes.

## Migration Rules

- Tank must not mint local browser JWTs or maintain a Tank-local JWKS.
- Native EventSource must not carry bearer tokens in query strings.
- WebSocket query-token carriers are allowed only where browser APIs cannot
  set `Authorization`.
- Do not keep unauthenticated fallback routes, compatibility auth paths, or raw
  API URLs for browser-native protected resources.
- Auth fixes must not be accepted as live-state fixes unless they prove the
  feature cursor followers converge.

## Live Behavior

- Protected fetches use the current auth.romaine.life bearer token and recover
  from stale stored tokens through the bootstrap/refresh path.
- Browser EventSource streams mint scoped opaque stream tickets through normal
  bearer auth before opening.
- Stream handlers authenticate the ticket, bind it to the requested stream
  kind and scope, and then follow durable state from the requested cursor.
- An open stream must emit later persisted events without requiring the browser
  to reconnect.
- Heartbeat, wake, reconnect, and visibility transitions must drain durable
  events until caught up.

## Failure And Recovery

- Expired or invalid browser tokens must produce explicit auth recovery, not a
  silently stale UI.
- Expired stream tickets should fail before stream open or force reconnect with
  a newly minted ticket.
- Unknown stream cursors trigger explicit resync.
- Service-principal calls must carry actor identity where user scoping is
  required.

## Observability

- Metrics must separate auth bootstrap failure, bearer verification failure,
  stream-ticket mint failure, ticket validation failure, stream open, stream
  reconnect, resync, heartbeat, emitted event, and cursor lag.
- Logs for auth failures should include route, stream kind, and scope without
  logging tokens.
- User-visible stale-state reports must be diagnosable by comparing durable
  tail, stream cursor, and browser-applied cursor.

## Acceptance Checks

- EventSource URL construction uses stream tickets and never bearer query
  strings.
- Stream tickets are scoped and single-purpose enough to prevent cross-stream
  or cross-session use.
- A stale stored JWT refreshes before protected fetches and ticket minting.
- With a stream already open, later durable events are emitted without
  reconnect.
- Unknown cursor and invalid ticket paths produce explicit resync/auth failure
  behavior rather than silent gaps.
