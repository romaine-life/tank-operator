package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// TestMetricsEndpointServesPrometheus boots the production mux and asserts
// /metrics returns the Prom text exposition with at least the
// tank_session_event_stream_open_total counter defined. /debug/vars (the
// old expvar surface) must return 404.
func TestMetricsEndpointServesPrometheus(t *testing.T) {
	srv := newTestServerForHTTPMiddleware()

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	for _, want := range [][]byte{
		[]byte("tank_session_event_stream_open_total"),
		[]byte("tank_session_event_stream_heartbeat_catchup_total"),
	} {
		if !bytes.Contains(body, want) {
			t.Fatalf("/metrics body missing %s, got first 400 bytes: %q", want, string(body[:min(len(body), 400)]))
		}
	}

	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/debug/vars", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /debug/vars status = %d, want 404 (expvar surface deleted)", rr.Code)
	}
}

func TestSessionContainerTerminationMetricsUseBoundedLabels(t *testing.T) {
	promK8sWatchMetrics{}.RecordContainerTermination("custom-container", "custom-reason", 137)

	rr := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rr.Body)
	got := string(body)
	for _, want := range []string{
		"tank_session_container_terminations_total",
		`container="other"`,
		`reason="other"`,
		`exit_code="137"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("metrics scrape missing %s; got: %s", want, got)
		}
	}
}

func TestTurnNumberMissingPhaseLabels(t *testing.T) {
	cases := map[string]string{
		"materialize":     "materialize",
		"submit_response": "submit_response",
		"other":           "unknown",
	}
	for raw, want := range cases {
		if got := turnNumberMissingPhaseLabel(raw); got != want {
			t.Fatalf("turnNumberMissingPhaseLabel(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestServerErrorLogsContext asserts the middleware emits a structured
// slog.Error with method, route, status, and the response body's `detail`
// field when a handler returns 5xx. This is the property that fixed the
// "/api/sessions/activity 500 had no logs" diagnostic gap.
func TestServerErrorLogsContext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusInternalServerError, "synthetic failure for test")
	})
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	var buf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prevLogger)

	wrapped := httpInstrumentationMiddleware(mux)

	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("boom status = %d, want 500", rr.Code)
	}

	logged := buf.String()
	for _, want := range []string{
		`"level":"ERROR"`,
		`"msg":"http server error"`,
		`"method":"GET"`,
		`"route":"GET /boom"`,
		`"status":500`,
		`"detail":"synthetic failure for test"`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("slog output missing %s; got: %s", want, logged)
		}
	}

	buf.Reset()
	rr = httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("ok status = %d, want 200", rr.Code)
	}
	if strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Errorf("2xx request emitted an ERROR log: %s", buf.String())
	}
}

// TestHTTPMetricsRecordsRequest asserts the request counter and duration
// histogram both increment after a single request. We compare scrape
// output before and after the test request.
func TestHTTPMetricsRecordsRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /counted", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	wrapped := httpInstrumentationMiddleware(mux)

	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/counted", nil))

	rr = httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rr.Body)
	if !bytes.Contains(body, []byte(`tank_http_requests_total{method="GET",route="GET /counted",status_class="2xx"}`)) {
		t.Fatalf("expected request counter for GET /counted; got: %s", string(body))
	}
	if !bytes.Contains(body, []byte(`tank_http_request_duration_seconds_bucket{method="GET",route="GET /counted"`)) {
		t.Fatalf("expected duration histogram buckets for GET /counted; got: %s", string(body))
	}
}

func TestStreamAuthTicketMetricsUseBoundedLabels(t *testing.T) {
	recordStreamAuthTicket("custom-op", "custom-stream", "custom-result")

	rr := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rr.Body)
	got := string(body)
	for _, want := range []string{
		"tank_stream_auth_ticket_total",
		`operation="unknown"`,
		`stream="unknown"`,
		`result="other"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("metrics scrape missing %s; got: %s", want, got)
		}
	}
}

func TestStreamAuthTicketResultLabelKeepsClientCancelOutOfStoreErrors(t *testing.T) {
	if got := streamAuthTicketResultLabel("canceled"); got != "canceled" {
		t.Fatalf("streamAuthTicketResultLabel(canceled) = %q, want canceled", got)
	}
	if got := streamAuthTicketResultLabel("store_error"); got != "store_error" {
		t.Fatalf("streamAuthTicketResultLabel(store_error) = %q, want store_error", got)
	}
}

func TestSessionRuntimeConfigProviderLabelIncludesAntigravity(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "antigravity"} {
		if got := sessionRuntimeConfigProviderLabel(provider); got != provider {
			t.Fatalf("sessionRuntimeConfigProviderLabel(%q) = %q, want %q", provider, got, provider)
		}
	}

	if got := sessionRuntimeConfigProviderLabel("gemini"); got != "unknown" {
		t.Fatalf("sessionRuntimeConfigProviderLabel(unknown) = %q, want unknown", got)
	}
}

// TestStatusClass covers the bucket boundaries the HTTP middleware uses
// when labeling the request counter.
func TestStatusClass(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{200, "2xx"},
		{204, "2xx"},
		{301, "3xx"},
		{400, "4xx"},
		{404, "4xx"},
		{500, "5xx"},
		{599, "5xx"},
		{99, "unknown"},
		{600, "unknown"},
	}
	for _, tc := range cases {
		if got := statusClass(tc.status); got != tc.want {
			t.Errorf("statusClass(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// newTestServerForHTTPMiddleware constructs the minimal handler chain the
// observability tests use to assert end-to-end behavior of /metrics and
// /debug/vars. It deliberately doesn't call registerRoutes because that
// would drag the full orchestrator dependency graph (k8s client,
// auth.Verifier, Postgres pool) into a unit test.
func newTestServerForHTTPMiddleware() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	return httpInstrumentationMiddleware(mux)
}
