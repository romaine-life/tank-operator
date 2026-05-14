package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

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
			// The internal API is already narrowed by exact service-account
			// allowlist. Accept the caller's normal Kubernetes API token so
			// MCP servers do not need a second projected token just for Tank.
			subject, err := auth.ValidateSAToken(r.Context(), k8s, token, nil)
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
