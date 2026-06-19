package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// fakeDataBrowser is an in-test dataBrowserReader. The real SQL is exercised
// against a live database in the test-slot smoke; here we assert handler shape
// (admin gate, table-name validation, limit clamp, error mapping, envelope).
type fakeDataBrowser struct {
	tables   []pgstore.DataTable
	page     *pgstore.DataRowsPage
	listErr  error
	rowsErr  error
	lastReq  pgstore.DataRowsRequest
	rowsCall int
}

func (f *fakeDataBrowser) ListTables(context.Context) ([]pgstore.DataTable, error) {
	return f.tables, f.listErr
}

func (f *fakeDataBrowser) ReadRows(_ context.Context, req pgstore.DataRowsRequest) (*pgstore.DataRowsPage, error) {
	f.rowsCall++
	f.lastReq = req
	if f.rowsErr != nil {
		return nil, f.rowsErr
	}
	return f.page, nil
}

func dataBrowserTestServer(t *testing.T, fake *fakeDataBrowser) *appServer {
	t.Helper()
	app := adminTestServer(t)
	if fake != nil {
		app.dataBrowser = fake
	}
	return app
}

func adminGet(t *testing.T, app *appServer, target string, pathVals map[string]string, role string, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	email := adminEmail
	if role != auth.RoleAdmin {
		email = otherUser
	}
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, email, role))
	for k, v := range pathVals {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestAdminDataTablesNonAdmin403(t *testing.T) {
	app := dataBrowserTestServer(t, &fakeDataBrowser{})
	rec := adminGet(t, app, "/api/admin/data/tables", nil, auth.RoleUser, app.handleAdminDataTables)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminDataTablesNotConfigured503(t *testing.T) {
	app := dataBrowserTestServer(t, nil) // dataBrowser stays nil
	rec := adminGet(t, app, "/api/admin/data/tables", nil, auth.RoleAdmin, app.handleAdminDataTables)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminDataTablesHappy200(t *testing.T) {
	fake := &fakeDataBrowser{tables: []pgstore.DataTable{
		{Name: "profiles", EstRows: 12},
		{Name: "session_events", EstRows: 1_000_000},
	}}
	app := dataBrowserTestServer(t, fake)
	rec := adminGet(t, app, "/api/admin/data/tables", nil, auth.RoleAdmin, app.handleAdminDataTables)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	for _, field := range []string{"description", "tables", "count", "fetched_at"} {
		if _, ok := body[field]; !ok {
			t.Errorf("missing field %q; body=%s", field, rec.Body.String())
		}
	}
	if count, _ := body["count"].(float64); int(count) != 2 {
		t.Errorf("count=%v want 2", body["count"])
	}
}

func TestAdminDataTablesStoreError500(t *testing.T) {
	app := dataBrowserTestServer(t, &fakeDataBrowser{listErr: errors.New("boom")})
	rec := adminGet(t, app, "/api/admin/data/tables", nil, auth.RoleAdmin, app.handleAdminDataTables)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminDataRowsNonAdmin403(t *testing.T) {
	app := dataBrowserTestServer(t, &fakeDataBrowser{})
	rec := adminGet(t, app, "/api/admin/data/tables/sessions/rows", map[string]string{"table": "sessions"}, auth.RoleUser, app.handleAdminDataTableRows)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminDataRowsInvalidTableName400(t *testing.T) {
	app := dataBrowserTestServer(t, &fakeDataBrowser{})
	for _, bad := range []string{"Sessions", "bad-name", "a;b", "drop table", "1abc"} {
		rec := adminGet(t, app, "/api/admin/data/tables/x/rows", map[string]string{"table": bad}, auth.RoleAdmin, app.handleAdminDataTableRows)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("table=%q status=%d want 400; body=%s", bad, rec.Code, rec.Body.String())
		}
	}
}

func TestAdminDataRowsNotConfigured503(t *testing.T) {
	app := dataBrowserTestServer(t, nil)
	rec := adminGet(t, app, "/api/admin/data/tables/sessions/rows", map[string]string{"table": "sessions"}, auth.RoleAdmin, app.handleAdminDataTableRows)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminDataRowsInvalidLimit400(t *testing.T) {
	app := dataBrowserTestServer(t, &fakeDataBrowser{page: &pgstore.DataRowsPage{}})
	for _, raw := range []string{"abc", "0", "-5"} {
		rec := adminGet(t, app, "/api/admin/data/tables/sessions/rows?limit="+raw, map[string]string{"table": "sessions"}, auth.RoleAdmin, app.handleAdminDataTableRows)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%q status=%d want 400; body=%s", raw, rec.Code, rec.Body.String())
		}
	}
}

func TestAdminDataRowsClampsLimit(t *testing.T) {
	fake := &fakeDataBrowser{page: &pgstore.DataRowsPage{Table: "sessions"}}
	app := dataBrowserTestServer(t, fake)
	rec := adminGet(t, app, "/api/admin/data/tables/sessions/rows?limit=99999", map[string]string{"table": "sessions"}, auth.RoleAdmin, app.handleAdminDataTableRows)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if fake.lastReq.Limit != adminDataBrowserMaxLimit {
		t.Errorf("limit=%d want clamp to %d", fake.lastReq.Limit, adminDataBrowserMaxLimit)
	}
}

func TestAdminDataRowsUnknownTable404(t *testing.T) {
	app := dataBrowserTestServer(t, &fakeDataBrowser{rowsErr: pgstore.ErrDataTableUnknown})
	rec := adminGet(t, app, "/api/admin/data/tables/nope/rows", map[string]string{"table": "nope"}, auth.RoleAdmin, app.handleAdminDataTableRows)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminDataRowsInvalidCursor400(t *testing.T) {
	app := dataBrowserTestServer(t, &fakeDataBrowser{rowsErr: pgstore.ErrDataCursorInvalid})
	rec := adminGet(t, app, "/api/admin/data/tables/sessions/rows?cursor=bogus", map[string]string{"table": "sessions"}, auth.RoleAdmin, app.handleAdminDataTableRows)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminDataRowsHappy200(t *testing.T) {
	fake := &fakeDataBrowser{page: &pgstore.DataRowsPage{
		Table:      "stream_auth_tickets",
		PrimaryKey: []string{"ticket"},
		Paginated:  true,
		Columns: []pgstore.DataColumn{
			{Name: "ticket", Type: "text", Kind: "redacted"},
			{Name: "email", Type: "text", Kind: "value"},
			{Name: "bytes", Type: "bytea", Kind: "bytes"},
		},
		Rows: [][]any{
			{"‹redacted›", "owner@example.com", int64(2048)},
		},
		HasMore:    true,
		NextCursor: "abc",
		EstTotal:   3,
	}}
	app := dataBrowserTestServer(t, fake)
	rec := adminGet(t, app, "/api/admin/data/tables/stream_auth_tickets/rows", map[string]string{"table": "stream_auth_tickets"}, auth.RoleAdmin, app.handleAdminDataTableRows)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if fake.lastReq.Table != "stream_auth_tickets" {
		t.Errorf("ReadRows table=%q want stream_auth_tickets", fake.lastReq.Table)
	}
	if fake.lastReq.Limit != adminDataBrowserDefaultLimit {
		t.Errorf("default limit=%d want %d", fake.lastReq.Limit, adminDataBrowserDefaultLimit)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	for _, field := range []string{"table", "columns", "primary_key", "rows", "has_more", "next_cursor", "est_total"} {
		if _, ok := body[field]; !ok {
			t.Errorf("missing field %q; body=%s", field, rec.Body.String())
		}
	}
}
