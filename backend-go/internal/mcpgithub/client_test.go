package mcpgithub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMCPServer combines an exchange endpoint (Hono shape from
// auth.romaine.life) and an MCP JSON-RPC endpoint into one HTTP test
// server. Both surfaces appear at their actual paths so the client
// under test sees the same request shape it would in production.
type fakeMCPServer struct {
	mu                sync.Mutex
	exchangeCalls     []map[string]any
	mcpCalls          int32
	exchangeStatus    int
	exchangeToken     string
	exchangeExpiresIn time.Duration
	mcpResponse       string
}

func newFakeMCPServer() *fakeMCPServer {
	return &fakeMCPServer{
		exchangeStatus:    http.StatusOK,
		exchangeToken:     "fake.jwt.token",
		exchangeExpiresIn: 15 * time.Minute,
		mcpResponse:       defaultMCPResponse(),
	}
}

func (f *fakeMCPServer) start(t *testing.T) (exchangeURL, mcpURL string, stop func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/exchange/k8s", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.exchangeCalls = append(f.exchangeCalls, body)
		f.mu.Unlock()
		if f.exchangeStatus != http.StatusOK {
			http.Error(w, "fake error", f.exchangeStatus)
			return
		}
		exp := time.Now().Add(f.exchangeExpiresIn).Unix()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":       f.exchangeToken,
			"expires_at":  exp,
			"actor_email": body["actor_email"],
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.mcpCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.mcpResponse))
	})
	srv := httptest.NewServer(mux)
	return srv.URL + "/api/auth/exchange/k8s", srv.URL, srv.Close
}

func defaultMCPResponse() string {
	// MCP SDK default codec wraps tool results in
	// {result: {structuredContent: {repositories: [...]}}}. Mirror that
	// shape so the client's parser exercises the production path.
	return `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"repositories":[{"full_name":"romaine-life/tank-operator","private":false,"default_branch":"main"},{"full_name":"romaine-life/mcp-github","private":true,"default_branch":"main"}],"count":2,"total_count":2,"truncated":false,"has_more":false,"limit":null}}}`
}

func TestListRepos_HappyPath(t *testing.T) {
	fake := newFakeMCPServer()
	exURL, mcpURL, stop := fake.start(t)
	defer stop()

	c := NewClient(Options{
		ExchangeURL:  exURL,
		MCPGitHubURL: mcpURL,
		ReadToken:    func(string) (string, error) { return "sa.token", nil },
	})
	repos, err := c.ListRepos(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("ListRepos error: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2", len(repos))
	}
	if repos[0].FullName != "romaine-life/tank-operator" {
		t.Errorf("repo[0].FullName = %q, want romaine-life/tank-operator", repos[0].FullName)
	}
	if !repos[1].Private {
		t.Errorf("repo[1].Private = false, want true")
	}
	if len(fake.exchangeCalls) != 1 {
		t.Fatalf("exchange call count = %d, want 1", len(fake.exchangeCalls))
	}
	if got := fake.exchangeCalls[0]["actor_email"]; got != "alice@example.com" {
		t.Errorf("actor_email forwarded = %v, want alice@example.com", got)
	}
}

