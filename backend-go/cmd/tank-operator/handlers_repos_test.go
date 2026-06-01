package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
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

// TestValidatePinnedRepoSlugs locks the durable profile-backed pin contract.
// Pins share the GitHub slug validator with create-time repo selection, but
// they have their own metadata cap instead of the per-session clone cap.
func TestValidatePinnedRepoSlugs(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		wantOut []string
		wantErr string
	}{
		{
			name:    "empty stays empty",
			in:      nil,
			wantOut: []string{},
		},
		{
			name:    "dedups and preserves first casing",
			in:      []string{"  NelsonG6/Tank-Operator  ", "nelsong6/tank-operator", "nelsong6/glimmung"},
			wantOut: []string{"NelsonG6/Tank-Operator", "nelsong6/glimmung"},
		},
		{
			name:    "bad slug rejected",
			in:      []string{"https://github.com/nelsong6/tank-operator"},
			wantErr: "not a valid owner/name slug",
		},
		{
			name: "session clone cap does not apply",
			in: []string{
				"a/1", "b/2", "c/3", "d/4", "e/5", "f/6",
			},
			wantOut: []string{"a/1", "b/2", "c/3", "d/4", "e/5", "f/6"},
		},
		{
			name: "profile metadata cap applies",
			in: func() []string {
				in := make([]string, 0, maxPinnedReposPerUser+1)
				for i := 0; i < maxPinnedReposPerUser+1; i++ {
					in = append(in, "owner/repo"+strconv.Itoa(i))
				}
				return in
			}(),
			wantErr: "too many pinned repos",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := validatePinnedRepoSlugs(tc.in)
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
// Non-SDK-runner modes (cli, config, api_key) have no
// /workspace volume, so accepting repos for them would persist data
// with no runtime path to use it. The handler boundary rejects
// instead of silently dropping.
func TestSessionModeSupportsRepos(t *testing.T) {
	cases := map[string]bool{
		sessionmodel.ClaudeGUIMode:      true,
		sessionmodel.CodexGUIMode:       true,
		sessionmodel.CodexExecGUIMode:   true,
		sessionmodel.CodexAppServerMode: true,
		sessionmodel.GeminiGUIMode:      true,
		sessionmodel.GeminiTestMode:     true,
		sessionmodel.ClaudeCLIMode:      false,
		sessionmodel.CodexCLIMode:       false,
		sessionmodel.CodexConfigMode:    false,
		sessionmodel.GeminiConfigMode:   false,
		sessionmodel.APIKeyMode:         false,
		sessionmodel.ConfigMode:         false,
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

func TestFetchRecentRepoSlugsQueryUsesTypedInterval(t *testing.T) {
	if strings.Contains(fetchRecentRepoSlugsQuery, "|| ' days'") {
		t.Fatalf("recent repo query must not coerce lookbackDays through text concatenation")
	}
	if !strings.Contains(fetchRecentRepoSlugsQuery, "$3::int * interval '1 day'") {
		t.Fatalf("recent repo query must cast lookbackDays as int before interval math")
	}
}

func TestRepoLookupOwnerEmail_ServiceUsesActorEmail(t *testing.T) {
	user := auth.User{
		Email:      "pod-125@service.tank.romaine.life",
		Role:       auth.RoleService,
		ActorEmail: "owner@example.com",
	}
	if got := repoLookupOwnerEmail(user); got != "owner@example.com" {
		t.Fatalf("repoLookupOwnerEmail() = %q, want actor email", got)
	}
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
