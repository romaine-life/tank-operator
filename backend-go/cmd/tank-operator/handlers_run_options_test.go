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
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func TestSessionRunOptionsExposeTankOwnedCreateAndRunConfig(t *testing.T) {
	opts := sessionRunOptions()
	if !slices.Contains(opts.CreateModes, sessionmodel.CodexGUIMode) {
		t.Fatalf("create_modes = %v, want codex_gui", opts.CreateModes)
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
	if !slices.Contains(codexModels, "gpt-5.5") ||
		!slices.Contains(codexModels, "gpt-5.3-codex-spark") ||
		slices.Contains(codexModels, "gpt-5.3-codex") {
		t.Fatalf("codex models = %v", codexModels)
	}
	if !slices.Contains(opts.Efforts["codex"], "xhigh") ||
		slices.Contains(opts.Efforts["codex"], "max") {
		t.Fatalf("codex efforts = %v", opts.Efforts["codex"])
	}
	if opts.DefaultModels["claude"] != "claude-opus-4-8" || opts.DefaultEfforts["claude"] != "high" {
		t.Fatalf("claude defaults = model %q effort %q", opts.DefaultModels["claude"], opts.DefaultEfforts["claude"])
	}
	if opts.DefaultModels["codex"] != "gpt-5.5" || opts.DefaultEfforts["codex"] != "xhigh" {
		t.Fatalf("codex defaults = model %q effort %q", opts.DefaultModels["codex"], opts.DefaultEfforts["codex"])
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
}
