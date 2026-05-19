package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// AuthRomaineServiceProvider exchanges the orchestrator pod's
// audience-pinned projected SA token for an auth.romaine.life
// role=service JWT and caches the result until ~30s before expiry.
//
// Direct port of claude-container/mcp-auth-proxy/src/mcp_auth_proxy/
// server.py's AuthRomaineServiceProvider (Python class of the same name).
// Drift between this Go port and the Python original would mean two
// session-pod-style clients diverging from one orchestrator-style
// client; the call shape, header naming, and cache semantics
// deliberately match so a fix in one repo propagates straightforwardly
// to the other.
//
// Lifecycle: NewAuthRomaineServiceProvider returns a *Provider whose
// Token(ctx) method is goroutine-safe. Concurrent callers during a
// refresh collapse onto one outbound exchange call via sync.Mutex
// (single-flight). Re-reads the SA token file on every exchange so
// kubelet token rotation is invisible to callers.
//
// See nelsong6/tank-operator#540 + nelsong6/auth#42 for the consumer
// onboarding that makes the exchange call succeed.
type AuthRomaineServiceProvider struct {
	exchangeURL        string
	saTokenPath        string
	refreshSkew        time.Duration
	httpClient         *http.Client

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

// AuthRomaineOptions configures NewAuthRomaineServiceProvider. All
// fields except ExchangeURL and SATokenPath have sensible defaults.
type AuthRomaineOptions struct {
	// ExchangeURL is auth.romaine.life's k8s SA-token exchange
	// endpoint. Defaults to https://auth.romaine.life/api/auth/exchange/k8s.
	ExchangeURL string
	// SATokenPath is the filesystem path of the projected SA token
	// volume audience-pinned to https://auth.romaine.life. Tank's
	// orchestrator Deployment mounts this at
	// /var/run/secrets/auth.romaine.life/token via the projected
	// volume entry in k8s/templates/deployment.yaml. Defaults to
	// that path.
	SATokenPath string
	// RefreshSkew is how far in advance of `exp` to consider the
	// cached token stale. Defaults to 30s — matches mcp-auth-proxy's
	// Python implementation.
	RefreshSkew time.Duration
	// HTTPTimeout caps a single exchange call. Defaults to 10s.
	HTTPTimeout time.Duration
}

const (
	defaultAuthRomaineExchangeURL = "https://auth.romaine.life/api/auth/exchange/k8s"
	defaultAuthRomaineSATokenPath = "/var/run/secrets/auth.romaine.life/token"
	defaultAuthRomaineRefreshSkew = 30 * time.Second
	defaultAuthRomaineTimeout     = 10 * time.Second
)

// NewAuthRomaineServiceProvider constructs a provider. Returns nil when
// the SA token path doesn't exist on disk (orchestrator deployed without
// the audience-pinned projected token volume — bridges that depend on
// this provider must check for nil and 503 instead of panicking).
func NewAuthRomaineServiceProvider(opts AuthRomaineOptions) *AuthRomaineServiceProvider {
	exchangeURL := strings.TrimSpace(opts.ExchangeURL)
	if exchangeURL == "" {
		exchangeURL = defaultAuthRomaineExchangeURL
	}
	saTokenPath := strings.TrimSpace(opts.SATokenPath)
	if saTokenPath == "" {
		saTokenPath = defaultAuthRomaineSATokenPath
	}
	skew := opts.RefreshSkew
	if skew == 0 {
		skew = defaultAuthRomaineRefreshSkew
	}
	timeout := opts.HTTPTimeout
	if timeout == 0 {
		timeout = defaultAuthRomaineTimeout
	}

	if _, err := os.Stat(saTokenPath); err != nil {
		slog.Warn("hermes auth-romaine provider disabled (SA token path missing)",
			"path", saTokenPath, "error", err)
		return nil
	}

	return &AuthRomaineServiceProvider{
		exchangeURL: strings.TrimRight(exchangeURL, "/"),
		saTokenPath: saTokenPath,
		refreshSkew: skew,
		httpClient:  &http.Client{Timeout: timeout},
	}
}

// Token returns a non-expired role=service JWT. Goroutine-safe. On
// cache-miss this performs an exchange; concurrent callers collapse to
// one exchange via the internal mutex.
func (p *AuthRomaineServiceProvider) Token(ctx context.Context) (string, error) {
	if p == nil {
		return "", errors.New("auth-romaine provider not configured")
	}
	now := time.Now()
	p.mu.Lock()
	if p.cachedToken != "" && p.expiresAt.After(now.Add(p.refreshSkew)) {
		t := p.cachedToken
		p.mu.Unlock()
		return t, nil
	}
	defer p.mu.Unlock()
	// Re-check inside the lock (someone may have refreshed while we
	// were waiting).
	if p.cachedToken != "" && p.expiresAt.After(time.Now().Add(p.refreshSkew)) {
		return p.cachedToken, nil
	}
	token, exp, err := p.exchange(ctx)
	if err != nil {
		return "", err
	}
	p.cachedToken = token
	p.expiresAt = exp
	return token, nil
}

type exchangeResponse struct {
	Token     string `json:"token"`
	ExpiresAt any    `json:"expires_at"`
}

func (p *AuthRomaineServiceProvider) exchange(ctx context.Context) (string, time.Time, error) {
	saTokenBytes, err := os.ReadFile(p.saTokenPath)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read SA token %q: %w", p.saTokenPath, err)
	}
	saToken := strings.TrimSpace(string(saTokenBytes))
	if saToken == "" {
		return "", time.Time{}, fmt.Errorf("SA token %q is empty", p.saTokenPath)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.exchangeURL, strings.NewReader("{}"))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build exchange request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth-romaine exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(body))
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return "", time.Time{}, fmt.Errorf("auth-romaine exchange HTTP %d: %s", resp.StatusCode, detail)
	}

	var parsed exchangeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("decode exchange response: %w", err)
	}
	if parsed.Token == "" {
		return "", time.Time{}, errors.New("auth-romaine exchange response missing token")
	}
	exp := parseExpiresAt(parsed.ExpiresAt)
	if exp.Before(time.Now()) {
		return "", time.Time{}, fmt.Errorf("auth-romaine exchange returned already-expired token (exp=%v)", exp)
	}
	return parsed.Token, exp, nil
}

// parseExpiresAt accepts either a numeric epoch second/millisecond or
// an RFC3339 string. Same shape mcp-auth-proxy's
// _parse_expires_at handles in the Python original.
func parseExpiresAt(value any) time.Time {
	switch v := value.(type) {
	case float64:
		// Heuristic: values past year ~33,000 in seconds are
		// almost certainly milliseconds. Practical Better Auth
		// values are seconds.
		if v > 1e12 {
			return time.UnixMilli(int64(v))
		}
		return time.Unix(int64(v), 0)
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}
		}
		// Accept the "...Z" shorthand for the timezone offset.
		s = strings.TrimSuffix(s, "Z")
		if !strings.Contains(s[max0(len(s)-6):], "+") && !strings.Contains(s[max0(len(s)-6):], "-") {
			s = s + "Z"
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t, err = time.Parse(time.RFC3339, s)
		}
		if err != nil {
			return time.Time{}
		}
		return t
	}
	return time.Time{}
}

func max0(x int) int {
	if x < 0 {
		return 0
	}
	return x
}
