package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/profiles"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
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
	beforeOK := testutil.ToFloat64(sessionContextWindowReportTotal.WithLabelValues("codex", "codex_app_server_token_usage", "ok"))
	req := httptest.NewRequest(http.MethodPut, "/api/internal/sessions/12/runtime-config", strings.NewReader(`{
		"model":"gpt-5.5",
		"effort":"xhigh",
		"context_window_tokens":258400,
		"context_window_source":"codex_app_server_token_usage",
		"provider_session_id":"db0a8b4b-64cd-4a9a-a592-ad5622075dc8"
	}`))
	req.SetPathValue("session_id", "12")
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	server.handleInternalSessionRuntimeConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if after := testutil.ToFloat64(sessionContextWindowReportTotal.WithLabelValues("codex", "codex_app_server_token_usage", "ok")); after != beforeOK+1 {
		t.Fatalf("context window report ok counter = %v, want %v", after, beforeOK+1)
	}
	var body sessions.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RuntimeModel != "gpt-5.5" || body.RuntimeEffort != "xhigh" || body.RuntimeConfiguredAt == nil || *body.RuntimeConfiguredAt == "" {
		t.Fatalf("runtime config response = %#v", body)
	}
	if body.RuntimeContextWindowTokens != 258400 || body.RuntimeContextWindowSource != "codex_app_server_token_usage" || body.RuntimeContextWindowObservedAt == nil || *body.RuntimeContextWindowObservedAt == "" {
		t.Fatalf("runtime context window response = %#v", body)
	}
	if body.RuntimeProviderSessionID != "db0a8b4b-64cd-4a9a-a592-ad5622075dc8" || body.RuntimeProviderSessionObservedAt == nil || *body.RuntimeProviderSessionObservedAt == "" {
		t.Fatalf("runtime provider session response = %#v", body)
	}
	record, ok, err := registry.Get(context.Background(), "owner@example.test", "12")
	if err != nil || !ok {
		t.Fatalf("registry Get ok=%v err=%v", ok, err)
	}
	if record.RuntimeModel != "gpt-5.5" || record.RuntimeEffort != "xhigh" || record.RuntimeConfiguredAt == "" {
		t.Fatalf("registry runtime config = %#v", record)
	}
	if record.RuntimeContextWindowTokens != 258400 || record.RuntimeContextWindowSource != "codex_app_server_token_usage" || record.RuntimeContextWindowObservedAt == "" {
		t.Fatalf("registry runtime context window = %#v", record)
	}
	if record.RuntimeProviderSessionID != "db0a8b4b-64cd-4a9a-a592-ad5622075dc8" || record.RuntimeProviderSessionObservedAt == "" {
		t.Fatalf("registry runtime provider session = %#v", record)
	}
}

