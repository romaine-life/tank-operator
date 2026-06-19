package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// orchestrateSelfGrantSource is the audit marker stamped on the break-glass
// grant payload so the durable control-action ledger distinguishes the
// orchestrate hub's no-approval self-grant from a normal admin approval.
const orchestrateSelfGrantSource = "orchestrate-self-grant"

// orchestrateSelfGrantTTLSeconds is the fixed 24h ceiling for the hub's
// self-granted git authority. A run longer than this needs a human re-confirm
// (re-POST), matching the break-glass design's hard 24h cap — orchestrate does
// not invent a renewal model.
const orchestrateSelfGrantTTLSeconds = 24 * 3600

// handleOrchestrateLaunch turns the caller's own GUI chat session into the hub
// of a spoke fleet. On confirm it (1) validates the spoke config against the
// same run-options allowlists as session create, (2) persists it to the durable
// spoke_config column (which flips the Orchestrate surface from form to status
// and rides the snapshot/SSE), (3) self-grants full, all-repos, 24h git
// break-glass with an audit marker, and (4) enqueues the /orchestrate kickoff
// turn carrying the spoke config, the hub's own id (for ping-backs), and the
// break-glass status.
//
// Gating: human session OWNER only. Service principals are rejected outright
// (this is a human-initiated, full-power git grant, not a service path), and
// ownership is the write-class per-owner gate via GetByOwner — it is NOT
// admin-liftable, so an admin cannot launch orchestrate on someone else's
// session. The hub must itself be an SDK chat (GUI) session so spoke ping-backs
// can wake it as a new turn.
func (s *appServer) handleOrchestrateLaunch(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if user.IsService() {
		recordOrchestrateLaunch("service_rejected")
		writeError(w, http.StatusForbidden, "orchestrate requires a human session owner")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if s.mgr == nil || s.controlActions == nil || s.sessionBus == nil {
		recordOrchestrateLaunch("store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "orchestrate launch unavailable")
		return
	}

	owner := user.OwnerEmail()
	info, err := s.mgr.GetByOwner(r.Context(), owner, sessionID)
	if err != nil {
		// Not-owner and not-found collapse to 404 so a caller can't probe
		// another user's session ids — same masking the turn path uses.
		recordOrchestrateLaunch("not_owner")
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	hubProvider, isHub := sdkProviderForMode(info.Mode)
	if !isHub {
		recordOrchestrateLaunch("not_hub_mode")
		writeError(w, http.StatusBadRequest, "orchestrate hub must be a GUI chat session")
		return
	}

	var body struct {
		Provider string `json:"provider"`
		Surface  string `json:"surface"`
		Model    string `json:"model"`
		Effort   string `json:"effort"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlActionPayloadBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	spokeConfig, status, detail := validateSpokeConfig(body.Provider, body.Surface, body.Model, body.Effort)
	if status != 0 {
		recordOrchestrateLaunch("invalid_spoke_config")
		writeError(w, status, detail)
		return
	}
	spokeConfig["configured_by"] = owner
	spokeConfig["configured_at"] = time.Now().UTC().Format(time.RFC3339)

	// 1. Persist the spoke config — the durable "this session is now a hub"
	// flag. SetSpokeConfig bumps row_version so the snapshot/SSE re-read flips
	// the Orchestrate surface from form to status.
	if _, err := s.mgr.SetSpokeConfig(r.Context(), owner, sessionID, spokeConfig); err != nil {
		recordOrchestrateLaunch("store_error")
		writeError(w, http.StatusInternalServerError, "persist spoke config: "+err.Error())
		return
	}

	// 2. Self-grant full, all-repos, 24h git break-glass with the audit marker.
	// Reuses the canonical grant writer; no approval round-trip.
	grant, expiresAt, err := s.appendGitBreakGlassGrant(r.Context(), gitBreakGlassGrantInput{
		SessionID:   sessionID,
		OwnerEmail:  owner,
		RepoScope:   repoScope{Kind: "all_repos"},
		BranchScope: branchScope{Kind: "unlimited"},
		TTLSeconds:  orchestrateSelfGrantTTLSeconds,
		Operations: []string{
			gitBreakGlassOpMintToken,
			gitBreakGlassOpPushHead,
			gitBreakGlassOpWorkflows,
			gitBreakGlassOpFullAPI,
		},
		Reason:     "orchestrate hub self-grant",
		ApprovedBy: owner,
		Source:     orchestrateSelfGrantSource,
	})
	if err != nil {
		recordControlActionEvent("tank-operator", "git_break_glass_approval", "github.break_glass.grant", "succeeded", "store_error")
		recordOrchestrateLaunch("grant_error")
		writeError(w, http.StatusInternalServerError, "self-grant break-glass: "+err.Error())
		return
	}
	recordControlActionEvent(grant.SourceService, grant.SourceTool, grant.Action, grant.Status, "ok")

	// 3. Enqueue the /orchestrate kickoff turn. Routes through the exact same
	// enqueueSDKTurn path a spoke's ping-back will use, so the hub is woken the
	// same way it later wakes on spoke reports.
	prompt := orchestrateKickoffPrompt(hubProvider, sessionID, spokeConfig, expiresAt)
	resp, status, detail := s.enqueueSDKTurn(r.Context(), owner, sessionID, sdkTurnRequest{
		ClientNonce:  orchestrateLaunchTurnNonce(sessionID + ":" + grant.EventID),
		RequireNonce: true,
		Prompt:       prompt,
		DisplayText:  orchestrateKickoffDisplayText(spokeConfig),
		SkillName:    "orchestrate",
		Source:       string(conversation.TurnSubmittedSourceOrchestrateLaunch),
		AuthorKind:   authorKindForUser(user),
		CreatedAt:    time.Now().UTC(),
	})
	if detail != "" {
		recordOrchestrateLaunch("kickoff_error")
		slog.Warn("orchestrate spoke config persisted and break-glass granted but kickoff turn failed",
			"session_id", sessionID, "grant_event_id", grant.EventID, "status", status, "detail", detail)
		writeError(w, status, "orchestrate hub configured but kickoff turn failed: "+strings.TrimSpace(detail))
		return
	}

	recordOrchestrateLaunch("ok")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"active":             true,
		"session_id":         sessionID,
		"spoke_config":       spokeConfig,
		"break_glass":        map[string]any{"active": true, "all_repos": true, "expires_at": expiresAt.Format(time.RFC3339)},
		"kickoff_turn":       turnIDFromEnqueueResponse(resp),
		"grant_event_id":     grant.EventID,
		"orchestrate_source": orchestrateSelfGrantSource,
	})
}

// validateSpokeConfig validates the launch form's provider/surface/model/effort
// against the same allowlists session create uses, deriving the concrete spoke
// session mode from provider+surface. Returns the normalized spoke_config map
// to persist, or (nil, status, detail) on rejection. Run-config rejections also
// ride tank_session_run_config_rejected_total{surface="orchestrate"} so the
// allowlist contract is observable from the orchestrate surface too.
func validateSpokeConfig(rawProvider, rawSurface, rawModel, rawEffort string) (map[string]any, int, string) {
	provider := strings.ToLower(strings.TrimSpace(rawProvider))
	switch provider {
	case "claude", "codex":
	default:
		recordSessionRunConfigRejected("orchestrate", "unknown", "unsupported_provider")
		return nil, http.StatusBadRequest, "provider must be claude or codex"
	}

	surface := strings.ToLower(strings.TrimSpace(rawSurface))
	if surface == "" {
		surface = "gui"
	}
	mode, ok := spokeModeFor(provider, surface)
	if !ok {
		recordSessionRunConfigRejected("orchestrate", provider, "invalid_mode")
		return nil, http.StatusBadRequest, "surface must be gui or cli"
	}
	if !sessionmodel.IsSessionMode(mode) {
		recordSessionRunConfigRejected("orchestrate", provider, "invalid_mode")
		return nil, http.StatusBadRequest, "session mode is invalid"
	}

	modelInput := strings.TrimSpace(rawModel)
	effortInput := strings.TrimSpace(rawEffort)
	if isDefaultModelAlias(modelInput) {
		recordSessionRunConfigRejected("orchestrate", provider, "default_model")
		return nil, http.StatusBadRequest, "model must be explicit; default is not accepted"
	}
	if providerRequiresExplicitModel(provider) && modelInput == "" {
		recordSessionRunConfigRejected("orchestrate", provider, "missing_model")
		return nil, http.StatusBadRequest, explicitModelRequiredMessage(provider, "spokes")
	}
	model := validateModelArg(provider, modelInput)
	if modelInput != "" && model == "" {
		recordSessionRunConfigRejected("orchestrate", provider, "unsupported_model")
		return nil, http.StatusBadRequest, modelUnsupportedMessage(provider)
	}
	effort := validateEffort(provider, effortInput)
	if effortInput != "" && effort == "" {
		recordSessionRunConfigRejected("orchestrate", provider, "unsupported_effort")
		return nil, http.StatusBadRequest, effortUnsupportedMessage(provider, "spokes")
	}

	return map[string]any{
		"provider": provider,
		"surface":  surface,
		"mode":     mode,
		"model":    model,
		"effort":   effort,
	}, 0, ""
}

// spokeModeFor maps the form's provider+surface choice to a concrete session
// mode the hub passes to spawn_run_session. GUI spokes are the first-class path
// (they ride the SDK turn channel for ping-backs); CLI spokes are supported but
// the hub falls back to polling them, per the skill.
func spokeModeFor(provider, surface string) (string, bool) {
	switch provider {
	case "claude":
		switch surface {
		case "gui":
			return sessionmodel.ClaudeGUIMode, true
		case "cli":
			return sessionmodel.ClaudeCLIMode, true
		}
	case "codex":
		switch surface {
		case "gui":
			return sessionmodel.CodexGUIMode, true
		case "cli":
			return sessionmodel.CodexCLIMode, true
		}
	}
	return "", false
}

// orchestrateLaunchTurnNonce derives a deterministic, turn-id-syntax-compliant
// nonce from the launch seed so a re-confirm produces a distinct kickoff turn
// (the seed includes the fresh grant event id).
func orchestrateLaunchTurnNonce(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		seed = randomHex(12)
	}
	sum := sha256.Sum256([]byte(seed))
	return "turn_orchestrate_launch_" + hex.EncodeToString(sum[:12])
}

func spokeConfigString(config map[string]any, key string) string {
	v, _ := config[key].(string)
	return v
}

// orchestrateKickoffPrompt is the prompt body the /orchestrate kickoff turn
// carries into the hub. It MUST begin with the provider-specific skill trigger
// (`/orchestrate` for Claude, `$orchestrate` for Codex) so enqueueSDKTurn's
// skill_name↔prompt-trigger guard accepts it and the SKILL.md loads. After the
// trigger it embeds the spoke config (for spawn_run_session), the hub's own id
// (so spokes can send_prompt their ping-backs back here), and the break-glass
// status — the three things the skill needs that aren't in the SKILL.md itself.
func orchestrateKickoffPrompt(hubProvider, sessionID string, config map[string]any, expiresAt time.Time) string {
	model := spokeConfigString(config, "model")
	if model == "" {
		model = "(provider default)"
	}
	effort := spokeConfigString(config, "effort")
	if effort == "" {
		effort = "(provider default)"
	}
	var b strings.Builder
	b.WriteString(skillPromptTrigger(hubProvider, "orchestrate"))
	b.WriteString("\n\nYou are now the orchestration hub for this session.\n\n")
	b.WriteString("Spoke fleet config — use this for every spoke you spawn with spawn_run_session:\n")
	b.WriteString(fmt.Sprintf("- provider: %s\n", spokeConfigString(config, "provider")))
	b.WriteString(fmt.Sprintf("- surface: %s\n", spokeConfigString(config, "surface")))
	b.WriteString(fmt.Sprintf("- mode: %s\n", spokeConfigString(config, "mode")))
	b.WriteString(fmt.Sprintf("- model: %s\n", model))
	b.WriteString(fmt.Sprintf("- effort: %s\n\n", effort))
	b.WriteString(fmt.Sprintf("Your hub session id — spokes MUST send_prompt their ping-backs here: %s\n\n", sessionID))
	b.WriteString(fmt.Sprintf("Git break-glass is active for you: all repositories, full GitHub write + direct push, until %s. If the privileged git MCP tools (mint_full_git_token, push_current_head) aren't visible yet, call request_git_break_glass once to activate them.\n\n", expiresAt.Format(time.RFC3339)))
	b.WriteString("Before delegating any slice, make sure a concrete plan has been presented to and approved by the user — that is the one human checkpoint. Then run the per-slice brief → ping-back → integrate loop. Spokes report to you, not to the user.")
	return b.String()
}

func orchestrateKickoffDisplayText(config map[string]any) string {
	parts := []string{
		spokeConfigString(config, "provider"),
		spokeConfigString(config, "surface"),
	}
	if model := spokeConfigString(config, "model"); model != "" {
		parts = append(parts, model)
	}
	if effort := spokeConfigString(config, "effort"); effort != "" {
		parts = append(parts, effort)
	}
	return "Started orchestration — this session is now the hub (" + strings.Join(parts, " · ") + ")."
}
