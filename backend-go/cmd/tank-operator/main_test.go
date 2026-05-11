package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
)

type fakeProfileStore struct {
	profile profiles.Profile
	err     error
}

func (s fakeProfileStore) GetOrCreate(_ context.Context, _ string) (profiles.Profile, error) {
	return s.profile, s.err
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

func TestMe(t *testing.T) {
	login := "octocat"
	installationID := int64(123)
	verifier := auth.NewVerifier("secret", "user@example.com")
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

func TestMeRejectsUnauthenticated(t *testing.T) {
	handler := me(auth.NewVerifier("secret", "user@example.com"), profiles.StubStore{})
	response := httptest.NewRecorder()

	handler(response, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func signedMainToken(t *testing.T, secret, email string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "sub-1",
		"email": email,
		"name":  "User",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
