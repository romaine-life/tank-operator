package sessionbus

import (
	"strings"
	"testing"
)

// TestSessionRowUpdateSubjectIncludesScope is the wire-shape lockdown
// for the (email, scope) row-update subject. The pre-Phase-3 typed-
// event subject was email-only originally (tank-operator#524), then
// email+scope (tank-operator#83 follow-up). Phase 3 of
// docs/session-list-redesign.md retires it entirely in favor of the
// row-update subject built here.
//
// Prod and slot orchestrators share one NATS broker; cross-environment
// delivery has to be physically unreachable on the wire, not filtered
// after the fact.
func TestSessionRowUpdateSubjectIncludesScope(t *testing.T) {
	const email = "u@example.com"

	prod := SessionRowUpdateSubject(email, "default")
	slot := SessionRowUpdateSubject(email, "tank-operator-slot-0")

	if prod == slot {
		t.Fatalf("subjects must differ across scopes; got %q for both prod (default) and slot (tank-operator-slot-0)", prod)
	}
	if !strings.HasSuffix(prod, ".rows") {
		t.Fatalf("subject must end in .rows; got %q", prod)
	}
	if !strings.HasSuffix(slot, ".rows") {
		t.Fatalf("subject must end in .rows; got %q", slot)
	}

	// Same email and scope must hash to the same subject (idempotent +
	// case-insensitive on the email half).
	if got := SessionRowUpdateSubject(strings.ToUpper(email), "default"); got != prod {
		t.Fatalf("email casing must not change the subject; got %q, want %q", got, prod)
	}
}

// TestSessionRowUpdateSubjectFormat keeps the on-the-wire token shape
// pinned. Both halves are base64-url-encoded so arbitrary scope names
// (Helm release names containing `.`, etc.) survive NATS's reserved
// `.`/`*`/`>` separators without ad-hoc sanitization.
func TestSessionRowUpdateSubjectFormat(t *testing.T) {
	subject := SessionRowUpdateSubject("u@example.com", "tank-operator-slot-0")
	parts := strings.Split(subject, ".")
	// Expected shape: tank.live.sessions.<email_token>.<scope_token>.rows
	if len(parts) != 6 {
		t.Fatalf("subject tokens = %d, want 6: %q", len(parts), subject)
	}
	if parts[0] != "tank" || parts[1] != "live" || parts[2] != "sessions" || parts[5] != "rows" {
		t.Fatalf("subject scaffolding changed: %q", subject)
	}
	if parts[3] == "" || parts[4] == "" {
		t.Fatalf("email and scope tokens must be non-empty: %q", subject)
	}
	if strings.Contains(parts[4], ".") {
		t.Fatalf("scope token must not contain '.', the NATS separator; got %q", parts[4])
	}
}
