package main

import "testing"

func TestTranscriptBackfillScopesIncludesProdForTestSlots(t *testing.T) {
	scopes := transcriptBackfillScopeNames("tank-operator-slot-3", true)
	if len(scopes) != 2 || scopes[0] != "tank-operator-slot-3" || scopes[1] != "default" {
		t.Fatalf("test slot scopes = %#v, want local then default", scopes)
	}

	scopes = transcriptBackfillScopeNames("default", true)
	if len(scopes) != 1 || scopes[0] != "default" {
		t.Fatalf("prod scopes = %#v, want default only", scopes)
	}

	scopes = transcriptBackfillScopeNames("tank-operator-slot-3", false)
	if len(scopes) != 1 || scopes[0] != "tank-operator-slot-3" {
		t.Fatalf("without postgres scopes = %#v, want local only", scopes)
	}
}
