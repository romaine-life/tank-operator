package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/kubeexec"
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

// requireInternalCaller validates the Bearer SA token and checks that the
// caller's namespace/name is in the allowedSubjects map.
func requireInternalCaller(k8s kubernetes.Interface, allowedSubjects map[string]string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token, err := auth.ParseSAToken(r)
			if err != nil {
				writeError(w, auth.ErrorStatus(err), err.Error())
				return
			}
			// Internal callers must present a token minted for tank-operator.
			// The exact service-account allowlist gates access after TokenReview
			// succeeds.
			subject, err := auth.ValidateSAToken(r.Context(), k8s, token, []string{"tank-operator"})
			if err != nil {
				writeError(w, auth.ErrorStatus(err), err.Error())
				return
			}
			if _, ok := allowedSubjects[subject.Qualified()]; !ok {
				writeError(w, http.StatusForbidden, "caller not in allowed subjects: "+subject.Qualified())
				return
			}
			next(w, r)
		}
	}
}

// handleInternalResolveCaller resolves a caller's identity by pod IP.
func (s *appServer) handleInternalResolveCaller(w http.ResponseWriter, r *http.Request) {
	protect := requireInternalCaller(s.k8s, s.internalAllowedSubjects)
	protect(s.doInternalResolveCaller)(w, r)
}

func (s *appServer) doInternalResolveCaller(w http.ResponseWriter, r *http.Request) {
	podIP := r.URL.Query().Get("pod_ip")
	if podIP == "" {
		writeError(w, http.StatusBadRequest, "missing pod_ip")
		return
	}

	email, podName, err := s.mgr.FindPodByIP(r.Context(), podIP)
	if err != nil {
		writeError(w, http.StatusNotFound, "no session pod with IP: "+podIP)
		return
	}

	hostEmail := os.Getenv("HOST_EMAIL")
	superAdmins := parseEmailSet(envDefault("SUPER_ADMIN_EMAILS", hostEmail))
	var installationID *int64

	if s.profiles != nil {
		profile, profErr := s.profiles.GetOrCreate(r.Context(), email)
		if profErr == nil {
			installationID = profile.InstallationID
		}
	}

	tankUIHost := envDefault("TANK_UI_HOST", "https://tank.romaine.life")
	_ = tankUIHost

	writeJSON(w, http.StatusOK, map[string]any{
		"email":           email,
		"installation_id": installationID,
		"is_host":         strings.EqualFold(email, hostEmail),
		"is_super_admin":  superAdmins[strings.ToLower(strings.TrimSpace(email))],
		"host_email":      hostEmail,
		"pod_name":        podName,
	})
}

// handleInternalListSessions lists sessions for the caller's email.
func (s *appServer) handleInternalListSessions(w http.ResponseWriter, r *http.Request) {
	protect := requireInternalCaller(s.k8s, s.internalAllowedSubjects)
	protect(s.doInternalListSessions)(w, r)
}

