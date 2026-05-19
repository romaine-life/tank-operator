// handlers_repos.go — server-side surface for the per-session repo
// selection feature. Stage 1 of the auto-clone rollout
// (docs/quality-timeframes.md: each stage ships coherent state).
//
// This file owns:
//   - Slug validation + dedup at the handler boundary (validateRepoSlugs).
//   - The mode predicate that decides whether a session shape supports
//     a non-empty repo selection (sessionModeSupportsRepos).
//   - The GET /api/github/recent-repos endpoint that surfaces the
//     caller's recently-selected repo slugs to the splash-page picker.
//
// The splash picker shows two sections: "Recent" (this endpoint) and
// "All repos" (stage 2's mcp-github passthrough, not in this PR).
// Recent works the moment the schema migration lands — no mcp-github
// dependency — so stage 1 ships a fully functional UI for users who
// re-use the same repos session to session, even before stage 2 makes
// the "All repos" enumeration available.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// maxReposPerSession caps how many repos a session can auto-clone.
// Picked to match the cost/scaling boundary called out in the
// pre-implementation plan: stage 3's init container clones serially,
// and 5 medium repos at --depth=50 fits comfortably under the 90s
// pod-ready timeout (manager.go: podReadyTimeout). Bound the input at
// the handler so a malicious or buggy SPA can't push a 1000-repo list
// through and stall every new session.
const maxReposPerSession = 5

// repoSlugPattern matches a permissive "owner/name" shape. Bounds:
//   - Owner: starts alphanumeric, then alphanumeric / hyphen, up to
//     39 chars (GitHub's limit for usernames/orgs).
//   - Name: alphanumeric plus `.`, `_`, `-`, up to 100 chars (GitHub
//     limit is 100; we let the upstream API enforce the lower bound
//     of "not just dots").
//
// Strict enough to reject path traversal (`../`), scheme-injection
// (`https://…`), and shell metacharacters that could escape the
// stage 3 clone script, while permissive enough to admit every real
// GitHub repo slug. The upstream mcp-github call in stage 2 is the
// authoritative check for "this repo exists and the caller can read
// it" — the regex here is the defense-in-depth on input shape.
var repoSlugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// sessionModeSupportsRepos reports whether a session mode has a
// /workspace volume the stage 3 init container could clone into.
// Today only the SDK-runner modes (claude_gui, codex_gui,
// codex_app_server) provision a workspace emptyDir — see
// sessionmodel.PodManifest: `wantSDKRunner`. CLI / config / api_key
// / hermes_gui modes do not, so accepting a repo selection for them
// would persist data with no runtime path to use it.
//
// Per docs/quality-timeframes.md "settled contracts": we reject at
// the handler boundary rather than silently dropping the selection
// later. The SPA also hides the picker for unsupported modes, but
// the backend is the authority.
func sessionModeSupportsRepos(mode string) bool {
	switch sessionmodel.NormalizeSessionMode(mode) {
	case sessionmodel.ClaudeGUIMode,
		sessionmodel.CodexGUIMode,
		sessionmodel.CodexAppServerMode:
		return true
	default:
		return false
	}
}

// errReposUnsupportedForMode signals that the request body carried a
// non-empty repos[] but the chosen mode has no workspace for them.
// Surfaced as 400 to the SPA so the UI can show a clear message
// rather than silently producing a session with phantom repos.
var errReposUnsupportedForMode = errors.New("repos selection is not supported for this session mode")

// validateRepoSlugs normalizes the incoming slug list. Returns the
// normalized slice (trim, dedup case-insensitively, preserve original
// case), the count for telemetry, and an error on first malformed
// entry or over-cap.
//
// Empty input returns ([], nil). Order is preserved so the picker's
// presentation order survives the round trip (the SPA reads chips
// back from the durable row on existing sessions; preserving order
// keeps the chip list stable across re-renders).
func validateRepoSlugs(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	if len(raw) > maxReposPerSession {
		return nil, fmt.Errorf("too many repos: %d > %d", len(raw), maxReposPerSession)
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for i, slug := range raw {
		trimmed := strings.TrimSpace(slug)
		if trimmed == "" {
			return nil, fmt.Errorf("repos[%d]: empty slug", i)
		}
		if !repoSlugPattern.MatchString(trimmed) {
			return nil, fmt.Errorf("repos[%d]: %q is not a valid owner/name slug", i, trimmed)
		}
		key := strings.ToLower(trimmed)
		if _, dup := seen[key]; dup {
			// Dedup silently — picker can produce a dup if the
			// user clicks twice; treating it as an error would
			// be annoying.
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out, nil
}

// recentRepoLimit caps the recent-repos response size. Picked to
// roughly fill the picker's "Recent" section on screen without
// crowding the search list.
const recentRepoLimit = 8

// recentRepoLookbackDays bounds the recency window. A repo that
// hasn't appeared on a session in the last 30 days probably isn't
// "recent" anymore.
const recentRepoLookbackDays = 30

// handleGitHubRecentRepos returns the caller's recently-selected repo
// slugs, deduped, in most-recent-first order. The picker uses this to
// surface the "Recent" section before the stage 2 enumeration call
// has loaded (or, when stage 2 isn't deployed yet, as the only
// section).
//
// Reads the durable sessions.repos column directly — there's no
// mcp-github hop on this path, so the endpoint works from the moment
// the schema migration lands. Per-user-scoped: an admin reading
// `?owner=` is intentionally not supported here (we don't need
// cross-user repo lists, and gating opens a footgun).
func (s *appServer) handleGitHubRecentRepos(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.pgPool == nil {
		writeJSON(w, http.StatusOK, map[string]any{"repos": []string{}})
		return
	}
	slugs, err := fetchRecentRepoSlugs(r.Context(), s.pgPool, user.Email, s.sessionScope, recentRepoLimit, recentRepoLookbackDays)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recent repos: "+err.Error())
		return
	}
	if slugs == nil {
		slugs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": slugs})
}

// fetchRecentRepoSlugs reads the caller's recent repo selections,
// most-recent-first. Implementation note: we want "the N most recent
// distinct slugs" — Postgres can express this in one statement with
// DISTINCT ON over an ordered subquery. Avoids pulling every row's
// repos[] into Go memory and de-duping in the application.
func fetchRecentRepoSlugs(ctx context.Context, pool *pgxpool.Pool, owner, scope string, limit, lookbackDays int) ([]string, error) {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if owner == "" {
		return []string{}, nil
	}
	const q = `
		WITH recent AS (
			SELECT unnest(repos) AS slug, created_at
			FROM sessions
			WHERE email = $1
			  AND session_scope = $2
			  AND created_at > now() - ($3 || ' days')::interval
		)
		SELECT slug
		FROM (
			SELECT slug, MAX(created_at) AS last_used
			FROM recent
			GROUP BY slug
		) ranked
		ORDER BY last_used DESC
		LIMIT $4
	`
	rows, err := pool.Query(ctx, q, owner, scope, lookbackDays, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, limit)
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}
