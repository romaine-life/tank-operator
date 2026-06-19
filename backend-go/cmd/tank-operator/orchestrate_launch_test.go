package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// recordingOrchestrateRegistry wraps testSessionRegistry and captures
// SetSpokeConfig calls so tests can assert on the persisted hub config.
type recordingOrchestrateRegistry struct {
	testSessionRegistry
	spokeConfigCalls []recordedSpokeConfigCall
}

type recordedSpokeConfigCall struct {
	Owner     string
	SessionID string
	Config    map[string]any
}

// SetSpokeConfig satisfies sessions.SessionRegistry and records each call.
func (r *recordingOrchestrateRegistry) SetSpokeConfig(_ context.Context, owner, sessionID string, config map[string]any) error {
	r.spokeConfigCalls = append(r.spokeConfigCalls, recordedSpokeConfigCall{
		Owner:     owner,
		SessionID: sessionID,
		Config:    config,
	})
	// Also update the embedded registry so GetByOwner returns the updated row.
	if r.testSessionRegistry.records == nil {
		r.testSessionRegistry.records = map[string]map[string]sessionmodel.SessionRecord{}
	}
	if r.testSessionRegistry.records[owner] != nil {
		if rec, ok := r.testSessionRegistry.records[owner][sessionID]; ok {
			rec.SpokeConfig = config
			r.testSessionRegistry.records[owner][sessionID] = rec
		}
	}
	return nil
}

// Compile-time check: *recordingOrchestrateRegistry must satisfy SessionRegistry.
var _ sessions.SessionRegistry = (*recordingOrchestrateRegistry)(nil)

// newOrchestrateRegistry builds a recordingOrchestrateRegistry seeded with
// the given session records.
func newOrchestrateRegistry(records ...sessionmodel.SessionRecord) *recordingOrchestrateRegistry {
	r := &recordingOrchestrateRegistry{}
	r.testSessionRegistry = *newTestSessionRegistry(records...)
	return r
}

// orchestrateApp builds an appServer for orchestrate handler tests.
// pods are registered in k8s (for the pod-backed GetByOwner path),
// registry overrides SetSpokeConfig capture, and controlStore records grant
// appends.
func orchestrateApp(
	t *testing.T,
	registry sessions.SessionRegistry,
	controlStore controlActionStore,
	pods ...*corev1.Pod,
) *appServer {
	t.Helper()
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, pods...)
	if registry != nil {
		app.mgr = sessions.NewManager(
			app.k8s, nil, sessionmodel.SessionsNamespace, registry, nil, sessions.ManagerOptions{},
		)
	}
	app.controlActions = controlStore
	app.sessionScope = "default"
	return app
}

// orchestrateReq builds an authenticated POST request for the orchestrate endpoint.
func orchestrateReq(t *testing.T, email, role, sessionID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/orchestrate", strings.NewReader(body))
	req.SetPathValue("session_id", sessionID)
	req.Header.Set("Content-Type", "application/json")
	var tok string
	if role == auth.RoleService {
		tok = signedServiceToken(t, "pod-svc@service.tank.romaine.life", email)
	} else {
		tok = signedTokenWithRole(t, email, role)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return req
}

// --- Pure unit tests for validateSpokeConfig ---

func TestValidateSpokeConfig_ClaudeGUIValidModelEffort(t *testing.T) {
	config, status, detail := validateSpokeConfig("claude", "gui", "claude-sonnet-4-6", "high")
	if status != 0 {
		t.Fatalf("expected success, got status=%d detail=%q", status, detail)
	}
	if config == nil {
		t.Fatal("expected non-nil config map")
	}
	if config["provider"] != "claude" {
		t.Fatalf("provider=%v, want claude", config["provider"])
	}
	if config["surface"] != "gui" {
		t.Fatalf("surface=%v, want gui", config["surface"])
	}
	if config["mode"] != sessionmodel.ClaudeGUIMode {
		t.Fatalf("mode=%v, want %s", config["mode"], sessionmodel.ClaudeGUIMode)
	}
	if config["model"] != "claude-sonnet-4-6" {
		t.Fatalf("model=%v, want claude-sonnet-4-6", config["model"])
	}
	if config["effort"] != "high" {
		t.Fatalf("effort=%v, want high", config["effort"])
	}
}

func TestValidateSpokeConfig_ClaudeCLIMode(t *testing.T) {
	config, status, detail := validateSpokeConfig("claude", "cli", "", "")
	if status != 0 {
		t.Fatalf("expected success for claude+cli, got status=%d detail=%q", status, detail)
	}
	if config["mode"] != sessionmodel.ClaudeCLIMode {
		t.Fatalf("mode=%v, want %s", config["mode"], sessionmodel.ClaudeCLIMode)
	}
}

func TestValidateSpokeConfig_CodexGUIEmptyModelRejected(t *testing.T) {
	_, status, detail := validateSpokeConfig("codex", "gui", "", "")
	if status != http.StatusBadRequest {
		t.Fatalf("codex+no model: expected 400, got %d", status)
	}
	if !strings.Contains(detail, "model is required for Codex") {
		t.Fatalf("detail=%q, want codex explicit model message", detail)
	}
}

func TestValidateSpokeConfig_UnknownProviderRejected(t *testing.T) {
	_, status, detail := validateSpokeConfig("openai", "gui", "", "")
	if status != http.StatusBadRequest {
		t.Fatalf("unknown provider: expected 400, got %d", status)
	}
	if !strings.Contains(detail, "provider must be claude or codex") {
		t.Fatalf("detail=%q, want provider rejection message", detail)
	}
}

func TestValidateSpokeConfig_InvalidSurfaceRejected(t *testing.T) {
	_, status, detail := validateSpokeConfig("claude", "tui", "", "")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid surface: expected 400, got %d", status)
	}
	if !strings.Contains(detail, "surface must be gui or cli") {
		t.Fatalf("detail=%q, want surface rejection", detail)
	}
}

