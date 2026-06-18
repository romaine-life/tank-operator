// Package azurepersonal is Tank's narrow server-side client for the
// mcp-azure-personal MCP server's internal control surface.
//
// Its one job: POST /internal/grant-activated when an azure break-glass grant
// goes active, so mcp-azure-personal fires tools/list_changed on that session's
// live MCP stream and the azure tools surface for the agent without a
// reconnect/re-request. The endpoint is in mcp-azure-personal's kube-rbac-proxy
// --ignore-paths and is gated by the auth.romaine.life service JWT instead, so
// the orchestrator authenticates with its own role=service principal (no actor
// override), presented in the X-Auth-Romaine-Token header.
package azurepersonal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the in-cluster address of mcp-azure-personal (the
	// kube-rbac-proxy front). /internal/grant-activated rides the proxy's
	// --ignore-paths and the server enforces the auth.romaine.life JWT.
	DefaultBaseURL     = "http://mcp-azure-personal.mcp-azure-personal.svc:80"
	DefaultExchangeURL = "https://auth.romaine.life/api/auth/exchange/k8s"
	DefaultSATokenPath = "/var/run/secrets/auth.romaine.life/token"
)

type Options struct {
	HTTPClient  *http.Client
	BaseURL     string
	ExchangeURL string
	SATokenPath string
	ReadToken   func(path string) (string, error)
}

type Client struct {
	http      *http.Client
	baseURL   string
	exchange  string
	saPath    string
	readToken func(path string) (string, error)
}

func NewClient(opts Options) *Client {
	c := &Client{
		http:      opts.HTTPClient,
		baseURL:   strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		exchange:  strings.TrimSpace(opts.ExchangeURL),
		saPath:    strings.TrimSpace(opts.SATokenPath),
		readToken: opts.ReadToken,
	}
	if c.http == nil {
		// Short timeout: this is a best-effort surfacing nudge, never on the
		// critical path of writing the grant.
		c.http = &http.Client{Timeout: 8 * time.Second}
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.exchange == "" {
		c.exchange = DefaultExchangeURL
	}
	if c.saPath == "" {
		c.saPath = DefaultSATokenPath
	}
	if c.readToken == nil {
		c.readToken = readFileTrim
	}
	return c
}

// NotifyGrantActivated POSTs /internal/grant-activated so mcp-azure-personal
// fires tools/list_changed on the session's stream. Best-effort by contract:
// callers should not fail the grant if this errors.
func (c *Client) NotifyGrantActivated(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("azurepersonal: session id is empty")
	}
	token, err := c.mintToken(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]string{"session_id": sessionID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/grant-activated", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	// azure-personal's CallerJWTMiddleware reads X-Auth-Romaine-Token (raw, no
	// "Bearer "); we use it rather than Authorization so the kube-rbac-proxy
	// never consumes it.
	req.Header.Set("X-Auth-Romaine-Token", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("grant-activated returned %d: %s", resp.StatusCode, responseDetail(resp))
	}
	return nil
}

// mintToken exchanges the orchestrator's projected auth.romaine.life SA token
// for a role=service JWT representing the orchestrator itself — an empty body
// (no actor_email override) yields the orchestrator's own service principal.
func (c *Client) mintToken(ctx context.Context) (string, error) {
	saToken, err := c.readToken(c.saPath)
	if err != nil {
		return "", fmt.Errorf("read SA token at %s: %w", c.saPath, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.exchange, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth exchange returned %d: %s", resp.StatusCode, responseDetail(resp))
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode auth exchange response: %w", err)
	}
	if strings.TrimSpace(payload.Token) == "" {
		return "", errors.New("auth exchange response missing token")
	}
	return strings.TrimSpace(payload.Token), nil
}

func responseDetail(resp *http.Response) string {
	var payload struct {
		Detail string `json:"detail"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil {
		if d := strings.TrimSpace(payload.Detail); d != "" {
			return d
		}
		if r := strings.TrimSpace(payload.Reason); r != "" {
			return r
		}
	}
	return http.StatusText(resp.StatusCode)
}

func readFileTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
