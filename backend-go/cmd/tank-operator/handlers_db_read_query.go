package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	dbReadQueryDefaultMaxRows = 200
	dbReadQueryMaxRows        = 2000
	dbReadQueryTimeout        = 20 * time.Second
)

// handleInternalSessionDBReadQuery runs READ-ONLY SQL against the tank-operator
// Postgres DB for a NON-RESTRICTED session — the diagnostic path the
// diagnostic-discipline doc calls for (query session_events, profiles, etc.).
// It backs the proxy-injected `query_tank_db` MCP tool.
//
// Read-only is enforced by a read-only transaction + a statement timeout + a row
// cap; restricted/test sessions are refused (slots stay locked). The query runs
// under the orchestrator pool (the DB's AAD admin), but the read-only tx blocks
// any write/DDL, and Flexible-Server's admin is not a filesystem superuser — so
// the blast radius is "read the app's own data", which is the intent for the
// trusted owner's non-restricted sessions.
func (s *appServer) handleInternalSessionDBReadQuery(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/db-read-query")
	if user == nil {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	pod, err := s.k8s.CoreV1().Pods(s.namespace).Get(r.Context(), "session-"+sessionID, metav1.GetOptions{})
	if err != nil {
		writeError(w, http.StatusForbidden, "session pod not found")
		return
	}
	if podRestrictedGit(pod) {
		writeError(w, http.StatusForbidden, "read-only DB access is not available to restricted-git sessions")
		return
	}
	if s.pgPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database pool unavailable")
		return
	}

	var body struct {
		SQL     string `json:"sql"`
		MaxRows int    `json:"max_rows"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	sql := strings.TrimSpace(body.SQL)
	if sql == "" {
		writeError(w, http.StatusBadRequest, "sql is required")
		return
	}
	maxRows := body.MaxRows
	if maxRows <= 0 {
		maxRows = dbReadQueryDefaultMaxRows
	}
	if maxRows > dbReadQueryMaxRows {
		maxRows = dbReadQueryMaxRows
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbReadQueryTimeout)
	defer cancel()
	tx, err := s.pgPool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin read-only tx: "+err.Error())
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '15s'"); err != nil {
		writeError(w, http.StatusInternalServerError, "set statement_timeout: "+err.Error())
		return
	}

	rows, err := tx.Query(ctx, sql)
	if err != nil {
		// A query error is a normal diagnostic result, not a 500 — surface it.
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()
	fds := rows.FieldDescriptions()
	cols := make([]string, len(fds))
	for i, fd := range fds {
		cols[i] = fd.Name
	}
	outRows := make([][]any, 0, 64)
	truncated := false
	for rows.Next() {
		if len(outRows) >= maxRows {
			truncated = true
			break
		}
		vals, verr := rows.Values()
		if verr != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": verr.Error(), "columns": cols, "rows": outRows})
			return
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = stringifyDBValue(v)
		}
		outRows = append(outRows, row)
	}
	if rerr := rows.Err(); rerr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": rerr.Error(), "columns": cols, "rows": outRows})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"columns":   cols,
		"rows":      outRows,
		"row_count": len(outRows),
		"truncated": truncated,
	})
}

// stringifyDBValue keeps JSON output safe for pg types that don't marshal
// natively (numeric, uuid, jsonb bytes, timestamps).
func stringifyDBValue(v any) any {
	switch x := v.(type) {
	case nil, bool, string, float64, float32, int64, int32, int, int16, int8, uint64, uint32:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%v", x)
	}
}
