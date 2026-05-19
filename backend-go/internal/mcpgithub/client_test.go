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
	return `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"repositories":[{"full_name":"nelsong6/tank-operator","private":false,"default_branch":"main"},{"full_name":"nelsong6/mcp-github","private":true,"default_branch":"main"}],"count":2,"total_count":2,"truncated":false,"has_more":false,"limit":null}}}`
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
	if repos[0].FullName != "nelsong6/tank-operator" {
		t.Errorf("repo[0].FullName = %q, want nelsong6/tank-operator", repos[0].FullName)
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
// SPA's session JWT is always non-empty, but a regression on the
// inbound auth path that sets user.Email = "" must not result in
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
