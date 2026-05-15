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

func EventSubject(sessionStorageKey string) string {
	return fmt.Sprintf("%s.%s.events", subjectRoot, StorageToken(sessionStorageKey))
}

func WakeSubject(sessionStorageKey string) string {
	return fmt.Sprintf("%s.%s.wake", liveRoot, StorageToken(sessionStorageKey))
}

// SessionListWakeSubject names the per-owner wake subject that signals
// the user's /api/sessions list has changed. SSE subscribers listen on
// this subject; Manager mutations (Upsert, MarkDeleted, SetName, etc.)
// publish here so the in-process EventBus is no longer needed.
func SessionListWakeSubject(email string) string {
	normalized := strings.TrimSpace(strings.ToLower(email))
	return fmt.Sprintf("%s.sessions.%s.wake", liveRoot, base64.RawURLEncoding.EncodeToString([]byte(normalized)))
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
