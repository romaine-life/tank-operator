package hermes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
)

// Translator maps Hermes' /v1/runs/:id/events SSE events to the Tank
// conversation schema. One Translator per turn; thread-unsafe by design
// (the bridge guarantees a single goroutine drives translation per run).
//
// Stateful because Hermes emits item lifecycle as add/done pairs plus
// optional text deltas in between; we accumulate text per output_item so
// the emitted item.completed carries the full assembled content.
//
// Provider event shapes are documented in
// NousResearch/hermes-agent → website/docs/user-guide/features/api-server.md
// under "Streaming" + "Tool progress in streams." Unknown event types are
// counted (UnhandledCount) and ignored, so a Hermes upstream that adds a
// new event type degrades gracefully rather than crashes the bridge.
type Translator struct {
	cfg TranslatorConfig

	// State carried across multiple SSE events within one run.
	startedEmitted          bool
	itemTexts               map[string]*strings.Builder // accumulated text per output_item.id
	itemStarted             map[string]bool             // emitted item.started for output_item.id
	simpleMessageText       strings.Builder             // accumulated Hermes message.delta text
	assistantMessageEmitted bool
	terminalKind            string // "completed" / "failed" / "interrupted" / ""

	// Metrics surface for the bridge / observability layer.
	UnhandledCount   int
	UnhandledTypes   map[string]int
	TranslatorErrors []error
}

// TranslatorConfig is the immutable per-turn binding the Translator works
// against. SessionID is Tank's session id (also passed as Hermes
// session_id), TurnID is the Tank-side turn id derived from the client
// nonce.
type TranslatorConfig struct {
	SessionID         string
	SessionStorageKey string
	Email             string
	TurnID            string
	ClientNonce       string
	// Now is an optional clock override for deterministic golden tests;
	// when zero, time.Now().UTC() is used.
	Now func() time.Time
}

// NewTranslator constructs a fresh Translator for the given turn. Each
// session turn (one POST /v1/runs) gets its own Translator; do not reuse
// across turns or runs.
func NewTranslator(cfg TranslatorConfig) *Translator {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Translator{
		cfg:            cfg,
		itemTexts:      make(map[string]*strings.Builder),
		itemStarted:    make(map[string]bool),
		UnhandledTypes: make(map[string]int),
	}
}

// Terminal reports the terminal lifecycle outcome the translator observed
// for this run: "completed", "failed", "interrupted", or "" if no
// terminal event arrived yet. The bridge uses this to enforce the
// "Stop is only complete when the durable terminal arrives" contract
// inherited from nelsong6/tank-operator#532.
func (t *Translator) Terminal() string { return t.terminalKind }

// Translate consumes one Hermes RunEvent and returns the Tank-schema
// events that should be appended to session_events for this turn.
// Returns nil + no error when the event is ignored (e.g. a delta that
// hasn't yet warranted an item.started/completed emit). An error result
// is non-fatal — the bridge counts it as a translator error and keeps
// streaming.
func (t *Translator) Translate(evt RunEvent) []map[string]any {
	out, err := t.translate(evt)
	if err != nil {
		t.TranslatorErrors = append(t.TranslatorErrors, err)
	}
	return out
}

func (t *Translator) translate(evt RunEvent) ([]map[string]any, error) {
	switch evt.Type {
	case "response.created", "run.created", "run.started":
		return t.handleRunStarted(evt), nil
	case "response.output_text.delta":
		return t.handleTextDelta(evt)
	case "message.delta":
		return t.handleMessageDelta(evt)
	case "response.output_item.added":
		return t.handleItemAdded(evt)
	case "response.output_item.done":
		return t.handleItemDone(evt)
	case "response.completed", "run.completed":
		return t.handleTerminal(evt, conversation.EventTurnCompleted), nil
	case "response.failed", "response.error", "run.failed":
		return t.handleTerminal(evt, conversation.EventTurnFailed), nil
	case "response.cancelled", "run.cancelled":
		return t.handleTerminal(evt, conversation.EventTurnInterrupted), nil
	case "hermes.tool.progress":
		// Tool-start visibility is already conveyed by
		// response.output_item.added(type=function_call); ignoring this
		// avoids double-emitting item.started for the same tool call.
		return nil, nil
	case "":
		// Bare data line with no `event:` header; treat as a comment.
		return nil, nil
	default:
		t.UnhandledCount++
		t.UnhandledTypes[evt.Type]++
		return nil, nil
	}
}

