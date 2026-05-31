package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	t.Setenv("AUTH_URL", "https://auth.test.example")
	response := httptest.NewRecorder()

	config(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["auth_url"] != "https://auth.test.example" {
		t.Fatalf("body = %#v", body)
	}
	if body["fork_session_prompt_template"] == "" {
		t.Fatalf("missing fork_session_prompt_template: %#v", body)
	}
	// Initial-message mode directives default to their const fallback when no
	// ConfigMap file is mounted, so /api/config is never empty pre-mount.
	for _, key := range []string{
		"initial_mode_diagnose_directive",
		"initial_mode_quality_gaps_directive",
		"initial_mode_go_long_directive",
		"initial_mode_test_directive",
	} {
		if body[key] == "" {
			t.Fatalf("missing %s: %#v", key, body)
		}
	}
}

func TestConfigReadsInitialModeDirectiveFiles(t *testing.T) {
	// Each directive key reads its mounted ConfigMap file when present, so a
	// live edit on main flows through without a frontend rebuild.
	cases := []struct{ env, key string }{
		{"TANK_INITIAL_MODE_DIAGNOSE_FILE", "initial_mode_diagnose_directive"},
		{"TANK_INITIAL_MODE_QUALITY_GAPS_FILE", "initial_mode_quality_gaps_directive"},
		{"TANK_INITIAL_MODE_GO_LONG_FILE", "initial_mode_go_long_directive"},
		{"TANK_INITIAL_MODE_TEST_FILE", "initial_mode_test_directive"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "directive.md")
			want := "live-edited directive for " + tc.key
			if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(tc.env, path)
			response := httptest.NewRecorder()

			config(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if got := body[tc.key]; got != want {
				t.Fatalf("%s = %q, want %q", tc.key, got, want)
			}
		})
	}
}

