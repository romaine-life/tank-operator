package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestDebugObservabilitySummaryRequiresAdmin(t *testing.T) {
	app := &appServer{verifier: authVerifierForTests(t)}
	req := httptest.NewRequest(http.MethodGet, "/api/debug/observability-summary", nil)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	rec := httptest.NewRecorder()

	app.handleDebugObservabilitySummary(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s, want 403", rec.Code, rec.Body.String())
	}
}

func TestDebugObservabilitySummaryCollectsTankAlertsAnd5xx(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/alerts":
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "success",
				"data": map[string]any{
					"alerts": []map[string]any{
						{
							"state":    "firing",
							"activeAt": "2026-06-01T16:00:00Z",
							"labels": map[string]any{
								"alertname": "TankSessionBusPublishFailing",
								"severity":  "critical",
								"namespace": "tank-operator",
							},
							"annotations": map[string]any{
								"runbook": "Check JetStream command publish failures.",
							},
						},
						{
							"state": "firing",
							"labels": map[string]any{
								"alertname": "KubeCPUOvercommit",
								"severity":  "warning",
								"namespace": "monitoring",
							},
						},
						{
							"state": "pending",
							"labels": map[string]any{
								"alertname": "TankPendingDoesNotCount",
								"severity":  "critical",
							},
						},
					},
				},
			})
		case "/api/v1/query":
			query := r.URL.Query().Get("query")
			if !strings.Contains(query, `tank_http_requests_total{status_class="5xx"}`) {
				t.Fatalf("unexpected query %q", query)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "success",
				"data": map[string]any{
					"result": []map[string]any{
						{
							"metric": map[string]any{"route": "GET /api/sessions/{session_id}/events"},
							"value":  []any{1, "3"},
						},
						{
							"metric": map[string]any{"route": "GET /api/admin/session-report"},
							"value":  []any{1, "1.25"},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer prom.Close()
	t.Setenv("PROMETHEUS_URL", prom.URL)
	t.Setenv("SUPER_ADMIN_EMAILS", "admin@example.com")

	app := &appServer{verifier: authVerifierForTests(t)}
	req := httptest.NewRequest(http.MethodGet, "/api/debug/observability-summary", nil)
	req.Header.Set("Authorization", "Bearer "+signedAdminToken(t, "admin@example.com"))
	rec := httptest.NewRecorder()

	app.handleDebugObservabilitySummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	var body observabilitySummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "critical" {
		t.Fatalf("status = %q, want critical: %#v", body.Status, body)
	}
	if body.Alerts.FiringTotal != 2 || body.Alerts.TankFiringTotal != 1 || body.Alerts.PlatformFiring != 1 || body.Alerts.TankCritical != 1 {
		t.Fatalf("alerts = %#v", body.Alerts)
	}
	if body.Alerts.Items[0].Name != "TankSessionBusPublishFailing" || body.Alerts.Items[0].Runbook == "" {
		t.Fatalf("alert items = %#v", body.Alerts.Items)
	}
	if body.HTTP5xx.Status != "warning" || body.HTTP5xx.Total != 4.25 || len(body.HTTP5xx.Routes) != 2 {
		t.Fatalf("http_5xx = %#v", body.HTTP5xx)
	}
	if len(body.DebugLinks) == 0 || !strings.Contains(body.Description, "Prometheus") {
		t.Fatalf("missing diagnostic metadata: %#v", body)
	}
}

func TestDebugObservabilitySummaryPartialFailureIsUnknown(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/alerts":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/api/v1/query":
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "success",
				"data":   map[string]any{"result": []any{}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer prom.Close()
	t.Setenv("PROMETHEUS_URL", prom.URL)

	body := collectObservabilitySummary(
		httptest.NewRequest(http.MethodGet, "/", nil).Context(),
		time.Date(2026, 6, 1, 16, 0, 0, 0, time.UTC),
		prom.URL,
	)
	if body.Status != "unknown" {
		t.Fatalf("status = %q, want unknown: %#v", body.Status, body)
	}
	if len(body.Errors) != 1 || body.Errors[0].Surface != "alerts" {
		t.Fatalf("errors = %#v", body.Errors)
	}
}

func signedAdminToken(t *testing.T, email string) string {
	t.Helper()
	tok, err := testJWT(t).MintJWT(context.Background(), jwt.MapClaims{
		"sub":   "sub-admin",
		"email": email,
		"iss":   "https://auth.romaine.life",
		"name":  "Admin",
		"role":  "admin",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
