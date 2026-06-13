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

// runConfigRecord is a visible registry row owned by otherUser — the shape the
// run-config handler's success path needs (GetRegisteredByOwner only returns
// visible sessions, and SetRunConfig persists model/effort onto the row).
func runConfigRecord(mode string) sessionmodel.SessionRecord {
	return sessionmodel.SessionRecord{
		ID:      "71",
		Email:   otherUser,
		Scope:   prodSessionScope,
		Mode:    mode,
		Visible: true,
		Status:  "Active",
	}
}

func runConfigRequest(t *testing.T, sessionID, bodyJSON string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/sessions/"+sessionID+"/run-config",
		strings.NewReader(bodyJSON))
	req.SetPathValue("session_id", sessionID)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	return httptest.NewRecorder(), req
}

func TestHandleSetSessionRunConfig_ClaudeSwitchReturns200(t *testing.T) {
	app := registryOnlyAuthTestServer(t, runConfigRecord(sessionmodel.ClaudeGUIMode))
	res, req := runConfigRequest(t, "71", `{"model":"claude-opus-4-8","effort":"high"}`)

	app.handleSetSessionRunConfig(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", res.Code, res.Body.String())
	}
	var info sessions.Info
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Model != "claude-opus-4-8" {
		t.Fatalf("model = %q, want claude-opus-4-8", info.Model)
	}
	if info.Effort != "high" {
		t.Fatalf("effort = %q, want high", info.Effort)
	}
}

// A model-only body preserves the existing effort (the handler defaults the
// omitted field to the current desired value).
func TestHandleSetSessionRunConfig_ModelOnlyPreservesEffort(t *testing.T) {
	rec := runConfigRecord(sessionmodel.ClaudeGUIMode)
	rec.Model = "claude-haiku-4-5"
	rec.Effort = "high"
	app := registryOnlyAuthTestServer(t, rec)
	res, req := runConfigRequest(t, "71", `{"model":"claude-opus-4-8"}`)

	app.handleSetSessionRunConfig(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", res.Code, res.Body.String())
	}
	var info sessions.Info
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Model != "claude-opus-4-8" || info.Effort != "high" {
		t.Fatalf("info = {model:%q effort:%q}, want {claude-opus-4-8 high}", info.Model, info.Effort)
	}
}

func TestHandleSetSessionRunConfig_UnsupportedModelReturns400(t *testing.T) {
	app := registryOnlyAuthTestServer(t, runConfigRecord(sessionmodel.ClaudeGUIMode))
	res, req := runConfigRequest(t, "71", `{"model":"definitely-not-a-real-model"}`)

	app.handleSetSessionRunConfig(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", res.Code, res.Body.String())
	}
}

func TestHandleSetSessionRunConfig_CodexMissingModelReturns400(t *testing.T) {
	app := registryOnlyAuthTestServer(t, runConfigRecord(sessionmodel.CodexGUIMode))
	res, req := runConfigRequest(t, "71", `{"model":""}`)

	app.handleSetSessionRunConfig(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "required") {
		t.Fatalf("body = %s, want missing-model detail", res.Body.String())
	}
}

func TestHandleSetSessionRunConfig_AntigravityReturns400(t *testing.T) {
	app := registryOnlyAuthTestServer(t, runConfigRecord(sessionmodel.AntigravityGUIMode))
	res, req := runConfigRequest(t, "71", `{"model":"Gemini 3.5 Flash (Medium)"}`)

	app.handleSetSessionRunConfig(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Antigravity") {
		t.Fatalf("body = %s, want Antigravity exclusion detail", res.Body.String())
	}
}

func TestHandleSetSessionRunConfig_UnknownSessionReturns404(t *testing.T) {
	app := registryOnlyAuthTestServer(t, runConfigRecord(sessionmodel.ClaudeGUIMode))
	res, req := runConfigRequest(t, "999", `{"model":"claude-opus-4-8"}`)

	app.handleSetSessionRunConfig(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s, want 404", res.Code, res.Body.String())
	}
}