func TestConfigDefaultsAuthURL(t *testing.T) {
	// AUTH_URL not set â€” default to auth.romaine.life.
	t.Setenv("AUTH_URL", "")
	response := httptest.NewRecorder()
	config(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var body map[string]string
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	if body["auth_url"] != "https://auth.romaine.life" {
		t.Fatalf("auth_url default = %q", body["auth_url"])
	}
}

func TestConfigSpireLensAvailability(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "missing config disables capability",
			env: map[string]string{
				"SESSION_SPIRELENS_TAILSCALE_OIDC_CLIENT_ID": "oidc-client",
				"SESSION_SPIRELENS_TAILSCALE_TAILNET":        "-",
				"SESSION_SPIRELENS_HOST":                     "",
			},
			want: "false",
		},
		{
			name: "all required config enables capability",
			env: map[string]string{
				"SESSION_SPIRELENS_TAILSCALE_OIDC_CLIENT_ID": "oidc-client",
				"SESSION_SPIRELENS_TAILSCALE_TAILNET":        "-",
				"SESSION_SPIRELENS_HOST":                     "nelsonlaptop",
			},
			want: "true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			response := httptest.NewRecorder()
			config(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))
			var body map[string]string
			_ = json.Unmarshal(response.Body.Bytes(), &body)
			if got := body["spirelens_mcp_available"]; got != tc.want {
				t.Fatalf("spirelens_mcp_available = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfigReadsForkSessionPromptTemplateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fork-session.md")
	if err := os.WriteFile(path, []byte("fork template {{forked_message}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TANK_FORK_SESSION_PROMPT_FILE", path)
	response := httptest.NewRecorder()

	config(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, want := body["fork_session_prompt_template"], "fork template {{forked_message}}"; got != want {
		t.Fatalf("fork_session_prompt_template = %q, want %q", got, want)
	}
}

func TestParseEmailSetNormalizesAndSkipsEmptyEntries(t *testing.T) {
	got := parseEmailSet(" Alice@Example.test, ,BOB@example.test ")
	if !got["alice@example.test"] || !got["bob@example.test"] || len(got) != 2 {
		t.Fatalf("parseEmailSet = %#v", got)
	}
}

func TestAuthenticatedListSessionsUsesTokenEmail(t *testing.T) {
	reader := &fakeSessionReader{listOut: []sessions.Info{{ID: "1", Owner: "user@example.com"}}}
	handler := authenticatedListSessions(auth.NewVerifier(testJWT(t)), reader)
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
	handler := authenticatedGetSession(auth.NewVerifier(testJWT(t)), reader)
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
	handler := authenticatedGetSession(auth.NewVerifier(testJWT(t)), reader)
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
	handler := authenticatedListSessions(auth.NewVerifier(testJWT(t)), &fakeSessionReader{})
	response := httptest.NewRecorder()

	handler(response, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestMe(t *testing.T) {
	login := "octocat"
	installationID := int64(123)
	verifier := auth.NewVerifier(testJWT(t))
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
	if body["role"] != "user" || body["is_admin"] != false {
		t.Fatalf("role/is_admin = %#v/%#v, want user/false", body["role"], body["is_admin"])
	}
	if body["avatar_url"] != "https://www.gravatar.com/avatar/b58996c504c5638798eb6b511e6f49af?s=64&d=mp" {
		t.Fatalf("avatar_url = %q", body["avatar_url"])
	}
}

func TestMeReturnsAdminPowerForSuperAdminServiceActor(t *testing.T) {
	t.Setenv("SUPER_ADMIN_EMAILS", adminEmail)
	handler := me(auth.NewVerifier(testJWT(t)), profiles.StubStore{})
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-200@service.tank.romaine.life", adminEmail))
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["role"] != auth.RoleService || body["is_admin"] != true {
		t.Fatalf("role/is_admin = %#v/%#v, want service/true", body["role"], body["is_admin"])
	}
}

func TestMeKeepsRegularServiceActorNonAdmin(t *testing.T) {
	t.Setenv("SUPER_ADMIN_EMAILS", adminEmail)
	handler := me(auth.NewVerifier(testJWT(t)), profiles.StubStore{})
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-200@service.tank.romaine.life", otherUser))
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["role"] != auth.RoleService || body["is_admin"] != false {
		t.Fatalf("role/is_admin = %#v/%#v, want service/false", body["role"], body["is_admin"])
	}
}

func TestMeReturnsProfileError(t *testing.T) {
	handler := me(
		auth.NewVerifier(testJWT(t)),
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
	handler := me(auth.NewVerifier(testJWT(t)), profiles.StubStore{})
	response := httptest.NewRecorder()

	handler(response, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

// Pins the canonical user-response shape that /api/auth/me and GitHub
// install completion build via userResponseBody. A missing field reads as
// undefined in the SPA and
// because `undefined == null` in JS â€” flips installation_id-driven UI like
// the OnboardingWall into the "not installed" branch even for users who
// are installed. That was the bug in #391; this test prevents it
// reappearing if the helper is refactored.
func TestUserResponseBodyCarriesProfileFields(t *testing.T) {
	login := "octocat"
	installationID := int64(42)
	prefs := map[string]any{"chatFontScale": 1.25}

	body := userResponseBody("sub-1", "user@example.com", "User Name", "admin", true, profiles.Profile{
		Email:          "user@example.com",
		GitHubLogin:    &login,
		InstallationID: &installationID,
		RunPrefs:       prefs,
	})

	// Cast InstallationID for comparison â€” go's map[string]any doesn't
	// equate *int64 with int64 directly, so we dereference via the helper.
	if got, ok := body["installation_id"].(*int64); !ok || got == nil || *got != installationID {
		t.Fatalf("installation_id = %#v, want pointer to %d", body["installation_id"], installationID)
	}
	if got, ok := body["github_login"].(*string); !ok || got == nil || *got != login {
		t.Fatalf("github_login = %#v, want pointer to %q", body["github_login"], login)
	}
	if got, _ := body["run_prefs"].(map[string]any); got == nil || got["chatFontScale"] != 1.25 {
		t.Fatalf("run_prefs = %#v", body["run_prefs"])
	}
	if body["email"] != "user@example.com" || body["sub"] != "sub-1" || body["name"] != "User Name" || body["role"] != "admin" {
		t.Fatalf("body = %#v", body)
	}
	if body["is_admin"] != true {
		t.Fatalf("is_admin = %#v, want true", body["is_admin"])
	}
	if got, _ := body["avatar_url"].(string); got == "" {
		t.Fatalf("avatar_url empty: %#v", body["avatar_url"])
	}
}

// Verifies the response shape also tolerates an empty profile (e.g. a
// first-time login where the profiles row doesn't exist yet). All profile-
// derived fields should serialize as JSON null â€” not be missing â€” so the
// SPA reads them as `null` and renders the install-CTA branch correctly
// instead of dead-ending on an undefined field access. Asserting on the
// marshaled JSON (not the map) because Go's typed-nil-in-interface
// semantics make (*int64)(nil) != nil for `==` purposes, but both
// marshal to JSON null. The SPA only sees the JSON.
func TestUserResponseBodyEmptyProfileNullsOutFields(t *testing.T) {
	body := userResponseBody("sub-1", "user@example.com", "User Name", "user", false, profiles.Profile{})
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"installation_id", "github_login", "run_prefs"} {
		if v, ok := parsed[key]; !ok {
			t.Fatalf("missing key %q (must be present even if null)", key)
		} else if v != nil {
			t.Fatalf("expected JSON null for %q, got %#v", key, v)
		}
	}
}

// testJWT returns a process-singleton InMemoryJWT so verifier and signed-token
// helpers in this file share the same key â€” necessary because each test now
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

func signedMainToken(t *testing.T, _ /*unused secret arg*/, email string) string {
	t.Helper()
	tok, err := testJWT(t).MintJWT(context.Background(), jwt.MapClaims{
		"sub":   "sub-1",
		"email": email,
		"iss":   "https://auth.romaine.life",
		"name":  "User",
		"role":  "user",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestEnvBoolDefaultRunMigrationsContract(t *testing.T) {
	const key = "RUN_MIGRATIONS_TEST_FLAG"
	cases := []struct {
		name string
		set  bool
		val  string
		want bool
	}{
		{name: "unset defaults on", set: false, want: true},
		{name: "empty defaults on", set: true, val: "", want: true},
		{name: "garbage defaults on", set: true, val: "maybe", want: true},
		{name: "explicit false disables", set: true, val: "false", want: false},
		{name: "zero disables", set: true, val: "0", want: false},
		{name: "no disables", set: true, val: "NO", want: false},
		{name: "true enables", set: true, val: "true", want: true},
		{name: "one enables", set: true, val: "1", want: true},
		{name: "yes enables", set: true, val: "  Yes  ", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Unsetenv(key)
			if tc.set {
				t.Setenv(key, tc.val)
			}
			if got := envBoolDefault(key, true); got != tc.want {
				t.Fatalf("envBoolDefault(%q=%q, true) = %v, want %v", key, tc.val, got, tc.want)
			}
		})
	}
}
