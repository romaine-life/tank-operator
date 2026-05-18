package sessionbus

import (
	"strings"
	"testing"
)

// TestSessionListEventSubjectIncludesScope is the wire-shape lockdown
// for the scope partition. Pre-#83-follow-up the subject was keyed on
// email alone; prod and slot orchestrators sharing one NATS broker
// received each other's events because they subscribed to the same
// subject. Cross-environment delivery now has to be physically
// unreachable on the broker, not filtered after the fact.
func TestSessionListEventSubjectIncludesScope(t *testing.T) {
	const email = "u@example.com"

	prod := SessionListEventSubject(email, "default")
	slot := SessionListEventSubject(email, "tank-operator-slot-0")

	if prod == slot {
		t.Fatalf("subjects must differ across scopes; got %q for both prod (default) and slot (tank-operator-slot-0)", prod)
	}
	if !strings.HasSuffix(prod, ".events") {
		t.Fatalf("subject must end in .events; got %q", prod)
	}
	if !strings.HasSuffix(slot, ".events") {
		t.Fatalf("subject must end in .events; got %q", slot)
	}

	// Same email and scope must hash to the same subject (idempotent +
	// case-insensitive on the email half).
	if got := SessionListEventSubject(strings.ToUpper(email), "default"); got != prod {
		t.Fatalf("email casing must not change the subject; got %q, want %q", got, prod)
	}
}

// TestSessionListEventSubjectFormat keeps the on-the-wire token shape
// pinned. Both halves are base64-url-encoded so arbitrary scope names
// (Helm release names containing `.`, etc.) survive NATS's reserved
// `.`/`*`/`>` separators without ad-hoc sanitization.
func TestSessionListEventSubjectFormat(t *testing.T) {
	subject := SessionListEventSubject("u@example.com", "tank-operator-slot-0")
	parts := strings.Split(subject, ".")
	// Expected shape: tank.live.sessions.<email_token>.<scope_token>.events
	if len(parts) != 6 {
		t.Fatalf("subject tokens = %d, want 6: %q", len(parts), subject)
	}
	if parts[0] != "tank" || parts[1] != "live" || parts[2] != "sessions" || parts[5] != "events" {
		t.Fatalf("subject scaffolding changed: %q", subject)
	}
	if parts[3] == "" || parts[4] == "" {
		t.Fatalf("email and scope tokens must be non-empty: %q", subject)
	}
	// Scope tokens must not contain the raw "-" or "." from the source
	// scope name — that's what base64-url-encoding buys us. (RawURLEncoding
	// emits only [A-Za-z0-9_-], no `.` or `*`.)
	if strings.Contains(parts[4], ".") {
		t.Fatalf("scope token must not contain '.', the NATS separator; got %q", parts[4])
	}
}
