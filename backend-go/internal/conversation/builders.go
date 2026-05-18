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
	display, err := userMessageDisplay(args.SkillName, text)
	if err != nil {
		return "", nil, err
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
	Runtime           string
	SkillName         string
	Now               time.Time
}

// TurnCommandFailedArgs describes the durable event emitted when the
// orchestrator fails to publish a session command (submit_turn,
// interrupt_turn, input_reply) to the session bus. The runner never
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
