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
	branchCodexImg = "romainecr.azurecr.io/codex-container:codex-BRANCHTEST"
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
	req, rec := putOverrideReq(t, "sandbox-scope", serviceToken(t), map[string]string{
		"codex_image": branchCodexImg,
		"git_ref":     "feat/x",
	})
	app.handleInternalSetSessionImageOverride(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := store.rows["sandbox-scope"]
	if got.CodexImage != branchCodexImg || got.GitRef != "feat/x" {
		t.Fatalf("stored override = %+v", got)
	}
}

func TestSetSessionImageOverrideRequiresGlimmungForSlotScope(t *testing.T) {
	app, store := imageOverrideTestServer(t, true)
	req, rec := putOverrideReq(t, "tank-operator-slot-1", serviceToken(t), map[string]string{
		"codex_image": branchCodexImg,
		"git_ref":     "feat/x",
	})
	app.handleInternalSetSessionImageOverride(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if len(store.rows) != 0 {
		t.Fatalf("override should not be stored without glimmung: %+v", store.rows)
	}
}

func TestSetSessionImageOverrideExtendsMatchingGlimmungLease(t *testing.T) {
	app, store := imageOverrideTestServer(t, true)
	glim := &fakeGlimmungClient{}
	app.glimmung = glim
	req, rec := putOverrideReq(t, "tank-operator-slot-1", serviceToken(t), map[string]string{
		"codex_image": branchCodexImg,
		"git_ref":     "feat/x",
	})
	app.handleInternalSetSessionImageOverride(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := store.rows["tank-operator-slot-1"]; !ok {
		t.Fatalf("override was not stored")
	}
	if glim.extendCalls != 1 {
		t.Fatalf("extendCalls=%d, want 1", glim.extendCalls)
	}
	if glim.extendReq.Project != "tank-operator" || glim.extendReq.SlotName == nil || *glim.extendReq.SlotName != "tank-operator-slot-1" {
		t.Fatalf("extend request=%+v", glim.extendReq)
	}
	if glim.extendReq.ExtendSeconds == nil || *glim.extendReq.ExtendSeconds != sessionImageOverrideLeaseExtendSeconds {
		t.Fatalf("extend seconds=%v, want %d", glim.extendReq.ExtendSeconds, sessionImageOverrideLeaseExtendSeconds)
	}
	if glim.extendReq.Source != "tank-operator.session-image-override" || glim.extendReq.Reason == "" {
		t.Fatalf("extend source/reason=%q/%q", glim.extendReq.Source, glim.extendReq.Reason)
	}
	if glim.extendReqEmail != otherUser {
		t.Fatalf("extend actor=%q, want %q", glim.extendReqEmail, otherUser)
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
