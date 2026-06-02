// handlers_repos.go — server-side surface for the per-session repo
// selection and auto-clone feature.
//
// This file owns:
//   - Slug validation + dedup at the handler boundary (validateRepoSlugs).
//   - The mode predicate that decides whether a session shape supports
//     a non-empty repo selection (sessionModeSupportsRepos).
//   - The GET /api/github/recent-repos endpoint that surfaces the
//     caller's recently-selected repo slugs to the splash-page picker.
//
// The splash picker shows two sections: "Recent" (this endpoint) and
// "All repos" (the mcp-github passthrough). Recent has no mcp-github
// dependency, so it remains available even when full enumeration fails.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/mcpgithub"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// maxReposPerSession caps how many repos a session can auto-clone.
// Picked to match the cost/scaling boundary called out in the
// pre-implementation plan: the init container clones serially, and 5
// medium repos at --depth=50 fits comfortably under the 90s
// pod-ready timeout (manager.go: podReadyTimeout). Bound the input at
// the handler so a malicious or buggy SPA can't push a 1000-repo list
// through and stall every new session.
const maxReposPerSession = 5

// maxPinnedReposPerUser bounds the per-user shortcut list stored on
// profiles.pinned_repos. It is intentionally larger than the per-session
// clone cap because pins are cheap metadata, but still finite so a bad client
// cannot turn the profile row into unbounded storage.
const maxPinnedReposPerUser = 64

// repoSlugPattern matches a permissive "owner/name" shape. Bounds:
//   - Owner: starts alphanumeric, then alphanumeric / hyphen, up to
//     39 chars (GitHub's limit for usernames/orgs).
//   - Name: alphanumeric plus `.`, `_`, `-`, up to 100 chars (GitHub
//     limit is 100; we let the upstream API enforce the lower bound
//     of "not just dots").
//
// Strict enough to reject path traversal (`../`), scheme-injection
// (`https://…`), and shell metacharacters that could escape the
// repo-cloner script, while permissive enough to admit every real
// GitHub repo slug. The upstream mcp-github call is the authoritative
// check for "this repo exists and the caller can read it" — the regex
// here is the defense-in-depth on input shape.
var repoSlugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// sessionModeSupportsRepos reports whether a session mode has a
// /workspace volume the repo-cloner init container can clone into.
// Today only the SDK-runner modes provision a workspace emptyDir — see
// sessionmodel.PodManifest: `wantSDKRunner`. CLI / config / api_key
// modes do not, so accepting a repo selection for them
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
		sessionmodel.CodexExecGUIMode,
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
// presentation order survives the round trip through the durable row.
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

// validatePinnedRepoSlugs normalizes the durable per-user pin list. It uses
// the same slug contract as session repo selection but a separate cap because
// pins are shortcuts, not a clone workload.
func validatePinnedRepoSlugs(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	if len(raw) > maxPinnedReposPerUser {
		return nil, fmt.Errorf("too many pinned repos: %d > %d", len(raw), maxPinnedReposPerUser)
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

const fetchRecentRepoSlugsQuery = `
	WITH recent AS (
		SELECT unnest(repos) AS slug, created_at
		FROM sessions
		WHERE email = $1
		  AND session_scope = $2
		  AND created_at > now() - ($3::int * interval '1 day')
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

// handleGitHubRecentRepos returns the caller's recently-selected repo
// slugs, deduped, in most-recent-first order. The picker uses this to
// surface the "Recent" section before the full enumeration call has
// loaded.
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
	slugs, err := fetchRecentRepoSlugs(r.Context(), s.pgPool, repoLookupOwnerEmail(user), s.sessionScope, recentRepoLimit, recentRepoLookbackDays)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recent repos: "+err.Error())
		return
	}
	if slugs == nil {
		slugs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": slugs})
}

// handleGitHubPinnedRepos returns or replaces the caller's durable
// splash-picker pins. The profile row is the source of truth; the SPA does
// not keep a localStorage shadow for this list.
func (s *appServer) handleGitHubPinnedRepos(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		profile, err := s.profiles.GetOrCreate(r.Context(), repoLookupOwnerEmail(user))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "pinned repos: "+err.Error())
			return
		}
		repos := profile.PinnedRepos
		if repos == nil {
			repos = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
	case http.MethodPut:
		pinsStore, ok := s.profiles.(profilesPinnedReposStore)
		if !ok {
			recordPinnedReposUpdate("unavailable")
			writeError(w, http.StatusServiceUnavailable, "profile store does not support pinned repo writes")
			return
		}
		var body struct {
			Repos []string `json:"repos"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			recordPinnedReposUpdate("invalid")
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		repos, err := validatePinnedRepoSlugs(body.Repos)
		if err != nil {
			recordPinnedReposUpdate("invalid")
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		profile, err := pinsStore.UpdatePinnedRepos(r.Context(), repoLookupOwnerEmail(user), repos)
		if err != nil {
			recordPinnedReposUpdate("error")
			writeError(w, http.StatusInternalServerError, "pinned repos: "+err.Error())
			return
		}
		next := profile.PinnedRepos
		if next == nil {
			next = []string{}
		}
		recordPinnedReposUpdate("ok")
		writeJSON(w, http.StatusOK, map[string]any{"repos": next})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
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
	rows, err := pool.Query(ctx, fetchRecentRepoSlugsQuery, owner, scope, lookbackDays, limit)
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

func repoLookupOwnerEmail(user auth.User) string {
	return user.OwnerEmail()
}

// handleGitHubRepos returns the full set of repos visible to the
// caller's GitHub App installation, by way of mcp-github.
//
// The all-repos endpoint pairs with the nelsong6/auth on-behalf-of
// exchange (PR #43) so the orchestrator can present a service JWT
// carrying the SPA caller's email as `actor_email`, which mcp-github
// routes back to that user's installation_id via tank-operator's
// existing /api/internal/github/installation surface. mcp-github needs
// no changes — the same MCP tool session pods already call.
//
// This endpoint is hidden behind the same authedFetch wrapper the
// recent-repos endpoint uses. Failures from mcp-github (unconnected
// installation, IdP down, transport error) surface as 502 with a
// stable detail string the SPA can render in the picker.
func (s *appServer) handleGitHubRepos(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.mcpGitHub == nil {
		writeError(w, http.StatusServiceUnavailable, "mcp-github client not configured")
		return
	}
	start := time.Now()
	// Bound the upstream call independently of the inbound HTTP
	// request — the picker is willing to wait a few seconds, but the
	// SPA's fetch budget is the only ceiling we want callers to hit.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	repos, err := s.mcpGitHub.ListRepos(ctx, repoLookupOwnerEmail(user))
	if err != nil {
		githubRepoListRequestsTotal.WithLabelValues("error").Inc()
		slog.Warn("mcp-github list_installation_repos failed", "email", user.Email, "error", err)
		writeError(w, http.StatusBadGateway, "list repos: "+err.Error())
		return
	}
	githubRepoListRequestsTotal.WithLabelValues("ok").Inc()
	githubRepoListDurationSeconds.Observe(time.Since(start).Seconds())
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

// AppServerMCPGitHub abstracts the mcpgithub client surface so tests
// can swap in a recorder without standing up a fake HTTP server.
// Production wires *mcpgithub.Client directly via main.go.
type AppServerMCPGitHub interface {
	ListRepos(ctx context.Context, userEmail string) ([]mcpgithub.Repo, error)
}
