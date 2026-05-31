package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/kubeexec"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

type menuCommandEntry struct {
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	Source string `json:"source"`
}

type sessionMenuSnapshot struct {
	SlashCommands []menuCommandEntry `json:"slash_commands"`
	MCPServers    []mcpServerEntry   `json:"mcp_servers"`
}

type sessionCapabilitiesResponse struct {
	Session       sessions.Info        `json:"session"`
	Skills        []skillEntryResponse `json:"skills"`
	MCPServers    []mcpServerEntry     `json:"mcp_servers"`
	MCPTools      []mcpToolEntry       `json:"mcp_tools"`
	MCPToolErrors []map[string]string  `json:"mcp_tool_errors"`
	Menus         sessionMenuSnapshot  `json:"menus"`
}

var builtinSlashCommands = []menuCommandEntry{
	{Name: "/clear", Desc: "Clear the conversation history", Source: "builtin"},
	{Name: "/compact", Desc: "Compact the conversation context", Source: "builtin"},
	{Name: "/context", Desc: "Show context window usage", Source: "builtin"},
	{Name: "/help", Desc: "List available commands", Source: "builtin"},
	{Name: "/init", Desc: "Initialize a project", Source: "builtin"},
	{Name: "/model", Desc: "Switch model", Source: "builtin"},
	{Name: "/review", Desc: "Review the pending changes", Source: "builtin"},
	{Name: "/security-review", Desc: "Run a security review", Source: "builtin"},
	{Name: "/usage", Desc: "Show token / billing usage", Source: "builtin"},
}

