package providerhealth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassifySnapshotMapsResultToLayer1Status(t *testing.T) {
	cases := []struct {
		name   string
		result string
		want   string
	}{
		{"success → healthy", "success", "healthy"},
		// "unknown" is the pre-first-refresh state — the cached blob
		// may still be serving a long-lived token. Do not flip to
		// failed on absence of data.
		{"unknown → healthy", "unknown", "healthy"},
		{"http_error → failed", "http_error", "failed"},
		{"request_failed → failed", "request_failed", "failed"},
		{"no_refresh_token → failed", "no_refresh_token", "failed"},
		{"unrecognized → healthy (defensive default)", "weird", "healthy"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := classifySnapshot(Snapshot{Result: tt.result})
			if got != tt.want {
				t.Fatalf("classifySnapshot(%q) = %q, want %q", tt.result, got, tt.want)
			}
		})
	}
}

func TestHTTPSourceParsesProxySnapshotShape(t *testing.T) {
	// Pins the wire contract between the api-proxy's /health/<provider>
	// endpoint and this poller. A drift here would silently break the
	// transcript banner — the poller is the only consumer.
	body := map[string]any{
		"provider":          "codex",
		"result":            "http_error",
		"reason":            "refresh_token_reused",
		"text":              "Codex sign-in expired. Re-authenticate to restore service.",
		"last_attempted_at": 1700000100.0,
		"last_succeeded_at": nil,
		"attempt_id":        7,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	src := NewHTTPSource("codex", srv.URL, srv.Client())
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got.Provider != "codex" || got.Result != "http_error" {
		t.Fatalf("snapshot = %#v", got)
	}
	if got.Reason != "refresh_token_reused" {
		t.Fatalf("reason = %q, want refresh_token_reused", got.Reason)
	}
	if got.LastAttemptedAt == nil || *got.LastAttemptedAt != 1700000100.0 {
		t.Fatalf("last_attempted_at = %v", got.LastAttemptedAt)
	}
	if got.LastSucceededAt != nil {
		t.Fatalf("last_succeeded_at should be nil; got %v", got.LastSucceededAt)
	}
}

func TestHTTPSourceTreatsNon2xxAsError(t *testing.T) {
	// A proxy that returns 503 (snapshot temporarily unavailable, per
	// the metrics.py contract) must NOT flip Layer 1 — the Source
	// errors out, the poller's RecordPoll(false) increments, no
	// transition is attempted.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("snapshot unavailable"))
	}))
	defer srv.Close()

	src := NewHTTPSource("codex", srv.URL, srv.Client())
	_, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for 503; got nil")
	}
}

func TestBannerTextPrefersProxyText(t *testing.T) {
	cfg := ProviderConfig{Provider: "codex"}
	snap := Snapshot{
		Result: "http_error",
		Reason: "refresh_token_reused",
		Text:   "Sign-in expired. Re-authenticate to restore service.",
	}
	got := bannerText(cfg, "failed", snap)
	if got != snap.Text {
		t.Fatalf("bannerText = %q, want proxy text", got)
	}
}

func TestBannerTextFallsBackWhenProxyTextIsEmpty(t *testing.T) {
	cfg := ProviderConfig{Provider: "codex"}
	got := bannerText(cfg, "failed", Snapshot{Result: "http_error"})
	if got == "" {
		t.Fatal("bannerText must not be empty on failed status — would emit a content-free banner")
	}
}

func TestBannerTextIsEmptyForHealthy(t *testing.T) {
	// Recovery emits a session.status:ready event (status="ready" on
	// the same timeline_id). That event carries its own canonical text
	// from emitForSession; bannerText is only used to populate Layer 1
	// for failed transitions.
	got := bannerText(ProviderConfig{Provider: "codex"}, "healthy", Snapshot{})
	if got != "" {
		t.Fatalf("bannerText(healthy) = %q, want empty", got)
	}
}
