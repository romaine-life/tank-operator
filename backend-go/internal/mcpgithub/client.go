// Package mcpgithub is the orchestrator's narrow client for mcp-github.
//
// Stage 2 of the per-session repo-selection feature
// (docs/quality-timeframes.md: each stage ships coherent state). The
// splash-page picker needs to enumerate a SPA user's installation
// repos, but the orchestrator pod isn't bound to any one user and
// (by design) does not hold GitHub App credentials. The flow this
// package implements:
//
//  1. Read the orchestrator's projected SA token whose audience is
//     pinned to auth.romaine.life.
//  2. POST that SA token to /api/auth/exchange/k8s with the SPA
//     user's email in the body's `actor_email` field. auth.romaine.life
//     mints a role=service JWT whose `actor_email` claim equals the
//     supplied email (privilege gated at the IdP - orchestrator is
//     the only namespace with allowActorOverride=true).
//  3. Present that JWT to mcp-github over the cluster network. The
//     mcp-github auth middleware reads `actor_email`, calls back to
//     tank-operator's /api/internal/github/installation to resolve
//     the installation_id, mints an installation token, and lists
//     repos via the GitHub API.
//
// No mcp-github changes are needed: mcp-github already routes by
// `actor_email` for session pods, and this package presents a JWT
// shaped identically to what a session pod presents. The on-behalf-of
// surface lives at the IdP layer.
//
// Per docs/observability.md: outbound exchange + MCP calls are
// instrumented at the call site (see handlers_repos.go), not here -
// this package is a transport, not a policy layer.
package mcpgithub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Default cluster-internal address for mcp-github. Mirrors the
// session-pod mcp-auth-proxy's LISTENERS map entry (port 9992 ->
// http://mcp-github.mcp-github.svc:80). Configurable on the Client
// struct for tests + local dev.
const DefaultMCPGitHubURL = "http://mcp-github.mcp-github.svc:80"

// Default exchange URL. The orchestrator deployment mounts the
// audience-pinned projected SA token at
// /var/run/secrets/auth.romaine.life/token.
const DefaultExchangeURL = "https://auth.romaine.life/api/auth/exchange/k8s"

// Default path to the projected SA token mounted on the orchestrator
// pod. The deployment template provisions this with audience pinned
// to auth.romaine.life; this package reads the file fresh on every
// exchange because kubelet rotates it under us (~50 min default).
const DefaultSATokenPath = "/var/run/secrets/auth.romaine.life/token"

