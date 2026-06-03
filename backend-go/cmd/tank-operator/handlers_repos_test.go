package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/profiles"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
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
			in:      []string{"romaine-life/tank-operator"},
			wantOut: []string{"romaine-life/tank-operator"},
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
			in:      []string{"  romaine-life/tank-operator  "},
			wantOut: []string{"romaine-life/tank-operator"},
		},
		{
			name:    "case-insensitive dedup, first-seen wins",
			in:      []string{"Romaine-Life/Tank-Operator", "romaine-life/tank-operator"},
			wantOut: []string{"Romaine-Life/Tank-Operator"},
		},
		{
			name:    "empty entry rejected",
			in:      []string{""},
			wantErr: "empty slug",
		},
		{
			name:    "scheme-injection rejected",
			in:      []string{"https://github.com/romaine-life/tank-operator"},
			wantErr: "not a valid owner/name slug",
		},
		{
			name:    "path traversal rejected",
			in:      []string{"../etc/passwd"},
			wantErr: "not a valid owner/name slug",
		},
		{
			name:    "shell metacharacters rejected",
			in:      []string{"romaine-life/tank-operator;rm -rf /"},
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
			in:      []string{"  Romaine-Life/Tank-Operator  ", "romaine-life/tank-operator", "romaine-life/glimmung"},
			wantOut: []string{"Romaine-Life/Tank-Operator", "romaine-life/glimmung"},
		},
		{
			// The splash picker's drag-and-drop pin reordering relies on the
			// durable write preserving the exact array order it PUTs — there
			// is no separate "order" field, the text[] order IS the pin order.
			// This case guards that contract: a deliberately non-sorted input
			// survives validation in the same order.
			name:    "reorder is preserved through validation",
			in:      []string{"c/3", "a/1", "b/2"},
			wantOut: []string{"c/3", "a/1", "b/2"},
		},
		{
			name:    "bad slug rejected",
			in:      []string{"https://github.com/romaine-life/tank-operator"},
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
		sessionmodel.ClaudeCLIMode:      false,
		sessionmodel.CodexCLIMode:       false,
		sessionmodel.CodexConfigMode:    false,
		"gemini_gui":                    false,
		"gemini_test":                   false,
		"gemini_config":                 false,
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

type pinnedReposProfileStore struct {
	repos       []string
	getEmail    string
	updateEmail string
}

func (s *pinnedReposProfileStore) GetOrCreate(_ context.Context, email string) (profiles.Profile, error) {
	s.getEmail = email
	return profiles.Profile{Email: email, PinnedRepos: s.repos}, nil
}

func (s *pinnedReposProfileStore) UpdatePinnedRepos(_ context.Context, email string, repos []string) (profiles.Profile, error) {
	s.updateEmail = email
	s.repos = repos
	return profiles.Profile{Email: email, PinnedRepos: repos}, nil
}

func TestHandleGitHubPinnedReposServiceActorReadsOwnerProfile(t *testing.T) {
	store := &pinnedReposProfileStore{repos: []string{"owner/repo"}}
	server := &appServer{
		verifier: authVerifierForTests(t),
		profiles: store,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/github/pinned-repos", nil)
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-485@service.tank.romaine.life", "owner@example.com"))
	rec := httptest.NewRecorder()

	server.handleGitHubPinnedRepos(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if store.getEmail != "owner@example.com" {
		t.Fatalf("profile email = %q, want owner@example.com", store.getEmail)
	}
	var body struct {
		Repos []string `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !stringSliceEqual(body.Repos, []string{"owner/repo"}) {
		t.Fatalf("repos = %#v", body.Repos)
	}
}

func TestHandleGitHubPinnedReposServiceActorWritesOwnerProfile(t *testing.T) {
	store := &pinnedReposProfileStore{}
	bus := &recordingSessionBus{}
	server := &appServer{
		verifier:   authVerifierForTests(t),
		profiles:   store,
		sessionBus: bus,
	}
	req := httptest.NewRequest(http.MethodPut, "/api/github/pinned-repos", strings.NewReader(`{"repos":["owner/repo"]}`))
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-485@service.tank.romaine.life", "owner@example.com"))
	rec := httptest.NewRecorder()

	server.handleGitHubPinnedRepos(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if store.updateEmail != "owner@example.com" {
		t.Fatalf("profile email = %q, want owner@example.com", store.updateEmail)
	}
	var body struct {
		Repos []string `json:"repos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !stringSliceEqual(body.Repos, []string{"owner/repo"}) {
		t.Fatalf("repos = %#v", body.Repos)
	}
	if !stringSliceEqual(bus.pinnedPublishEmails, []string{"owner@example.com"}) {
		t.Fatalf("pinned wake emails = %#v, want owner@example.com", bus.pinnedPublishEmails)
	}
}

func TestHandleGitHubPinnedReposEventsEmitsInitialOwnerSnapshot(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	store := &pinnedReposProfileStore{repos: []string{"owner/repo"}}
	bus := &recordingSessionBus{pinnedUpdateCh: closed}
	tickets := &fakeStreamAuthTicketStore{
		validateResponse: pgstore.StreamAuthTicket{
			Sub:          "sub-pod",
			Email:        "pod-485@service.tank.romaine.life",
			Name:         "pod-485",
			Role:         auth.RoleService,
			ActorEmail:   "owner@example.com",
			StreamKind:   streamKindPinnedRepos,
			SessionScope: "default",
		},
	}
	server := &appServer{
		profiles:          store,
		streamAuthTickets: tickets,
		sessionBus:        bus,
		sessionScope:      "default",
	}
	req := httptest.NewRequest(http.MethodGet, "/api/github/pinned-repos/events?stream_ticket=ticket-123", nil)
	rec := httptest.NewRecorder()

	server.handleGitHubPinnedReposEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if tickets.validateKind != streamKindPinnedRepos || tickets.validateSession != "" {
		t.Fatalf("validate args kind=%q session=%q", tickets.validateKind, tickets.validateSession)
	}
	if store.getEmail != "owner@example.com" {
		t.Fatalf("profile email = %q, want owner@example.com", store.getEmail)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: ready") {
		t.Fatalf("body missing ready event: %s", body)
	}
	if !strings.Contains(body, "event: pinned-repos") || !strings.Contains(body, `"repos":["owner/repo"]`) {
		t.Fatalf("body missing pinned repos snapshot: %s", body)
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
