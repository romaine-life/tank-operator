package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/mcpgithub"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// TestValidateRepoSlugs locks in the handler-boundary contract for
// the repo-selection input shape. Anything that gets through this
// helper is what manager.Create receives, so the rules here are the
// product surface — bad shapes must surface as 400, not flow through
// to a session row with a malformed slug.
func TestValidateRepoSlugs(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		wantOut []string
		wantErr string // substring; empty = expect no error
	}{
		{
			name:    "empty stays empty",
			in:      nil,
			wantOut: []string{},
		},
		{
			name:    "empty slice stays empty",
			in:      []string{},
			wantOut: []string{},
		},
		{
			name:    "single slug passes",
			in:      []string{"nelsong6/tank-operator"},
			wantOut: []string{"nelsong6/tank-operator"},
		},
		{
			name:    "multiple slugs preserve order",
			in:      []string{"a-org/repo", "b-org/repo2"},
			wantOut: []string{"a-org/repo", "b-org/repo2"},
		},
		{
			name:    "dots and underscores allowed in name",
			in:      []string{"nelsong6/some.repo_name-1"},
			wantOut: []string{"nelsong6/some.repo_name-1"},
		},
		{
			name:    "whitespace trimmed",
			in:      []string{"  nelsong6/tank-operator  "},
			wantOut: []string{"nelsong6/tank-operator"},
		},
		{
			name:    "case-insensitive dedup, first-seen wins",
			in:      []string{"NelsonG6/Tank-Operator", "nelsong6/tank-operator"},
			wantOut: []string{"NelsonG6/Tank-Operator"},
		},
		{
			name:    "empty entry rejected",
			in:      []string{""},
			wantErr: "empty slug",
		},
		{
			name:    "scheme-injection rejected",
			in:      []string{"https://github.com/nelsong6/tank-operator"},
			wantErr: "not a valid owner/name slug",
		},
		{
			name:    "path traversal rejected",
			in:      []string{"../etc/passwd"},
			wantErr: "not a valid owner/name slug",
		},
		{
			name:    "shell metacharacters rejected",
			in:      []string{"nelsong6/tank-operator;rm -rf /"},
			wantErr: "not a valid owner/name slug",
		},
		{
			name:    "missing slash rejected",
			in:      []string{"nelsong6"},
			wantErr: "not a valid owner/name slug",
		},
		{
			name:    "owner cannot start with hyphen",
			in:      []string{"-org/repo"},
			wantErr: "not a valid owner/name slug",
		},
		{
			name: "over cap rejected",
			in: []string{
				"a/1", "b/2", "c/3", "d/4", "e/5", "f/6",
			},
			wantErr: "too many repos",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := validateRepoSlugs(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !stringSliceEqual(out, tc.wantOut) {
					t.Fatalf("out = %v, want %v", out, tc.wantOut)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

// TestSessionModeSupportsRepos pins the SDK-runner-modes-only contract.
// Non-SDK-runner modes (cli, config, api_key, hermes_gui) have no
// /workspace volume, so accepting repos for them would persist data
// with no runtime path to use it. The handler boundary rejects
// instead of silently dropping.
func TestSessionModeSupportsRepos(t *testing.T) {
	cases := map[string]bool{
		sessionmodel.ClaudeGUIMode:      true,
		sessionmodel.CodexGUIMode:       true,
		sessionmodel.CodexAppServerMode: true,
		sessionmodel.ClaudeCLIMode:      false,
		sessionmodel.CodexCLIMode:       false,
		sessionmodel.CodexConfigMode:    false,
		sessionmodel.PiCLIMode:          false,
		sessionmodel.APIKeyMode:         false,
		sessionmodel.ConfigMode:         false,
		sessionmodel.HermesGUIMode:      false,
		"":                              true, // normalizes to ClaudeGUIMode (DefaultSessionMode)
	}
	for mode, want := range cases {
		if got := sessionModeSupportsRepos(mode); got != want {
			t.Errorf("sessionModeSupportsRepos(%q) = %v, want %v", mode, got, want)
		}
	}
}

// TestRepoSelectionBucket pins the metric-label vocabulary so the
// Grafana dashboards and alert rules can rely on a fixed set.
func TestRepoSelectionBucket(t *testing.T) {
	cases := map[int]string{
		0:  "none",
		-1: "none",
		1:  "one",
		2:  "many",
		5:  "many",
	}
	for n, want := range cases {
		if got := repoSelectionBucket(n); got != want {
			t.Errorf("repoSelectionBucket(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestRecentRepoQueryUsesTypedLookbackInterval(t *testing.T) {
	if strings.Contains(recentRepoQuery, "|| ' days'") {
		t.Fatalf("recentRepoQuery still string-concats interval: %s", recentRepoQuery)
	}
	if !strings.Contains(recentRepoQuery, "$3::integer * interval '1 day'") {
		t.Fatalf("recentRepoQuery does not cast lookback days as integer interval: %s", recentRepoQuery)
	}
}

func TestHandleGitHubReposRequiresInstallationWhenNoRepoSource(t *testing.T) {
	t.Setenv("HOST_EMAIL", "host@example.test")
	server := &appServer{
		verifier: auth.NewVerifier(testJWT(t)),
		profiles: testProfilesStore{
			"user@example.test": profiles.Profile{Email: "user@example.test"},
		},
		mcpGitHub: &fakeRepoLister{},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/github/repos", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "user@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	server.handleGitHubRepos(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "github_installation_required" {
		t.Fatalf("body = %#v", body)
	}
}

func TestHandleGitHubReposHostUsesHostSourceWithoutInstallation(t *testing.T) {
	t.Setenv("HOST_EMAIL", "host@example.test")
	repoLister := &fakeRepoLister{
		repos: []mcpgithub.Repo{{FullName: "nelsong6/tank-operator", Private: false}},
	}
	server := &appServer{
		verifier:  auth.NewVerifier(testJWT(t)),
		profiles:  testProfilesStore{},
		mcpGitHub: repoLister,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/github/repos", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "host@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	server.handleGitHubRepos(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if repoLister.email != "host@example.test" {
		t.Fatalf("ListRepos email = %q, want host@example.test", repoLister.email)
	}
	var body struct {
		RepoSource string           `json:"repo_source"`
		Repos      []mcpgithub.Repo `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RepoSource != githubRepoSourceHost {
		t.Fatalf("repo_source = %q, want %q", body.RepoSource, githubRepoSourceHost)
	}
	if len(body.Repos) != 1 || body.Repos[0].FullName != "nelsong6/tank-operator" {
		t.Fatalf("repos = %+v", body.Repos)
	}
}

func TestHandleGitHubReposClassifiesUpstreamMissingInstallation(t *testing.T) {
	installationID := int64(42)
	server := &appServer{
		verifier: auth.NewVerifier(testJWT(t)),
		profiles: testProfilesStore{
			"user@example.test": profiles.Profile{Email: "user@example.test", InstallationID: &installationID},
		},
		mcpGitHub: &fakeRepoLister{err: errors.New("mcp-github error -32603: no GitHub App installation registered for user@example.test")},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/github/repos", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "user@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	server.handleGitHubRepos(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "github_installation_required" {
		t.Fatalf("body = %#v", body)
	}
}

type fakeRepoLister struct {
	repos []mcpgithub.Repo
	err   error
	email string
}

func (f *fakeRepoLister) ListRepos(_ context.Context, userEmail string) ([]mcpgithub.Repo, error) {
	f.email = userEmail
	if f.err != nil {
		return nil, f.err
	}
	return f.repos, nil
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
