package conversation

import (
	"fmt"
	"strings"
	"time"
)

// Backend-authored "notice turn" builders.
//
// A notice turn is a complete, runner-free turn the orchestrator authors end to
// end so a deterministic system workflow (the zero-LLM test-slot provision
// thread; the PR-ready ping) renders as a real, turn-anchored entry the user
// can land on — not an orphan role:system record with no turn_id.
//
// The turn lifecycle is the standard one, but every event is emitted by the
// backend via persistBackendEvent and NO submit_turn command is published, so
// the SDK runner is never woken:
//
//	open  -> UserSubmissionEventMaps(AuthorKind=system)   // user_message.created + turn.submitted
//	body  -> AssistantNoticeEventMap(...) per phase line  // assistant_message.created
//	close -> NoticeTurnCompletedEventMap(...)             // turn.completed
//
// The turn shows "running" while the workflow is in flight (correct — it is)
// and completes on the terminal phase. The pending-provision reconcile is
// responsible for emitting the close if the authoring goroutine dies before the
// terminal, so the turn is never stranded (turn.submitted without a matching
// terminal trips TankTurnSilentStranding).

// NoticeTurnOpenArgs opens a backend notice turn. OpenerText is the first line
// the system "says" (e.g. "Creating test slot."); it becomes the turn's
// user-side message, authored by the system (author_kind=system) so it renders
// with the session's system identity, not the human owner's avatar.
type NoticeTurnOpenArgs struct {
	SessionID         string
	SessionStorageKey string
	Email             string
	ClientNonce       string
	OpenerText        string
	Now               time.Time
}

// NoticeTurnOpenEventMaps returns the turn id and the open boundary events
// (user_message.created + turn.submitted) for a notice turn. Unlike
// UserSubmissionEventMaps it requires no provider runtime — a notice turn is
// not a provider submission — and produces no submit_turn command, so the SDK
// runner is never woken.
func NoticeTurnOpenEventMaps(args NoticeTurnOpenArgs) (string, []map[string]any, error) {
	text := strings.TrimSpace(args.OpenerText)
	nonce := strings.TrimSpace(args.ClientNonce)
	if text == "" {
		return "", nil, fmt.Errorf("opener text is required")
	}
	if nonce == "" {
		return "", nil, fmt.Errorf("client nonce is required")
	}
	createdAt := args.Now
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	turnID := TurnIDForClientNonce(nonce)
	producer := map[string]any{"name": "tank-operator"}
	events := []map[string]any{
		StampEventMap(map[string]any{
			"event_id":        turnID + ":user_message.created",
			"conversation_id": args.SessionID,
			"session_id":      args.SessionID,
			"turn_id":         turnID,
			"timeline_id":     turnID + ":user",
			"client_nonce":    nonce,
			"actor":           string(ActorUser),
			"source":          string(SourceTank),
			"type":            string(EventUserMessageCreated),
			"created_at":      createdAt.Format(time.RFC3339Nano),
			"producer":        producer,
			"visibility":      string(VisibilityDurable),
			"author_kind":     string(AuthorKindSystem),
			"payload": map[string]any{
				"text":    text,
				"message": map[string]any{"role": "user", "content": text},
				"display": map[string]any{"kind": "plain"},
			},
		}),
		StampEventMap(map[string]any{
			"event_id":        turnID + ":turn.submitted",
			"conversation_id": args.SessionID,
			"session_id":      args.SessionID,
			"turn_id":         turnID,
			"client_nonce":    nonce,
			"actor":           string(ActorRunner),
			"source":          string(SourceTank),
			"type":            string(EventTurnSubmitted),
			"created_at":      createdAt.Format(time.RFC3339Nano),
			"producer":        producer,
			"visibility":      string(VisibilityDurable),
			"author_kind":     string(AuthorKindSystem),
			"payload":         map[string]any{"status": "submitted"},
		}),
	}
	for _, event := range events {
		stampNoticeIdentity(event, args.SessionStorageKey, args.SessionID, args.Email)
	}
	return turnID, events, nil
}

