package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func TestSessionRunOptionsExposeTankOwnedCreateAndRunConfig(t *testing.T) {
	opts := sessionRunOptions()
	if !slices.Contains(opts.CreateModes, sessionmodel.CodexGUIMode) {
		t.Fatalf("create_modes = %v, want codex_gui", opts.CreateModes)
	}
	if !slices.Contains(opts.CreateModes, sessionmodel.ClaudeSecondaryGUIMode) ||
		!slices.Contains(opts.CreateModes, sessionmodel.ClaudeSecondaryConfigMode) {
		t.Fatalf("create_modes = %v, want claude secondary modes", opts.CreateModes)
	}
	if !slices.Contains(opts.SDKChatModes, sessionRunOptionMode{Mode: sessionmodel.ClaudeSecondaryGUIMode, Provider: "claude"}) {
		t.Fatalf("sdk_chat_modes = %v, want claude secondary gui as claude provider", opts.SDKChatModes)
	}
	if slices.Contains(opts.CreateModes, sessionmodel.CodexExecGUIMode) ||
		slices.Contains(opts.CreateModes, sessionmodel.CodexAppServerMode) {
		t.Fatalf("create_modes = %v, want retired Codex GUI modes excluded", opts.CreateModes)
	}
	if opts.RetiredCreateModes[sessionmodel.CodexExecGUIMode] != "use codex_gui" ||
		opts.RetiredCreateModes[sessionmodel.CodexAppServerMode] != "use codex_gui" {
		t.Fatalf("retired_create_modes = %#v", opts.RetiredCreateModes)
	}
	codexModels := opts.Models["codex"]
	wantCodexModels := []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"}
	if !slices.Equal(codexModels, wantCodexModels) {
		t.Fatalf("codex models = %v, want %v", codexModels, wantCodexModels)
	}
	if slices.Contains(codexModels, "gpt-5.3-codex") {
		t.Fatalf("codex models = %v", codexModels)
	}
	if want := []string{"claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5", "claude-fable-5"}; !slices.Equal(opts.Models["claude"], want) {
		t.Fatalf("claude models = %v, want %v", opts.Models["claude"], want)
	}
	if want := []string{"Gemini 3.1 Pro", "Gemini 3.5 Flash (Medium)"}; !slices.Equal(opts.Models["antigravity"], want) {
		t.Fatalf("antigravity models = %v, want %v", opts.Models["antigravity"], want)
	}
	if want := []string{"low", "medium", "high", "xhigh"}; !slices.Equal(opts.Efforts["codex"], want) {
		t.Fatalf("codex efforts = %v, want %v", opts.Efforts["codex"], want)
	}
	if want := []string{"low", "medium", "high", "xhigh", "max"}; !slices.Equal(opts.Efforts["claude"], want) {
		t.Fatalf("claude efforts = %v, want %v", opts.Efforts["claude"], want)
	}
	if opts.DefaultModels["claude"] != "claude-opus-4-8" || opts.DefaultEfforts["claude"] != "high" {
		t.Fatalf("claude defaults = model %q effort %q", opts.DefaultModels["claude"], opts.DefaultEfforts["claude"])
	}
	if opts.DefaultModels["codex"] != "gpt-5.5" || opts.DefaultEfforts["codex"] != "xhigh" {
		t.Fatalf("codex defaults = model %q effort %q", opts.DefaultModels["codex"], opts.DefaultEfforts["codex"])
	}
	if opts.DefaultModels["antigravity"] != "Gemini 3.5 Flash (Medium)" {
		t.Fatalf("antigravity default model = %q", opts.DefaultModels["antigravity"])
	}
	if opts.TestSlotDefaults.Mode != sessionmodel.ClaudeGUIMode || opts.TestSlotDefaults.Model != "" || opts.TestSlotDefaults.Effort != "" {
		t.Fatalf("test slot defaults = %#v, want bare claude_gui", opts.TestSlotDefaults)
	}
}