// TestListRepos_TokenCachedAcrossCalls confirms the per-user token
// cache prevents the orchestrator from minting a fresh JWT on every
// SPA picker open. This is load-bearing for cost — without it, a
// burst of opens fans out to N exchange calls.
func TestListRepos_TokenCachedAcrossCalls(t *testing.T) {
	fake := newFakeMCPServer()
	exURL, mcpURL, stop := fake.start(t)
	defer stop()

	c := NewClient(Options{
		ExchangeURL:  exURL,
		MCPGitHubURL: mcpURL,
		ReadToken:    func(string) (string, error) { return "sa.token", nil },
	})
	for i := 0; i < 5; i++ {
		if _, err := c.ListRepos(context.Background(), "alice@example.com"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(fake.exchangeCalls) != 1 {
		t.Fatalf("exchange call count = %d, want 1 (cache hit on 4 follow-ups)", len(fake.exchangeCalls))
	}
	if got := atomic.LoadInt32(&fake.mcpCalls); got != 5 {
		t.Fatalf("mcp call count = %d, want 5 (each ListRepos must hit mcp-github)", got)
	}
}

// TestListRepos_CacheBoundary verifies a freshly-cached token below
// the 30s refresh skew triggers re-mint. Tests the cache invariant
// at the boundary so a future skew change is caught.
func TestListRepos_CacheRefreshesNearExpiry(t *testing.T) {
	fake := newFakeMCPServer()
	fake.exchangeExpiresIn = 20 * time.Second // < 30s refresh skew → always re-mint
	exURL, mcpURL, stop := fake.start(t)
	defer stop()

	c := NewClient(Options{
		ExchangeURL:  exURL,
		MCPGitHubURL: mcpURL,
		ReadToken:    func(string) (string, error) { return "sa.token", nil },
	})
	for i := 0; i < 3; i++ {
		if _, err := c.ListRepos(context.Background(), "alice@example.com"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(fake.exchangeCalls) != 3 {
		t.Fatalf("exchange call count = %d, want 3 (each call must re-mint near-expiry)", len(fake.exchangeCalls))
	}
}

// TestListRepos_DifferentUsersGetDifferentTokens guards the cache
// key shape — the orchestrator serves many users; one user's token
// must not be served to another's request.
func TestListRepos_DifferentUsersGetDifferentTokens(t *testing.T) {
	fake := newFakeMCPServer()
	exURL, mcpURL, stop := fake.start(t)
	defer stop()

	c := NewClient(Options{
		ExchangeURL:  exURL,
		MCPGitHubURL: mcpURL,
		ReadToken:    func(string) (string, error) { return "sa.token", nil },
	})
	for _, email := range []string{"alice@example.com", "bob@example.com", "carol@example.com"} {
		if _, err := c.ListRepos(context.Background(), email); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.exchangeCalls) != 3 {
		t.Fatalf("exchange call count = %d, want 3 (one per distinct user)", len(fake.exchangeCalls))
	}
}

// TestListRepos_RejectsEmptyEmail catches the load-bearing
// "actor_email is required" contract at the client boundary. The
// SPA's verified auth.romaine.life JWT is always non-empty, but a regression
// on the inbound auth path that sets user.Email = "" must not result in
// an exchange call (which would land on the legacy synthetic
// actor_email branch in auth.romaine.life).
func TestListRepos_RejectsEmptyEmail(t *testing.T) {
	c := NewClient(Options{
		ReadToken: func(string) (string, error) { return "sa.token", nil },
	})
	_, err := c.ListRepos(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty userEmail")
	}
}

// TestListRepos_ExchangeFailure surfaces a 5xx from the IdP as a
// concrete error rather than a silent empty list — the picker's
// error banner depends on a non-nil error here.
func TestListRepos_ExchangeFailure(t *testing.T) {
	fake := newFakeMCPServer()
	fake.exchangeStatus = http.StatusInternalServerError
	exURL, mcpURL, stop := fake.start(t)
	defer stop()

	c := NewClient(Options{
		ExchangeURL:  exURL,
		MCPGitHubURL: mcpURL,
		ReadToken:    func(string) (string, error) { return "sa.token", nil },
	})
	_, err := c.ListRepos(context.Background(), "alice@example.com")
	if err == nil {
		t.Fatal("expected error when exchange returns 500")
	}
}

// TestListRepos_ReadTokenError surfaces SA-token-read failures (e.g.,
// volume not mounted) as a clear error rather than panicking. Without
// this guard a startup-time mount regression would surface only on
// first /api/github/repos call as a stack trace in logs.
func TestListRepos_ReadTokenError(t *testing.T) {
	fake := newFakeMCPServer()
	exURL, mcpURL, stop := fake.start(t)
	defer stop()

	c := NewClient(Options{
		ExchangeURL:  exURL,
		MCPGitHubURL: mcpURL,
		ReadToken: func(string) (string, error) {
			return "", errors.New("token file missing")
		},
	})
	_, err := c.ListRepos(context.Background(), "alice@example.com")
	if err == nil || !strings.Contains(err.Error(), "token file missing") {
		t.Fatalf("expected token-read error to surface, got %v", err)
	}
}

// imageBuildServer fronts the two endpoints ImageBuildSucceededForHead exercises:
// the auth.romaine.life k8s exchange + mcp-github mint_clone_token (to obtain the
// GitHub installation token) and the GitHub REST workflow-runs endpoint. The
// workflow-runs handler echoes back what it was asked and returns a configurable
// run set so a test can model "built for this head", "built for a different head",
// or a GitHub error.
type imageBuildServer struct {
	runsStatus  int
	runsBody    string
	lastRunsURL string
}

func (s *imageBuildServer) start(t *testing.T) (exchangeURL, mcpURL, githubURL string, stop func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/exchange/k8s", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":       "service.jwt",
			"expires_at":  time.Now().Add(15 * time.Minute).Unix(),
			"actor_email": body["actor_email"],
		})
	})
	// mcp-github mint_clone_token: return the GitHub installation token the REST
	// call then carries.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"token":"gh-installation.token"}}}`))
	})
	// GitHub REST: the workflow-runs lookup keyed by head_sha + status=success.
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		s.lastRunsURL = r.URL.String()
		status := s.runsStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(s.runsBody))
	})
	srv := httptest.NewServer(mux)
	return srv.URL + "/api/auth/exchange/k8s", srv.URL, srv.URL, srv.Close
}

