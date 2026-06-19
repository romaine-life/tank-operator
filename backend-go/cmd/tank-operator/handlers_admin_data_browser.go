// Admin-only, read-only database browser.
//
// GET /api/admin/data/tables                  -> directory of public tables
// GET /api/admin/data/tables/{table}/rows     -> one keyset page of a table
//
// This is the in-app, click-around counterpart to the free-form
// service-principal SQL endpoint (handlers_db_read_query.go). It never accepts
// caller SQL: the browser picks a table and a page, the orchestrator builds a
// parameterized, quoted, read-only query (internal/pgstore.DataBrowser).
//
// Hardening lives in the store layer (read-only tx + statement timeout,
// catalog-validated table names, reltuples estimates instead of count(*),
// keyset pagination, and a code-owned redaction policy that masks bearer
// tokens and never ships blob bytes). This file owns the admin gate, request
// parsing, the audit slog line, and the bounded result metric.
//
// Auth: Tank admin power required. Counts as a privileged data read; emits a
// structured slog audit line per call and increments
// tank_admin_data_browser_reads_total{surface,result} at /metrics.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

const (
	adminDataBrowserDefaultLimit = 100
	adminDataBrowserMaxLimit     = 500
	// adminDataBrowserTimeout bounds the whole handler (the store also sets a
	// per-statement timeout inside its read-only transaction).
	adminDataBrowserTimeout = 20 * time.Second
)

// dataBrowserReader is the read-only seam the handlers depend on. The
// production implementation is *pgstore.DataBrowser; tests inject a fake so the
// gate, parsing, and response envelope are exercised without a live database.
type dataBrowserReader interface {
	ListTables(ctx context.Context) ([]pgstore.DataTable, error)
	ReadRows(ctx context.Context, req pgstore.DataRowsRequest) (*pgstore.DataRowsPage, error)
}

func (s *appServer) handleAdminDataTables(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		recordAdminDataBrowserRead("table_list", "forbidden")
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	if s.dataBrowser == nil {
		recordAdminDataBrowserRead("table_list", "not_configured")
		writeError(w, http.StatusServiceUnavailable, "database browser not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), adminDataBrowserTimeout)
	defer cancel()
	tables, err := s.dataBrowser.ListTables(ctx)
	if err != nil {
		recordAdminDataBrowserRead("table_list", "error")
		slog.Error("admin data-browser table list failed",
			"caller_email", user.Email,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	recordAdminDataBrowserRead("table_list", "ok")
	slog.Info("admin data-browser table list",
		"caller_email", user.Email,
		"count", len(tables),
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"description": adminDataBrowserDescription,
		"tables":      tables,
		"count":       len(tables),
		"fetched_at":  time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *appServer) handleAdminDataTableRows(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		recordAdminDataBrowserRead("rows", "forbidden")
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}

	table := strings.TrimSpace(r.PathValue("table"))
	if !pgstore.ValidDataTableIdent(table) {
		recordAdminDataBrowserRead("rows", "bad_request")
		writeError(w, http.StatusBadRequest, "invalid table name")
		return
	}

	limit := adminDataBrowserDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			recordAdminDataBrowserRead("rows", "bad_request")
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	if limit > adminDataBrowserMaxLimit {
		limit = adminDataBrowserMaxLimit
	}

	if s.dataBrowser == nil {
		recordAdminDataBrowserRead("rows", "not_configured")
		writeError(w, http.StatusServiceUnavailable, "database browser not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), adminDataBrowserTimeout)
	defer cancel()
	page, err := s.dataBrowser.ReadRows(ctx, pgstore.DataRowsRequest{
		Table:  table,
		Cursor: strings.TrimSpace(r.URL.Query().Get("cursor")),
		Limit:  limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, pgstore.ErrDataTableUnknown):
			recordAdminDataBrowserRead("rows", "not_found")
			writeError(w, http.StatusNotFound, "unknown table")
			return
		case errors.Is(err, pgstore.ErrDataCursorInvalid):
			recordAdminDataBrowserRead("rows", "bad_request")
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		default:
			recordAdminDataBrowserRead("rows", "error")
			slog.Error("admin data-browser rows failed",
				"caller_email", user.Email,
				"table", table,
				"error", err,
			)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	result := "ok"
	if len(page.Rows) == 0 {
		result = "empty"
	}
	recordAdminDataBrowserRead("rows", result)
	slog.Info("admin data-browser rows",
		"caller_email", user.Email,
		"table", table,
		"limit", limit,
		"count", len(page.Rows),
		"has_more", page.HasMore,
		"result", result,
	)
	writeJSON(w, http.StatusOK, page)
}

// adminDataBrowserDescription is rendered into the table-directory response so
// an operator running `curl | jq` understands the surface without leaving the
// terminal. Pair with docs/observability.md → "Admin Data Browser".
const adminDataBrowserDescription = `Admin-only, read-only browser over the orchestrator's Postgres tables.

GET /api/admin/data/tables                    Directory of public base tables
                                              with reltuples row estimates.
GET /api/admin/data/tables/{table}/rows       One keyset page of a table.
  ?limit=     Optional. Default 100, max 500.
  ?cursor=    Optional. Opaque next_cursor from the previous page.

Reads run inside a read-only transaction with a statement timeout; no caller
SQL is accepted. Secret columns (bearer tokens, install nonces) render as the
redacted placeholder and bytea columns render as a byte count, never their
contents. Row counts are reltuples estimates, not count(*). For raw event-level
audit of one session use GET /api/debug/session-event-ledger instead.

Counts as a privileged admin data read. Emits a structured slog line per call
and increments tank_admin_data_browser_reads_total{surface,result} at /metrics.`
