package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// Admin cross-user reads: role=admin bypasses the per-owner gate on
// read-only handlers (events list/SSE, session metadata, read-state
// cursor update, file/MCP/skill listings). Non-admin still gets 404 on
// any session that isn't theirs.
//
// Writes (turns, uploads, terminal attach, name/test/rollout patches,
// delete) intentionally stay per-owner — those handlers keep calling
// mgr.GetByOwner or s.resolveSessionPod (write variant), so an admin
// token cannot mutate someone else's session. The "writes stay
// per-owner" guarantee is structural rather than a per-handler test:
// every write handler in the repo calls the write helper by name, and
// the write helper takes an email rather than a User. Anyone adding a
// new write handler that bypasses that contract has to actively choose
// to pass a foreign owner — which would be caught in review.

const (
	adminEmail = "admin@example.com"
	otherUser  = "other@example.com"
)

func signedTokenWithRole(t *testing.T, email, role string) string {
	t.Helper()
	tok, err := testJWT(t).MintJWT(context.Background(), jwt.MapClaims{
		"sub":   "sub-" + email,
		"email": email,
		"name":  email,
		"role":  role,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func signedServiceToken(t *testing.T, email, actorEmail string) string {
	t.Helper()
	tok, err := testJWT(t).MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "sub-" + email,
		"email":       email,
		"name":        email,
		"role":        auth.RoleService,
		"actor_email": actorEmail,
		"iat":         time.Now().Unix(),
		"exp":         time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func adminTestServer(t *testing.T) *appServer {
	t.Helper()
	// Two pods: one owned by `otherUser`, one owned by adminEmail. The
	// admin should see both via GetByID; otherUser should see only
	// their own.
	client := fake.NewSimpleClientset(
		activitySessionPod("63", otherUser),
		activitySessionPod("64", adminEmail),
	)
	return &appServer{
		verifier: auth.NewVerifier(testJWT(t)),
		mgr: sessions.NewManager(
			client,
			nil,
			sessionmodel.SessionsNamespace,
			nil,
			nil,
			sessions.ManagerOptions{},
		),
		sessionEvents: store.StubSessionEventStore{},
		readStates:    store.NewStubConversationReadStateStore(),
	}
}

func TestAuthorizeSessionRead_AdminCanReadAnyOwner(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	user, _ := app.verifier.CurrentUser(req)
	info, status, err := app.authorizeSessionRead(req.Context(), user, "63")
	if err != nil {
		t.Fatalf("admin authorize: err=%v status=%d", err, status)
	}
	if info.Owner != otherUser {
		t.Fatalf("admin read returned owner=%q, want %q", info.Owner, otherUser)
	}
}

func TestAuthorizeSessionRead_NonAdminCrossUserReturns404NotLeak(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "intruder@example.com", auth.RoleUser))
	user, _ := app.verifier.CurrentUser(req)
	_, status, err := app.authorizeSessionRead(req.Context(), user, "63")
	if status != http.StatusNotFound {
		t.Fatalf("non-admin cross-user: status=%d, want 404", status)
	}
	// Must NOT leak the real owner email in the error message — same
	// shape ErrNotFound returns when the session truly doesn't exist.
	if err == nil || err.Error() == otherUser {
		t.Fatalf("error should not leak owner email; got %q", err)
	}
}

func TestAuthorizeSessionRead_NonAdminOwnSessionAllowed(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63", nil)
	// otherUser is the actual owner of session 63
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	user, _ := app.verifier.CurrentUser(req)
	info, status, err := app.authorizeSessionRead(req.Context(), user, "63")
	if err != nil {
		t.Fatalf("owner read: err=%v status=%d", err, status)
	}
	if info.Owner != otherUser {
		t.Fatalf("owner read returned owner=%q, want %q", info.Owner, otherUser)
	}
}

func TestAuthorizeSessionRead_ServiceActorOwnSessionAllowed(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63", nil)
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(
		t,
		"pod-94@service.tank.romaine.life",
		otherUser,
	))
	user, _ := app.verifier.CurrentUser(req)
	info, status, err := app.authorizeSessionRead(req.Context(), user, "63")
	if err != nil {
		t.Fatalf("service actor read: err=%v status=%d", err, status)
	}
	if info.Owner != otherUser {
		t.Fatalf("service actor read returned owner=%q, want %q", info.Owner, otherUser)
	}
}

func TestAuthorizeSessionRead_ServiceActorCrossUserReturns404(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63", nil)
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(
		t,
		"pod-94@service.tank.romaine.life",
		"intruder@example.com",
	))
	user, _ := app.verifier.CurrentUser(req)
	_, status, err := app.authorizeSessionRead(req.Context(), user, "63")
	if status != http.StatusNotFound {
		t.Fatalf("service actor cross-user: status=%d, want 404", status)
	}
	if err == nil || err.Error() == otherUser {
		t.Fatalf("error should not leak owner email; got %q", err)
	}
}