func newImageBuildClient(exURL, mcpURL, ghURL string) *Client {
	return NewClient(Options{
		ExchangeURL:  exURL,
		MCPGitHubURL: mcpURL,
		GitHubAPIURL: ghURL,
		ReadToken:    func(string) (string, error) { return "sa.token", nil },
	})
}

// TestImageBuildSucceededForHead_SuccessForExactHead is the gate's green path: a
// docker-build-check run with conclusion=success on the EXACT head SHA means the
// deployable image for that commit exists, so the method returns true. It also
// asserts the request scopes by head_sha + status=success so GitHub does the
// filtering, not the client.
func TestImageBuildSucceededForHead_SuccessForExactHead(t *testing.T) {
	srv := &imageBuildServer{
		runsBody: `{"workflow_runs":[{"status":"completed","conclusion":"success","head_sha":"deadbeefcafe"}]}`,
	}
	exURL, mcpURL, ghURL, stop := srv.start(t)
	defer stop()

	c := newImageBuildClient(exURL, mcpURL, ghURL)
	built, err := c.ImageBuildSucceededForHead(context.Background(), "alice@example.com", "romaine-life", "tank-operator", "docker-build-check.yaml", "deadbeefcafe")
	if err != nil {
		t.Fatalf("ImageBuildSucceededForHead error: %v", err)
	}
	if !built {
		t.Fatal("built = false, want true (a success run exists for the exact head)")
	}
	if !strings.Contains(srv.lastRunsURL, "docker-build-check.yaml") {
		t.Errorf("runs URL %q did not target the docker-build-check workflow", srv.lastRunsURL)
	}
	if !strings.Contains(srv.lastRunsURL, "head_sha=deadbeefcafe") {
		t.Errorf("runs URL %q did not scope by head_sha", srv.lastRunsURL)
	}
	if !strings.Contains(srv.lastRunsURL, "status=success") {
		t.Errorf("runs URL %q did not scope by status=success", srv.lastRunsURL)
	}
}

// TestImageBuildSucceededForHead_NoRunForHead is the gate's hold path: the only
// success run is for a DIFFERENT commit, so the image for the requested head is
// not built and the method returns false (the gate keeps watching rather than
// provisioning a commit whose image does not exist).
func TestImageBuildSucceededForHead_NoRunForHead(t *testing.T) {
	srv := &imageBuildServer{
		runsBody: `{"workflow_runs":[{"status":"completed","conclusion":"success","head_sha":"some-other-sha"}]}`,
	}
	exURL, mcpURL, ghURL, stop := srv.start(t)
	defer stop()

	c := newImageBuildClient(exURL, mcpURL, ghURL)
	built, err := c.ImageBuildSucceededForHead(context.Background(), "alice@example.com", "romaine-life", "tank-operator", "docker-build-check.yaml", "deadbeefcafe")
	if err != nil {
		t.Fatalf("ImageBuildSucceededForHead error: %v", err)
	}
	if built {
		t.Fatal("built = true, want false (no success run matches the requested head)")
	}
}

// TestImageBuildSucceededForHead_EmptyResult covers the post-open window where the
// build workflow has not registered a run yet: an empty list is "not built", not
// an error.
func TestImageBuildSucceededForHead_EmptyResult(t *testing.T) {
	srv := &imageBuildServer{runsBody: `{"workflow_runs":[]}`}
	exURL, mcpURL, ghURL, stop := srv.start(t)
	defer stop()

	c := newImageBuildClient(exURL, mcpURL, ghURL)
	built, err := c.ImageBuildSucceededForHead(context.Background(), "alice@example.com", "romaine-life", "tank-operator", "docker-build-check.yaml", "deadbeefcafe")
	if err != nil {
		t.Fatalf("ImageBuildSucceededForHead error: %v", err)
	}
	if built {
		t.Fatal("built = true, want false (no runs yet)")
	}
}