func TestHandleInternalSessionRuntimeConfigCountsIgnoredWindow(t *testing.T) {
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
	beforeIgnored := testutil.ToFloat64(sessionContextWindowReportTotal.WithLabelValues("codex", "other", "ignored"))
	// A runtime-config report that carries model/effort but no positive
	// context window must persist config and count the window report as
	// "ignored" without touching the durable context-window columns.
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
	if after := testutil.ToFloat64(sessionContextWindowReportTotal.WithLabelValues("codex", "other", "ignored")); after != beforeIgnored+1 {
		t.Fatalf("context window report ignored counter = %v, want %v", after, beforeIgnored+1)
	}
	record, ok, err := registry.Get(context.Background(), "owner@example.test", "12")
	if err != nil || !ok {
		t.Fatalf("registry Get ok=%v err=%v", ok, err)
	}
	if record.RuntimeContextWindowTokens != 0 || record.RuntimeContextWindowSource != "" || record.RuntimeContextWindowObservedAt != "" {
		t.Fatalf("ignored window should not persist columns: %#v", record)
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

// TestHandleInternalCreateSessionSetsNameNotRequestedAt pins the fix for the
// pre-refactor oddity where the service-principal create handler threaded the
// inline `name` into CreateOptions.RequestedAt (a timestamp field) and never
// set CreateOptions.Name — so a name passed by spawn_run_session/create_session
// was dropped (the UI fell back to the short pod id) and would have polluted
// requested_at. The internal handler must behave like the public one and route
// `name` into the session's display Name.
func TestHandleInternalCreateSessionSetsNameNotRequestedAt(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	registry := newTestSessionRegistry()
	k8s := fake.NewSimpleClientset()
	server := &appServer{
		verifier:              auth.NewVerifier(jwtKey),
		k8s:                   k8s,
		namespace:             "tank-operator-sessions",
		sessionScope:          "default",
		sessionServiceAccount: "claude-session",
		sessionEvents:         &recordingSessionEventStore{},
		sessionBus:            &recordingSessionBus{},
	}
	server.mgr = sessions.NewManager(k8s, nil, server.namespace, registry, nil, sessions.ManagerOptions{})

	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions",
		strings.NewReader(`{
			"mode":"claude_gui",
			"name":"My Recovery Session",
			"initial_turn":{"client_nonce":"turn-name-test","prompt":"start the recovery"}
		}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalCreateSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var info sessions.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "My Recovery Session" {
		t.Fatalf("Info.Name = %q, want \"My Recovery Session\" (inline name must set session Name, not RequestedAt)", info.Name)
	}
	if info.RequestedAt != nil && *info.RequestedAt == "My Recovery Session" {
		t.Fatalf("RequestedAt = %q; inline name must never leak into requested_at", *info.RequestedAt)
	}
	rec2, ok, err := registry.Get(context.Background(), "owner@example.test", info.ID)
	if err != nil || !ok {
		t.Fatalf("registry Get ok=%v err=%v", ok, err)
	}
	if rec2.Name != "My Recovery Session" {
		t.Fatalf("registry record Name = %q, want \"My Recovery Session\"", rec2.Name)
	}
	if !rec2.Visible {
		t.Fatalf("registry record Visible = false, want true")
	}
	if got := len(server.sessionEvents.(*recordingSessionEventStore).upserts); got != 2 {
		t.Fatalf("session-event upserts = %d, want 2", got)
	}
	if got := len(server.sessionBus.(*recordingSessionBus).commands); got != 1 {
		t.Fatalf("published commands = %d, want 1", got)
	}
}

func TestHandleInternalCreateSessionRejectsPromptlessGUI(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	registry := newTestSessionRegistry()
	k8s := fake.NewSimpleClientset()
	server := &appServer{
		verifier:              auth.NewVerifier(jwtKey),
		k8s:                   k8s,
		namespace:             "tank-operator-sessions",
		sessionScope:          "default",
		sessionServiceAccount: "claude-session",
	}
	server.mgr = sessions.NewManager(k8s, nil, server.namespace, registry, nil, sessions.ManagerOptions{})

	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions",
		strings.NewReader(`{"mode":"claude_gui","name":"Empty GUI"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalCreateSession(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "initial_turn.prompt is required") {
		t.Fatalf("body = %s, want initial_turn prompt error", rec.Body.String())
	}
	if records, err := registry.List(context.Background(), "owner@example.test"); err != nil || len(records) != 0 {
		t.Fatalf("registry rows = %d err=%v, want none", len(records), err)
	}
}

func TestHandleInternalCreateSessionAllowsPromptlessCLI(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	registry := newTestSessionRegistry()
	k8s := fake.NewSimpleClientset()
	server := &appServer{
		verifier:              auth.NewVerifier(jwtKey),
		k8s:                   k8s,
		namespace:             "tank-operator-sessions",
		sessionScope:          "default",
		sessionServiceAccount: "claude-session",
	}
	server.mgr = sessions.NewManager(k8s, nil, server.namespace, registry, nil, sessions.ManagerOptions{})

	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions",
		strings.NewReader(`{"mode":"claude_cli","name":"CLI workspace"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalCreateSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var info sessions.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Mode != sessionmodel.ClaudeCLIMode {
		t.Fatalf("mode = %q, want claude_cli", info.Mode)
	}
}

func TestHandleInternalCreateSessionUsesTestSlotDefaultsWhenModeOmitted(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	registry := newTestSessionRegistry()
	k8s := fake.NewSimpleClientset()
	server := &appServer{
		verifier:              auth.NewVerifier(jwtKey),
		k8s:                   k8s,
		namespace:             "tank-operator-slot-2-sessions",
		sessionScope:          "tank-operator-slot-2",
		sessionServiceAccount: "claude-session",
		sessionEvents:         &recordingSessionEventStore{},
		sessionBus:            &recordingSessionBus{},
		platformSettings: &fakePlatformSettingsStore{
			defaults: pgstore.TestSlotSessionDefaults{
				Mode:   sessionmodel.CodexGUIMode,
				Model:  "gpt-5.3-codex-spark",
				Effort: "low",
			},
		},
	}
	server.mgr = sessions.NewManager(k8s, nil, server.namespace, registry, nil, sessions.ManagerOptions{})

	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions",
		strings.NewReader(`{
			"name":"slot validation",
			"initial_turn":{"client_nonce":"turn-slot-default","prompt":"validate the slot"}
		}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalCreateSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var info sessions.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Mode != sessionmodel.CodexGUIMode || info.Model != "gpt-5.3-codex-spark" || info.Effort != "low" {
		t.Fatalf("created info = mode %q model %q effort %q", info.Mode, info.Model, info.Effort)
	}
	rec2, ok, err := registry.Get(context.Background(), "owner@example.test", info.ID)
	if err != nil || !ok {
		t.Fatalf("registry Get ok=%v err=%v", ok, err)
	}
	if rec2.Mode != sessionmodel.CodexGUIMode || rec2.Model != "gpt-5.3-codex-spark" || rec2.Effort != "low" {
		t.Fatalf("registry record = mode %q model %q effort %q", rec2.Mode, rec2.Model, rec2.Effort)
	}
	if got := len(server.sessionEvents.(*recordingSessionEventStore).upserts); got != 2 {
		t.Fatalf("session-event upserts = %d, want 2", got)
	}
	if got := len(server.sessionBus.(*recordingSessionBus).commands); got != 1 {
		t.Fatalf("published commands = %d, want 1", got)
	}
}

func TestHandleInternalCreateSessionInTestSlotBlocksExpensiveModelWithApprovalURL(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeControlActionStore{}
	server := &appServer{
		verifier:       auth.NewVerifier(jwtKey),
		sessionScope:   "tank-operator-slot-2",
		controlActions: store,
	}
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:origin-77",
		"email":       "pod-origin-77@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions", strings.NewReader(`{
		"mode":"claude_gui",
		"model":"claude-opus-4-8",
		"effort":"high",
		"initial_turn":{"client_nonce":"turn-expensive","prompt":"validate the slot"}
	}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set(originSessionHeader, "origin-77")
	rec := httptest.NewRecorder()

	server.handleInternalCreateSession(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "approval_required" {
		t.Fatalf("body = %#v", body)
	}
	approvalURL, _ := body["approval_url"].(string)
	if !strings.HasPrefix(approvalURL, "https://tank-operator-slot-2.tank.dev.romaine.life/sessions/origin-77/test-slot-model/tank-test-slot-model-request-origin-77-") ||
		strings.Contains(approvalURL, "auth.romaine.life") ||
		strings.Contains(approvalURL, "model=claude-opus-4-8") {
		t.Fatalf("approval_url = %q", approvalURL)
	}
	if body["low_model"] != "claude-haiku-4-5" || body["low_effort"] != "low" {
		t.Fatalf("low guidance = model %v effort %v", body["low_model"], body["low_effort"])
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != testSlotModelRequestAction || got.SessionID != "origin-77" || got.OwnerEmail != "owner@example.test" {
		t.Fatalf("recorded request = %#v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["approval_url"] != approvalURL {
		t.Fatalf("payload approval_url = %v, want %q", payload["approval_url"], approvalURL)
	}
}

func TestHandleInternalCreateSessionInTestSlotAllowsExpensiveModelAfterApproval(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	registry := newTestSessionRegistry()
	k8s := fake.NewSimpleClientset()
	grantPayload, _ := json.Marshal(map[string]any{
		"mode":       sessionmodel.ClaudeGUIMode,
		"provider":   "claude",
		"model":      "claude-opus-4-8",
		"effort":     "high",
		"expires_at": "2999-01-01T00:00:00Z",
	})
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:      "grant-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-2",
			SessionID:    "origin-77",
			Action:       testSlotModelGrantAction,
			Status:       "succeeded",
			Payload:      grantPayload,
		}},
	}
	server := &appServer{
		verifier:              auth.NewVerifier(jwtKey),
		k8s:                   k8s,
		namespace:             "tank-operator-slot-2-sessions",
		sessionScope:          "tank-operator-slot-2",
		sessionServiceAccount: "claude-session",
		sessionEvents:         &recordingSessionEventStore{},
		sessionBus:            &recordingSessionBus{},
		controlActions:        store,
	}
	server.mgr = sessions.NewManager(k8s, nil, server.namespace, registry, nil, sessions.ManagerOptions{})
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:origin-77",
		"email":       "pod-origin-77@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions", strings.NewReader(`{
		"mode":"claude_gui",
		"model":"claude-opus-4-8",
		"effort":"high",
		"initial_turn":{"client_nonce":"turn-approved","prompt":"validate the slot"}
	}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set(originSessionHeader, "origin-77")
	rec := httptest.NewRecorder()

	server.handleInternalCreateSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var info sessions.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Model != "claude-opus-4-8" || info.Effort != "high" {
		t.Fatalf("created info model=%q effort=%q", info.Model, info.Effort)
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want no new approval request", len(store.appendCalls))
	}
	if got := len(server.sessionBus.(*recordingSessionBus).commands); got != 1 {
		t.Fatalf("published commands = %d, want 1", got)
	}
}

func TestHandleInternalCreateSessionRejectsUnavailableCodexModel(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	server := &appServer{
		verifier: auth.NewVerifier(jwtKey),
	}
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions", strings.NewReader(`{
		"mode":"codex_gui",
		"model":"gpt-5.3-codex",
		"effort":"medium"
	}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalCreateSession(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "model is not available for codex") ||
		!strings.Contains(body, "gpt-5.5") ||
		!strings.Contains(body, "gpt-5.3-codex-spark") ||
		strings.Contains(body, "gpt-5.3-codex|") {
		t.Fatalf("body = %s, want supported Codex model list without gpt-5.3-codex", body)
	}
}

func TestHandleInternalCreateSessionRejectsRetiredCodexGUIMode(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	server := &appServer{
		verifier: auth.NewVerifier(jwtKey),
	}
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions", strings.NewReader(`{
		"mode":"codex_exec_gui",
		"model":"gpt-5.5",
		"effort":"high"
	}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalCreateSession(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session mode codex_exec_gui is retired; use codex_gui") {
		t.Fatalf("body = %s, want retired mode error", rec.Body.String())
	}
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
