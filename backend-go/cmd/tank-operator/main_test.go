package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

type fakeProfileStore struct {
	profile profiles.Profile
	err     error
}

func (s fakeProfileStore) GetOrCreate(_ context.Context, _ string) (profiles.Profile, error) {
	return s.profile, s.err
}

type fakeSessionReader struct {
	listOwner string
	getOwner  string
	getID     string
	listOut   []sessions.Info
	getOut    sessions.Info
	getErr    error
}

func (r *fakeSessionReader) List(_ context.Context, owner string) ([]sessions.Info, error) {
	r.listOwner = owner
	return r.listOut, nil
}

func (r *fakeSessionReader) Get(_ context.Context, owner, sessionID string) (sessions.Info, error) {
	r.getOwner = owner
	r.getID = sessionID
	return r.getOut, r.getErr
}

func TestConfig(t *testing.T) {
	t.Setenv("ENTRA_CLIENT_ID", "client-1")
	response := httptest.NewRecorder()

	config(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["entra_client_id"] != "client-1" || body["entra_authority"] != "https://login.microsoftonline.com/common" {
		t.Fatalf("body = %#v", body)
	}
}

func TestAuthenticatedListSessionsUsesTokenEmail(t *testing.T) {
	reader := &fakeSessionReader{listOut: []sessions.Info{{ID: "1", Owner: "user@example.com"}}}
	handler := authenticatedListSessions(auth.NewVerifier(testJWT(t), "user@example.com"), reader)
	request := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if reader.listOwner != "user@example.com" {
		t.Fatalf("list owner = %q", reader.listOwner)
	}
	var body []sessions.Info
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body[0].ID != "1" {
		t.Fatalf("body = %#v", body)
	}
}

func TestAuthenticatedGetSessionUsesTokenEmail(t *testing.T) {
	reader := &fakeSessionReader{getOut: sessions.Info{ID: "2", Owner: "user@example.com"}}
	handler := authenticatedGetSession(auth.NewVerifier(testJWT(t), "user@example.com"), reader)
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/2", nil)
	request.SetPathValue("session_id", "2")
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if reader.getOwner != "user@example.com" || reader.getID != "2" {
		t.Fatalf("get owner/id = %q/%q", reader.getOwner, reader.getID)
	}
}

func TestAuthenticatedGetSessionHidesNotOwned(t *testing.T) {
	reader := &fakeSessionReader{getErr: sessions.ErrNotOwned}
	handler := authenticatedGetSession(auth.NewVerifier(testJWT(t), "user@example.com"), reader)
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/2", nil)
	request.SetPathValue("session_id", "2")
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestAuthenticatedListSessionsRejectsUnauthenticated(t *testing.T) {
	handler := authenticatedListSessions(auth.NewVerifier(testJWT(t), "user@example.com"), &fakeSessionReader{})
	response := httptest.NewRecorder()

	handler(response, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestMe(t *testing.T) {
	login := "octocat"
	installationID := int64(123)
	verifier := auth.NewVerifier(testJWT(t), "user@example.com")
	handler := me(verifier, fakeProfileStore{profile: profiles.Profile{
		Email:          "user@example.com",
		GitHubLogin:    &login,
		InstallationID: &installationID,
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["email"] != "user@example.com" || body["github_login"] != login || body["installation_id"] != float64(123) {
		t.Fatalf("body = %#v", body)
	}
	if body["avatar_url"] != "https://www.gravatar.com/avatar/b58996c504c5638798eb6b511e6f49af?s=64&d=mp" {
		t.Fatalf("avatar_url = %q", body["avatar_url"])
	}
}

func TestMeReturnsProfileError(t *testing.T) {
	handler := me(
		auth.NewVerifier(testJWT(t), "user@example.com"),
		fakeProfileStore{err: errors.New("profile failed")},
	)
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestMeRejectsUnauthenticated(t *testing.T) {
	handler := me(auth.NewVerifier(testJWT(t), "user@example.com"), profiles.StubStore{})
	response := httptest.NewRecorder()

	handler(response, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

// testJWT returns a process-singleton InMemoryJWT so verifier and signed-token
// helpers in this file share the same key — necessary because each test now
// constructs a verifier and a token separately and they must agree on the kid.
var sharedTestJWT *auth.InMemoryJWT

func testJWT(t *testing.T) *auth.InMemoryJWT {
	t.Helper()
	if sharedTestJWT != nil {
		return sharedTestJWT
	}
	j, err := auth.NewInMemoryJWT("main-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	sharedTestJWT = j
	return j
}

func signedMainToken(t *testing.T, _ /*legacy secret arg*/, email string) string {
	t.Helper()
	tok, err := testJWT(t).MintJWT(context.Background(), jwt.MapClaims{
		"sub":   "sub-1",
		"email": email,
		"name":  "User",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