// TestImageBuildSucceededForHead_RejectsEmptyInputs guards the input contract so a
// missing workflow name or head SHA fails loudly at the boundary instead of
// issuing a malformed GitHub query that could match the wrong runs.
func TestImageBuildSucceededForHead_RejectsEmptyInputs(t *testing.T) {
	c := newImageBuildClient("", "", "")
	cases := []struct {
		name                              string
		email, owner, repo, workflow, sha string
	}{
		{"empty email", "", "o", "r", "w.yaml", "sha"},
		{"empty owner", "a@example.com", "", "r", "w.yaml", "sha"},
		{"empty repo", "a@example.com", "o", "", "w.yaml", "sha"},
		{"empty workflow", "a@example.com", "o", "r", "", "sha"},
		{"empty sha", "a@example.com", "o", "r", "w.yaml", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.ImageBuildSucceededForHead(context.Background(), tc.email, tc.owner, tc.repo, tc.workflow, tc.sha); err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
		})
	}
}

// TestImageBuildSucceededForHead_GitHubError surfaces a GitHub failure as a
// concrete error so the gate fails closed (verdict=error) rather than silently
// treating an API outage as "not built".
func TestImageBuildSucceededForHead_GitHubError(t *testing.T) {
	srv := &imageBuildServer{runsStatus: http.StatusInternalServerError, runsBody: "boom"}
	exURL, mcpURL, ghURL, stop := srv.start(t)
	defer stop()

	c := newImageBuildClient(exURL, mcpURL, ghURL)
	if _, err := c.ImageBuildSucceededForHead(context.Background(), "alice@example.com", "romaine-life", "tank-operator", "docker-build-check.yaml", "deadbeefcafe"); err == nil {
		t.Fatal("expected an error when GitHub returns 500")
	}
}

// TestParseListReposResponse_SSE parses mcp-github's streamable_http
// response shape (text/event-stream frames). The MCP SDK emits the
// JSON-RPC envelope inside a `data:` line.
func TestParseListReposResponse_SSE(t *testing.T) {
	sse := "event: message\n" +
		`data: {"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"repositories":[{"full_name":"foo/bar","private":false,"default_branch":"main"}]}}}` + "\n\n"
	repos, err := parseListReposResponse(strings.NewReader(sse))
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].FullName != "foo/bar" {
		t.Fatalf("repos = %+v", repos)
	}
}

// TestParseListReposResponse_ContentTextFallback covers the legacy
// MCP SDK shape where the tool result is JSON-encoded inside a
// content[].text item — the FastMCP default codec before it grew
// `structuredContent`. Tolerated so a future SDK rev that switches
// shapes doesn't break the picker silently.
func TestParseListReposResponse_ContentTextFallback(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"repositories\":[{\"full_name\":\"foo/bar\",\"private\":false,\"default_branch\":\"main\"}]}"}]}}`
	repos, err := parseListReposResponse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].FullName != "foo/bar" {
		t.Fatalf("repos = %+v", repos)
	}
}

// TestParseListReposResponse_LegacyReposAlias keeps the pre-fix
// Tank-side field name readable. Production mcp-github returns
// `repositories`, but accepting `repos` makes this client tolerant of
// old fakes and any preexisting local dev shims.
func TestParseListReposResponse_LegacyReposAlias(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"repos":[{"owner":"foo","name":"bar","full_name":"foo/bar","private":false}]}}}`
	repos, err := parseListReposResponse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].FullName != "foo/bar" {
		t.Fatalf("repos = %+v", repos)
	}
}

// TestParseListReposResponse_RPCError surfaces a JSON-RPC error
// envelope as an error to the caller. mcp-github's
// auth/installation/GitHub-API failure modes all end up here.
func TestParseListReposResponse_RPCError(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"no installation for alice@example.com"}}`
	_, err := parseListReposResponse(strings.NewReader(body))
	if err == nil || !strings.Contains(err.Error(), "no installation") {
		t.Fatalf("expected installation error, got %v", err)
	}
}

// TestParseListReposResponse_EmptyResult tolerates an empty repos
// list (user with installation but no accessible repos) and renders
// it as an empty array rather than nil — keeps the SPA's downstream
// `.map` simple.
func TestParseListReposResponse_EmptyResult(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"repositories":[]}}}`
	repos, err := parseListReposResponse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if repos == nil {
		t.Fatal("repos was nil; expected non-nil empty slice")
	}
	if len(repos) != 0 {
		t.Fatalf("repos = %+v", repos)
	}
}
