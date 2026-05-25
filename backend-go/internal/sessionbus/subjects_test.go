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

func TestStorageScopeAndSessionID(t *testing.T) {
	scope, sessionID := StorageScopeAndSessionID("tank-operator-slot-3:17")
	if scope != "tank-operator-slot-3" || sessionID != "17" {
		t.Fatalf("scoped storage key parsed as (%q, %q), want (tank-operator-slot-3, 17)", scope, sessionID)
	}

	scope, sessionID = StorageScopeAndSessionID("17")
	if scope != "default" || sessionID != "17" {
		t.Fatalf("default storage key parsed as (%q, %q), want (default, 17)", scope, sessionID)
	}
}

func TestSessionBusSubjectsIncludeScopeAndSessionTokens(t *testing.T) {
	prod := SessionEventSubject("17")
	slot := SessionEventSubject("tank-operator-slot-3:17")

	if prod == slot {
		t.Fatalf("event subjects must differ across scopes; got %q for both", prod)
	}
	if prod != "tank.session.ZGVmYXVsdA.MTc.events" {
		t.Fatalf("default event subject = %q, want scoped token shape", prod)
	}
	if slot != "tank.session.dGFuay1vcGVyYXRvci1zbG90LTM.MTc.events" {
		t.Fatalf("slot event subject = %q, want scoped token shape", slot)
	}

	legacySlotSubject := "tank.session." + StorageToken("tank-operator-slot-3:17") + ".events"
	if slot == legacySlotSubject {
		t.Fatalf("event subject must not use legacy storage-token-only shape %q", legacySlotSubject)
	}
}

func TestEventPersisterConsumerIsScopePartitioned(t *testing.T) {
	prodFilter := EventSubjectFilter("default")
	slotFilter := EventSubjectFilter("tank-operator-slot-3")
	if prodFilter == slotFilter {
		t.Fatalf("persister filters must differ across scopes; got %q for both", prodFilter)
	}
	if prodFilter == subjectRoot+".*.events" || slotFilter == subjectRoot+".*.events" {
		t.Fatalf("persister filter must not use legacy broad filter; got prod=%q slot=%q", prodFilter, slotFilter)
	}
	if slotFilter != "tank.session.dGFuay1vcGVyYXRvci1zbG90LTM.*.events" {
		t.Fatalf("slot persister filter = %q, want scope-token wildcard", slotFilter)
	}

	prodConsumer := EventPersisterConsumerName("default")
	slotConsumer := EventPersisterConsumerName("tank-operator-slot-3")
	if prodConsumer == slotConsumer {
		t.Fatalf("persister durable names must differ across scopes; got %q for both", prodConsumer)
	}
	if prodConsumer == "tank-session-event-persister" || slotConsumer == "tank-session-event-persister" {
		t.Fatalf("persister durable name must not use legacy shared consumer name")
	}
}
