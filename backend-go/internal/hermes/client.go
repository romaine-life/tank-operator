// Package hermes drives Hermes Agent's OpenAI-compatible API server for
// hermes_gui session turns. See nelsong6/tank-operator#540 for the
// integration design.
//
// External shape: this package owns the Tank → Hermes wire protocol.
// Inputs are Tank session IDs + user turn text; outputs land in the
// session_events ledger via the bridge's translator. Nothing in this
// package writes to Postgres directly — that's the caller's job, mirroring
// the agent-runner / codex-runner pattern.
package hermes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Defaults reflect Hermes' documented surface
// (NousResearch/hermes-agent → website/docs/user-guide/features/api-server.md).
// Override via NewClient options when the deployment env wires different
// values; the defaults are appropriate for the cluster-internal Service
// `hermes-api.hermes.svc.cluster.local:8642` provisioned in
// nelsong6/hermes#12.
const (
	DefaultBaseURL = "http://hermes-api.hermes.svc.cluster.local:8642"
	DefaultTimeout = 10 * time.Minute // worst-case run duration
)

// TokenSource produces a fresh bearer for outbound calls. Concrete
// implementation today is AuthRomaineServiceProvider (exchanges the
// orchestrator's audience-pinned projected SA token at
// auth.romaine.life/api/auth/exchange/k8s; caches until ~30s before exp).
// Goroutine-safe by contract.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Client speaks Hermes' /v1 surface. Goroutine-safe; an underlying
// *http.Client is shared across calls.
type Client struct {
	baseURL string
	tokens  TokenSource
	http    *http.Client
}

// Options configures NewClient. All fields except Tokens are optional.
type Options struct {
	BaseURL string        // default DefaultBaseURL
	Tokens  TokenSource   // required; produces a fresh role=service JWT per call
	Timeout time.Duration // default DefaultTimeout (per non-streaming call)
}

