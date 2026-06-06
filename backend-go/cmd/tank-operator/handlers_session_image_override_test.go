package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

const (
	branchCodexImg       = "romainecr.azurecr.io/codex-container:codex-BRANCHTEST"
	branchAntigravityImg = "romainecr.azurecr.io/antigravity-container:antigravity-BRANCHTEST"
)

type fakeImageOverrideStore struct {
	rows map[string]pgstore.SessionImageOverride
}

func newFakeImageOverrideStore() *fakeImageOverrideStore {
	return &fakeImageOverrideStore{rows: map[string]pgstore.SessionImageOverride{}}
}

func (f *fakeImageOverrideStore) Get(ctx context.Context, scope string) (pgstore.SessionImageOverride, error) {
	if ov, ok := f.rows[scope]; ok {
		return ov, nil
	}
	return pgstore.SessionImageOverride{}, pgstore.ErrSessionImageOverrideNotFound
}

func (f *fakeImageOverrideStore) Upsert(ctx context.Context, ov pgstore.SessionImageOverride) error {
	f.rows[ov.SessionScope] = ov
	return nil
}

func (f *fakeImageOverrideStore) Delete(ctx context.Context, scope string) (bool, error) {
	_, ok := f.rows[scope]
	delete(f.rows, scope)
	return ok, nil
}

func imageOverrideTestServer(t *testing.T, enabled bool) (*appServer, *fakeImageOverrideStore) {
	t.Helper()
	store := newFakeImageOverrideStore()
	return &appServer{
		verifier:                     auth.NewVerifier(testJWT(t)),
		imageOverrides:               store,
		sessionImageOverridesEnabled: enabled,
	}, store
}

func putOverrideReq(t *testing.T, scope, token string, body map[string]string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/internal/session-scopes/"+scope+"/image-override", bytes.NewReader(raw))
	req.SetPathValue("session_scope", scope)
	req.Header.Set("Authorization", "Bearer "+token)
	return req, httptest.NewRecorder()
}

func serviceToken(t *testing.T) string {
	return signedServiceToken(t, "pod-1@service.tank.romaine.life", otherUser)
}

func TestSetSessionImageOverride_HappyPath(t *testing.T) {
	app, store := imageOverrideTestServer(t, true)
	req, rec := putOverrideReq(t, "tank-operator-slot-1", serviceToken(t), map[string]string{
		"codex_image": branchCodexImg,
		"git_ref":     "feat/x",
	})
	app.handleInternalSetSessionImageOverride(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := store.rows["tank-operator-slot-1"]
	if got.CodexImage != branchCodexImg || got.GitRef != "feat/x" {
		t.Fatalf("stored override = %+v", got)
	}
}

func TestSetSessionImageOverride_AntigravityImage(t *testing.T) {
	app, store := imageOverrideTestServer(t, true)
	req, rec := putOverrideReq(t, "tank-operator-slot-1", serviceToken(t), map[string]string{
		"antigravity_image": branchAntigravityImg,
		"git_ref":           "feat/antigravity",
	})
	app.handleInternalSetSessionImageOverride(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := store.rows["tank-operator-slot-1"]
	if got.AntigravityImage != branchAntigravityImg || got.GitRef != "feat/antigravity" {
		t.Fatalf("stored override = %+v", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["antigravity_image"] != branchAntigravityImg {
		t.Fatalf("response antigravity_image = %v, want %q", body["antigravity_image"], branchAntigravityImg)
	}
}

// PROD SAFETY: the endpoint refuses the production scope outright.
func TestSetSessionImageOverride_RefusesProdScope(t *testing.T) {
	app, store := imageOverrideTestServer(t, true)
	req, rec := putOverrideReq(t, prodSessionScope, serviceToken(t), map[string]string{"codex_image": branchCodexImg})
	app.handleInternalSetSessionImageOverride(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(store.rows) != 0 {
		t.Fatalf("prod override was written: %+v", store.rows)
	}
}

// PROD SAFETY: with the test-env gate off (production), the write path is 403.
func TestSetSessionImageOverride_GateOff(t *testing.T) {
	app, _ := imageOverrideTestServer(t, false)
	req, rec := putOverrideReq(t, "tank-operator-slot-1", serviceToken(t), map[string]string{"codex_image": branchCodexImg})
	app.handleInternalSetSessionImageOverride(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 when disabled; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSetSessionImageOverride_RequiresImage(t *testing.T) {
	app, _ := imageOverrideTestServer(t, true)
	req, rec := putOverrideReq(t, "tank-operator-slot-1", serviceToken(t), map[string]string{})
	app.handleInternalSetSessionImageOverride(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for empty body; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSetSessionImageOverride_RejectsHumanRole(t *testing.T) {
	app, _ := imageOverrideTestServer(t, true)
	tok := signedTokenWithRole(t, otherUser, auth.RoleUser)
	req, rec := putOverrideReq(t, "tank-operator-slot-1", tok, map[string]string{"codex_image": branchCodexImg})
	app.handleInternalSetSessionImageOverride(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 for role=user; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetSessionImageOverride_NotSet(t *testing.T) {
	app, _ := imageOverrideTestServer(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/session-scopes/tank-operator-slot-1/image-override", nil)
	req.SetPathValue("session_scope", "tank-operator-slot-1")
	req.Header.Set("Authorization", "Bearer "+serviceToken(t))
	rec := httptest.NewRecorder()
	app.handleInternalGetSessionImageOverride(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 when unset; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteSessionImageOverride(t *testing.T) {
	app, store := imageOverrideTestServer(t, true)
	store.rows["tank-operator-slot-1"] = pgstore.SessionImageOverride{
		SessionScope: "tank-operator-slot-1",
		CodexImage:   branchCodexImg,
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/internal/session-scopes/tank-operator-slot-1/image-override", nil)
	req.SetPathValue("session_scope", "tank-operator-slot-1")
	req.Header.Set("Authorization", "Bearer "+serviceToken(t))
	rec := httptest.NewRecorder()
	app.handleInternalDeleteSessionImageOverride(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.rows) != 0 {
		t.Fatalf("override not deleted: %+v", store.rows)
	}
}
