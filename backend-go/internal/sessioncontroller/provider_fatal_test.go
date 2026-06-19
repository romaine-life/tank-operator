package sessioncontroller

import "testing"

// TestProviderFatalDerivesFailedStatus pins the contract that a runner's
// provider-fatal report moves the session row to the same terminal Failed
// status pod death produces.
func TestProviderFatalDerivesFailedStatus(t *testing.T) {
	changes, ok := deriveRowColumnChanges(Event{Type: EventTypeProviderFatal})
	if !ok {
		t.Fatal("EventTypeProviderFatal must have a row-column effect")
	}
	if changes.status != "Failed" {
		t.Fatalf("status = %q, want Failed", changes.status)
	}
}
