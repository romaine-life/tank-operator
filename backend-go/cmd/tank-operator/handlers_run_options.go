package main

import (
	"net/http"

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
}

func sessionRunOptions() sessionRunOptionsResponse {
	return sessionRunOptionsResponse{
		CreateModes: []string{
			sessionmodel.APIKeyMode,
			sessionmodel.ClaudeCLIMode,
			sessionmodel.ClaudeGUIMode,
			sessionmodel.ConfigMode,
			sessionmodel.CodexCLIMode,
			sessionmodel.CodexGUIMode,
			sessionmodel.CodexConfigMode,
			sessionmodel.AntigravityConfigMode,
			sessionmodel.AntigravityGUIMode,
		},
		SDKChatModes: []sessionRunOptionMode{
			{Mode: sessionmodel.ClaudeGUIMode, Provider: "claude"},
			{Mode: sessionmodel.CodexGUIMode, Provider: "codex"},
			{Mode: sessionmodel.AntigravityGUIMode, Provider: "antigravity"},
		},
		RetiredCreateModes: map[string]string{
			sessionmodel.CodexExecGUIMode:   "use " + sessionmodel.CodexGUIMode,
			sessionmodel.CodexAppServerMode: "use " + sessionmodel.CodexGUIMode,
		},
		Models: map[string][]string{
			"claude":      allowedModelsForProvider("claude"),
			"codex":       allowedModelsForProvider("codex"),
			"antigravity": allowedModelsForProvider("antigravity"),
		},
		Efforts: map[string][]string{
			"claude":      allowedEffortsForProvider("claude"),
			"codex":       allowedEffortsForProvider("codex"),
			"antigravity": allowedEffortsForProvider("antigravity"),
		},
		DefaultModels: map[string]string{
			"claude":      "claude-opus-4-8",
			"codex":       "gpt-5.5",
			"antigravity": "Gemini 3.5 Flash (Medium)",
		},
		DefaultEfforts: map[string]string{
			"claude": "high",
			"codex":  "xhigh",
		},
	}
}

func (s *appServer) handleInternalSessionRunOptions(w http.ResponseWriter, r *http.Request) {
	if user := s.requireServicePrincipal(w, r, "GET /api/internal/session-run-options"); user == nil {
		return
	}
	writeJSON(w, http.StatusOK, sessionRunOptions())
}

func (s *appServer) handleSessionRunOptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, sessionRunOptions())
}
