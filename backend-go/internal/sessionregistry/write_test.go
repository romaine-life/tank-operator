package sessionregistry

import "testing"

// TestCounterDocCarriesPartitionKeyEmail pins the fix for the regression that
// broke session creation after the Python→Go rewrite: the counter document
// must carry an `email` field equal to the partition value the request header
// claims, because the profiles container is partitioned on /email. Without
// this, Cosmos rejects every write with 400 BadRequest.
func TestCounterDocCarriesPartitionKeyEmail(t *testing.T) {
	doc := buildCounterDoc("default", 60, true, "2026-05-11T14:00:00Z")

	if got, want := doc["email"], counterPartitionKey; got != want {
		t.Fatalf("email field = %q, want partition-key sentinel %q", got, want)
	}
	if got, want := doc["id"], "session-counter"; got != want {
		t.Fatalf("id = %q, want %q (Python convention)", got, want)
	}
	if got, want := doc["next_session_number"], int64(61); got != want {
		t.Fatalf("next_session_number = %v, want %v (stored value is next to allocate, not last allocated)", got, want)
	}
	if got, want := doc["type"], "session_counter"; got != want {
		t.Fatalf("type = %q, want %q", got, want)
	}
	if got, want := doc["session_scope"], "default"; got != want {
		t.Fatalf("session_scope = %q, want %q", got, want)
	}
	if _, hasCreated := doc["created_at"]; hasCreated {
		t.Fatal("created_at present on existing-doc write — should only be set on first create")
	}
}

func TestCounterDocSetsCreatedAtOnFirstCreate(t *testing.T) {
	doc := buildCounterDoc("default", 1, false, "2026-05-11T14:00:00Z")

	if got := doc["created_at"]; got != "2026-05-11T14:00:00Z" {
		t.Fatalf("created_at = %v, want timestamp on first create", got)
	}
	if got, want := doc["next_session_number"], int64(2); got != want {
		t.Fatalf("next_session_number on first create = %v, want 2 (so the returned id is 1)", got)
	}
}

func TestCounterDocIDIncludesNonDefaultScope(t *testing.T) {
	doc := buildCounterDoc("slot-a", 5, true, "2026-05-11T14:00:00Z")

	if got, want := doc["id"], "session-counter:slot-a"; got != want {
		t.Fatalf("id = %q, want %q for non-default scope", got, want)
	}
	if got, want := doc["session_scope"], "slot-a"; got != want {
		t.Fatalf("session_scope = %q, want %q", got, want)
	}
}

func TestSessionDocIDsTriesScopedThenLegacyID(t *testing.T) {
	ids := sessionDocIDs("slot-a", "12")

	if got, want := len(ids), 2; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
	if got, want := ids[0], "session:slot-a:12"; got != want {
		t.Fatalf("primary id = %q, want %q", got, want)
	}
	if got, want := ids[1], "session:12"; got != want {
		t.Fatalf("legacy id = %q, want %q", got, want)
	}
}

func TestSessionDocIDsDefaultScopeOnlyReturnsLegacyShape(t *testing.T) {
	ids := sessionDocIDs("default", "12")

	if got, want := len(ids), 1; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
	if got, want := ids[0], "session:12"; got != want {
		t.Fatalf("id = %q, want %q", got, want)
	}
}
