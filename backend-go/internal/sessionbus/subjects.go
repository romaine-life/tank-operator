package sessionbus

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	defaultStream = "TANK_SESSION_BUS"
	subjectRoot   = "tank.session"
	liveRoot      = "tank.live"
)

func StreamName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return defaultStream
	}
	return name
}

func StorageToken(sessionStorageKey string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(sessionStorageKey)))
}

func CommandSubject(sessionStorageKey, provider string) string {
	return fmt.Sprintf("%s.%s.commands.%s", subjectRoot, StorageToken(sessionStorageKey), sanitizeSubjectToken(provider))
}

// ControlSubject names the per-session/per-provider control-plane subject
// that carries low-latency commands which must not be blocked by an
// in-flight turn (today: interrupt_turn; future: any control signal whose
// usefulness collapses if delivery is queued behind a long-running
// submit_turn). The runner subscribes to this subject with a dedicated
// JetStream consumer whose `max_ack_pending` is sized for control
// throughput, not data-plane serialization. See
// docs/tank-conversation-protocol.md → "Durable turn interruption" for
// the contract and the reason data plane and control plane are split.
func ControlSubject(sessionStorageKey, provider string) string {
	return fmt.Sprintf("%s.%s.control.%s", subjectRoot, StorageToken(sessionStorageKey), sanitizeSubjectToken(provider))
}

// SubjectForCommand selects the publish subject for a command based on its
// Type. Control-plane commands MUST go to ControlSubject so they are not
// delivered behind the in-flight ack of a long submit_turn; data-plane
// commands (submit_turn, anything else) go to CommandSubject and are
// serialized by the runner's single-in-flight command consumer. The split
// is the load-bearing fix for the "Stop doesn't interrupt deep tool-use
// loops" failure mode where a JetStream `max_ack_pending: 1` consumer
// held interrupt_turn behind submit_turn for the full duration of the
// turn.
//
// input_reply is also control-plane: an input_reply is only meaningful
// *while a submit_turn is parked in canUseTool* (the AskUserQuestion
// gate), and that exact parked submit_turn is what's holding the
// data-plane consumer's single ack slot. Delivering input_reply on the
// data plane would mean it can never reach the runner — JetStream would
// hold it behind the parked submit_turn, and the parked submit_turn
// would hold forever waiting for the input_reply it just got stuck
// behind. Same shape as the original interrupt deadlock; same fix
// (control plane). Pinned by TestSubjectForCommandRoutesInputReplyToControlPlane
// in bus_test.go.
func SubjectForCommand(command Command) string {
	if command.Type == CommandInterrupt || command.Type == CommandInputReply {
		return ControlSubject(command.SessionStorageKey, command.Provider)
	}
	return CommandSubject(command.SessionStorageKey, command.Provider)
}

func WakeSubject(sessionStorageKey string) string {
	return fmt.Sprintf("%s.%s.wake", liveRoot, StorageToken(sessionStorageKey))
}

// SessionListEventSubject names the per-(owner, scope) typed-event subject
// that carries session_lifecycle_events Append payloads to the sidebar SSE
// handlers. Replaces the prior opaque wake subject (an empty-payload
// resync trigger) per tank-operator#83 — see
// docs/product-inspirations.md "Work delivery should use a real
// command/event fabric. Browser polling, process memory fanout, and
// database polling are not the normal live path for app-managed GUI
// chat." The wire payload is one lifecycleevents.Event JSON document;
// SSE handlers forward it to clients verbatim.
//
// Scope is part of the subject because every other partition of the
// durable system uses (email, session_scope) as the natural key — the
// session_lifecycle_events row PK, the registry PK, the per-scope
// session_id allocator. An email-only subject conflated prod and slot
// environments that share the same Postgres + NATS broker (one row in
// prod's sidebar per slot session, deletes that came back when a slot
// republished session.created on resync). The subject now matches the
// durable partition so cross-scope events are unreachable on the wire,
// not filtered after delivery.
func SessionListEventSubject(email, scope string) string {
	normalizedEmail := strings.TrimSpace(strings.ToLower(email))
	normalizedScope := strings.TrimSpace(scope)
	return fmt.Sprintf(
		"%s.sessions.%s.%s.events",
		liveRoot,
		base64.RawURLEncoding.EncodeToString([]byte(normalizedEmail)),
		base64.RawURLEncoding.EncodeToString([]byte(normalizedScope)),
	)
}

func sanitizeSubjectToken(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