func TestSessionRunOptionsExposeConfiguredTestSlotDefaults(t *testing.T) {
	opts := sessionRunOptions(testSlotDefaults{Mode: sessionmodel.CodexGUIMode, Model: "gpt-5.4-mini", Effort: "low"})
	if opts.TestSlotDefaults.Mode != sessionmodel.CodexGUIMode ||
		opts.TestSlotDefaults.Model != "gpt-5.4-mini" ||
		opts.TestSlotDefaults.Effort != "low" {
		t.Fatalf("test slot defaults = %#v", opts.TestSlotDefaults)
	}
}

func TestHandleInternalSessionRunOptions(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	server := &appServer{
		verifier: auth.NewVerifier(jwtKey),
	}
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:session-x",
		"email":       "pod-session-x@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/internal/session-run-options", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	server.handleInternalSessionRunOptions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body sessionRunOptionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(body.CreateModes, sessionmodel.CodexGUIMode) {
		t.Fatalf("body = %#v, want codex_gui create mode", body)
	}
	if strings.Contains(rec.Body.String(), `"gpt-5.3-codex"`) {
		t.Fatalf("body = %s, must not advertise unsupported bare gpt-5.3-codex", rec.Body.String())
	}
}

func TestHandleSessionRunOptionsRequiresUserAuth(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{})
	app.platformSettings = &fakePlatformSettingsStore{
		defaults: pgstore.TestSlotSessionDefaults{Mode: sessionmodel.CodexGUIMode, Model: "gpt-5.4-mini", Effort: "low"},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/session-run-options", nil)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	rec := httptest.NewRecorder()

	app.handleSessionRunOptions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body sessionRunOptionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(body.CreateModes, sessionmodel.CodexGUIMode) {
		t.Fatalf("body = %#v, want codex_gui create mode", body)
	}
	if body.TestSlotDefaults.Mode != sessionmodel.CodexGUIMode || body.TestSlotDefaults.Model != "gpt-5.4-mini" || body.TestSlotDefaults.Effort != "low" {
		t.Fatalf("test slot defaults = %#v", body.TestSlotDefaults)
	}
}

func TestHandleAdminSetTestSlotSessionDefaults(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{})
	store := &fakePlatformSettingsStore{}
	app.platformSettings = store
	req := httptest.NewRequest(http.MethodPut, "/api/admin/test-slot-session-defaults", strings.NewReader(`{
		"mode":"codex_gui",
		"model":"gpt-5.4-mini",
		"effort":"low"
	}`))
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleAdminSetTestSlotSessionDefaults(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if store.defaults.Mode != sessionmodel.CodexGUIMode || store.defaults.Model != "gpt-5.4-mini" || store.defaults.Effort != "low" {
		t.Fatalf("stored defaults = %#v", store.defaults)
	}
}

func TestHandleAdminSetTestSlotSessionDefaultsRejectsInvalidRunConfig(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{})
	app.platformSettings = &fakePlatformSettingsStore{}
	req := httptest.NewRequest(http.MethodPut, "/api/admin/test-slot-session-defaults", strings.NewReader(`{
		"mode":"codex_gui",
		"model":"claude-opus-4-8"
	}`))
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleAdminSetTestSlotSessionDefaults(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "model is not available for codex") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

type fakePlatformSettingsStore struct {
	defaults pgstore.TestSlotSessionDefaults
	missing  bool
}

func (s *fakePlatformSettingsStore) GetTestSlotSessionDefaults(context.Context) (pgstore.TestSlotSessionDefaults, error) {
	if s.missing {
		return pgstore.TestSlotSessionDefaults{}, pgstore.ErrPlatformSettingNotFound
	}
	return s.defaults, nil
}

func (s *fakePlatformSettingsStore) UpsertTestSlotSessionDefaults(_ context.Context, defaults pgstore.TestSlotSessionDefaults, updatedBy string) (pgstore.TestSlotSessionDefaults, error) {
	defaults.UpdatedBy = updatedBy
	defaults.UpdatedAt = "2026-06-12T10:00:00.000000Z"
	s.defaults = defaults
	return defaults, nil
}
