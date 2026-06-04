package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var eventSequence atomic.Int64

// TurnIDForClientNonce mirrors the pod-side SDK runners. A client nonce is
// the idempotency key; the turn id is the provider-neutral timeline identity.
func TurnIDForClientNonce(clientNonce string) string {
	return "turn_" + stableIDPart(clientNonce)
}

func UserSubmissionEventMaps(args UserSubmissionArgs) (string, []map[string]any, error) {
	text := strings.TrimSpace(args.Text)
	clientNonce := strings.TrimSpace(args.ClientNonce)
	if text == "" {
		return "", nil, fmt.Errorf("text is required")
	}
	if clientNonce == "" {
		return "", nil, fmt.Errorf("client nonce is required")
	}
	runtime := strings.TrimSpace(args.Runtime)
	if runtime != string(SourceClaude) && runtime != string(SourceCodex) {
		return "", nil, fmt.Errorf("runtime is required")
	}
	createdAt := args.Now
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	turnID := TurnIDForClientNonce(clientNonce)
	producer := map[string]any{
		"name":    "tank-operator",
		"runtime": runtime,
	}
	display := args.Display
	if display == nil {
		var err error
		display, err = userMessageDisplay(args.SkillName, text)
		if err != nil {
			return "", nil, err
		}
	}
	message := args.Message
	if message == nil {
		message = map[string]any{"role": "user", "content": text}
	}
	events := []map[string]any{
		StampEventMap(map[string]any{
			"event_id":        turnID + ":user_message.created",
			"conversation_id": args.SessionID,
			"session_id":      args.SessionID,
			"turn_id":         turnID,
			"timeline_id":     turnID + ":user",
			"client_nonce":    clientNonce,
			"actor":           string(ActorUser),
			"source":          string(SourceTank),
			"type":            string(EventUserMessageCreated),
			"created_at":      createdAt.Format(time.RFC3339Nano),
			"producer":        producer,
			"visibility":      string(VisibilityDurable),
			"payload": map[string]any{
				"text":    text,
				"message": message,
				"display": display,
			},
		}),
		StampEventMap(map[string]any{
			"event_id":        turnID + ":turn.submitted",
			"conversation_id": args.SessionID,
			"session_id":      args.SessionID,
			"turn_id":         turnID,
			"client_nonce":    clientNonce,
			"actor":           string(ActorRunner),
			"source":          string(SourceTank),
			"type":            string(EventTurnSubmitted),
			"created_at":      createdAt.Format(time.RFC3339Nano),
			"producer":        producer,
			"visibility":      string(VisibilityDurable),
			"payload": map[string]any{
				"status": "submitted",
			},
		}),
	}
	if attachments := userMessageAttachments(args.Attachments); len(attachments) > 0 {
		payload := events[0]["payload"].(map[string]any)
		payload["attachments"] = attachments
	}
	originSessionID := strings.TrimSpace(args.OriginSessionID)
	authorKind := strings.TrimSpace(args.AuthorKind)
	for _, event := range events {
		if args.SessionStorageKey != "" {
			event["tank_session_id"] = args.SessionStorageKey
		}
		if args.SessionID != "" {
			event["tank_public_session_id"] = args.SessionID
		}
		if args.Email != "" {
			event["email"] = args.Email
		}
		event["runtime"] = runtime
		// Carry the originating session id on both events so the frontend
		// can pick the parent session's avatar for the user bubble. Stamped
		// only when the turn came in from a sibling tank-operator session
		// via the mcp-tank-operator send_prompt / spawn_run_session path;
		// human-typed turns leave this absent so the human's Gravatar
		// continues to render.
		if originSessionID != "" && originSessionID != args.SessionID {
			event["origin_session_id"] = originSessionID
		}
		// Authorship attribution for non-interactive principals (an
		// auth.romaine.life bot token). Stamped on both boundary events so
		// the durable user_message.created carries it for the renderer's
		// avatar selection; human-typed turns leave it absent. See
		// AuthorKind.
		if authorKind != "" {
			event["author_kind"] = authorKind
		}
	}
	return turnID, events, nil
}

type UserSubmissionArgs struct {
	SessionID         string
	SessionStorageKey string
	Email             string
	ClientNonce       string
	Text              string
	Message           any
	Attachments       []UserMessageAttachment
	Runtime           string
	SkillName         string
	// Display, when non-nil, is used verbatim as the user_message.created
	// payload.display instead of being derived from SkillName/text. Display
	// takes precedence over SkillName when both are set.
	Display map[string]any
	// OriginSessionID identifies the sibling tank-operator session that
	// authored this turn via an MCP handoff (send_prompt /
	// spawn_run_session). Empty for human-typed browser turns. Only
	// stamped on the emitted events when it differs from SessionID —
	// a session sending a prompt to itself reads as a normal user turn.
	OriginSessionID string
	// AuthorKind marks a turn submitted by a non-interactive principal so
	// the transcript attributes it to the session's system identity rather
	// than the human owner. Empty for human-typed turns. Currently set to
	// string(AuthorKindSystem) for auth.romaine.life bot tokens. See
	// AuthorKind for the precedence rules vs OriginSessionID.
	AuthorKind string
	Now        time.Time
}

