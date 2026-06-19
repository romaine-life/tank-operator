package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// Admin cross-user reads: Tank admin power bypasses the per-owner gate on
// read-only handlers (events list/SSE, session metadata, read-state
// cursor update, file/MCP/skill listings). Non-admin still gets 404 on
// any session that isn't theirs. Human role=admin has admin power, and
// role=service gets the same power only when actor_email is a configured
// super admin.
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
		"iss":   "https://auth.romaine.life",
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
		"iss":         "https://auth.romaine.life",
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

func registryOnlyAuthTestServer(t *testing.T, records ...sessionmodel.SessionRecord) *appServer {
	t.Helper()
	registry := newTestSessionRegistry(records...)
	return &appServer{
		verifier: auth.NewVerifier(testJWT(t)),
		mgr: sessions.NewManager(
			fake.NewSimpleClientset(),
			nil,
			sessionmodel.SessionsNamespace,
			registry,
			nil,
			sessions.ManagerOptions{},
		),
		sessionEvents: store.StubSessionEventStore{},
		readStates:    store.NewStubConversationReadStateStore(),
		sessionScope:  prodSessionScope,
	}
}

type fakeStreamAuthTicketStore struct {
	created          pgstore.StreamAuthTicket
	createErr        error
	validateToken    string
	validateKind     string
	validateScope    string
	validateSession  string
	validateResponse pgstore.StreamAuthTicket
	validateErr      error
}

func (s *fakeStreamAuthTicketStore) Create(_ context.Context, ticket pgstore.StreamAuthTicket) error {
	s.created = ticket
	return s.createErr
}

func (s *fakeStreamAuthTicketStore) Validate(_ context.Context, token, streamKind, sessionScope, sessionID string) (pgstore.StreamAuthTicket, error) {
	s.validateToken = token
	s.validateKind = streamKind
	s.validateScope = sessionScope
	s.validateSession = sessionID
	return s.validateResponse, s.validateErr
}

func TestRequireBrowserStreamAuthAcceptsStreamTicket(t *testing.T) {
	tickets := &fakeStreamAuthTicketStore{
		validateResponse: pgstore.StreamAuthTicket{
			Sub:          "sub-user@example.com",
			Email:        "user@example.com",
			Name:         "User",
			Role:         auth.RoleUser,
			StreamKind:   streamKindSessionEvents,
			SessionScope: "default",
			SessionID:    "63",
		},
	}
	app := &appServer{streamAuthTickets: tickets, sessionScope: "default"}
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/63/events?stream_ticket=ticket-123", nil)
	response := httptest.NewRecorder()

	user, sessionScope, ok := app.requireBrowserStreamAuth(response, request, streamKindSessionEvents, "63")
	if !ok {
		t.Fatalf("requireBrowserStreamAuth rejected stream ticket: status=%d body=%s", response.Code, response.Body.String())
	}
	if user.Email != "user@example.com" {
		t.Fatalf("user email = %q, want user@example.com", user.Email)
	}
	if sessionScope != "default" {
		t.Fatalf("session scope = %q, want default", sessionScope)
	}
	if tickets.validateToken != "ticket-123" || tickets.validateKind != streamKindSessionEvents || tickets.validateScope != "default" || tickets.validateSession != "63" {
		t.Fatalf("validate args = (%q,%q,%q,%q)", tickets.validateToken, tickets.validateKind, tickets.validateScope, tickets.validateSession)
	}
}

func TestRequireBrowserStreamAuthDoesNotTurnClientCancelIntoStoreFailure(t *testing.T) {
	tickets := &fakeStreamAuthTicketStore{validateErr: context.Canceled}
	app := &appServer{streamAuthTickets: tickets, sessionScope: "default"}
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/63/events?stream_ticket=ticket-123", nil)
	response := httptest.NewRecorder()

	if _, _, ok := app.requireBrowserStreamAuth(response, request, streamKindSessionEvents, "63"); ok {
		t.Fatal("requireBrowserStreamAuth accepted a canceled validation")
	}
	if response.Code != statusClientClosedRequest {
		t.Fatalf("status = %d, want %d", response.Code, statusClientClosedRequest)
	}
}

