// Package glimmung contains Tank's narrow server-side client for Glimmung.
package glimmung

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
	DefaultBaseURL     = "https://glimmung.romaine.life"
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

type StateSnapshot struct {
	ActiveLeases []Lease `json:"active_leases"`
}

type Lease struct {
	Ref       string         `json:"ref"`
	Project   string         `json:"project"`
	State     string         `json:"state"`
	Metadata  map[string]any `json:"metadata"`
	Requester *Requester     `json:"requester"`
}

type Requester struct {
	Ref      string            `json:"ref"`
	Metadata map[string]string `json:"metadata"`
}

type ReturnTestSlotRequest struct {
	Project         string  `json:"project"`
	SlotIndex       *int    `json:"slot_index,omitempty"`
	SlotName        *string `json:"slot_name,omitempty"`
	CallerSessionID *string `json:"caller_session_id,omitempty"`
	Source          string  `json:"source,omitempty"`
	Reason          string  `json:"reason,omitempty"`
}

type ReturnTestSlotResult struct {
	State          string  `json:"state"`
	Project        string  `json:"project"`
	Lease          string  `json:"lease"`
	SlotIndex      *int    `json:"slot_index,omitempty"`
	SlotName       *string `json:"slot_name,omitempty"`
	CleanupStarted bool    `json:"cleanup_started"`
	Usable         bool    `json:"usable"`
	StatusURL      *string `json:"status_url,omitempty"`
	Detail         *string `json:"detail,omitempty"`
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
		c.http = &http.Client{Timeout: 20 * time.Second}
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

func (c *Client) State(ctx context.Context, actorEmail string) (StateSnapshot, error) {
	token, err := c.mintToken(ctx, actorEmail)
	if err != nil {
		return StateSnapshot{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/state", nil)
	if err != nil {
		return StateSnapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return StateSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StateSnapshot{}, fmt.Errorf("glimmung state returned %d: %s", resp.StatusCode, responseDetail(resp))
	}
	var snapshot StateSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return StateSnapshot{}, fmt.Errorf("decode glimmung state: %w", err)
	}
	return snapshot, nil
}

func (c *Client) ReturnTestSlot(ctx context.Context, actorEmail string, body ReturnTestSlotRequest) (ReturnTestSlotResult, error) {
	token, err := c.mintToken(ctx, actorEmail)
	if err != nil {
		return ReturnTestSlotResult{}, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ReturnTestSlotResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/test-slots/return", bytes.NewReader(raw))
	if err != nil {
		return ReturnTestSlotResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return ReturnTestSlotResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ReturnTestSlotResult{}, fmt.Errorf("glimmung return returned %d: %s", resp.StatusCode, responseDetail(resp))
	}
	var result ReturnTestSlotResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ReturnTestSlotResult{}, fmt.Errorf("decode glimmung return: %w", err)
	}
	return result, nil
}

func (c *Client) mintToken(ctx context.Context, actorEmail string) (string, error) {
	actorEmail = strings.ToLower(strings.TrimSpace(actorEmail))
	if actorEmail == "" {
		return "", errors.New("glimmung: actor email is empty")
	}
	saToken, err := c.readToken(c.saPath)
	if err != nil {
		return "", fmt.Errorf("read SA token at %s: %w", c.saPath, err)
	}
	body, err := json.Marshal(map[string]string{"actor_email": actorEmail})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.exchange, bytes.NewReader(body))
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil && strings.TrimSpace(payload.Detail) != "" {
		return strings.TrimSpace(payload.Detail)
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