func (t *Translator) handleRunStarted(evt RunEvent) []map[string]any {
	if t.startedEmitted {
		return nil
	}
	t.startedEmitted = true
	return []map[string]any{t.stamp(map[string]any{
		"event_id":   t.cfg.TurnID + ":turn.started",
		"session_id": t.cfg.SessionID,
		"turn_id":    t.cfg.TurnID,
		"actor":      string(conversation.ActorRunner),
		"source":     string(conversation.SourceHermes),
		"type":       string(conversation.EventTurnStarted),
		"created_at": t.nowRFC3339(),
		"producer":   t.producer(),
		"visibility": string(conversation.VisibilityDurable),
	})}
}

// outputTextDelta is the documented Responses-API SSE shape:
//
//	{ "item_id": "...", "delta": "...", ... }
type outputTextDelta struct {
	ItemID string `json:"item_id"`
	Delta  string `json:"delta"`
}

func (t *Translator) handleTextDelta(evt RunEvent) ([]map[string]any, error) {
	var d outputTextDelta
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return nil, fmt.Errorf("response.output_text.delta unmarshal: %w", err)
	}
	if d.ItemID == "" {
		return nil, nil
	}
	buf, ok := t.itemTexts[d.ItemID]
	if !ok {
		buf = &strings.Builder{}
		t.itemTexts[d.ItemID] = buf
	}
	buf.WriteString(d.Delta)
	// Deliberately do NOT emit a Tank event per delta — Tank's schema has
	// item.started + item.completed only, no intermediate update event.
	// The agent-runner / codex-runner adapters drop their per-token
	// streams the same way (see codex.ts's "item.updated" branch). When
	// a live partial-token channel lands, restore both Tank's item.delta
	// event type and visibility=live-only together.
	return nil, nil
}

type simpleMessageDelta struct {
	Delta string `json:"delta"`
	Text  string `json:"text"`
}

func (t *Translator) handleMessageDelta(evt RunEvent) ([]map[string]any, error) {
	var d simpleMessageDelta
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return nil, fmt.Errorf("message.delta unmarshal: %w", err)
	}
	chunk := d.Delta
	if chunk == "" {
		chunk = d.Text
	}
	if chunk == "" {
		return nil, nil
	}
	t.simpleMessageText.WriteString(chunk)
	// Hermes' /v1/runs stream currently exposes simple text deltas without
	// a Responses output_item lifecycle. Tank has no durable per-token event,
	// so the bridge emits the assistant message when run.completed arrives.
	return nil, nil
}

// outputItem mirrors the documented Responses-API output_item shape. Each
// item has a stable `id` (used as our provider_item_id), a `type` that
// drives Tank's payload kind, and type-specific fields.
type outputItem struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"` // "message" | "function_call" | "function_call_output"
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
}

type itemEnvelope struct {
	Item outputItem `json:"item"`
	// Some Hermes builds emit the item fields at the top level instead
	// of nested under `item`; the bridge accepts both via fallback in
	// decodeItem.
	OutputItem outputItem `json:"output_item"`
}

func decodeItem(raw json.RawMessage) (outputItem, error) {
	var env itemEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return outputItem{}, err
	}
	if env.Item.ID != "" || env.Item.Type != "" {
		return env.Item, nil
	}
	if env.OutputItem.ID != "" || env.OutputItem.Type != "" {
		return env.OutputItem, nil
	}
	// Fallback: top-level fields.
	var top outputItem
	if err := json.Unmarshal(raw, &top); err != nil {
		return outputItem{}, err
	}
	return top, nil
}