func NewClient(opts Options) *Client {
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	// Streaming calls (events) do NOT inherit this timeout — they pass a
	// dedicated context and explicitly use a no-timeout transport. See
	// streamEvents.
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		tokens:  opts.Tokens,
		http: &http.Client{
			Timeout: timeout,
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Capabilities

// Capabilities is the response shape of GET /v1/capabilities. Tank uses
// this as a smoke-test against a fresh Hermes deployment; the bridge
// checks Features.RunEventsSSE at startup so a config mistake fails loud
// rather than silently degrading to non-streaming behavior.
type Capabilities struct {
	Object   string `json:"object"`
	Platform string `json:"platform"`
	Model    string `json:"model"`
	Auth     struct {
		Type     string `json:"type"`
		Required bool   `json:"required"`
	} `json:"auth"`
	Features struct {
		ChatCompletions bool `json:"chat_completions"`
		ResponsesAPI    bool `json:"responses_api"`
		RunSubmission   bool `json:"run_submission"`
		RunStatus       bool `json:"run_status"`
		RunEventsSSE    bool `json:"run_events_sse"`
		RunStop         bool `json:"run_stop"`
	} `json:"features"`
}

func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var out Capabilities
	if err := c.do(ctx, http.MethodGet, "/v1/capabilities", nil, &out); err != nil {
		return Capabilities{}, fmt.Errorf("capabilities: %w", err)
	}
	return out, nil
}

func ValidateCapabilities(caps Capabilities) error {
	var missing []string
	if !caps.Features.RunSubmission {
		missing = append(missing, "run_submission")
	}
	if !caps.Features.RunStatus {
		missing = append(missing, "run_status")
	}
	if !caps.Features.RunEventsSSE {
		missing = append(missing, "run_events_sse")
	}
	if !caps.Features.RunStop {
		missing = append(missing, "run_stop")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required Hermes capabilities: %s", strings.Join(missing, ", "))
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────
// Runs

// CreateRunRequest is the body shape for POST /v1/runs. SessionID is the
// caller's stable identifier — Hermes "surfaces it in the run status so
// external UIs can correlate runs with their own conversation IDs"
// (api-server.md). Tank passes its session UUID.
//
// Instructions, when non-empty, is layered on top of Hermes' core system
// prompt — per the API doc: "Hermes agent keeps all its tools, memory,
// and skills — the frontend's system prompt adds extra instructions."
type CreateRunRequest struct {
	Input              string `json:"input"`
	SessionID          string `json:"session_id,omitempty"`
	Instructions       string `json:"instructions,omitempty"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
}

// CreateRunResponse is the immediate result of POST /v1/runs. Hermes
// returns this synchronously before the run actually executes; tail
// the events SSE for progress.
type CreateRunResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"` // "started" on success
}

func (c *Client) CreateRun(ctx context.Context, req CreateRunRequest) (CreateRunResponse, error) {
	var out CreateRunResponse
	if err := c.do(ctx, http.MethodPost, "/v1/runs", req, &out); err != nil {
		return CreateRunResponse{}, fmt.Errorf("create run: %w", err)
	}
	if out.RunID == "" {
		return CreateRunResponse{}, errors.New("create run: empty run_id in response")
	}
	return out, nil
}

// RunStatus is the response shape of GET /v1/runs/:id. Useful for
// post-stream reconciliation (e.g. SSE disconnect) and for the SPA's
// activity-summary lifecycle emitter.
type RunStatus struct {
	Object    string `json:"object"`
	RunID     string `json:"run_id"`
	Status    string `json:"status"` // "started" | "completed" | "failed" | "cancelled"
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Output    string `json:"output"`
	Usage     struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func (c *Client) GetRun(ctx context.Context, runID string) (RunStatus, error) {
	var out RunStatus
	if err := c.do(ctx, http.MethodGet, "/v1/runs/"+runID, nil, &out); err != nil {
		return RunStatus{}, fmt.Errorf("get run %s: %w", runID, err)
	}
	return out, nil
}

// StopRun cancels an in-flight run. Hermes "returns immediately with
// {status: stopping} while [it] asks the active agent to stop at the
// next safe interruption point" (api-server.md). A subsequent /events
// stream will emit the terminal lifecycle event when the agent
// actually stops.
type StopResponse struct {
	Status string `json:"status"` // "stopping"
}

func (c *Client) StopRun(ctx context.Context, runID string) (StopResponse, error) {
	var out StopResponse
	if err := c.do(ctx, http.MethodPost, "/v1/runs/"+runID+"/stop", struct{}{}, &out); err != nil {
		return StopResponse{}, fmt.Errorf("stop run %s: %w", runID, err)
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────
// Run event streaming
//
// Hermes' /v1/runs/:id/events is "Designed for dashboards and thick
// clients that want to attach/detach without losing state" (api-server.md
// → Runs API). The wire format is SSE with two channels Tank cares about:
// the OpenAI Responses-style event types (response.created,
// response.output_text.delta, response.output_item.added,
// response.output_item.done, response.completed) and Hermes' custom
// `hermes.tool.progress` (for tool-start visibility).
//
// We model each event as a (Type, Data) pair so the translator stays
// schema-agnostic — adding a new Hermes event type requires zero changes
// to this file.

// RunEvent is a single SSE-decoded record. Data is the raw JSON object
// shipped on the SSE `data:` line(s); the translator unmarshals into
// type-specific shapes.
type RunEvent struct {
	Type string
	Data json.RawMessage
}

// StreamEvents tails GET /v1/runs/:id/events and calls handler for each
// event in arrival order. Returns when the stream ends (normal terminal
// event), the context cancels, or an unrecoverable transport error
// occurs. Re-entry is supported: if the handler returns an error,
// streaming stops and that error is returned; a re-call resumes from
// Hermes' point-in-time (per upstream's attach/detach contract).
//
// Implementation notes:
//   - We deliberately do NOT inherit the *Client's http.Client.Timeout
//     for this call — SSE streams stay open for the run's duration,
//     which can be many minutes. A separate transport with no Timeout is
//     used; cancellation is via ctx.
//   - The SSE parser handles multi-line `data:` continuation and `:`
//     comment lines per RFC EventSource. `id:` and `retry:` lines are
//     ignored (Hermes doesn't use them).
func (c *Client) StreamEvents(ctx context.Context, runID string, handler func(RunEvent) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/runs/"+runID+"/events", nil)
	if err != nil {
		return fmt.Errorf("stream events %s: build request: %w", runID, err)
	}
	if err := c.applyAuth(ctx, req); err != nil {
		return fmt.Errorf("stream events %s: %w", runID, err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := streamingHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("stream events %s: %w", runID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("stream events %s: HTTP %d: %s", runID, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return parseSSE(resp.Body, handler)
}

// streamingHTTPClient is used only by StreamEvents. The zero Timeout
// makes long-poll-style streams safe; cancellation comes from the
// per-call context.
var streamingHTTPClient = &http.Client{}

func parseSSE(body io.Reader, handler func(RunEvent) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20) // 1MiB max line
	var (
		curEvent string
		dataBuf  bytes.Buffer
	)
	dispatch := func() error {
		if dataBuf.Len() == 0 && curEvent == "" {
			return nil
		}
		data := json.RawMessage(append([]byte(nil), bytes.TrimRight(dataBuf.Bytes(), "\n")...))
		eventType := curEvent
		if eventType == "" {
			eventType = eventTypeFromData(data)
		}
		evt := RunEvent{Type: eventType, Data: data}
		curEvent = ""
		dataBuf.Reset()
		return handler(evt)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // SSE comment
		}
		if strings.HasPrefix(line, "event:") {
			curEvent = strings.TrimSpace(line[len("event:"):])
			continue
		}
		if strings.HasPrefix(line, "data:") {
			// SSE spec: a single leading space after `data:` is stripped;
			// subsequent characters are preserved verbatim.
			payload := line[len("data:"):]
			if strings.HasPrefix(payload, " ") {
				payload = payload[1:]
			}
			dataBuf.WriteString(payload)
			dataBuf.WriteByte('\n')
			continue
		}
		// id: / retry: / unknown — ignored per spec.
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse scan: %w", err)
	}
	// Flush a trailing event with no blank-line terminator (some servers
	// close the connection without it).
	return dispatch()
}

func eventTypeFromData(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var env struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return ""
	}
	return env.Event
}

// ─────────────────────────────────────────────────────────────────────────
// Internal helpers

func (c *Client) applyAuth(ctx context.Context, req *http.Request) error {
	if c.tokens == nil {
		return errors.New("hermes client has no token source")
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("hermes auth: %w", err)
	}
	if token == "" {
		return errors.New("hermes token source returned empty token")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	if err := c.applyAuth(ctx, req); err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