func TestRequireAuthRejectsStreamTicketQuery(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	request := httptest.NewRequest(http.MethodGet, "/api/sessions?stream_ticket=ticket-123", nil)
	response := httptest.NewRecorder()

	if _, ok := app.requireAuth(response, request); ok {
		t.Fatal("requireAuth accepted stream_ticket query; only browser stream handlers should allow it")
	}
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestHandleCreateStreamTicketScopesSessionEventTicket(t *testing.T) {
	app := adminTestServer(t)
	tickets := &fakeStreamAuthTicketStore{}
	app.streamAuthTickets = tickets
	app.sessionScope = "default"
	request := httptest.NewRequest(http.MethodPost, "/api/auth/stream-ticket", strings.NewReader(`{
		"stream": "session-events",
		"session_id": "63"
	}`))
	request.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	response := httptest.NewRecorder()

	app.handleCreateStreamTicket(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if tickets.created.Ticket == "" {
		t.Fatal("created ticket is empty")
	}
	if tickets.created.Email != otherUser || tickets.created.StreamKind != streamKindSessionEvents || tickets.created.SessionID != "63" || tickets.created.SessionScope != "default" {
		t.Fatalf("created ticket = %#v", tickets.created)
	}
	if time.Until(tickets.created.ExpiresAt) <= 0 || time.Until(tickets.created.ExpiresAt) > streamAuthTicketTTL+time.Second {
		t.Fatalf("expiresAt = %s, want short-lived ticket", tickets.created.ExpiresAt)
	}
}

func TestHandleCreateStreamTicketScopesPinnedReposTicket(t *testing.T) {
	app := adminTestServer(t)
	tickets := &fakeStreamAuthTicketStore{}
	app.streamAuthTickets = tickets
	app.sessionScope = "default"
	request := httptest.NewRequest(http.MethodPost, "/api/auth/stream-ticket", strings.NewReader(`{
		"stream": "pinned-repos"
	}`))
	request.Header.Set("Authorization", "Bearer "+signedServiceToken(
		t,
		"pod-orchestrator@service.tank-operator.romaine.life",
		otherUser,
	))
	response := httptest.NewRecorder()

	app.handleCreateStreamTicket(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if tickets.created.Email != "pod-orchestrator@service.tank-operator.romaine.life" ||
		tickets.created.ActorEmail != otherUser ||
		tickets.created.Role != auth.RoleService ||
		tickets.created.StreamKind != streamKindPinnedRepos ||
		tickets.created.SessionID != "" ||
		tickets.created.SessionScope != "default" {
		t.Fatalf("created ticket = %#v", tickets.created)
	}
}

func TestHandleCreateStreamTicketScopesFileRawTicketToPath(t *testing.T) {
	app := adminTestServer(t)
	tickets := &fakeStreamAuthTicketStore{}
	app.streamAuthTickets = tickets
	app.sessionScope = "default"
	request := httptest.NewRequest(http.MethodPost, "/api/auth/stream-ticket", strings.NewReader(`{
		"stream": "file-raw",
		"session_id": "63",
		"path": "/workspace/screenshots/result.png"
	}`))
	request.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	response := httptest.NewRecorder()

	app.handleCreateStreamTicket(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if tickets.created.StreamKind != streamKindFileRaw ||
		tickets.created.SessionID != fileRawTicketResourceID("63", "screenshots/result.png") ||
		tickets.created.SessionScope != "default" ||
		tickets.created.Email != otherUser {
		t.Fatalf("created ticket = %#v", tickets.created)
	}
}

func TestHandleCreateStreamTicketDoesNotTurnClientCancelIntoStoreFailure(t *testing.T) {
	app := adminTestServer(t)
	tickets := &fakeStreamAuthTicketStore{createErr: context.Canceled}
	app.streamAuthTickets = tickets
	app.sessionScope = "default"
	request := httptest.NewRequest(http.MethodPost, "/api/auth/stream-ticket", strings.NewReader(`{
		"stream": "pinned-repos"
	}`))
	request.Header.Set("Authorization", "Bearer "+signedServiceToken(
		t,
		"pod-orchestrator@service.tank-operator.romaine.life",
		otherUser,
	))
	response := httptest.NewRecorder()

	app.handleCreateStreamTicket(response, request)
	if response.Code != statusClientClosedRequest {
		t.Fatalf("status = %d, want %d", response.Code, statusClientClosedRequest)
	}
}

func TestHandleCreateStreamTicketAllowsServiceActor(t *testing.T) {
	app := adminTestServer(t)
	tickets := &fakeStreamAuthTicketStore{}
	app.streamAuthTickets = tickets
	app.sessionScope = "default"
	request := httptest.NewRequest(http.MethodPost, "/api/auth/stream-ticket", strings.NewReader(`{
		"stream": "session-list"
	}`))
	request.Header.Set("Authorization", "Bearer "+signedServiceToken(
		t,
		"pod-orchestrator@service.tank-operator.romaine.life",
		otherUser,
	))
	response := httptest.NewRecorder()

	app.handleCreateStreamTicket(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if tickets.created.Email != "pod-orchestrator@service.tank-operator.romaine.life" ||
		tickets.created.ActorEmail != otherUser ||
		tickets.created.Role != auth.RoleService ||
		tickets.created.StreamKind != streamKindSessionList ||
		tickets.created.SessionID != "" {
		t.Fatalf("created ticket = %#v", tickets.created)
	}
}

type testSessionRegistry struct {
	records        map[string]map[string]sessionmodel.SessionRecord
	spawnedAppends []recordedSpawnedAppend
}

type recordedSpawnedAppend struct {
	Email           string
	ParentSessionID string
	Ref             sessionmodel.SpawnedSessionRef
}

func newTestSessionRegistry(records ...sessionmodel.SessionRecord) *testSessionRegistry {
	out := &testSessionRegistry{records: map[string]map[string]sessionmodel.SessionRecord{}}
	for _, record := range records {
		owner := record.Email
		if owner == "" {
			continue
		}
		if out.records[owner] == nil {
			out.records[owner] = map[string]sessionmodel.SessionRecord{}
		}
		out.records[owner][record.ID] = record
	}
	return out
}

func (r *testSessionRegistry) List(_ context.Context, owner string) ([]sessionmodel.SessionRecord, error) {
	var out []sessionmodel.SessionRecord
	for _, record := range r.records[owner] {
		out = append(out, record)
	}
	return out, nil
}

func (r *testSessionRegistry) Get(_ context.Context, owner, sessionID string) (sessionmodel.SessionRecord, bool, error) {
	records := r.records[owner]
	if records == nil {
		return sessionmodel.SessionRecord{}, false, nil
	}
	record, ok := records[sessionID]
	return record, ok, nil
}

func (r *testSessionRegistry) OwnerForSession(_ context.Context, scope, sessionID string) (string, error) {
	scope = normalizeSessionScope(scope)
	for owner, records := range r.records {
		if record, ok := records[sessionID]; ok {
			recordScope := normalizeSessionScope(record.Scope)
			if recordScope == scope {
				return owner, nil
			}
		}
	}
	return "", nil
}

func (r *testSessionRegistry) NextSessionID(_ context.Context) (string, error) {
	return "", nil
}

func (r *testSessionRegistry) Upsert(_ context.Context, record sessionmodel.SessionRecord) error {
	if r.records == nil {
		r.records = map[string]map[string]sessionmodel.SessionRecord{}
	}
	owner := record.Email
	if r.records[owner] == nil {
		r.records[owner] = map[string]sessionmodel.SessionRecord{}
	}
	if existing, ok := r.records[owner][record.ID]; ok && record.BugLabel == nil {
		record.BugLabel = existing.BugLabel
	}
	r.records[owner][record.ID] = record
	return nil
}

func (r *testSessionRegistry) SetName(_ context.Context, _, _ string, _ *string) error { return nil }
func (r *testSessionRegistry) SetRunConfig(_ context.Context, owner, sessionID, model, effort string) error {
	if r.records == nil || r.records[owner] == nil {
		return nil
	}
	record, ok := r.records[owner][sessionID]
	if !ok {
		return nil
	}
	record.Model = model
	record.Effort = effort
	r.records[owner][sessionID] = record
	return nil
}
func (r *testSessionRegistry) SetOpenTarget(_ context.Context, owner, sessionID, target string) error {
	if r.records == nil || r.records[owner] == nil {
		return nil
	}
	record, ok := r.records[owner][sessionID]
	if !ok {
		return nil
	}
	record.OpenTarget = target
	r.records[owner][sessionID] = record
	return nil
}
func (r *testSessionRegistry) SetBugLabel(_ context.Context, owner, sessionID string, label *sessionmodel.SessionBugLabel) error {
	if label == nil {
		return r.SetBugLabels(context.Background(), owner, sessionID, nil)
	}
	return r.SetBugLabels(context.Background(), owner, sessionID, []*sessionmodel.SessionBugLabel{label})
}
func (r *testSessionRegistry) SetBugLabels(_ context.Context, owner, sessionID string, labels []*sessionmodel.SessionBugLabel) error {
	if r.records == nil || r.records[owner] == nil {
		return nil
	}
	record := r.records[owner][sessionID]
	if len(labels) > 0 {
		record.BugLabel = labels[0]
	} else {
		record.BugLabel = nil
	}
	r.records[owner][sessionID] = record
	return nil
}
func (r *testSessionRegistry) SetTestState(_ context.Context, owner, sessionID string, state map[string]any) error {
	if r.records == nil || r.records[owner] == nil {
		return nil
	}
	record := r.records[owner][sessionID]
	record.TestState = state
	r.records[owner][sessionID] = record
	return nil
}
func (r *testSessionRegistry) SetRolloutState(_ context.Context, _, _ string, _ map[string]any) error {
	return nil
}
func (r *testSessionRegistry) SetCloneState(_ context.Context, _, _ string, _ map[string]any) error {
	return nil
}
func (r *testSessionRegistry) AppendSpawnedSession(_ context.Context, email, parentSessionID string, ref sessionmodel.SpawnedSessionRef) error {
	r.spawnedAppends = append(r.spawnedAppends, recordedSpawnedAppend{
		Email:           email,
		ParentSessionID: parentSessionID,
		Ref:             ref,
	})
	return nil
}
func (r *testSessionRegistry) SetRuntimeConfig(_ context.Context, email, sessionID, model, effort string) error {
	records := r.records[email]
	if records == nil {
		return nil
	}
	record, ok := records[sessionID]
	if !ok {
		return nil
	}
	record.RuntimeModel = model
	record.RuntimeEffort = effort
	record.RuntimeConfiguredAt = time.Now().UTC().Format(time.RFC3339Nano)
	records[sessionID] = record
	return nil
}
func (r *testSessionRegistry) SetRuntimeContextWindow(_ context.Context, email, sessionID string, tokens int64, source string) error {
	records := r.records[email]
	if records == nil {
		return nil
	}
	record, ok := records[sessionID]
	if !ok {
		return nil
	}
	// Latest-observed-wins, mirroring the store (a mid-session model re-pin
	// legitimately updates the window).
	record.RuntimeContextWindowTokens = tokens
	record.RuntimeContextWindowSource = source
	record.RuntimeContextWindowObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	records[sessionID] = record
	return nil
}
func (r *testSessionRegistry) SetRuntimeProviderSessionID(_ context.Context, email, sessionID, providerSessionID string) error {
	records := r.records[email]
	if records == nil {
		return nil
	}
	record, ok := records[sessionID]
	if !ok {
		return nil
	}
	record.RuntimeProviderSessionID = providerSessionID
	record.RuntimeProviderSessionObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	records[sessionID] = record
	return nil
}
func (r *testSessionRegistry) SetProviderRateLimitInfo(_ context.Context, email, sessionID string, info map[string]any) error {
	records := r.records[email]
	if records == nil {
		return nil
	}
	record, ok := records[sessionID]
	if !ok {
		return nil
	}
	record.ProviderRateLimitInfo = info
	record.ProviderRateLimitObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	records[sessionID] = record
	return nil
}
func (r *testSessionRegistry) Reorder(_ context.Context, _ string, orderedIDs []string) ([]string, error) {
	return orderedIDs, nil
}
func (r *testSessionRegistry) MarkDeleted(_ context.Context, _, _ string) error { return nil }

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

func TestAuthorizeSessionRead_ServiceAdminActorCanReadAnyOwner(t *testing.T) {
	t.Setenv("SUPER_ADMIN_EMAILS", adminEmail)
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63", nil)
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-200@service.tank.romaine.life", adminEmail))
	user, _ := app.verifier.CurrentUser(req)
	info, status, err := app.authorizeSessionRead(req.Context(), user, "63")
	if err != nil {
		t.Fatalf("service-admin authorize: err=%v status=%d", err, status)
	}
	if info.Owner != otherUser {
		t.Fatalf("service-admin read returned owner=%q, want %q", info.Owner, otherUser)
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

func TestAuthorizeSessionRead_UsesRegistryWhenPodIsGone(t *testing.T) {
	reg := newTestSessionRegistry(sessionmodel.SessionRecord{
		ID:      "71",
		Email:   otherUser,
		Mode:    sessionmodel.CodexGUIMode,
		Visible: true,
		Status:  "Failed",
	})
	app := &appServer{
		verifier: auth.NewVerifier(testJWT(t)),
		mgr: sessions.NewManager(
			fake.NewSimpleClientset(),
			nil,
			sessionmodel.SessionsNamespace,
			reg,
			nil,
			sessions.ManagerOptions{},
		),
		sessionEvents: store.StubSessionEventStore{},
		readStates:    store.NewStubConversationReadStateStore(),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/71/timeline", nil)
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(
		t,
		"pod-94@service.tank.romaine.life",
		otherUser,
	))
	user, _ := app.verifier.CurrentUser(req)
	info, status, err := app.authorizeSessionRead(req.Context(), user, "71")
	if err != nil {
		t.Fatalf("registry-backed service read: err=%v status=%d", err, status)
	}
	if info.Owner != otherUser || info.ID != "71" || info.Status != "Failed" {
		t.Fatalf("registry-backed info = %+v, want id=71 owner=%s status=Failed", info, otherUser)
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

func TestAuthorizeSessionRead_InvisibleRegistryRowStillHiddenFromGeneralRead(t *testing.T) {
	app := registryOnlyAuthTestServer(t, sessionmodel.SessionRecord{
		ID:      "71",
		Email:   otherUser,
		Scope:   prodSessionScope,
		Mode:    sessionmodel.CodexGUIMode,
		Visible: false,
		Status:  "Failed",
	})
	user := auth.User{Email: otherUser, Role: auth.RoleUser}

	_, status, err := app.authorizeSessionReadInScope(context.Background(), user, "71", prodSessionScope)
	if status != http.StatusNotFound {
		t.Fatalf("general read status=%d err=%v, want 404", status, err)
	}
}

func TestAuthorizeSessionTranscriptRead_OwnerCanReadInvisibleRegistryRow(t *testing.T) {
	app := registryOnlyAuthTestServer(t, sessionmodel.SessionRecord{
		ID:      "71",
		Email:   otherUser,
		Scope:   prodSessionScope,
		Mode:    sessionmodel.CodexGUIMode,
		Visible: false,
		Status:  "Failed",
	})
	user := auth.User{Email: otherUser, Role: auth.RoleUser}

	info, status, err := app.authorizeSessionTranscriptReadInScope(context.Background(), user, "71", prodSessionScope)
	if err != nil || status != http.StatusOK {
		t.Fatalf("transcript read status=%d err=%v", status, err)
	}
	if info.ID != "71" || info.Owner != otherUser || info.Status != "Failed" {
		t.Fatalf("transcript info = %+v", info)
	}
}

func TestAuthorizeSessionTranscriptRead_ServiceActorOwnInvisibleRowAllowed(t *testing.T) {
	app := registryOnlyAuthTestServer(t, sessionmodel.SessionRecord{
		ID:      "71",
		Email:   otherUser,
		Scope:   prodSessionScope,
		Mode:    sessionmodel.CodexGUIMode,
		Visible: false,
		Status:  "Failed",
	})
	user := auth.User{
		Email:      "pod-71@service.tank.romaine.life",
		Role:       auth.RoleService,
		ActorEmail: otherUser,
	}

	info, status, err := app.authorizeSessionTranscriptReadInScope(context.Background(), user, "71", prodSessionScope)
	if err != nil || status != http.StatusOK {
		t.Fatalf("service transcript read status=%d err=%v", status, err)
	}
	if info.Owner != otherUser {
		t.Fatalf("service transcript owner = %q, want %q", info.Owner, otherUser)
	}
}

func TestAuthorizeSessionTranscriptRead_InvisibleCrossUserMasked(t *testing.T) {
	app := registryOnlyAuthTestServer(t, sessionmodel.SessionRecord{
		ID:      "71",
		Email:   otherUser,
		Scope:   prodSessionScope,
		Mode:    sessionmodel.CodexGUIMode,
		Visible: false,
		Status:  "Failed",
	})
	user := auth.User{Email: "intruder@example.com", Role: auth.RoleUser}

	_, status, err := app.authorizeSessionTranscriptReadInScope(context.Background(), user, "71", prodSessionScope)
	if status != http.StatusNotFound {
		t.Fatalf("cross-user transcript status=%d err=%v, want 404", status, err)
	}
}

func TestAuthorizeSessionTranscriptRead_AdminCanReadInvisibleCrossUserRow(t *testing.T) {
	app := registryOnlyAuthTestServer(t, sessionmodel.SessionRecord{
		ID:      "71",
		Email:   otherUser,
		Scope:   prodSessionScope,
		Mode:    sessionmodel.CodexGUIMode,
		Visible: false,
		Status:  "Failed",
	})
	user := auth.User{Email: adminEmail, Role: auth.RoleAdmin}

	info, status, err := app.authorizeSessionTranscriptReadInScope(context.Background(), user, "71", prodSessionScope)
	if err != nil || status != http.StatusOK {
		t.Fatalf("admin transcript status=%d err=%v", status, err)
	}
	if info.Owner != otherUser || info.ID != "71" {
		t.Fatalf("admin transcript info = %+v", info)
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

func TestListSessionsOwner_ServiceAdminActorQueryOverridesEmail(t *testing.T) {
	t.Setenv("SUPER_ADMIN_EMAILS", adminEmail)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions?owner=target@example.com", nil)
	got := listSessionsOwner(
		auth.User{
			Email:      "pod-200@service.tank.romaine.life",
			Role:       auth.RoleService,
			ActorEmail: adminEmail,
		},
		req,
	)
	if got != "target@example.com" {
		t.Fatalf("service-admin with ?owner=: got %q, want target@example.com", got)
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

func TestResolveSessionScope_DefaultsToLocalScope(t *testing.T) {
	app := &appServer{sessionScope: "tank-operator-slot-1"}
	got, status, err := app.resolveSessionScope(
		auth.User{Email: "regular@example.com", Role: auth.RoleUser},
		"",
	)
	if err != nil || status != http.StatusOK || got != "tank-operator-slot-1" {
		t.Fatalf("resolve empty scope = (%q, %d, %v), want local slot", got, status, err)
	}
}

func TestResolveSessionScope_AdminCanViewProdFromTestSlot(t *testing.T) {
	app := &appServer{sessionScope: "tank-operator-slot-1"}
	got, status, err := app.resolveSessionScope(
		auth.User{Email: adminEmail, Role: auth.RoleAdmin},
		"default",
	)
	if err != nil || status != http.StatusOK || got != "default" {
		t.Fatalf("admin prod scope = (%q, %d, %v), want default", got, status, err)
	}
}

func TestResolveSessionScope_ServiceAdminActorCanViewProdFromTestSlot(t *testing.T) {
	t.Setenv("SUPER_ADMIN_EMAILS", adminEmail)
	app := &appServer{sessionScope: "tank-operator-slot-1"}
	got, status, err := app.resolveSessionScope(
		auth.User{
			Email:      "pod-200@service.tank.romaine.life",
			Role:       auth.RoleService,
			ActorEmail: adminEmail,
		},
		"default",
	)
	if err != nil || status != http.StatusOK || got != "default" {
		t.Fatalf("service-admin prod scope = (%q, %d, %v), want default", got, status, err)
	}
}

func TestResolveSessionScope_NonAdminCannotViewProdFromTestSlot(t *testing.T) {
	app := &appServer{sessionScope: "tank-operator-slot-1"}
	_, status, err := app.resolveSessionScope(
		auth.User{Email: "regular@example.com", Role: auth.RoleUser},
		"default",
	)
	if err == nil || status != http.StatusForbidden {
		t.Fatalf("non-admin prod scope status=%d err=%v, want 403", status, err)
	}
}