func (t *Translator) handleItemAdded(evt RunEvent) ([]map[string]any, error) {
	item, err := decodeItem(evt.Data)
	if err != nil {
		return nil, fmt.Errorf("response.output_item.added decode: %w", err)
	}
	if item.ID == "" {
		return nil, nil
	}
	// Suppress duplicate item.started for the same item id.
	if t.itemStarted[item.ID] {
		return nil, nil
	}
	t.itemStarted[item.ID] = true

	switch item.Type {
	case "message":
		// item.started for an assistant text item carries an empty
		// message — the actual content arrives via deltas + done.
		return []map[string]any{t.buildItem(itemArgs{
			Type:           conversation.EventItemStarted,
			Actor:          conversation.ActorAssistant,
			ProviderItemID: item.ID,
			Payload:        map[string]any{"kind": "message", "text": ""},
		})}, nil
	case "function_call":
		title := item.Name
		if title == "" {
			title = "tool"
		}
		// Tool calls are keyed by call_id when present so the matching
		// function_call_output (tool result) lands on the same
		// timeline_id; fall back to item.id otherwise.
		providerItemID := item.CallID
		if providerItemID == "" {
			providerItemID = item.ID
		}
		t.itemStarted[item.ID] = true
		t.itemStarted[providerItemID] = true
		return []map[string]any{t.buildItem(itemArgs{
			Type:           conversation.EventItemStarted,
			Actor:          conversation.ActorTool,
			ProviderItemID: providerItemID,
			Payload: map[string]any{
				"kind":      "tool",
				"title":     title,
				"name":      item.Name,
				"arguments": json.RawMessage(item.Arguments),
			},
		})}, nil
	default:
		// function_call_output and unknown item types: no item.started
		// emit. The done event will carry the final state.
		return nil, nil
	}
}

func (t *Translator) handleItemDone(evt RunEvent) ([]map[string]any, error) {
	item, err := decodeItem(evt.Data)
	if err != nil {
		return nil, fmt.Errorf("response.output_item.done decode: %w", err)
	}
	if item.ID == "" {
		return nil, nil
	}

	switch item.Type {
	case "message":
		text := ""
		if buf, ok := t.itemTexts[item.ID]; ok {
			text = buf.String()
		}
		// Some Hermes builds carry the final message body on the done
		// event itself rather than as a sequence of deltas. Accept
		// either shape.
		if text == "" && len(item.Content) > 0 {
			text = extractMessageText(item.Content)
		}
		t.assistantMessageEmitted = true
		return []map[string]any{t.buildItem(itemArgs{
			Type:           conversation.EventItemCompleted,
			Actor:          conversation.ActorAssistant,
			ProviderItemID: item.ID,
			Payload:        map[string]any{"kind": "message", "text": text},
		})}, nil
	case "function_call":
		providerItemID := item.CallID
		if providerItemID == "" {
			providerItemID = item.ID
		}
		title := item.Name
		if title == "" {
			title = "tool"
		}
		return []map[string]any{t.buildItem(itemArgs{
			Type:           conversation.EventItemCompleted,
			Actor:          conversation.ActorTool,
			ProviderItemID: providerItemID,
			Payload: map[string]any{
				"kind":      "tool",
				"title":     title,
				"name":      item.Name,
				"arguments": json.RawMessage(item.Arguments),
			},
		})}, nil
	case "function_call_output":
		providerItemID := item.CallID
		if providerItemID == "" {
			providerItemID = item.ID
		}
		// Render tool_result on the same timeline_id as the matching
		// tool_use so the SPA folds the call + result into one rendered
		// card. agent-runner / codex-runner use this same convention.
		return []map[string]any{t.buildItem(itemArgs{
			Type:           conversation.EventItemCompleted,
			Actor:          conversation.ActorTool,
			ProviderItemID: providerItemID,
			Payload: map[string]any{
				"kind":     "tool_result",
				"output":   json.RawMessage(item.Output),
				"is_error": false,
			},
		})}, nil
	default:
		return nil, nil
	}
}

type terminalPayload struct {
	Output string `json:"output"`
}

