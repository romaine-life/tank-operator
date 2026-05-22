# Artifacts And Files Contract

This contract applies to files produced by sessions, raw file/image access,
copy/download controls, artifact links, and browser display of protected
session-owned resources.

## Product Model

Artifacts and files are part of the session workspace experience. The user
should be able to inspect and retrieve outputs without leaking protected URLs,
depending on stale browser auth, or confusing a transient preview with durable
session state.

## Sources Of Truth

- The live session pod filesystem owns files while the pod is alive.
- Durable session metadata owns whether a session exists, who owns it, and
  which pod/workspace the file request belongs to.
- Artifact transcript events and message metadata own user-visible links to
  files.
- Browser object URLs are display handles only, not durable file references.

## Migration Rules

- Do not render protected raw API URLs directly in browser-native elements that
  cannot attach bearer auth.
- Do not keep unauthenticated file routes for browser convenience.
- Do not preserve old file-link shapes after moving to authenticated blob
  fetch or signed/mediated access.
- Do not imply file durability after session-pod death unless a separate
  durable artifact store exists for that file.

## Live Behavior

- Protected file previews fetch through authenticated code paths and render
  from browser object URLs or another safe carrier.
- Artifact links in the transcript remain tied to durable transcript metadata.
- Stale auth during file access follows the same recovery expectations as
  other protected browser fetches.
- Copy/download controls must report failure when the file is unavailable
  rather than showing success from local UI state.

## Failure And Recovery

- Browser reload can recreate previews from durable transcript metadata while
  the pod and file still exist.
- Session-pod death makes pod-local files unavailable unless they were copied
  into a durable artifact store.
- File-not-found, forbidden, stale-auth, and pod-gone errors should be visible
  and distinguishable.

## Observability

- Metrics should cover raw file fetches, preview blob fetches, auth failures,
  not-found responses, pod-gone responses, and download failures.
- Logs should identify session id, file identifier/path class, route, and
  caller without leaking protected contents.

## Acceptance Checks

- Browser-native previews do not use raw protected API URLs directly.
- Authenticated blob fetches recover from stale auth or show explicit failure.
- A copied/downloaded file is tied to an existing session and authorized user.
- Pod-gone and file-not-found states are visible and diagnostically distinct.
- Transcript artifact links can be reconstructed after reload while the file is
  still inside the durability boundary.
