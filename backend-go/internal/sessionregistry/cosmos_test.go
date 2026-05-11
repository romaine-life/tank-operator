package sessionregistry

import (
	"testing"
)

func TestSessionFromDocMatchesPythonShape(t *testing.T) {
	name := "Workbench"
	record, err := sessionFromDoc([]byte(`{
		"id": "session:12",
		"type": "session",
		"email": "USER@example.COM",
		"session_scope": "default",
		"session_id": "12",
		"mode": "codex_headless",
		"pod_name": "session-12",
		"name": "Workbench",
		"visible": true,
		"requested_at": "2026-05-11T00:00:00+00:00",
		"created_at": "2026-05-11T00:00:01+00:00",
		"updated_at": "2026-05-11T00:00:02+00:00"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "12" || record.Email != "USER@example.COM" || record.Mode != "codex_headless" {
		t.Fatalf("record identity = %#v", record)
	}
	if record.Name == nil || *record.Name != name {
		t.Fatalf("name = %#v, want %q", record.Name, name)
	}
	if record.Scope != "default" || record.PodName != "session-12" {
		t.Fatalf("record placement = %#v", record)
	}
	if !record.Visible {
		t.Fatal("visible = false, want true")
	}
}

func TestSessionFromDocFallsBackForLegacyShape(t *testing.T) {
	record, err := sessionFromDoc([]byte(`{
		"id": "session:slot-a:12",
		"email": "user@example.com",
		"scope": "slot-a"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "slot-a:12" {
		t.Fatalf("id = %q, want Python removeprefix fallback", record.ID)
	}
	if record.Scope != "slot-a" {
		t.Fatalf("scope = %q, want slot-a", record.Scope)
	}
	if record.Mode != "claude_cli" {
		t.Fatalf("mode = %q, want claude_cli", record.Mode)
	}
}

func TestCosmosQueriesMatchRegistryScope(t *testing.T) {
	defaultStore := &CosmosStore{scope: "default"}
	defaultQuery, defaultParams := defaultStore.query("user@example.com")
	if defaultQuery == "" || len(defaultParams) != 1 || defaultParams[0].Value != "user@example.com" {
		t.Fatalf("default query = %q params=%#v", defaultQuery, defaultParams)
	}
	if want := "default"; contains(defaultQuery, "c.session_scope = @scope") {
		t.Fatalf("%s query unexpectedly requires scope: %s", want, defaultQuery)
	}

	slotStore := &CosmosStore{scope: "slot-a"}
	slotQuery, slotParams := slotStore.query("user@example.com")
	if !contains(slotQuery, "c.session_scope = @scope") {
		t.Fatalf("slot query missing scope predicate: %s", slotQuery)
	}
	if len(slotParams) != 2 || slotParams[1].Value != "slot-a" {
		t.Fatalf("slot params = %#v", slotParams)
	}
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