func TestValidateSpokeConfig_UnsupportedModelRejected(t *testing.T) {
	_, status, detail := validateSpokeConfig("claude", "gui", "gpt-5.5", "")
	if status != http.StatusBadRequest {
		t.Fatalf("unsupported model: expected 400, got %d", status)
	}
	if !strings.Contains(detail, "model is not available for claude") {
		t.Fatalf("detail=%q, want model unsupported message for claude", detail)
	}
}

func TestValidateSpokeConfig_UnsupportedEffortRejected(t *testing.T) {
	_, status, detail := validateSpokeConfig("claude", "gui", "", "ultra")
	if status != http.StatusBadRequest {
		t.Fatalf("unsupported effort: expected 400, got %d", status)
	}
	if !strings.Contains(detail, "effort is invalid") {
		t.Fatalf("detail=%q, want effort rejection", detail)
	}
}

func TestValidateSpokeConfig_DefaultModelAliasRejected(t *testing.T) {
	_, status, detail := validateSpokeConfig("claude", "gui", "default", "")
	if status != http.StatusBadRequest {
		t.Fatalf("default alias: expected 400, got %d", status)
	}
	if !strings.Contains(detail, "default is not accepted") {
		t.Fatalf("detail=%q, want default alias rejection", detail)
	}
}

// --- Pure unit tests for spokeModeFor ---

func TestSpokeModeFor_AllCombinations(t *testing.T) {
	tests := []struct {
		provider string
		surface  string
		wantMode string
		wantOK   bool
	}{
		{"claude", "gui", sessionmodel.ClaudeGUIMode, true},
		{"claude", "cli", sessionmodel.ClaudeCLIMode, true},
		{"codex", "gui", sessionmodel.CodexGUIMode, true},
		{"codex", "cli", sessionmodel.CodexCLIMode, true},
		// Invalid surface.
		{"claude", "tui", "", false},
		// Invalid provider.
		{"unknown", "gui", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.provider+"+"+tc.surface, func(t *testing.T) {
			mode, ok := spokeModeFor(tc.provider, tc.surface)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if mode != tc.wantMode {
				t.Fatalf("mode=%q, want %q", mode, tc.wantMode)
			}
		})
	}
}

// --- HTTP-level tests for handleOrchestrateLaunch ---

