package azurepersonal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotifyGrantActivatedExchangesThenPosts(t *testing.T) {
	var sawExchange, sawNotify bool
	var gotHeader, gotSession string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/exchange/k8s", func(w http.ResponseWriter, r *http.Request) {
		sawExchange = true
		if got := r.Header.Get("Authorization"); got != "Bearer sa-token" {
			t.Errorf("exchange Authorization = %q, want Bearer sa-token", got)
		}
		// Empty body => orchestrator-as-itself (no actor_email override).
		b, _ := io.ReadAll(r.Body)
		if strings.TrimSpace(string(b)) != "{}" {
			t.Errorf("exchange body = %q, want {}", string(b))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"svc-jwt"}`)
	})
	mux.HandleFunc("/internal/grant-activated", func(w http.ResponseWriter, r *http.Request) {
		sawNotify = true
		gotHeader = r.Header.Get("X-Auth-Romaine-Token")
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("notify should not set Authorization, got %q", got)
		}
		var body struct {
			SessionID string `json:"session_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotSession = body.SessionID
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Options{
		BaseURL:     srv.URL,
		ExchangeURL: srv.URL + "/api/auth/exchange/k8s",
		ReadToken:   func(string) (string, error) { return "sa-token", nil },
	})
	if err := c.NotifyGrantActivated(context.Background(), "941"); err != nil {
		t.Fatalf("NotifyGrantActivated: %v", err)
	}
	if !sawExchange || !sawNotify {
		t.Fatalf("exchange=%v notify=%v (both should be true)", sawExchange, sawNotify)
	}
	if gotHeader != "svc-jwt" {
		t.Errorf("X-Auth-Romaine-Token = %q, want svc-jwt", gotHeader)
	}
	if gotSession != "941" {
		t.Errorf("session_id = %q, want 941", gotSession)
	}
}

func TestNotifyGrantActivatedRejectsEmptySession(t *testing.T) {
	c := NewClient(Options{ReadToken: func(string) (string, error) { return "x", nil }})
	if err := c.NotifyGrantActivated(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty session id")
	}
}

func TestNotifyGrantActivatedSurfacesNon2xx(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/exchange/k8s", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"token":"svc-jwt"}`)
	})
	mux.HandleFunc("/internal/grant-activated", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"ok":false,"reason":"caller not allowed"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Options{
		BaseURL:     srv.URL,
		ExchangeURL: srv.URL + "/api/auth/exchange/k8s",
		ReadToken:   func(string) (string, error) { return "sa", nil },
	})
	err := c.NotifyGrantActivated(context.Background(), "941")
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "caller not allowed") {
		t.Errorf("error = %v, want it to surface the reason", err)
	}
}
