// Admin-only debug surface for the durable session_events ledger.
// Returns raw event rows by tank session id, so an operator (or the AI
// support agent) can audit the durable ledger directly when the projected
// transcript is not enough.
//
// The user-facing sidebar still tombstones sessions when
// `sessions.visible=false`, but the projected timeline and copied message-link
// paths intentionally remain owner/admin-readable because visible=false is not
// a transcript retention boundary. The `session_events` rows themselves are
// durable (no FK, no cascade); this endpoint remains the raw-ledger diagnostic
// counterpart for cases where the transcript projection is insufficient.
//
// Pair with `/api/debug/session-list-state` to find an invisible
// session's id, then call this endpoint with that id. Pair with
// `tank_admin_debug_session_event_ledger_reads_total{result}` at
// /metrics to monitor usage volume.
//
// Auth: Tank admin power required. Counts as an admin cross-user
// read; emits a structured slog audit line per call.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// debugSessionEventLedgerMaxLimit caps a single page so a runaway
// request can't pull a giant ledger into one response. The underlying
// store also normalizes via normalizeSessionEventLimit (1000 hard cap
// inside the store), but we cap lower at the surface for predictability.
const debugSessionEventLedgerMaxLimit = 500

// debugSessionEventLedgerDefaultLimit is the page size when the caller
// does not pass `?limit=`. Matches the store's default of 200.
const debugSessionEventLedgerDefaultLimit = 200

func (s *appServer) handleDebugSessionEventLedger(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		recordDebugSessionEventLedgerRead("forbidden")
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		recordDebugSessionEventLedgerRead("bad_request")
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	scope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		recordDebugSessionEventLedgerRead("forbidden")
		writeError(w, status, scopeErr.Error())
		return
	}

	limit := debugSessionEventLedgerDefaultLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			recordDebugSessionEventLedgerRead("bad_request")
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	if limit > debugSessionEventLedgerMaxLimit {
		limit = debugSessionEventLedgerMaxLimit
	}

	direction := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("direction")))
	if direction != "" && direction != "asc" && direction != "desc" {
		recordDebugSessionEventLedgerRead("bad_request")
		writeError(w, http.StatusBadRequest, "direction must be asc or desc")
		return
	}

	cursor := store.SessionEventCursor{
		AfterOrderKey:  strings.TrimSpace(r.URL.Query().Get("after_order_key")),
		BeforeOrderKey: strings.TrimSpace(r.URL.Query().Get("before_order_key")),
		Direction:      direction,
	}
	if cursor.AfterOrderKey != "" && cursor.BeforeOrderKey != "" {
		recordDebugSessionEventLedgerRead("bad_request")
		writeError(w, http.StatusBadRequest, "specify at most one of after_order_key / before_order_key")
		return
	}

	eventStore := s.sessionEventStoreForScope(scope)
	if eventStore == nil {
		recordDebugSessionEventLedgerRead("not_configured")
		writeError(w, http.StatusServiceUnavailable, "session event store not configured")
		return
	}
	if _, ok := eventStore.(store.StubSessionEventStore); ok {
		recordDebugSessionEventLedgerRead("not_configured")
		writeError(w, http.StatusServiceUnavailable, "session event store running in stub mode")
		return
	}

	page, err := eventStore.ListBySession(r.Context(), sessionID, cursor, limit)
	if err != nil {
		recordDebugSessionEventLedgerRead("store_error")
		slog.Error("debug session-event ledger read failed",
			"caller_email", user.Email,
			"session_id", sessionID,
			"session_scope", scope,
			"limit", limit,
			"after_order_key", cursor.AfterOrderKey,
			"before_order_key", cursor.BeforeOrderKey,
			"direction", cursor.Direction,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := "ok"
	if len(page.Events) == 0 {
		result = "empty"
	}
	recordDebugSessionEventLedgerRead(result)
	slog.Info("debug session-event ledger read",
		"caller_email", user.Email,
		"session_id", sessionID,
		"session_scope", scope,
		"limit", limit,
		"after_order_key", cursor.AfterOrderKey,
		"before_order_key", cursor.BeforeOrderKey,
		"direction", cursor.Direction,
		"count", len(page.Events),
		"has_more", page.HasMore,
		"result", result,
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"description":    debugSessionEventLedgerDescription,
		"session_id":     sessionID,
		"session_scope":  scope,
		"storage_key":    sessionmodel.SessionStorageKey(scope, sessionID),
		"count":          len(page.Events),
		"events":         page.Events,
		"has_more":       page.HasMore,
		"next_order_key": page.NextOrderKey,
		"prev_order_key": page.PrevOrderKey,
		"found_oldest":   page.FoundOldest,
		"found_newest":   page.FoundNewest,
		"fetched_at":     time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// errDebugSessionEventLedgerNotConfigured surfaces the stub-store case
// to tests without coupling them to the string body. Kept package-level
// so the test file can assert against it.
var errDebugSessionEventLedgerNotConfigured = errors.New("session event store not configured")

// debugSessionEventLedgerDescription is rendered into the JSON response
// so an operator running `curl | jq` sees the meaning of each field
// without leaving the terminal. Per docs/quality-timeframes.md
// "Observability exists for the bugs a user would otherwise have to
// guess about." Pair with docs/observability.md → "Session Event Ledger
// Debug Surface" for the playbook.
const debugSessionEventLedgerDescription = `Durable session_events ledger for one tank session.

Use this when you need raw event audit detail beyond the projected
transcript. sessions.visible=false tombstones the sidebar only; owner/admin
GET /api/sessions/{id}/timeline and copied message links continue to resolve
while the durable row and transcript ledger remain in Postgres.

Query params:
  session_id        Required. Public session id (e.g., "203").
  session_scope     Optional. Defaults to this orchestrator's scope.
  limit             Optional. Default 200, max 500.
  after_order_key   Optional. Forward-paginate ASC strictly after this key.
  before_order_key  Optional. Backward-paginate DESC strictly before this key.
  direction         Optional. "asc" (default) or "desc"; "desc" with no cursor
                    returns the tail (latest events). Specify at most one of
                    after_order_key / before_order_key.

Response: events[] is always returned ASC by order_key. has_more / found_oldest
/ found_newest let the caller decide whether to paginate further. storage_key
is the underlying partition key (scope:session_id) for cross-referencing the
raw session_events table.

Counts as an admin cross-user audit read. Emits a structured slog line per
call (caller_email, session_id, session_scope, limit, cursor, result, count)
and increments tank_admin_debug_session_event_ledger_reads_total{result} at
/metrics.`
