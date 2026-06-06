package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// openTargetVisibleRecord is a visible registry row owned by otherUser, the
// shape the open-target handler's 200 path needs: SetOpenTarget persists to the
// row and GetRegisteredByOwner only returns visible sessions.
func openTargetVisibleRecord() sessionmodel.SessionRecord {
	return sessionmodel.SessionRecord{
		ID:      "71",
		Email:   otherUser,
		Scope:   prodSessionScope,
		Mode:    sessionmodel.ClaudeGUIMode,
		Visible: true,
		Status:  "Active",
	}
}

func TestHandleSetOpenTarget_ValidTurnsReturns200(t *testing.T) {
	app := registryOnlyAuthTestServer(t, openTargetVisibleRecord())
	req := httptest.NewRequest(http.MethodPut, "/api/sessions/71/open-target",
		strings.NewReader(`{"open_target":"turns"}`))
	req.SetPathValue("session_id", "71")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	res := httptest.NewRecorder()

	app.handleSetOpenTarget(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", res.Code, res.Body.String())
	}
	var info sessions.Info
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.ID != "71" || info.Owner != otherUser {
		t.Fatalf("info = %+v, want id=71 owner=%s", info, otherUser)
	}
	if info.OpenTarget != "turns" {
		t.Fatalf("open_target = %q, want turns", info.OpenTarget)
	}
}

func TestHandleSetOpenTarget_InvalidValueReturns400(t *testing.T) {
	app := registryOnlyAuthTestServer(t, openTargetVisibleRecord())
	req := httptest.NewRequest(http.MethodPut, "/api/sessions/71/open-target",
		strings.NewReader(`{"open_target":"sidebar"}`))
	req.SetPathValue("session_id", "71")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	res := httptest.NewRecorder()

	app.handleSetOpenTarget(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "invalid open_target") {
		t.Fatalf("body = %s, want invalid open_target detail", res.Body.String())
	}
}