// Repo is the projection mcp-github's `list_installation_repos`
// returns, narrowed to the fields the picker renders. Mirrors the
// JSON-RPC tool's `repositories[]` row schema; extra fields the tool
// emits (default_branch, description, language, etc.) are dropped at
// parse time.
type Repo struct {
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

// Options configures a Client. Zero-value fields use the
// production-default constants above; tests can override every URL.
type Options struct {
	HTTPClient   *http.Client
	ExchangeURL  string
	MCPGitHubURL string
	SATokenPath  string
	// ReadToken is the strategy for reading the SA token from disk
	// each call. Overridable for tests; production passes nil and the
	// client uses os.ReadFile.
	ReadToken func(path string) (string, error)
	// Now is the wall-clock injection point for the token cache.
	// Tests override; production passes nil -> time.Now.
	Now func() time.Time
}

// Client is the orchestrator's outbound surface to mcp-github. Safe
// for concurrent use; per-user token mint is single-flighted so a
// burst of SPA opens against the picker doesn't fan out to N exchange
// requests for the same user.
type Client struct {
	http      *http.Client
	exchange  string
	mcpURL    string
	saPath    string
	readToken func(path string) (string, error)
	now       func() time.Time
	cacheLock sync.RWMutex
	cache     map[string]cachedToken
	mintGroup singleflight.Group
}

type cachedToken struct {
	token string
	exp   time.Time
}

// NewClient builds a configured client. Passes through every overridable
// field; missing options fall through to the production defaults so
// the orchestrator-side call site reads as a one-liner.
func NewClient(opts Options) *Client {
	c := &Client{
		http:      opts.HTTPClient,
		exchange:  opts.ExchangeURL,
		mcpURL:    opts.MCPGitHubURL,
		saPath:    opts.SATokenPath,
		readToken: opts.ReadToken,
		now:       opts.Now,
		cache:     map[string]cachedToken{},
	}
	if c.http == nil {
		// 20s aligns with the picker's UX budget - bigger than a
		// normal MCP call (sub-second) but bounded so a hung
		// mcp-github doesn't tie up the SPA's request indefinitely.
		c.http = &http.Client{Timeout: 20 * time.Second}
	}
	if c.exchange == "" {
		c.exchange = DefaultExchangeURL
	}
	if c.mcpURL == "" {
		c.mcpURL = DefaultMCPGitHubURL
	}
	if c.saPath == "" {
		c.saPath = DefaultSATokenPath
	}
	if c.readToken == nil {
		c.readToken = readFileTrim
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

// ListRepos enumerates the repos visible to the SPA caller's GitHub
// App installation. Mints (or reuses a cached) on-behalf-of JWT for
// the user, then forwards to mcp-github's `list_installation_repos`
// tool.
//
// userEmail is the SPA caller's verified email; it must come from the
// verified auth.romaine.life JWT on the inbound request, not from the
// SPA's request body. mcp-github will read
// the `actor_email` claim out of the minted JWT and route the call
// to that user's installation.
func (c *Client) ListRepos(ctx context.Context, userEmail string) ([]Repo, error) {
	userEmail = strings.ToLower(strings.TrimSpace(userEmail))
	if userEmail == "" {
		return nil, errors.New("mcpgithub: user email is empty")
	}
	token, err := c.tokenFor(ctx, userEmail)
	if err != nil {
		return nil, fmt.Errorf("mint on-behalf-of token: %w", err)
	}
	return c.callListInstallationRepos(ctx, token)
}

// MintActorToken exchanges the orchestrator's projected SA token for a fresh
// auth.romaine.life role=service JWT whose actor_email is the supplied email,
// returning the token and its expiry. Unlike ListRepos it bypasses the
// per-email cache so the caller always receives a full-TTL token: the admin
// break-glass token surface hands this JWT to a stuck agent, so a near-expiry
// cached entry would be useless. The token shape matches what a session pod's
// mcp-auth-proxy obtains on its own behalf.
func (c *Client) MintActorToken(ctx context.Context, actorEmail string) (string, time.Time, error) {
	actorEmail = strings.ToLower(strings.TrimSpace(actorEmail))
	if actorEmail == "" {
		return "", time.Time{}, errors.New("mcpgithub: actor email is empty")
	}
	return c.mintToken(ctx, actorEmail)
}

// tokenFor returns a cached or freshly-minted service JWT for the
// caller's email. Single-flighted per email so a burst of concurrent
// picker opens collapses to one exchange.
func (c *Client) tokenFor(ctx context.Context, userEmail string) (string, error) {
	// Cheap path: cache hit with sufficient headroom.
	const refreshSkew = 30 * time.Second
	c.cacheLock.RLock()
	entry, hit := c.cache[userEmail]
	c.cacheLock.RUnlock()
	if hit && entry.exp.After(c.now().Add(refreshSkew)) {
		return entry.token, nil
	}
	v, err, _ := c.mintGroup.Do(userEmail, func() (any, error) {
		// Re-check under the singleflight critical section so we
		// don't re-mint when a concurrent caller already did.
		c.cacheLock.RLock()
		fresh, ok := c.cache[userEmail]
		c.cacheLock.RUnlock()
		if ok && fresh.exp.After(c.now().Add(refreshSkew)) {
			return fresh.token, nil
		}
		token, exp, err := c.mintToken(ctx, userEmail)
		if err != nil {
			return "", err
		}
		c.cacheLock.Lock()
		c.cache[userEmail] = cachedToken{token: token, exp: exp}
		c.cacheLock.Unlock()
		return token, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// mintToken exchanges the orchestrator's projected SA token for a
// service JWT whose `actor_email` is the supplied user email.
// Body shape mirrors the auth.romaine.life route's `actor_email`
// JSON field; missing/empty would fall through to the legacy
// synthetic actor_email path, which is the wrong identity here.
func (c *Client) mintToken(ctx context.Context, userEmail string) (string, time.Time, error) {
	saToken, err := c.readToken(c.saPath)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read SA token at %s: %w", c.saPath, err)
	}
	body, err := json.Marshal(map[string]string{"actor_email": userEmail})
	if err != nil {
		return "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.exchange, bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("exchange returned %d", resp.StatusCode)
	}
	var payload struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", time.Time{}, fmt.Errorf("decode exchange response: %w", err)
	}
	if payload.Token == "" {
		return "", time.Time{}, errors.New("exchange response missing token")
	}
	// `expires_at` is seconds-since-epoch (mirrors the JWT `exp` claim).
	// Fall back to a conservative 10-min window if the field is
	// missing, so a server-side regression doesn't strand the cache.
	exp := time.Unix(payload.ExpiresAt, 0)
	if payload.ExpiresAt == 0 {
		exp = c.now().Add(10 * time.Minute)
	}
	return payload.Token, exp, nil
}

// callListInstallationRepos issues a single MCP JSON-RPC `tools/call`
// against mcp-github with the `list_installation_repos` tool name.
// Response shape: mcp-github replies as `text/event-stream` framing
// JSON-RPC results in `data:` lines. We tolerate either shape - bare
// JSON or SSE - so a future mcp-github content-negotiation change
// doesn't quietly break this path.
func (c *Client) callListInstallationRepos(ctx context.Context, token string) ([]Repo, error) {
	rpc := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_installation_repos",
			"arguments": map[string]any{},
		},
	}
	body, err := json.Marshal(rpc)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mcpURL+"/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp-github returned %d", resp.StatusCode)
	}
	return parseListReposResponse(resp.Body)
}