func TestAuthorizeSessionRead_MissingSessionIs404ForEveryone(t *testing.T) {
	app := adminTestServer(t)
	for _, tc := range []struct {
		role string
	}{
		{auth.RoleAdmin},
		{auth.RoleUser},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/999", nil)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, tc.role))
		user, _ := app.verifier.CurrentUser(req)
		_, status, _ := app.authorizeSessionRead(req.Context(), user, "999")
		if status != http.StatusNotFound {
			t.Fatalf("role=%s missing-session status=%d, want 404", tc.role, status)
		}
	}
}

func TestHandleGetSession_AdminCrossUserOK(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63", nil)
	req.SetPathValue("session_id", "63")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleGetSession(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("admin GET cross-user session: code=%d body=%s", res.Code, res.Body.String())
	}
	var body sessions.Info
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Owner != otherUser {
		t.Fatalf("returned owner=%q, want %q", body.Owner, otherUser)
	}
}

func TestHandleGetSession_NonAdminCrossUserIs404(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63", nil)
	req.SetPathValue("session_id", "63")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "intruder@example.com", auth.RoleUser))
	res := httptest.NewRecorder()

	app.handleGetSession(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("non-admin GET cross-user: code=%d body=%s, want 404", res.Code, res.Body.String())
	}
}

func TestListSessionsOwner_AdminQueryOverridesEmail(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/sessions?owner=target@example.com", nil)
	got := listSessionsOwner(
		auth.User{Email: adminEmail, Role: auth.RoleAdmin},
		req,
	)
	if got != "target@example.com" {
		t.Fatalf("admin with ?owner=: got %q, want target@example.com", got)
	}
}

func TestListSessionsOwner_ServiceUsesActorEmail(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/sessions?owner=target@example.com", nil)
	got := listSessionsOwner(
		auth.User{
			Email:      "pod-94@service.tank.romaine.life",
			Role:       auth.RoleService,
			ActorEmail: otherUser,
		},
		req,
	)
	if got != otherUser {
		t.Fatalf("service with ?owner=: got %q, want actor %q", got, otherUser)
	}
}

func TestListSessionsOwner_AdminWithoutQueryUsesOwnEmail(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	got := listSessionsOwner(
		auth.User{Email: adminEmail, Role: auth.RoleAdmin},
		req,
	)
	if got != adminEmail {
		t.Fatalf("admin without ?owner=: got %q, want %q", got, adminEmail)
	}
}

func TestListSessionsOwner_NonAdminQueryParamIsIgnored(t *testing.T) {
	// Non-admin passing ?owner= must be ignored — otherwise the query
	// param becomes a privilege-escalation footgun ("I'll just request
	// my admin friend's list").
	req := httptest.NewRequest(http.MethodGet, "/api/sessions?owner=target@example.com", nil)
	got := listSessionsOwner(
		auth.User{Email: "regular@example.com", Role: auth.RoleUser},
		req,
	)
	if got != "regular@example.com" {
		t.Fatalf("non-admin with ?owner=: got %q, want their own email", got)
	}
}
