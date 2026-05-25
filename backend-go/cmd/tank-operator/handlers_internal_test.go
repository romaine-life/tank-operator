package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// Pre-#486 the /api/internal/sessions/* surface used a raw SA-TokenReview
// middleware (removed; see migration guard) gated by an (ns, sa) allowlist
// plus X-Forwarded-For-derived pod-IP identity. Stage 4 retired both in
// favor of auth.romaine.life service-principal JWTs — see
// TestRequireServicePrincipal_RejectionPaths below. The TokenReview-fixture
// tests for the old gate were deleted in the same migration; regression
// guard against re-introducing the legacy patterns lives in
// scripts/check-removed-chat-runtime.mjs.

func TestHandleInternalGitHubInstallationResolvesFromProfile(t *testing.T) {
	t.Setenv("HOST_EMAIL", "host@example.test")
	t.Setenv("SUPER_ADMIN_EMAILS", "host@example.test, admin@example.test")
	installationID := int64(424242)
	jwtKey, err := auth.NewInMemoryJWT("svc-kid")
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewVerifier(jwtKey)
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"name":        "Service: tank pod-session-x",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &appServer{
		verifier: verifier,
		profiles: testProfilesStore{
			"owner@example.test": {InstallationID: &installationID},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/internal/github/installation", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalGitHubInstallation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, want := body["email"], "owner@example.test"; got != want {
		t.Fatalf("email = %v, want %q", got, want)
	}
	if got, want := body["installation_id"], float64(424242); got != want {
		t.Fatalf("installation_id = %v, want %v", got, want)
	}
	if got, want := body["is_host"], false; got != want {
		t.Fatalf("is_host = %v, want %v", got, want)
	}
	if got, want := body["is_super_admin"], false; got != want {
		t.Fatalf("is_super_admin = %v, want %v", got, want)
	}
}

func TestHandleInternalGitHubInstallationFlagsHost(t *testing.T) {
	t.Setenv("HOST_EMAIL", "host@example.test")
	t.Setenv("SUPER_ADMIN_EMAILS", "host@example.test")
	jwtKey, err := auth.NewInMemoryJWT("svc-kid")
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewVerifier(jwtKey)
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"name":        "Service: tank pod-session-x",
		"role":        "service",
		"actor_email": "host@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &appServer{
		verifier: verifier,
		// Host has no installation row; the response should still flag
		// is_host=true so mcp-github routes to the host minter without
		// needing an installation_id.
		profiles: testProfilesStore{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/internal/github/installation", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalGitHubInstallation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, want := body["is_host"], true; got != want {
		t.Fatalf("is_host = %v, want %v", got, want)
	}
	if got, want := body["is_super_admin"], true; got != want {
		t.Fatalf("is_super_admin = %v, want %v", got, want)
	}
	if body["installation_id"] != nil {
		t.Fatalf("installation_id = %v, want nil for host", body["installation_id"])
	}
}

func TestHandleInternalRetireSessionScopeRejectsDefaultScope(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-kid")
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewVerifier(jwtKey)
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"name":        "Service: tank pod-session-x",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &appServer{verifier: verifier}

	req := httptest.NewRequest(http.MethodPost, "/api/internal/session-scopes/default/retire", nil)
	req.SetPathValue("session_scope", "default")
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalRetireSessionScope(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "production session scope") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleInternalGitHubInstallationReturnsNullForUnknownEmail(t *testing.T) {
	t.Setenv("HOST_EMAIL", "host@example.test")
	jwtKey, err := auth.NewInMemoryJWT("svc-kid")
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewVerifier(jwtKey)
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"name":        "Service: tank pod-session-x",
		"role":        "service",
		"actor_email": "stranger@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &appServer{
		verifier: verifier,
		profiles: testProfilesStore{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/internal/github/installation", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalGitHubInstallation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["installation_id"] != nil {
		t.Fatalf("installation_id = %v, want nil for unknown email", body["installation_id"])
	}
	if got, want := body["is_host"], false; got != want {
		t.Fatalf("is_host = %v, want %v", got, want)
	}
}

func TestHandleInternalGitHubInstallationRejectsNonService(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-kid")
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewVerifier(jwtKey)
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":   "u-admin",
		"email": "admin@example.test",
		"iss":   "https://auth.romaine.life",
		"name":  "Admin",
		"role":  "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &appServer{verifier: verifier}
	req := httptest.NewRequest(http.MethodGet, "/api/internal/github/installation", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalGitHubInstallation(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleInternalSessionTurnTerminalReturnsTerminalEvent(t *testing.T) {
	server := internalSessionRuntimeServer(t, "12")
	server.sessionEvents = terminalEventStore{event: map[string]any{
		"type":     "turn.interrupted",
		"turn_id":  "turn-active",
		"event_id": "turn-active:turn.interrupted:client_interrupt",
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/12/turns/turn-active/terminal", nil)
	req.SetPathValue("session_id", "12")
	req.SetPathValue("turn_id", "turn-active")
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	server.handleInternalSessionTurnTerminal(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Terminal bool           `json:"terminal"`
		Event    map[string]any `json:"event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Terminal || body.Event["type"] != "turn.interrupted" {
		t.Fatalf("terminal response = %#v", body)
	}
}

func TestHandleInternalSessionTurnTerminalRejectsOtherSession(t *testing.T) {
	server := internalSessionRuntimeServer(t, "12")
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/13/turns/turn-active/terminal", nil)
	req.SetPathValue("session_id", "13")
	req.SetPathValue("turn_id", "turn-active")
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	server.handleInternalSessionTurnTerminal(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleInternalSessionRuntimeConfigRecordsAppliedConfig(t *testing.T) {
	server := internalSessionRuntimeServer(t, "12")
	registry := newTestSessionRegistry(sessionmodel.SessionRecord{
		ID:      "12",
		Email:   "owner@example.test",
		Mode:    sessionmodel.CodexGUIMode,
		Visible: true,
		Status:  "Active",
		Model:   "gpt-5.5",
		Effort:  "xhigh",
	})
	server.mgr = sessions.NewManager(server.k8s, nil, server.namespace, registry, nil, sessions.ManagerOptions{})
	req := httptest.NewRequest(http.MethodPut, "/api/internal/sessions/12/runtime-config", strings.NewReader(`{
		"model":"gpt-5.5",
		"effort":"xhigh"
	}`))
	req.SetPathValue("session_id", "12")
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	server.handleInternalSessionRuntimeConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body sessions.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RuntimeModel != "gpt-5.5" || body.RuntimeEffort != "xhigh" || body.RuntimeConfiguredAt == nil || *body.RuntimeConfiguredAt == "" {
		t.Fatalf("runtime config response = %#v", body)
	}
	record, ok, err := registry.Get(context.Background(), "owner@example.test", "12")
	if err != nil || !ok {
		t.Fatalf("registry Get ok=%v err=%v", ok, err)
	}
	if record.RuntimeModel != "gpt-5.5" || record.RuntimeEffort != "xhigh" || record.RuntimeConfiguredAt == "" {
		t.Fatalf("registry runtime config = %#v", record)
	}
}

func internalSessionRuntimeServer(t *testing.T, sessionID string) *appServer {
	t.Helper()
	k8s := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-" + sessionID,
			Namespace: "tank-operator-sessions",
			UID:       types.UID("pod-uid-" + sessionID),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tank-operator",
				"tank-operator/session-id":     sessionID,
				"tank-operator/session-scope":  "slot-a",
			},
			Annotations: map[string]string{
				"tank-operator/owner-email": "owner@example.test",
			},
		},
		Spec: corev1.PodSpec{ServiceAccountName: "claude-session"},
	})
	k8s.Fake.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authv1.TokenReview)
		if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != "tank-operator" {
			t.Fatalf("audiences=%#v, want tank-operator audience", review.Spec.Audiences)
		}
		return true, &authv1.TokenReview{
			Status: authv1.TokenReviewStatus{
				Authenticated: true,
				User: authv1.UserInfo{
					Username: "system:serviceaccount:tank-operator-sessions:claude-session",
					Extra: map[string]authv1.ExtraValue{
						"authentication.kubernetes.io/pod-name": {"session-" + sessionID},
						"authentication.kubernetes.io/pod-uid":  {"pod-uid-" + sessionID},
					},
				},
			},
		}, nil
	})
	return &appServer{
		k8s:                   k8s,
		namespace:             "tank-operator-sessions",
		sessionScope:          "slot-a",
		sessionServiceAccount: "claude-session",
		sessionEvents:         store.StubSessionEventStore{},
	}
}

// TestRequireServicePrincipal_RejectionPaths exercises the auth-gate
// shared by every /api/internal/sessions/* handler without requiring a
// real session manager. Each rejection path is a distinct telemetry
// reason (see observability.go → tank_service_role_requests_total).
func TestRequireServicePrincipal_RejectionPaths(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewVerifier(jwtKey)
	server := &appServer{verifier: verifier}

	mint := func(t *testing.T, role string, extra jwt.MapClaims) string {
		t.Helper()
		claims := jwt.MapClaims{
			"sub":   "svc:tank:session-x",
			"email": "pod-session-x@service.tank.romaine.life",
			"iss":   "https://auth.romaine.life",
			"name":  "Service: tank pod-session-x",
			"role":  role,
		}
		for k, v := range extra {
			claims[k] = v
		}
		tok, err := jwtKey.MintJWT(context.Background(), claims)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	t.Run("missing bearer → 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions", nil)
		rec := httptest.NewRecorder()
		if user := server.requireServicePrincipal(rec, req, "test-route"); user != nil {
			t.Fatalf("expected nil user; got %+v", user)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("role=user → 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+mint(t, "user", nil))
		rec := httptest.NewRecorder()
		if user := server.requireServicePrincipal(rec, req, "test-route"); user != nil {
			t.Fatalf("expected nil user; got %+v", user)
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("role=service with actor_email → accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+mint(t, "service", jwt.MapClaims{
			"actor_email": "owner@example.com",
		}))
		rec := httptest.NewRecorder()
		user := server.requireServicePrincipal(rec, req, "test-route")
		if user == nil {
			t.Fatalf("expected non-nil user; rec status=%d body=%s", rec.Code, rec.Body.String())
		}
		if user.ActorEmail != "owner@example.com" {
			t.Fatalf("ActorEmail = %q, want owner@example.com", user.ActorEmail)
		}
		if !user.IsService() {
			t.Fatalf("IsService() = false; want true")
		}
	})

	t.Run("role=service missing actor_email → 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+mint(t, "service", nil))
		rec := httptest.NewRecorder()
		if user := server.requireServicePrincipal(rec, req, "test-route"); user != nil {
			t.Fatalf("expected nil user; got %+v", user)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}

type terminalEventStore struct {
	store.StubSessionEventStore
	event map[string]any
}

func (s terminalEventStore) FindTurnTerminal(context.Context, string, string) (map[string]any, error) {
	return s.event, nil
}

type testProfilesStore map[string]profiles.Profile

func (s testProfilesStore) GetOrCreate(_ context.Context, email string) (profiles.Profile, error) {
	if profile, ok := s[email]; ok {
		return profile, nil
	}
	return profiles.Profile{Email: email}, nil
}