func TestHandleOrchestrateLaunch_ServicePrincipalRejected(t *testing.T) {
	store := &fakeControlActionStore{}
	app := orchestrateApp(t, nil, store)

	req := orchestrateReq(t, "user@example.com", auth.RoleService, "63",
		`{"provider":"claude","surface":"gui","model":"claude-sonnet-4-6"}`)
	resp := httptest.NewRecorder()

	app.handleOrchestrateLaunch(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("service principal: status=%d body=%s, want 403", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "human session owner") {
		t.Fatalf("body=%s, want human owner error", resp.Body.String())
	}
	// No side effects.
	if len(store.appendCalls) != 0 {
		t.Fatalf("appendCalls=%d, want 0", len(store.appendCalls))
	}
	bus := app.sessionBus.(*recordingSessionBus)
	if len(bus.commands) != 0 {
		t.Fatalf("commands=%d, want 0", len(bus.commands))
	}
}

func TestHandleOrchestrateLaunch_SessionNotFound(t *testing.T) {
	store := &fakeControlActionStore{}
	// No pods registered for session "99".
	app := orchestrateApp(t, nil, store)

	req := orchestrateReq(t, "user@example.com", auth.RoleUser, "99",
		`{"provider":"claude","surface":"gui","model":"claude-sonnet-4-6"}`)
	resp := httptest.NewRecorder()

	app.handleOrchestrateLaunch(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("missing session: status=%d body=%s, want 404", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "session not found") {
		t.Fatalf("body=%s, want session not found", resp.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("appendCalls=%d, want 0", len(store.appendCalls))
	}
}

func TestHandleOrchestrateLaunch_HubNotGUIMode(t *testing.T) {
	store := &fakeControlActionStore{}
	// CLI-mode pod — not a supported SDK GUI hub.
	app := orchestrateApp(t, nil, store,
		sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeCLIMode, "sandbox"),
	)

	req := orchestrateReq(t, "user@example.com", auth.RoleUser, "63",
		`{"provider":"claude","surface":"gui","model":"claude-sonnet-4-6"}`)
	resp := httptest.NewRecorder()

	app.handleOrchestrateLaunch(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("non-GUI hub: status=%d body=%s, want 400", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "GUI chat session") {
		t.Fatalf("body=%s, want GUI mode error", resp.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("appendCalls=%d, want 0", len(store.appendCalls))
	}
}

func TestHandleOrchestrateLaunch_InvalidSpokeConfigRejected(t *testing.T) {
	store := &fakeControlActionStore{}
	reg := newOrchestrateRegistry(sessionmodel.SessionRecord{
		ID:      "63",
		Email:   "user@example.com",
		Mode:    sessionmodel.ClaudeGUIMode,
		Visible: true,
	})
	app := orchestrateApp(t, reg, store,
		sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "claude-runner"),
	)

	// Unknown provider — should fail validation before any write.
	req := orchestrateReq(t, "user@example.com", auth.RoleUser, "63",
		`{"provider":"unknown_xyz","surface":"gui"}`)
	resp := httptest.NewRecorder()

	app.handleOrchestrateLaunch(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid spoke config: status=%d body=%s, want 400", resp.Code, resp.Body.String())
	}
	// Validation must fire BEFORE any side effects.
	if len(store.appendCalls) != 0 {
		t.Fatalf("appendCalls=%d, want 0 (validation before side effects)", len(store.appendCalls))
	}
	if len(reg.spokeConfigCalls) != 0 {
		t.Fatalf("SetSpokeConfig calls=%d, want 0", len(reg.spokeConfigCalls))
	}
	bus := app.sessionBus.(*recordingSessionBus)
	if len(bus.commands) != 0 {
		t.Fatalf("commands=%d, want 0", len(bus.commands))
	}
}

// TestHandleOrchestrateLaunch_SpokeConfigAndGrantPersisted validates the full
// happy path: a human owner launching orchestrate on their GUI hub session gets
// 202, the spoke config is persisted, the all-repos/unlimited/24h break-glass
// grant is appended with the orchestrate-self-grant marker, and exactly one
// /orchestrate kickoff turn is enqueued. The kickoff prompt begins with the
// hub's provider-specific skill trigger so enqueueSDKTurn's skill_name↔trigger
// guard accepts it.
func TestHandleOrchestrateLaunch_SpokeConfigAndGrantPersisted(t *testing.T) {
	store := &fakeControlActionStore{}
	reg := newOrchestrateRegistry(sessionmodel.SessionRecord{
		ID:      "63",
		Email:   "user@example.com",
		Mode:    sessionmodel.ClaudeGUIMode,
		Visible: true,
	})
	app := orchestrateApp(t, reg, store,
		sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "claude-runner"),
	)

	req := orchestrateReq(t, "user@example.com", auth.RoleUser, "63",
		`{"provider":"claude","surface":"gui","model":"claude-sonnet-4-6","effort":"high"}`)
	resp := httptest.NewRecorder()

	app.handleOrchestrateLaunch(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", resp.Code, resp.Body.String())
	}

	// --- SetSpokeConfig MUST have been called (step 1 completes before step 3) ---
	if len(reg.spokeConfigCalls) != 1 {
		t.Fatalf("SetSpokeConfig calls=%d, want 1 (persisted before kickoff)", len(reg.spokeConfigCalls))
	}
	sc := reg.spokeConfigCalls[0]
	if sc.Owner != "user@example.com" || sc.SessionID != "63" {
		t.Fatalf("SetSpokeConfig owner/session=%q/%q", sc.Owner, sc.SessionID)
	}
	if sc.Config["provider"] != "claude" {
		t.Fatalf("spoke_config provider=%v, want claude", sc.Config["provider"])
	}
	if sc.Config["surface"] != "gui" {
		t.Fatalf("spoke_config surface=%v, want gui", sc.Config["surface"])
	}
	if sc.Config["mode"] != sessionmodel.ClaudeGUIMode {
		t.Fatalf("spoke_config mode=%v, want %s", sc.Config["mode"], sessionmodel.ClaudeGUIMode)
	}
	if sc.Config["model"] != "claude-sonnet-4-6" {
		t.Fatalf("spoke_config model=%v, want claude-sonnet-4-6", sc.Config["model"])
	}
	if sc.Config["effort"] != "high" {
		t.Fatalf("spoke_config effort=%v, want high", sc.Config["effort"])
	}

	// --- Break-glass grant MUST have been appended (step 2 completes before step 3) ---
	if len(store.appendCalls) != 1 {
		t.Fatalf("appendCalls=%d, want 1 (grant appended before kickoff)", len(store.appendCalls))
	}
	grant := store.appendCalls[0]
	if grant.Action != "github.break_glass.grant" || grant.Status != "succeeded" {
		t.Fatalf("grant action/status=%s/%s", grant.Action, grant.Status)
	}
	if grant.SessionID != "63" || grant.OwnerEmail != "user@example.com" {
		t.Fatalf("grant session/owner=%s/%s", grant.SessionID, grant.OwnerEmail)
	}

	var payload map[string]any
	if err := json.Unmarshal(grant.Payload, &payload); err != nil {
		t.Fatalf("unmarshal grant payload: %v", err)
	}
	if payload["source"] != orchestrateSelfGrantSource {
		t.Fatalf("grant payload source=%v, want %q", payload["source"], orchestrateSelfGrantSource)
	}
	repoScope, ok := payload["repo_scope"].(map[string]any)
	if !ok || repoScope["kind"] != "all_repos" {
		t.Fatalf("grant repo_scope=%#v, want all_repos", payload["repo_scope"])
	}
	branchScope, ok := payload["branch_scope"].(map[string]any)
	if !ok || branchScope["kind"] != "unlimited" {
		t.Fatalf("grant branch_scope=%#v, want unlimited", payload["branch_scope"])
	}
	ttlSeconds, _ := payload["ttl_seconds"].(float64)
	if int(ttlSeconds) != orchestrateSelfGrantTTLSeconds {
		t.Fatalf("grant ttl_seconds=%v, want %d", payload["ttl_seconds"], orchestrateSelfGrantTTLSeconds)
	}
	ops, _ := payload["operations"].([]any)
	wantOps := map[string]bool{
		gitBreakGlassOpFullAPI:   true,
		gitBreakGlassOpMintToken: true,
		gitBreakGlassOpPushHead:  true,
		gitBreakGlassOpWorkflows: true,
	}
	for _, op := range ops {
		delete(wantOps, op.(string))
	}
	if len(wantOps) != 0 {
		missing := make([]string, 0, len(wantOps))
		for k := range wantOps {
			missing = append(missing, k)
		}
		t.Fatalf("grant operations missing: %v (got %v)", missing, ops)
	}

	// --- Exactly one /orchestrate kickoff turn enqueued ---
	bus := app.sessionBus.(*recordingSessionBus)
	if len(bus.commands) != 1 {
		t.Fatalf("kickoff commands=%d, want 1", len(bus.commands))
	}
	kickoff := bus.commands[0]
	if kickoff.SkillName != "orchestrate" {
		t.Fatalf("kickoff skill_name=%q, want orchestrate", kickoff.SkillName)
	}
	if !strings.HasPrefix(kickoff.Prompt, "/orchestrate") {
		t.Fatalf("kickoff prompt must start with the claude skill trigger; got %q", kickoff.Prompt)
	}
	if !strings.Contains(kickoff.Prompt, "63") {
		t.Fatalf("kickoff prompt must embed the hub session id for ping-backs; got %q", kickoff.Prompt)
	}
}

// TestHandleOrchestrateLaunch_KickoffErrorIsMentionedInBody forces the kickoff
// turn enqueue to fail (the session bus rejects the publish) AFTER spoke config
// and the grant are already persisted, and asserts the handler surfaces a
// non-2xx whose body distinguishes the kickoff failure from an earlier-step
// failure. This is the partial-failure mode the orchestrate launch sequence
// (persist → self-grant → kickoff) must report honestly.
func TestHandleOrchestrateLaunch_KickoffErrorIsMentionedInBody(t *testing.T) {
	store := &fakeControlActionStore{}
	reg := newOrchestrateRegistry(sessionmodel.SessionRecord{
		ID:      "63",
		Email:   "user@example.com",
		Mode:    sessionmodel.ClaudeGUIMode,
		Visible: true,
	})
	app := orchestrateApp(t, reg, store,
		sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "claude-runner"),
	)
	// Make the kickoff turn's command publish fail; persist + grant still succeed.
	app.sessionBus.(*recordingSessionBus).err = errors.New("bus publish boom")

	req := orchestrateReq(t, "user@example.com", auth.RoleUser, "63",
		`{"provider":"claude","surface":"gui","model":"claude-sonnet-4-6"}`)
	resp := httptest.NewRecorder()

	app.handleOrchestrateLaunch(resp, req)

	if resp.Code == http.StatusAccepted {
		t.Fatalf("expected a non-2xx kickoff failure, got 202 body=%s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "kickoff turn failed") {
		t.Fatalf("body=%s, want 'kickoff turn failed' message", resp.Body.String())
	}
	// The earlier steps still completed durably.
	if len(reg.spokeConfigCalls) != 1 {
		t.Fatalf("SetSpokeConfig calls=%d, want 1 (persist precedes kickoff)", len(reg.spokeConfigCalls))
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("appendCalls=%d, want 1 (grant precedes kickoff)", len(store.appendCalls))
	}
}

// TestHandleOrchestrateLaunch_SecondPostAppendsSecondGrant confirms that
// each call to handleOrchestrateLaunch appends a new break-glass grant
// (additive, not idempotent) — a re-confirm (e.g. extending past the 24h
// ceiling) grants fresh authority rather than mutating the prior grant.
func TestHandleOrchestrateLaunch_SecondPostAppendsSecondGrant(t *testing.T) {
	store := &fakeControlActionStore{}
	reg := newOrchestrateRegistry(sessionmodel.SessionRecord{
		ID:      "63",
		Email:   "user@example.com",
		Mode:    sessionmodel.ClaudeGUIMode,
		Visible: true,
	})
	app := orchestrateApp(t, reg, store,
		sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "claude-runner"),
	)
	body := `{"provider":"claude","surface":"gui","model":"claude-sonnet-4-6"}`

	// First POST → 202.
	req1 := orchestrateReq(t, "user@example.com", auth.RoleUser, "63", body)
	resp1 := httptest.NewRecorder()
	app.handleOrchestrateLaunch(resp1, req1)
	if resp1.Code != http.StatusAccepted {
		t.Fatalf("first POST: status=%d body=%s, want 202", resp1.Code, resp1.Body.String())
	}

	// Second POST → 202, appends a fresh grant (additive, like admin approval path).
	req2 := orchestrateReq(t, "user@example.com", auth.RoleUser, "63", body)
	resp2 := httptest.NewRecorder()
	app.handleOrchestrateLaunch(resp2, req2)
	if resp2.Code != http.StatusAccepted {
		t.Fatalf("second POST: status=%d body=%s, want 202", resp2.Code, resp2.Body.String())
	}

	// Two independent grant events appended (one per call, regardless of
	// kickoff outcome — grant write is durable before kickoff is attempted).
	if len(store.appendCalls) != 2 {
		t.Fatalf("appendCalls=%d, want 2 (one per re-confirm)", len(store.appendCalls))
	}
	for i, call := range store.appendCalls {
		if call.Action != "github.break_glass.grant" {
			t.Fatalf("appendCalls[%d].Action=%q, want github.break_glass.grant", i, call.Action)
		}
		var p map[string]any
		if err := json.Unmarshal(call.Payload, &p); err != nil {
			t.Fatalf("appendCalls[%d] payload: %v", i, err)
		}
		if p["source"] != orchestrateSelfGrantSource {
			t.Fatalf("appendCalls[%d] source=%v, want %q", i, p["source"], orchestrateSelfGrantSource)
		}
	}
}