func (s *appServer) doInternalListSessions(w http.ResponseWriter, r *http.Request) {
	email, callerPodName := s.resolveCallerEmail(r)
	if email == "" {
		writeError(w, http.StatusBadRequest, "could not resolve caller email (missing caller_pod_ip or pod not found)")
		return
	}
	_ = callerPodName

	infos, err := s.mgr.ListSessions(r.Context(), email)
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

// handleInternalCreateSession creates a new session for the caller.
func (s *appServer) handleInternalCreateSession(w http.ResponseWriter, r *http.Request) {
	protect := requireInternalCaller(s.k8s, s.internalAllowedSubjects)
	protect(s.doInternalCreateSession)(w, r)
}

func (s *appServer) doInternalCreateSession(w http.ResponseWriter, r *http.Request) {
	email, _ := s.resolveCallerEmail(r)
	if email == "" {
		writeError(w, http.StatusBadRequest, "could not resolve caller email")
		return
	}

	var body struct {
		Mode            string         `json:"mode"`
		GlimmungContext map[string]any `json:"glimmung_context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Mode = ""
	}

	info, err := s.mgr.Create(r.Context(), email, body.Mode, body.GlimmungContext, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

// handleInternalDeleteSession deletes a session.
func (s *appServer) handleInternalDeleteSession(w http.ResponseWriter, r *http.Request) {
	protect := requireInternalCaller(s.k8s, s.internalAllowedSubjects)
	protect(s.doInternalDeleteSession)(w, r)
}

func (s *appServer) doInternalDeleteSession(w http.ResponseWriter, r *http.Request) {
	email, _ := s.resolveCallerEmail(r)
	if email == "" {
		writeError(w, http.StatusBadRequest, "could not resolve caller email")
		return
	}
	sessionID := r.PathValue("session_id")
	if err := s.mgr.Delete(r.Context(), email, sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleInternalPatchSession updates a session's name.
func (s *appServer) handleInternalPatchSession(w http.ResponseWriter, r *http.Request) {
	protect := requireInternalCaller(s.k8s, s.internalAllowedSubjects)
	protect(s.doInternalPatchSession)(w, r)
}

func (s *appServer) doInternalPatchSession(w http.ResponseWriter, r *http.Request) {
	email, _ := s.resolveCallerEmail(r)
	if email == "" {
		writeError(w, http.StatusBadRequest, "could not resolve caller email")
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
	info, err := s.mgr.SetName(r.Context(), email, sessionID, body.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleInternalSessionCapabilities returns the skills and MCP tools visible inside a session pod.
func (s *appServer) handleInternalSessionCapabilities(w http.ResponseWriter, r *http.Request) {
	protect := requireInternalCaller(s.k8s, s.internalAllowedSubjects)
	protect(s.doInternalSessionCapabilities)(w, r)
}

func (s *appServer) doInternalSessionCapabilities(w http.ResponseWriter, r *http.Request) {
	email, _ := s.resolveCallerEmail(r)
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
	protect := requireInternalCaller(s.k8s, s.internalAllowedSubjects)
	protect(s.doInternalSetTestState)(w, r)
}

func (s *appServer) doInternalSetTestState(w http.ResponseWriter, r *http.Request) {
	email, _ := s.resolveCallerEmail(r)
	if email == "" {
		writeError(w, http.StatusBadRequest, "could not resolve caller email")
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
	info, err := s.mgr.SetTestState(r.Context(), email, sessionID, body.Active, body.SlotIndex, body.URL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleInternalSetRolloutState sets the rollout state for a session.
func (s *appServer) handleInternalSetRolloutState(w http.ResponseWriter, r *http.Request) {
	protect := requireInternalCaller(s.k8s, s.internalAllowedSubjects)
	protect(s.doInternalSetRolloutState)(w, r)
}

func (s *appServer) doInternalSetRolloutState(w http.ResponseWriter, r *http.Request) {
	email, _ := s.resolveCallerEmail(r)
	if email == "" {
		writeError(w, http.StatusBadRequest, "could not resolve caller email")
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
	info, err := s.mgr.SetRolloutState(r.Context(), email, sessionID, body.Active)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleInternalSendMessage enqueues a follow-up turn to a chat-capable session.
func (s *appServer) handleInternalSendMessage(w http.ResponseWriter, r *http.Request) {
	protect := requireInternalCaller(s.k8s, s.internalAllowedSubjects)
	protect(s.doInternalSendMessage)(w, r)
}

func (s *appServer) doInternalSendMessage(w http.ResponseWriter, r *http.Request) {
	email, _ := s.resolveCallerEmail(r)
	if email == "" {
		writeError(w, http.StatusBadRequest, "could not resolve caller email")
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

	resp, status, detail := s.enqueueSDKTurn(r.Context(), email, sessionID, sdkTurnRequest{
		Prompt:         body.Prompt,
		Model:          body.Model,
		PermissionMode: body.PermissionMode,
		SkillName:      body.SkillName,
		FollowUp:       true,
	})
	if detail != "" {
		writeError(w, status, detail)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// resolveCallerEmail resolves the caller's email from caller_pod_ip query param.
func (s *appServer) resolveCallerEmail(r *http.Request) (email, podName string) {
	callerPodIP := r.URL.Query().Get("caller_pod_ip")
	if callerPodIP == "" {
		return "", ""
	}
	email, podName, err := s.mgr.FindPodByIP(r.Context(), callerPodIP)
	if err != nil {
		return "", ""
	}
	return email, podName
}
