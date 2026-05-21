package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
)

type fakeGitHubInstallStateStore struct {
	createdState string
	createdEmail string
	expiresAt    time.Time
	attached     map[string]int64
	consumeID    int64
	consumeErr   error
	consumeEmail string
}

func (s *fakeGitHubInstallStateStore) Create(_ context.Context, state, email string, expiresAt time.Time) error {
	s.createdState = state
	s.createdEmail = email
	s.expiresAt = expiresAt
	return nil
}

func (s *fakeGitHubInstallStateStore) AttachInstallation(_ context.Context, state string, installationID int64) error {
	if s.attached == nil {
		s.attached = map[string]int64{}
	}
	s.attached[state] = installationID
	return nil
}

func (s *fakeGitHubInstallStateStore) Consume(_ context.Context, state, email string) (int64, error) {
	s.consumeEmail = email
	if s.consumeErr != nil {
		return 0, s.consumeErr
	}
	return s.consumeID, nil
}

type installUpdatingProfiles struct {
	updatedEmail string
	updatedID    int64
}

func (s *installUpdatingProfiles) GetOrCreate(_ context.Context, email string) (profiles.Profile, error) {
	return profiles.Profile{Email: email}, nil
}

func (s *installUpdatingProfiles) UpdateInstallation(_ context.Context, email string, installationID int64, githubLogin *string) (profiles.Profile, error) {
	s.updatedEmail = email
	s.updatedID = installationID
	return profiles.Profile{
		Email:          email,
		GitHubLogin:    githubLogin,
		InstallationID: &installationID,
	}, nil
}

func TestGitHubInstallURLCreatesOpaqueState(t *testing.T) {
	states := &fakeGitHubInstallStateStore{}
	server := &appServer{
		verifier:            authVerifierForTests(t),
		gitHubInstallStates: states,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/github/install/url", nil)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	rec := httptest.NewRecorder()

	server.handleGitHubInstallURL(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if states.createdEmail != "user@example.com" || states.createdState == "" {
		t.Fatalf("created state/email = %q/%q", states.createdState, states.createdEmail)
	}
	if strings.Contains(states.createdState, ".") {
		t.Fatalf("state looks like a JWT, want opaque nonce: %q", states.createdState)
	}
	location := rec.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "github.com" || parsed.Query().Get("state") != states.createdState {
		t.Fatalf("Location = %q, state = %q", location, states.createdState)
	}
}

func TestGitHubInstallCallbackAttachesInstallWithoutAuth(t *testing.T) {
	states := &fakeGitHubInstallStateStore{}
	server := &appServer{gitHubInstallStates: states}
	t.Setenv("TANK_UI_HOST", "https://tank.test")
	req := httptest.NewRequest(http.MethodGet, "/api/github/install/callback?state=opaque-state&installation_id=4242", nil)
	rec := httptest.NewRecorder()

	server.handleGitHubInstallCallback(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := states.attached["opaque-state"]; got != 4242 {
		t.Fatalf("attached installation = %d, want 4242", got)
	}
	if got := rec.Header().Get("Location"); got != "https://tank.test/?github_install_state=opaque-state" {
		t.Fatalf("Location = %q", got)
	}
}

func TestGitHubInstallCompleteConsumesStateAndUpdatesProfile(t *testing.T) {
	states := &fakeGitHubInstallStateStore{consumeID: 4242}
	profileStore := &installUpdatingProfiles{}
	server := &appServer{
		verifier:            authVerifierForTests(t),
		gitHubInstallStates: states,
		profiles:            profileStore,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/github/install/complete", strings.NewReader(`{"state":"opaque-state"}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	rec := httptest.NewRecorder()

	server.handleGitHubInstallComplete(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if states.consumeEmail != "user@example.com" {
		t.Fatalf("consume email = %q", states.consumeEmail)
	}
	if profileStore.updatedEmail != "user@example.com" || profileStore.updatedID != 4242 {
		t.Fatalf("profile update = %q/%d", profileStore.updatedEmail, profileStore.updatedID)
	}
	var body map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := body["user"]["installation_id"]; got != float64(4242) {
		t.Fatalf("installation_id = %#v", got)
	}
}

func authVerifierForTests(t *testing.T) *auth.Verifier {
	t.Helper()
	return auth.NewVerifier(testJWT(t))
}