func (t *Translator) handleTerminal(evt RunEvent, eventType conversation.EventType) []map[string]any {
	switch eventType {
	case conversation.EventTurnCompleted:
		t.terminalKind = "completed"
	case conversation.EventTurnFailed:
		t.terminalKind = "failed"
	case conversation.EventTurnInterrupted:
		t.terminalKind = "interrupted"
	}
	payload := map[string]any{}
	if eventType == conversation.EventTurnFailed {
		payload["reason"] = "provider_failure"
	}
	event := t.stamp(map[string]any{
		"event_id":     t.cfg.TurnID + ":" + string(eventType),
		"session_id":   t.cfg.SessionID,
		"turn_id":      t.cfg.TurnID,
		"client_nonce": t.cfg.ClientNonce,
		"actor":        string(conversation.ActorRunner),
		"source":       string(conversation.SourceHermes),
		"type":         string(eventType),
		"created_at":   t.nowRFC3339(),
		"producer":     t.producer(),
		"visibility":   string(conversation.VisibilityDurable),
	})
	if len(payload) > 0 {
		event["payload"] = payload
	}
	events := []map[string]any{}
	if eventType == conversation.EventTurnCompleted {
		if assistant := t.emitSimpleAssistantMessage(evt); assistant != nil {
			events = append(events, assistant)
		}
	}
	events = append(events, event)
	return events
}

// ─────────────────────────────────────────────────────────────────────────
// helpers

type itemArgs struct {
	Type           conversation.EventType
	Actor          conversation.Actor
	ProviderItemID string
	Payload        map[string]any
}

// itemTimelineID mirrors runner-shared/conversation-builders.js's
// itemTimelineID: `${turnID}:item:${stableIDPart(providerItemID)}`.
func itemTimelineID(turnID, providerItemID string) string {
	return turnID + ":item:" + stableIDPart(providerItemID)
}

func stableIDPart(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:16]
}

func (t *Translator) buildItem(args itemArgs) map[string]any {
	timelineID := itemTimelineID(t.cfg.TurnID, args.ProviderItemID)
	event := map[string]any{
		"event_id":         t.cfg.TurnID + ":" + string(args.Type) + ":" + stableIDPart(args.ProviderItemID),
		"session_id":       t.cfg.SessionID,
		"turn_id":          t.cfg.TurnID,
		"timeline_id":      timelineID,
		"provider_item_id": args.ProviderItemID,
		"parent_id":        t.cfg.TurnID,
		"actor":            string(args.Actor),
		"source":           string(conversation.SourceHermes),
		"type":             string(args.Type),
		"created_at":       t.nowRFC3339(),
		"producer":         t.producer(),
		"visibility":       string(conversation.VisibilityDurable),
		"payload":          args.Payload,
	}
	return t.stamp(event)
}

func (t *Translator) emitSimpleAssistantMessage(evt RunEvent) map[string]any {
	if t.assistantMessageEmitted {
		return nil
	}
	text := t.terminalOutput(evt)
	if text == "" {
		text = t.simpleMessageText.String()
	}
	if text == "" {
		return nil
	}
	t.assistantMessageEmitted = true
	return t.buildItem(itemArgs{
		Type:           conversation.EventItemCompleted,
		Actor:          conversation.ActorAssistant,
		ProviderItemID: "message",
		Payload:        map[string]any{"kind": "message", "text": text},
	})
}

func (t *Translator) terminalOutput(evt RunEvent) string {
	if len(evt.Data) == 0 {
		return ""
	}
	var p terminalPayload
	if err := json.Unmarshal(evt.Data, &p); err != nil {
		return ""
	}
	return p.Output
}

func (t *Translator) stamp(event map[string]any) map[string]any {
	if t.cfg.SessionStorageKey != "" {
		event["tank_session_id"] = t.cfg.SessionStorageKey
	}
	if t.cfg.SessionID != "" {
		event["tank_public_session_id"] = t.cfg.SessionID
	}
	if t.cfg.Email != "" {
		event["email"] = t.cfg.Email
	}
	event["runtime"] = "hermes"
	return conversation.StampEventMap(event)
}

func (t *Translator) producer() map[string]any {
	return map[string]any{
		"name":    "hermes-bridge",
		"runtime": "hermes",
	}
}

func (t *Translator) nowRFC3339() string {
	return t.cfg.Now().Format(time.RFC3339Nano)
}

// extractMessageText pulls a flat text string out of the OpenAI Responses
// `output_text` content shape: an array of {type: "output_text", text:
// "..."} parts. Concatenation matches what the upstream final assistant
// message would render.
func extractMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try array shape first.
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if s, _ := p["text"].(string); s != "" {
				b.WriteString(s)
			}
		}
		return b.String()
	}
	// Fallback: bare string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}
