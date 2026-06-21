package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// fakeCIImageAvailableStore records UpsertCIImageAvailable calls so the receiver
// tests can assert the durable write without Postgres.
type fakeCIImageAvailableStore struct {
	upserts   []pgstore.CIImageAvailable
	upsertErr error

	availableResult bool
	availableErr    error
}

func (s *fakeCIImageAvailableStore) UpsertCIImageAvailable(_ context.Context, rec pgstore.CIImageAvailable) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserts = append(s.upserts, rec)
	return nil
}

func (s *fakeCIImageAvailableStore) ImageAvailableForCommit(_ context.Context, _, _, _ string) (bool, error) {
	return s.availableResult, s.availableErr
}

func postACRWebhook(t *testing.T, app *appServer, body, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/acr", strings.NewReader(body))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	app.handleACRWebhook(rec, req)
	return rec
}

const acrPushBody = `{"action":"push","target":{"repository":"chess-tactics","tag":"sha-abc123","digest":"sha256:deadbeef"},"request":{"host":"romainecr.azurecr.io"}}`

func TestVerifyACRWebhookSecret(t *testing.T) {
	app := &appServer{acrWebhookSecret: "topsecret"}
	if !app.verifyACRWebhookSecret("Bearer topsecret") {
		t.Fatal("valid bearer rejected")
	}
	if app.verifyACRWebhookSecret("Bearer wrong") {
		t.Fatal("wrong bearer accepted")
	}
	if app.verifyACRWebhookSecret("topsecret") {
		t.Fatal("missing Bearer prefix accepted")
	}
	// Fail closed when no secret is configured.
	empty := &appServer{}
	if empty.verifyACRWebhookSecret("Bearer topsecret") {
		t.Fatal("unconfigured secret should reject all deliveries")
	}
}

// TestHandleACRWebhookEmptySecretRejectsEverything proves the fail-closed
// posture: with no configured secret, even a well-formed push with a plausible
// bearer is rejected 401 and nothing is recorded.
func TestHandleACRWebhookEmptySecretRejectsEverything(t *testing.T) {
	fake := &fakeCIImageAvailableStore{}
	app := &appServer{ciImageAvailable: fake} // acrWebhookSecret intentionally empty
	rec := postACRWebhook(t, app, acrPushBody, "Bearer topsecret")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
	if len(fake.upserts) != 0 {
		t.Fatalf("empty-secret receiver recorded: %+v", fake.upserts)
	}
}

func TestHandleACRWebhookWrongBearerRejected(t *testing.T) {
	fake := &fakeCIImageAvailableStore{}
	app := &appServer{acrWebhookSecret: "topsecret", ciImageAvailable: fake}
	rec := postACRWebhook(t, app, acrPushBody, "Bearer nope")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
	if len(fake.upserts) != 0 {
		t.Fatalf("wrong-bearer receiver recorded: %+v", fake.upserts)
	}
}

func TestHandleACRWebhookRecordsShaPush(t *testing.T) {
	fake := &fakeCIImageAvailableStore{}
	app := &appServer{acrWebhookSecret: "topsecret", ciImageAvailable: fake}
	rec := postACRWebhook(t, app, acrPushBody, "Bearer topsecret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(fake.upserts) != 1 {
		t.Fatalf("upsert calls = %+v, want exactly one", fake.upserts)
	}
	got := fake.upserts[0]
	if got.Registry != "romainecr.azurecr.io" || got.RepoName != "chess-tactics" ||
		got.CommitSHA != "abc123" || got.ImageTag != "sha-abc123" || got.ImageDigest != "sha256:deadbeef" {
		t.Fatalf("recorded image = %+v", got)
	}
}

// TestHandleACRWebhookEmptyHostFallsBack proves request.host="" falls back to the
// default registry rather than dropping the signal.
func TestHandleACRWebhookEmptyHostFallsBack(t *testing.T) {
	fake := &fakeCIImageAvailableStore{}
	app := &appServer{acrWebhookSecret: "topsecret", ciImageAvailable: fake}
	body := `{"action":"push","target":{"repository":"chess-tactics","tag":"sha-abc123"},"request":{"host":""}}`
	rec := postACRWebhook(t, app, body, "Bearer topsecret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(fake.upserts) != 1 || fake.upserts[0].Registry != defaultACRRegistry {
		t.Fatalf("expected fallback registry, got %+v", fake.upserts)
	}
}

func TestHandleACRWebhookIgnoresNonPushAction(t *testing.T) {
	fake := &fakeCIImageAvailableStore{}
	app := &appServer{acrWebhookSecret: "topsecret", ciImageAvailable: fake}
	body := `{"action":"delete","target":{"repository":"chess-tactics","tag":"sha-abc123"},"request":{"host":"romainecr.azurecr.io"}}`
	rec := postACRWebhook(t, app, body, "Bearer topsecret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (ignored)", rec.Code)
	}
	if len(fake.upserts) != 0 {
		t.Fatalf("non-push action recorded: %+v", fake.upserts)
	}
}

func TestHandleACRWebhookIgnoresNonShaTag(t *testing.T) {
	fake := &fakeCIImageAvailableStore{}
	app := &appServer{acrWebhookSecret: "topsecret", ciImageAvailable: fake}
	for _, tag := range []string{"app-abc123", "claude-container", "api-proxy-abc", "latest"} {
		body := `{"action":"push","target":{"repository":"chess-tactics","tag":"` + tag + `"},"request":{"host":"romainecr.azurecr.io"}}`
		rec := postACRWebhook(t, app, body, "Bearer topsecret")
		if rec.Code != http.StatusOK {
			t.Fatalf("tag %q: status=%d want 200 (ignored)", tag, rec.Code)
		}
	}
	if len(fake.upserts) != 0 {
		t.Fatalf("non-sha tags recorded: %+v", fake.upserts)
	}
}

func TestHandleACRWebhookMalformedJSON(t *testing.T) {
	fake := &fakeCIImageAvailableStore{}
	app := &appServer{acrWebhookSecret: "topsecret", ciImageAvailable: fake}
	rec := postACRWebhook(t, app, `{not json`, "Bearer topsecret")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if len(fake.upserts) != 0 {
		t.Fatalf("malformed body recorded: %+v", fake.upserts)
	}
}

// TestHandleACRWebhookStoreErrorIs500 proves a durable-write failure surfaces as
// 500 so ACR retries the at-least-once delivery rather than silently dropping it.
func TestHandleACRWebhookStoreErrorIs500(t *testing.T) {
	fake := &fakeCIImageAvailableStore{upsertErr: errors.New("boom")}
	app := &appServer{acrWebhookSecret: "topsecret", ciImageAvailable: fake}
	rec := postACRWebhook(t, app, acrPushBody, "Bearer topsecret")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}