// AssistantNoticeArgs builds one body line of a notice turn: an
// assistant_message.created anchored to TurnID. Text is the phase line
// ("Validating PR readiness…", "Test environment ready at <url>", …).
type AssistantNoticeArgs struct {
	SessionID         string
	SessionStorageKey string
	Email             string
	TurnID            string
	// TimelineID must be stable + unique per body line so a re-emit (reconcile
	// re-drive) is idempotent. Convention: <turnID>:notice:<phase>.
	TimelineID string
	Text       string
	Now        time.Time
}

// AssistantNoticeEventMap returns a backend-authored assistant_message.created
// body line for a notice turn. actor=assistant so it renders as the turn's
// response through the standard turn renderer (no bespoke display type).
func AssistantNoticeEventMap(args AssistantNoticeArgs) map[string]any {
	createdAt := args.Now
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	timelineID := strings.TrimSpace(args.TimelineID)
	if timelineID == "" {
		timelineID = args.TurnID + ":notice"
	}
	event := StampEventMap(map[string]any{
		"event_id":        timelineID,
		"conversation_id": args.SessionID,
		"session_id":      args.SessionID,
		"turn_id":         args.TurnID,
		"timeline_id":     timelineID,
		"actor":           string(ActorAssistant),
		"source":          string(SourceTank),
		"type":            string(EventAssistantMessageCreated),
		"created_at":      createdAt.Format(time.RFC3339Nano),
		"producer":        map[string]any{"name": "tank-operator"},
		"visibility":      string(VisibilityDurable),
		"payload": map[string]any{
			"text":    args.Text,
			"display": map[string]any{"kind": "plain"},
		},
	})
	stampNoticeIdentity(event, args.SessionStorageKey, args.SessionID, args.Email)
	return event
}

// NoticeTurnCompletedArgs builds the terminal of a notice turn. FinalTimelineIDs
// (optional) are the body line(s) that constitute the turn's final answer; when
// set they are recorded in payload.final_answer so the renderer can anchor the
// completed turn's answer the same way a runner-produced turn does.
type NoticeTurnCompletedArgs struct {
	SessionID         string
	SessionStorageKey string
	Email             string
	TurnID            string
	FinalTimelineIDs  []string
	Now               time.Time
}

// NoticeTurnCompletedEventMap returns the turn.completed terminal for a notice
// turn. Emitting this (paired with the open) is what keeps the turn from
// stranding.
func NoticeTurnCompletedEventMap(args NoticeTurnCompletedArgs) map[string]any {
	createdAt := args.Now
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	payload := map[string]any{"status": "completed"}
	if len(args.FinalTimelineIDs) > 0 {
		payload["final_answer"] = map[string]any{
			"timeline_ids": args.FinalTimelineIDs,
		}
	}
	event := StampEventMap(map[string]any{
		"event_id":        args.TurnID + ":turn.completed",
		"conversation_id": args.SessionID,
		"session_id":      args.SessionID,
		"turn_id":         args.TurnID,
		"timeline_id":     args.TurnID + ":turn.completed",
		"actor":           string(ActorRunner),
		"source":          string(SourceTank),
		"type":            string(EventTurnCompleted),
		"created_at":      createdAt.Format(time.RFC3339Nano),
		"producer":        map[string]any{"name": "tank-operator"},
		"visibility":      string(VisibilityDurable),
		"payload":         payload,
	})
	stampNoticeIdentity(event, args.SessionStorageKey, args.SessionID, args.Email)
	return event
}

func stampNoticeIdentity(event map[string]any, storageKey, sessionID, email string) {
	if storageKey != "" {
		event["tank_session_id"] = storageKey
	}
	if sessionID != "" {
		event["tank_public_session_id"] = sessionID
	}
	if email != "" {
		event["email"] = email
	}
}
