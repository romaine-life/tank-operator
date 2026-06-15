package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type sessionRunOptionMode struct {
	Mode     string `json:"mode"`
	Provider string `json:"provider"`
}

type sessionRunOptionsResponse struct {
	CreateModes        []string               `json:"create_modes"`
	SDKChatModes       []sessionRunOptionMode `json:"sdk_chat_modes"`
	RetiredCreateModes map[string]string      `json:"retired_create_modes"`
	Models             map[string][]string    `json:"models"`
	Efforts            map[string][]string    `json:"efforts"`
	DefaultModels      map[string]string      `json:"default_models"`
	DefaultEfforts     map[string]string      `json:"default_efforts"`
	TestSlotDefaults   testSlotDefaults       `json:"test_slot_defaults"`
}

type testSlotDefaults struct {
	Mode      string `json:"mode"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func defaultTestSlotSessionDefaults() testSlotDefaults {
	return testSlotDefaults{
		Mode:   sessionmodel.DefaultSessionMode,
		Model:  lowCostModelForProvider("claude"),
		Effort: lowCostEffortForProvider("claude"),
	}
}

func sessionRunOptions(defaults ...testSlotDefaults) sessionRunOptionsResponse {
	testSlot := defaultTestSlotSessionDefaults()
	if len(defaults) > 0 {
		testSlot = normalizeTestSlotDefaults(defaults[0])
	}
	return sessionRunOptionsResponse{
		CreateModes: []string{
			sessionmodel.APIKeyMode,
			sessionmodel.ClaudeCLIMode,
			sessionmodel.ClaudeGUIMode,
			sessionmodel.ConfigMode,
			sessionmodel.ClaudeSecondaryCLIMode,
			sessionmodel.ClaudeSecondaryGUIMode,
			sessionmodel.ClaudeSecondaryConfigMode,
			sessionmodel.CodexCLIMode,
			sessionmodel.CodexGUIMode,
			sessionmodel.CodexConfigMode,
		},
		SDKChatModes: []sessionRunOptionMode{
			{Mode: sessionmodel.ClaudeGUIMode, Provider: "claude"},
			{Mode: sessionmodel.ClaudeSecondaryGUIMode, Provider: "claude"},
			{Mode: sessionmodel.CodexGUIMode, Provider: "codex"},
		},
		RetiredCreateModes: map[string]string{
			sessionmodel.CodexExecGUIMode:   "use " + sessionmodel.CodexGUIMode,
			sessionmodel.CodexAppServerMode: "use " + sessionmodel.CodexGUIMode,
		},
		Models: map[string][]string{
			"claude": allowedModelsForProvider("claude"),
			"codex":  allowedModelsForProvider("codex"),
		},
		Efforts: map[string][]string{
			"claude": allowedEffortsForProvider("claude"),
			"codex":  allowedEffortsForProvider("codex"),
		},
		DefaultModels: map[string]string{
			"claude": "claude-opus-4-8",
			"codex":  "gpt-5.5",
		},
		DefaultEfforts: map[string]string{
			"claude": "high",
			"codex":  "xhigh",
		},
		TestSlotDefaults: testSlot,
	}
}

func (s *appServer) handleInternalSessionRunOptions(w http.ResponseWriter, r *http.Request) {
	if user := s.requireServicePrincipal(w, r, "GET /api/internal/session-run-options"); user == nil {
		return
	}
	defaults, err := s.effectiveTestSlotSessionDefaults(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionRunOptions(defaults))
}

func (s *appServer) handleSessionRunOptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	defaults, err := s.effectiveTestSlotSessionDefaults(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionRunOptions(defaults))
}

func (s *appServer) handleAdminGetTestSlotSessionDefaults(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	defaults, err := s.effectiveTestSlotSessionDefaults(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (s *appServer) handleAdminSetTestSlotSessionDefaults(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	if s.platformSettings == nil {
		writeError(w, http.StatusServiceUnavailable, "platform settings store not configured")
		return
	}
	var body testSlotDefaults
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	defaults, status, detail := validateTestSlotDefaults(body)
	if status != 0 {
		writeError(w, status, detail)
		return
	}
	updated, err := s.platformSettings.UpsertTestSlotSessionDefaults(r.Context(), pgstore.TestSlotSessionDefaults{
		Mode:   defaults.Mode,
		Model:  defaults.Model,
		Effort: defaults.Effort,
	}, user.OwnerEmail())
	if errors.Is(err, pgstore.ErrPlatformSettingsUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "platform settings table unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, testSlotDefaultsFromStore(updated))
}

func (s *appServer) effectiveTestSlotSessionDefaults(ctx context.Context) (testSlotDefaults, error) {
	if s.platformSettings == nil {
		return defaultTestSlotSessionDefaults(), nil
	}
	stored, err := s.platformSettings.GetTestSlotSessionDefaults(ctx)
	if errors.Is(err, pgstore.ErrPlatformSettingNotFound) ||
		errors.Is(err, pgstore.ErrPlatformSettingsUnavailable) {
		return defaultTestSlotSessionDefaults(), nil
	}
	if err != nil {
		return testSlotDefaults{}, err
	}
	defaults, status, detail := validateTestSlotDefaults(testSlotDefaultsFromStore(stored))
	if status != 0 {
		return testSlotDefaults{}, errors.New("stored test-slot session defaults invalid: " + detail)
	}
	return defaults, nil
}

func testSlotDefaultsFromStore(stored pgstore.TestSlotSessionDefaults) testSlotDefaults {
	return testSlotDefaults{
		Mode:      stored.Mode,
		Model:     stored.Model,
		Effort:    stored.Effort,
		UpdatedBy: stored.UpdatedBy,
		UpdatedAt: stored.UpdatedAt,
	}
}

func validateTestSlotDefaults(raw testSlotDefaults) (testSlotDefaults, int, string) {
	defaults := normalizeTestSlotDefaults(raw)
	mode := sessionmodel.NormalizeSessionMode(defaults.Mode)
	if !sessionmodel.IsSessionMode(mode) {
		return testSlotDefaults{}, http.StatusBadRequest, "session mode is invalid"
	}
	if mode == sessionmodel.CodexExecGUIMode || mode == sessionmodel.CodexAppServerMode {
		return testSlotDefaults{}, http.StatusBadRequest, "session mode " + mode + " is retired; use codex_gui"
	}
	provider, ok := sdkProviderForMode(mode)
	if !ok {
		return testSlotDefaults{}, http.StatusBadRequest, "test-slot defaults must use an SDK chat mode"
	}
	if provider == "claude" && defaults.Model == "" && defaults.Effort == "" {
		return testSlotDefaults{Mode: mode, UpdatedAt: defaults.UpdatedAt, UpdatedBy: defaults.UpdatedBy}, 0, ""
	}
	if isDefaultModelAlias(defaults.Model) {
		return testSlotDefaults{}, http.StatusBadRequest, "model must be explicit; default is not accepted"
	}
	if providerRequiresExplicitModel(provider) && defaults.Model == "" {
		return testSlotDefaults{}, http.StatusBadRequest, explicitModelRequiredMessage(provider, "sessions")
	}
	model := validateModelArg(provider, defaults.Model)
	if defaults.Model != "" && model == "" {
		return testSlotDefaults{}, http.StatusBadRequest, modelUnsupportedMessage(provider)
	}
	effort := validateEffort(provider, defaults.Effort)
	if defaults.Effort != "" && effort == "" {
		return testSlotDefaults{}, http.StatusBadRequest, effortUnsupportedMessage(provider, "sessions")
	}
	return testSlotDefaults{
		Mode:      mode,
		Model:     model,
		Effort:    effort,
		UpdatedAt: defaults.UpdatedAt,
		UpdatedBy: defaults.UpdatedBy,
	}, 0, ""
}

func normalizeTestSlotDefaults(raw testSlotDefaults) testSlotDefaults {
	mode := strings.TrimSpace(raw.Mode)
	if mode == "" {
		mode = sessionmodel.DefaultSessionMode
	}
	return testSlotDefaults{
		Mode:      mode,
		Model:     strings.TrimSpace(raw.Model),
		Effort:    strings.TrimSpace(raw.Effort),
		UpdatedBy: strings.TrimSpace(raw.UpdatedBy),
		UpdatedAt: strings.TrimSpace(raw.UpdatedAt),
	}
}
