package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

// postLivePreviewToggle drives handleSetLivePreviewEnabled through the shared
// test-workflow harness (session id 77, owner provisionTestOwner, no active
// slot), mirroring getTestSlotStatus.
func postLivePreviewToggle(t *testing.T, app *appServer, owner string, enabled bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/77/test-slot/live-preview", bytes.NewReader(body))
	req.SetPathValue("session_id", "77")
	if owner != "" {
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, owner, auth.RoleUser))
	}
	rec := httptest.NewRecorder()
	app.handleSetLivePreviewEnabled(rec, req)
	return rec
}

func TestSetLivePreviewEnabled_RequiresAuth(t *testing.T) {
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), &provisionFakeGitHub{}, &fakeGlimmungClient{})
	if rec := postLivePreviewToggle(t, app, "", true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 without auth", rec.Code)
	}
}

func TestSetLivePreviewEnabled_RejectsOtherOwner(t *testing.T) {
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), &provisionFakeGitHub{}, &fakeGlimmungClient{})
	if rec := postLivePreviewToggle(t, app, otherUser, true); rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 for another owner's session", rec.Code)
	}
}

// Enabling live preview without a running slot (with a URL) is refused: the lane
// streams scratch on top of a real slot, so there is nothing to preview against.
func TestSetLivePreviewEnabled_EnableRequiresActiveSlot(t *testing.T) {
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), &provisionFakeGitHub{}, &fakeGlimmungClient{})
	if rec := postLivePreviewToggle(t, app, provisionTestOwner, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 enabling preview with no active slot", rec.Code)
	}
}