type UserMessageAttachment struct {
	Label   string `json:"label"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	AbsPath string `json:"abs_path"`
	Size    int64  `json:"size"`
}

func userMessageAttachments(input []UserMessageAttachment) []map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(input))
	for _, attachment := range input {
		label := strings.TrimSpace(attachment.Label)
		name := strings.TrimSpace(attachment.Name)
		if label == "" && name == "" {
			continue
		}
		kind := strings.TrimSpace(attachment.Kind)
		if kind != "image" {
			kind = "file"
		}
		item := map[string]any{
			"label": firstNonEmpty(label, name),
			"name":  firstNonEmpty(name, label),
			"kind":  kind,
		}
		if path := strings.TrimSpace(attachment.Path); path != "" {
			item["path"] = path
		}
		if absPath := strings.TrimSpace(attachment.AbsPath); absPath != "" {
			item["absPath"] = absPath
		}
		if attachment.Size > 0 {
			item["size"] = attachment.Size
		}
		out = append(out, item)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// TurnCommandFailedArgs describes the durable event emitted when the
// orchestrator fails to publish a session command (submit_turn,
// interrupt_turn, stop_background_task) to the session bus. The runner never
// gets the command, so the runner-emitted turn.* terminal events never
// arrive. Without this event, a client refreshing /timeline sees the
// user_message.created stranded and the turn perpetually "submitted."
type TurnCommandFailedArgs struct {
	SessionID         string
	SessionStorageKey string
	Email             string
	TurnID            string
	ClientNonce       string
	Runtime           string
	Reason            string
	Now               time.Time
}

// TurnInterruptRequestedArgs describes the durable event emitted when a
// user-initiated stop is accepted at the /interrupt boundary. The event
// lands in the Postgres session_events ledger before the JetStream
// interrupt_turn command is published, so a refresh-after-stop replays
// the stopping projection state instead of silently losing it. Mirrors
// turn.command_failed in actor/source (backend is the producer). The
// event_id is deterministic in TurnID so a double-click POST dedupes at
// the (tank_session_id, event_id) UNIQUE constraint.
type TurnInterruptRequestedArgs struct {
	SessionID         string
	SessionStorageKey string
	Email             string
	TurnID            string
	ClientNonce       string
	Runtime           string
	Now               time.Time
}

// TurnCommandFailedEventMap builds a turn.command_failed event keyed
// by the same turn_id the failed command targeted, so client renderers
// associate it with the stranded turn submission.
func TurnCommandFailedEventMap(args TurnCommandFailedArgs) map[string]any {
	createdAt := args.Now
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	producer := map[string]any{"name": "tank-operator"}
	if args.Runtime != "" {
		producer["runtime"] = args.Runtime
	}
	event := StampEventMap(map[string]any{
		"event_id":        args.TurnID + ":turn.command_failed",
		"conversation_id": args.SessionID,
		"session_id":      args.SessionID,
		"turn_id":         args.TurnID,
		"client_nonce":    args.ClientNonce,
		"actor":           string(ActorSystem),
		"source":          string(SourceTank),
		"type":            string(EventTurnCommandFailed),
		"created_at":      createdAt.Format(time.RFC3339Nano),
		"producer":        producer,
		"visibility":      string(VisibilityDurable),
		"payload": map[string]any{
			"reason": args.Reason,
		},
	})
	if args.SessionStorageKey != "" {
		event["tank_session_id"] = args.SessionStorageKey
	}
	if args.SessionID != "" {
		event["tank_public_session_id"] = args.SessionID
	}
	if args.Email != "" {
		event["email"] = args.Email
	}
	if args.Runtime != "" {
		event["runtime"] = args.Runtime
	}
	return event
}

type TurnInputAnsweredArgs struct {
	SessionID          string
	SessionStorageKey  string
	Email              string
	TurnID             string
	ClientNonce        string
	ProviderItemID     string
	QuestionTimelineID string
	Answers            map[string][]string
	Annotations        map[string]any
	Now                time.Time
}

func TurnInputAnsweredEventMap(args TurnInputAnsweredArgs) map[string]any {
	now := args.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	payload := map[string]any{
		"question_timeline_id": args.QuestionTimelineID,
		"provider_item_id":     args.ProviderItemID,
		"answers":              stringSlicesToAnyMap(args.Answers),
	}
	if len(args.Annotations) > 0 {
		payload["annotations"] = args.Annotations
	}
	event := StampEventMap(map[string]any{
		"event_id":         args.TurnID + ":turn.input_answered:" + args.ClientNonce,
		"conversation_id":  args.SessionID,
		"session_id":       args.SessionID,
		"tank_session_id":  args.SessionStorageKey,
		"email":            args.Email,
		"turn_id":          args.TurnID,
		"timeline_id":      args.QuestionTimelineID + ":answer",
		"provider_item_id": args.ProviderItemID,
		"client_nonce":     args.ClientNonce,
		"actor":            string(ActorUser),
		"source":           string(SourceTank),
		"type":             string(EventTurnInputAnswered),
		"created_at":       now.Format(time.RFC3339Nano),
		"visibility":       string(VisibilityDurable),
		"payload":          payload,
	})
	return event
}

func stringSlicesToAnyMap(in map[string][]string) map[string]any {
	out := make(map[string]any, len(in))
	for key, values := range in {
		copied := make([]any, 0, len(values))
		for _, value := range values {
			copied = append(copied, value)
		}
		out[key] = copied
	}
	return out
}

// TurnInterruptRequestedEventMap builds the durable event posted at the
// /interrupt handler boundary. Symmetric with the submit boundary's
// user_message.created + turn.submitted pair: the orchestrator owns the
// write, no runner involvement, no payload requirements beyond turn_id.
func TurnInterruptRequestedEventMap(args TurnInterruptRequestedArgs) map[string]any {
	createdAt := args.Now
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	producer := map[string]any{"name": "tank-operator"}
	if args.Runtime != "" {
		producer["runtime"] = args.Runtime
	}
	event := StampEventMap(map[string]any{
		"event_id":        args.TurnID + ":turn.interrupt_requested",
		"conversation_id": args.SessionID,
		"session_id":      args.SessionID,
		"turn_id":         args.TurnID,
		"client_nonce":    args.ClientNonce,
		"actor":           string(ActorSystem),
		"source":          string(SourceTank),
		"type":            string(EventTurnInterruptRequested),
		"created_at":      createdAt.Format(time.RFC3339Nano),
		"producer":        producer,
		"visibility":      string(VisibilityDurable),
	})
	if args.SessionStorageKey != "" {
		event["tank_session_id"] = args.SessionStorageKey
	}
	if args.SessionID != "" {
		event["tank_public_session_id"] = args.SessionID
	}
	if args.Email != "" {
		event["email"] = args.Email
	}
	if args.Runtime != "" {
		event["runtime"] = args.Runtime
	}
	return event
}

// StampEventMap attaches uuid, order_key, sequence, and written_at to a
// built Tank event. Panics if the input is missing event_id or visibility
// so a coding bug in the builder layer is loud at runtime instead of
// silently emitting a half-stamped doc. Matches the JS
// stampTankEvent semantics in runner-shared/conversation-builders.js.
func StampEventMap(event map[string]any) map[string]any {
	eventID, _ := event["event_id"].(string)
	if eventID == "" {
		panic(fmt.Sprintf("StampEventMap: event_id is required (type=%v)", event["type"]))
	}
	visibility, _ := event["visibility"].(string)
	if visibility == "" {
		panic(fmt.Sprintf("StampEventMap: visibility is required (type=%v)", event["type"]))
	}
	out := make(map[string]any, len(event)+4)
	for k, v := range event {
		out[k] = v
	}
	now := time.Now().UTC()
	seq := eventSequence.Add(1)
	uuid, _ := out["uuid"].(string)
	if uuid == "" {
		uuid = eventID
	}
	writtenAt, _ := out["written_at"].(string)
	if writtenAt == "" {
		writtenAt = now.Format(time.RFC3339Nano)
	}
	out["uuid"] = uuid
	out["id"] = uuid
	out["written_at"] = writtenAt
	if _, ok := out["order_key"].(string); !ok {
		out["order_key"] = fmt.Sprintf("%013d-%08d-%s", now.UnixMilli(), seq, uuid)
	}
	if _, ok := out["sequence"].(int64); !ok {
		out["sequence"] = seq
	}
	if _, ok := out["created_at"].(string); !ok {
		out["created_at"] = writtenAt
	}
	return out
}

func stableIDPart(value string) string {
	trimmed := strings.TrimSpace(value)
	safe := regexp.MustCompile(`[^A-Za-z0-9_.:-]+`).ReplaceAllString(trimmed, "-")
	safe = regexp.MustCompile(`-+`).ReplaceAllString(safe, "-")
	safe = strings.Trim(safe, "-")
	sum := sha256.Sum256([]byte(value))
	hash := hex.EncodeToString(sum[:])[:12]
	if len(safe) >= 6 && len(safe) <= 80 {
		return safe
	}
	if len(safe) > 80 {
		return safe[:64] + "-" + hash
	}
	return hash
}

func userMessageDisplay(skillName, text string) (map[string]any, error) {
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return map[string]any{"kind": "plain"}, nil
	}
	if !skillNamePattern.MatchString(skillName) {
		return nil, fmt.Errorf("skill_name is invalid")
	}
	trigger := regexp.MustCompile(`(?i)^[$/]` + regexp.QuoteMeta(skillName) + `(?:\s+|\n+)?`)
	return map[string]any{
		"kind":              "skill_invocation",
		"skill_name":        skillName,
		"supplemental_text": strings.TrimSpace(trigger.ReplaceAllString(strings.TrimSpace(text), "")),
	}, nil
}