// handleInternalGitHubInstallation resolves the caller's actor_email to a
// {installation_id, is_host, is_super_admin} triple by reading the user's
// profile row. The canonical lookup mcp-github performs on every request:
// returns the routing inputs only, leaving JWT minting to auth.romaine.life.
//
// Returns {installation_id: null} when the email has no profile (treated
// as "no installation" on the caller side — mcp-github will reject the
// request rather than silently falling back to the host minter).
func (s *appServer) handleInternalGitHubInstallation(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/github/installation")
	if user == nil {
		return
	}

	email := strings.ToLower(strings.TrimSpace(user.ActorEmail))
	if email == "" {
		writeError(w, http.StatusBadRequest, "service token missing actor_email")
		return
	}

	if s.profiles == nil {
		writeError(w, http.StatusInternalServerError, "profile store not configured")
		return
	}
	profile, err := s.profiles.GetOrCreate(r.Context(), email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "profile lookup failed: "+err.Error())
		return
	}

	hostEmail := hostAdminEmail()
	isHost := hostEmail != "" && email == hostEmail
	isSuperAdmin := configuredSuperAdmins()[email]

	resp := map[string]any{
		"email":          email,
		"is_host":        isHost,
		"is_super_admin": isSuperAdmin,
	}
	if profile.InstallationID != nil {
		resp["installation_id"] = *profile.InstallationID
	} else {
		resp["installation_id"] = nil
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleInternalListSessions lists sessions for the caller's actor_email.
func (s *appServer) handleInternalListSessions(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/sessions")
	if user == nil {
		return
	}

	infos, err := s.mgr.ListSessions(r.Context(), user.ActorEmail)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	tankUIHost := envDefault("TANK_UI_HOST", "https://tank.romaine.life")
	type sessionWithURL struct {
		sessions.Info
		URL string `json:"url"`
	}
	out := make([]sessionWithURL, 0, len(infos))
	for _, info := range infos {
		out = append(out, sessionWithURL{
			Info: info,
			URL:  tankUIHost + "/?session=" + info.ID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleInternalCreateSession creates a new session for the caller's
// actor_email. Canonical service-principal session-create endpoint
// after the post-#486 API cleanup that retired the parallel `/spawn`
// alias on this surface.
func (s *appServer) handleInternalCreateSession(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions")
	if user == nil {
		return
	}
	if !s.gateSpawnQuota(w, r, user) {
		return
	}

	var body struct {
		Mode            string         `json:"mode"`
		Model           string         `json:"model,omitempty"`
		Effort          string         `json:"effort,omitempty"`
		GlimmungContext map[string]any `json:"glimmung_context"`
		Name            string         `json:"name"`
		Repos           []string       `json:"repos"`
		Capabilities    []string       `json:"capabilities"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Mode = ""
	}
	mode := sessionmodel.NormalizeSessionMode(body.Mode)

	repos, err := validateRepoSlugs(body.Repos)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(repos) > 0 && !sessionModeSupportsRepos(mode) {
		writeError(w, http.StatusBadRequest, errReposUnsupportedForMode.Error())
		return
	}
	capabilities, status, detail := validateCreateSessionCapabilities(mode, body.Capabilities)
	if status != 0 {
		writeError(w, status, detail)
		return
	}
	runConfig, status, detail := validateCreateRunConfig(mode, body.Model, body.Effort)
	if status != 0 {
		writeError(w, status, detail)
		return
	}

	// The internal handler historically passes body.Name as the
	// requestedAt argument — that's a pre-refactor naming oddity
	// preserved by setting RequestedAt to body.Name here. (Yes, the
	// field is named "name" but threads into RequestedAt — same as
	// the prior positional code.) Worth a separate cleanup PR; out
	// of scope for the repos feature.
	info, err := s.mgr.Create(r.Context(), sessions.CreateOptions{
		Owner:           user.ActorEmail,
		Mode:            mode,
		GlimmungContext: body.GlimmungContext,
		RequestedAt:     body.Name,
		Repos:           repos,
		Capabilities:    capabilities,
		Model:           runConfig.Model,
		Effort:          runConfig.Effort,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessionReposSelectedTotal.WithLabelValues(repoSelectionBucket(len(repos))).Inc()
	writeJSON(w, http.StatusCreated, info)
}

// gateSpawnQuota enforces the per-`sub` rate limit before a
// session-creation handler hits the manager. Returns true to proceed,
// false when the rate limit fired (the response has been written,
// including the throttle counter). The per-`actor_email` concurrent-cap
// previously enforced here was removed — see quota.go for the rationale.
func (s *appServer) gateSpawnQuota(w http.ResponseWriter, _ *http.Request, user *auth.User) bool {
	if s.spawnQuota != nil && !s.spawnQuota.CheckRate(user.Sub, serviceSpawnRatePerMin()) {
		writeError(w, http.StatusTooManyRequests, "spawn rate limit exceeded for this service-principal sub")
		return false
	}
	return true
}

// handleInternalDeleteSession deletes a session owned by the caller's actor_email.
func (s *appServer) handleInternalDeleteSession(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "DELETE /api/internal/sessions/{session_id}")
	if user == nil {
		return
	}
	sessionID := r.PathValue("session_id")
	if err := s.mgr.Delete(r.Context(), user.ActorEmail, sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleInternalRetireSessionScope hides every visible session row in a
// non-production session scope. Glimmung calls this while returning a test
// slot because it owns the K8s runtime teardown and therefore does not go
// through Manager.Delete for each session pod.
func (s *appServer) handleInternalRetireSessionScope(w http.ResponseWriter, r *http.Request) {
	user := s.requireSessionScopeRetireCaller(w, r)
	if user == nil {
		return
	}
	scope := normalizeSessionScope(r.PathValue("session_scope"))
	if scope == prodSessionScope {
		writeError(w, http.StatusBadRequest, "refusing to retire production session scope")
		return
	}
	registry := s.sessionRegistryForScope(scope)
	if registry == nil {
		writeError(w, http.StatusServiceUnavailable, "session registry not configured")
		return
	}

	retired, err := registry.MarkScopeRetired(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	rowPublisher := &sessioncontroller.RowPublisher{
		Fetcher:   registry,
		Publisher: s.sessionBus,
		Scope:     scope,
	}
	for _, row := range retired {
		rowPublisher.PublishCurrentRow(r.Context(), row.Email, row.ID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"session_scope":  scope,
		"retired_count":  len(retired),
		"requested_role": user.Role,
	})
}

// handleInternalPatchSession updates a session's name.
func (s *appServer) handleInternalPatchSession(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "PATCH /api/internal/sessions/{session_id}")
	if user == nil {
		return
	}
	sessionID := r.PathValue("session_id")
	var body struct {
		Name *string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	info, err := s.mgr.SetName(r.Context(), user.ActorEmail, sessionID, body.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleInternalSessionCapabilities returns the skills and MCP tools visible inside a session pod.
func (s *appServer) handleInternalSessionCapabilities(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/sessions/{session_id}/capabilities")
	if user == nil {
		return
	}
	s.doInternalSessionCapabilities(w, r, user.ActorEmail)
}

// handleInternalSessionTimeline returns the projected transcript-row read
// model for one of the caller's sessions, scoped to the service token's
// actor_email. This is the service-principal read path that backs the
// mcp-tank-operator read_transcript tool: it lets a session pod inspect a
// sibling session's conversation — for example, to triage a sibling session
// that stalled — without a browser or a human bearer token.
//
// Authorization, pagination, and projection are deliberately the *same code
// path* the browser uses (sessionTimelineBody → authorizeSessionReadInScope).
// There is no second transcript read model and no service-only relaxation of
// the ownership rule:
//
//   - role=service may read only sessions whose owner == actor_email; a
//     super-admin service token additionally inherits the admin cross-user
//     read already documented on authorizeSessionRead.
//   - a cross-user or missing session collapses to 404 so the surface does
//     not leak the existence of sessions the caller can't read.
//
// The query contract matches GET /api/sessions/{session_id}/timeline
// (anchor=newest|oldest, rows, before_cursor, timeline_id). The default
// newest-anchored tail is the right default for "what was this session doing
// when it stalled"; callers page backward through history with the returned
// prev_cursor. The response body shape is identical to the browser endpoint.
func (s *appServer) handleInternalSessionTimeline(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/sessions/{session_id}/timeline")
	if user == nil {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(*user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	body, status, err := s.sessionTimelineBody(r.Context(), r, *user, sessionID, sessionScope)
	if err != nil {
		if status >= 500 {
			recordSessionEventTimelineFailure()
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *appServer) doInternalSessionCapabilities(w http.ResponseWriter, r *http.Request, email string) {
	if email == "" {
		writeError(w, http.StatusBadRequest, "could not resolve caller email")
		return
	}
	sessionID := r.PathValue("session_id")
	info, podName, herr := s.resolveSessionPod(r.Context(), email, sessionID)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"bash", "-lc", `python3 - <<'PY'
import json
import os
import urllib.request

def parse_skill(path, source):
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as fh:
            text = fh.read(8192)
    except OSError:
        return None
    name = os.path.basename(os.path.dirname(path)) or "skill"
    description = ""
    body = text
    if text.startswith("---\n"):
        end = text.find("\n---\n", 4)
        if end >= 0:
            frontmatter = text[4:end].splitlines()
            body = text[end + 5:]
            for line in frontmatter:
                key, sep, value = line.partition(":")
                if not sep:
                    continue
                key = key.strip()
                value = value.strip().strip("\"'")
                if key == "name" and value:
                    name = value
                elif key == "description":
                    description = value
    return {
        "name": name,
        "path": path,
        "source": source,
        "description": description,
        "body_preview": " ".join(body.strip().split())[:240],
    }

def sse_json(body):
    text = body.decode("utf-8", "replace")
    for line in text.splitlines():
        if line.startswith("data: "):
            try:
                return json.loads(line[6:])
            except json.JSONDecodeError:
                continue
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return {}

skills = []
seen_skills = set()
config_dir = "/opt/tank/session-config"
if os.path.isdir(config_dir):
    for filename in sorted(os.listdir(config_dir)):
        if filename.startswith("skills__") and filename.endswith("__SKILL.md"):
            entry = parse_skill(os.path.join(config_dir, filename), "bundled")
            if entry and entry["name"] not in seen_skills:
                seen_skills.add(entry["name"])
                skills.append(entry)

for root, source in [
    ("/home/node/.codex/skills", "codex"),
    ("/home/node/.claude/skills", "claude"),
    ("/workspace", "workspace"),
]:
    if not os.path.isdir(root):
        continue
    for current, dirs, files in os.walk(root):
        dirs[:] = [d for d in dirs if d not in {".git", "node_modules"}]
        if "SKILL.md" not in files:
            continue
        entry = parse_skill(os.path.join(current, "SKILL.md"), source)
        if entry and entry["name"] not in seen_skills:
            seen_skills.add(entry["name"])
            skills.append(entry)

try:
    with open("/workspace/.mcp.json", "r", encoding="utf-8") as fh:
        mcp_config = json.load(fh)
except Exception:
    mcp_config = {}

mcp_servers = []
mcp_tools = []
mcp_tool_errors = []
for server, raw in sorted((mcp_config.get("mcpServers") or {}).items()):
    if not isinstance(raw, dict):
        continue
    transport = str(raw.get("type") or ("stdio" if raw.get("command") else "unknown"))
    target = str(raw.get("url") or raw.get("command") or "")
    mcp_servers.append({
        "name": server,
        "transport": transport,
        "target": target,
        "source": "/workspace/.mcp.json",
        "enabled": True,
    })
    url = str(raw.get("url") or "").strip()
    if not url:
        continue
    payload = json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/list",
        "params": {},
    }).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=payload,
        headers={
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=4) as resp:
            msg = sse_json(resp.read())
    except Exception as exc:
        mcp_tool_errors.append({"server": server, "error": str(exc)})
        continue
    for tool in (((msg.get("result") or {}).get("tools")) or []):
        if not isinstance(tool, dict):
            continue
        name = str(tool.get("name") or "").strip()
        if not name:
            continue
        mcp_tools.append({
            "server": server,
            "name": name,
            "description": str(tool.get("description") or "").strip(),
        })

print(json.dumps({
    "skills": skills,
    "mcp_servers": mcp_servers,
    "mcp_tools": mcp_tools,
    "mcp_tool_errors": mcp_tool_errors,
}))
PY`})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var capabilities sessionCapabilitiesResponse
	if err := json.Unmarshal(out, &capabilities); err != nil {
		writeError(w, http.StatusInternalServerError, "parse capabilities: "+err.Error())
		return
	}
	capabilities.Session = info
	capabilities.Menus = sessionMenus(capabilities.Skills, capabilities.MCPServers)
	writeJSON(w, http.StatusOK, capabilities)
}

func sessionMenus(skills []skillEntryResponse, mcpServers []mcpServerEntry) sessionMenuSnapshot {
	commands := make([]menuCommandEntry, 0, len(builtinSlashCommands)+len(skills))
	indexByName := make(map[string]int, len(builtinSlashCommands)+len(skills))
	for _, command := range builtinSlashCommands {
		indexByName[command.Name] = len(commands)
		commands = append(commands, command)
	}
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		desc := firstNonEmpty(skill.Description, skill.BodyPreview, skill.Source+" skill")
		entry := menuCommandEntry{Name: name, Desc: desc, Source: "skill"}
		if idx, ok := indexByName[name]; ok {
			commands[idx] = entry
			continue
		}
		indexByName[name] = len(commands)
		commands = append(commands, entry)
	}

	servers := append([]mcpServerEntry(nil), mcpServers...)
	sort.Slice(servers, func(i, j int) bool {
		return strings.ToLower(servers[i].Name) < strings.ToLower(servers[j].Name)
	})
	return sessionMenuSnapshot{
		SlashCommands: commands,
		MCPServers:    servers,
	}
}

// handleInternalSetTestState sets the test state for a session.
func (s *appServer) handleInternalSetTestState(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/test-state")
	if user == nil {
		return
	}
	sessionID := r.PathValue("session_id")
	var body struct {
		Active    bool    `json:"active"`
		SlotIndex *int    `json:"slot_index"`
		URL       *string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	info, err := s.mgr.SetTestState(r.Context(), user.ActorEmail, sessionID, body.Active, body.SlotIndex, body.URL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleInternalSetRolloutState sets the rollout state for a session.
func (s *appServer) handleInternalSetRolloutState(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/rollout-state")
	if user == nil {
		return
	}
	sessionID := r.PathValue("session_id")
	var body struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	info, err := s.mgr.SetRolloutState(r.Context(), user.ActorEmail, sessionID, body.Active)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleInternalSetCloneState stores repo-cloner init-container progress on
// the durable sessions row so clone failures are visible without reading pod
// logs. The caller is a session pod service-principal token whose actor_email
// owns the target session.
func (s *appServer) handleInternalSetCloneState(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/clone-state")
	if user == nil {
		return
	}
	sessionID := r.PathValue("session_id")
	var body struct {
		CloneState map[string]any `json:"clone_state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.CloneState == nil {
		writeError(w, http.StatusBadRequest, "clone_state is required")
		return
	}
	info, err := s.mgr.SetCloneState(r.Context(), user.ActorEmail, sessionID, body.CloneState)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleInternalSendMessage enqueues a follow-up turn to a chat-capable session.
//
// Origin attribution: when the caller is itself a tank-operator session pod
// (the only post-#486 caller shape — service-principal JWT minted from that
// pod's projected SA token), the mcp-auth-proxy sidecar in the originating
// pod stamps the X-Tank-Origin-Session-Id header on the way out. That id
// flows through the mcp-tank-operator MCP server unchanged and lands here.
// We thread it onto the persisted user_message.created event so the
// frontend renders the user bubble with the parent session's deterministic
// avatar instead of the human owner's Gravatar. The header is advisory:
// missing/invalid values fall through to the human-Gravatar rendering — a
// caller cannot escalate or spoof identity by setting it, because owner
// attribution (`email`) still comes from the verified service-JWT
// `actor_email` claim.
func (s *appServer) handleInternalSendMessage(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "POST /api/internal/sessions/{session_id}/messages")
	if user == nil {
		return
	}
	sessionID := r.PathValue("session_id")
	var body struct {
		Prompt         string `json:"prompt"`
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
		SkillName      string `json:"skill_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		writeError(w, http.StatusBadRequest, "missing prompt")
		return
	}

	resp, status, detail := s.enqueueSDKTurn(r.Context(), user.ActorEmail, sessionID, sdkTurnRequest{
		Prompt:          body.Prompt,
		Model:           body.Model,
		PermissionMode:  body.PermissionMode,
		SkillName:       body.SkillName,
		FollowUp:        true,
		OriginSessionID: strings.TrimSpace(r.Header.Get(originSessionHeader)),
	})
	if detail != "" {
		writeError(w, status, detail)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// originSessionHeader carries the originating tank-operator session id on
// service-principal handoff calls (POST /api/internal/sessions/{id}/messages).
// The mcp-auth-proxy sidecar in each session pod injects it from the pod's
// SESSION_ID env var; mcp-tank-operator forwards it unchanged. Header name is
// shared with both repos (claude-container/mcp-auth-proxy/src/mcp_auth_proxy
// /server.py and mcp-tank-operator/src/mcp_tank_operator/{caller,client,http}.py)
// — changing it requires a coordinated cross-repo deploy.
const originSessionHeader = "X-Tank-Origin-Session-Id"

// requireServicePrincipal validates an inbound auth.romaine.life JWT and
// returns the verified User iff the role claim is `service`. The
// canonical authentication path for every /api/internal/sessions/*
// handler post-#486 Stage 4.
//
// On rejection, writes the structured error response and increments the
// observability counter with the labeled reason so the deny-rate is
// visible on the dashboard. Returns nil to signal "handler should
// return immediately" — mirrors http.Handler semantics for middlewares
// that complete the response themselves.
func (s *appServer) requireServicePrincipal(w http.ResponseWriter, r *http.Request, route string) *auth.User {
	if s.verifier == nil {
		recordServiceRoleRequest(route, "error_verifier_unconfigured")
		writeError(w, http.StatusInternalServerError, "JWT verifier not configured")
		return nil
	}
	user, err := s.verifier.CurrentUser(r)
	if err != nil {
		recordServiceRoleRequest(route, "denied_token")
		writeError(w, auth.ErrorStatus(err), err.Error())
		return nil
	}
	if !user.IsService() {
		recordServiceRoleRequest(route, "denied_role")
		writeError(w, http.StatusForbidden, "route requires role=service; caller is role="+user.Role)
		return nil
	}
	if user.ActorEmail == "" {
		// Belt and suspenders — auth.Decode already enforces this for
		// role=service, but guarding here too keeps the contract local
		// to anything reading user.ActorEmail downstream.
		recordServiceRoleRequest(route, "denied_actor_missing")
		writeError(w, http.StatusUnauthorized, "service-role token missing actor_email")
		return nil
	}
	return &user
}

func (s *appServer) requireSessionScopeRetireCaller(w http.ResponseWriter, r *http.Request) *auth.User {
	if s.verifier == nil {
		writeError(w, http.StatusInternalServerError, "JWT verifier not configured")
		return nil
	}
	user, err := s.verifier.CurrentUser(r)
	if err != nil {
		writeError(w, auth.ErrorStatus(err), err.Error())
		return nil
	}
	if user.Role == auth.RoleService {
		if user.ActorEmail == "" {
			writeError(w, http.StatusUnauthorized, "service-role token missing actor_email")
			return nil
		}
		return &user
	}
	if hasAdminPower(user) {
		return &user
	}
	writeError(w, http.StatusForbidden, "route requires role=service or admin")
	return nil
}