// parseListReposResponse decodes either a bare JSON-RPC response or
// an SSE-framed one and projects the embedded MCP tool result into
// []Repo. Split out for unit-test exercise without spinning up a
// real HTTP fake.
func parseListReposResponse(reader interface {
	Read(p []byte) (int, error)
}) ([]Repo, error) {
	scanner := bufio.NewScanner(reader)
	// Default scanner buf is 64 KB - bump so a many-repo response
	// (5 KB/repo x 100 repos) fits on a single SSE data line.
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<22)

	var rpcRaw []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Bare JSON path - the first line that starts with '{' is the
		// whole response.
		if line[0] == '{' {
			rpcRaw = append([]byte{}, line...)
			break
		}
		// SSE frame: `data: <json>`. Drop the prefix and parse.
		if bytes.HasPrefix(line, []byte("data: ")) {
			rpcRaw = append([]byte{}, line[len("data: "):]...)
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read mcp-github response: %w", err)
	}
	if len(rpcRaw) == 0 {
		return nil, errors.New("mcp-github response was empty")
	}

	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			// mcp-github wraps the tool result in
			// {content: [{type:"text", text: "<JSON-encoded result>"}]}
			// when responding via the MCP SDK's default codec, OR
			// it returns the dict directly under `structuredContent`.
			// Tolerate both shapes; whichever is present wins.
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rpcRaw, &envelope); err != nil {
		return nil, fmt.Errorf("decode mcp-github envelope: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("mcp-github error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}

	// Pick whichever projection the server emitted. mcp-github's public
	// tool result uses `repositories`; early Tank-side tests used `repos`,
	// so keep that alias readable instead of parsing a successful result
	// as an empty installation.
	var toolResult struct {
		Repositories []Repo `json:"repositories"`
		Repos        []Repo `json:"repos"`
	}
	if len(envelope.Result.StructuredContent) > 0 {
		if err := json.Unmarshal(envelope.Result.StructuredContent, &toolResult); err != nil {
			return nil, fmt.Errorf("decode structuredContent: %w", err)
		}
	} else if len(envelope.Result.Content) > 0 {
		for _, item := range envelope.Result.Content {
			if item.Type != "text" || item.Text == "" {
				continue
			}
			if err := json.Unmarshal([]byte(item.Text), &toolResult); err != nil {
				return nil, fmt.Errorf("decode content[].text: %w", err)
			}
			break
		}
	}
	repos := toolResult.Repositories
	if repos == nil {
		repos = toolResult.Repos
	}
	if repos == nil {
		repos = []Repo{}
	}
	return repos, nil
}
