package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
)

// fakeAuthMinter satisfies AppServerMCPGitHub so the handler test can inject a
// deterministic auth.romaine exchange without standing up auth.romaine.life.
type fakeAuthMinter struct {
	token    string
	expires  time.Time
	err      error
	gotEmail string
}

func (f *fakeAuthMinter) ListRepos(_ context.Context, _ string) ([]mcpgithub.Repo, error) {
	return nil, nil
}

func (f *fakeAuthMinter) MintActorToken(_ context.Context, actorEmail string) (string, time.Time, error) {
	f.gotEmail = actorEmail
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return f.token, f.expires, nil
}

func TestHandleAdminMintSessionAuthTokenReturnsServiceJWT(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	minter := &fakeAuthMinter{token: "eyJfake.jwt.token", expires: time.Unix(1700003600, 0).UTC()}
	app.mcpGitHub = minter

	req := httptest.NewRequest(http.MethodPost, "/api/admin/sessions/47/auth-token", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleAdminMintSessionAuthToken(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["token"] != "eyJfake.jwt.token" {
		t.Fatalf("token = %v", resp["token"])
	}
	if resp["actor_email"] != "owner@example.test" || resp["role"] != "service" {
		t.Fatalf("actor/role = %v / %v", resp["actor_email"], resp["role"])
	}
	// The token is minted on behalf of the target session's owner, not the admin.
	if minter.gotEmail != "owner@example.test" {
		t.Fatalf("minted for %q, want owner@example.test", minter.gotEmail)
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("audit append calls = %d, want 1", len(store.appendCalls))
	}
	audit := store.appendCalls[0]
	if audit.Action != "auth.break_glass.token" || audit.Status != "succeeded" {
		t.Fatalf("audit action/status = %s/%s", audit.Action, audit.Status)
	}
	if audit.OwnerEmail != "owner@example.test" {
		t.Fatalf("audit owner = %q", audit.OwnerEmail)
	}
	// The minted token must never be persisted in the audit payload.
	if strings.Contains(string(audit.Payload), "eyJfake.jwt.token") {
		t.Fatalf("audit payload leaked the token: %s", audit.Payload)
	}
}

func TestHandleAdminMintSessionAuthTokenForbidsNonAdmin(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	minter := &fakeAuthMinter{token: "x", expires: time.Unix(1700003600, 0).UTC()}
	app.mcpGitHub = minter

	req := httptest.NewRequest(http.MethodPost, "/api/admin/sessions/47/auth-token", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleAdminMintSessionAuthToken(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("non-admin must not mint or audit; append calls = %d", len(store.appendCalls))
	}
	if minter.gotEmail != "" {
		t.Fatalf("non-admin must not reach the minter; gotEmail=%q", minter.gotEmail)
	}
}
