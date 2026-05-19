package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
)

const tankMessageLinkScriptID = "tank-message-link"

func isTankMessageLinkRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	sessionID, timelineID := tankMessageLinkParts(r)
	return sessionID != "" && timelineID != ""
}

func wantsTankMessageLinkJSON(r *http.Request) bool {
	q := r.URL.Query()
	switch strings.ToLower(strings.TrimSpace(q.Get("format"))) {
	case "json", "api":
		return true
	}
	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	if accept == "" || accept == "*/*" {
		return true
	}
	if strings.Contains(accept, "application/json") {
		return true
	}
	return !strings.Contains(accept, "text/html")
}

func tankMessageLinkParts(r *http.Request) (string, string) {
	q := r.URL.Query()
	sessionID := strings.TrimSpace(q.Get("session"))
	timelineID := strings.TrimSpace(q.Get("message"))
	if timelineID == "" {
		timelineID = strings.TrimSpace(q.Get("timeline_id"))
	}
	return sessionID, timelineID
}

func tankMessageLinkContract(r *http.Request) map[string]any {
	sessionID, timelineID := tankMessageLinkParts(r)
	origin := requestOrigin(r)
	browserURL := absoluteURL(origin, r.URL)
	jsonURL := cloneURL(r.URL)
	jsonQuery := jsonURL.Query()
	jsonQuery.Set("format", "json")
	jsonURL.RawQuery = jsonQuery.Encode()
	timelineURL := &url.URL{
		Path: "/api/sessions/" + url.PathEscape(sessionID) + "/timeline",
	}
	timelineQuery := url.Values{}
	timelineQuery.Set("message", timelineID)
	timelineQuery.Set("num_before", "100")
	timelineQuery.Set("num_after", "100")
	timelineURL.RawQuery = timelineQuery.Encode()
	sessionURL := &url.URL{Path: "/api/sessions/" + url.PathEscape(sessionID)}

	return map[string]any{
		"kind":        "tank.message_link",
		"version":     1,
		"session_id":  sessionID,
		"timeline_id": timelineID,
		"message":     timelineID,
		"browser_url": browserURL,
		"json_url":    absoluteURL(origin, jsonURL),
		"api": map[string]any{
			"session_url":  absoluteURL(origin, sessionURL),
			"timeline_url": absoluteURL(origin, timelineURL),
		},
		"usage": []string{
			"Use the timeline_url with Tank authentication to fetch a bounded durable transcript page around the linked message.",
			"Equivalently, request this same URL with Accept: application/json or ?format=json; authenticated callers receive the resolved timeline payload inline.",
			"From a Tank session pod, exchange /run/secrets/auth.romaine.life/token at https://auth.romaine.life/api/auth/exchange/k8s, POST that auth_jwt to this origin's /api/auth/exchange, then call timeline_url with Authorization: Bearer <tank-token>.",
		},
	}
}

func (s *appServer) handleTankMessageLink(w http.ResponseWriter, r *http.Request) {
	setTankMessageLinkHeaders(w, r)
	w.Header().Set("Cache-Control", "no-store")
	body := tankMessageLinkContract(r)
	body["authenticated"] = false
	body["resolved"] = false

	if !tankMessageLinkHasCredentials(r) {
		body["auth_required"] = true
		writeJSON(w, http.StatusOK, body)
		return
	}
	if s.verifier == nil {
		body["detail"] = "JWT verifier not configured"
		writeJSON(w, http.StatusInternalServerError, body)
		return
	}
	user, err := s.verifier.CurrentUser(r)
	if err != nil {
		body["auth_required"] = true
		body["detail"] = err.Error()
		writeJSON(w, auth.ErrorStatus(err), body)
		return
	}
	attachAuthToRequest(r, user)

	sessionID, _ := tankMessageLinkParts(r)
	timeline, status, err := s.sessionTimelineBody(r.Context(), r, user, sessionID)
	if err != nil {
		body["detail"] = err.Error()
		writeJSON(w, status, body)
		return
	}
	body["authenticated"] = true
	body["resolved"] = true
	body["timeline"] = timeline
	if v, ok := timeline["target_order_key"]; ok {
		body["target_order_key"] = v
	}
	writeJSON(w, http.StatusOK, body)
}

func tankMessageLinkHasCredentials(r *http.Request) bool {
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" {
		return true
	}
	if cookie, err := r.Cookie(auth.CookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		return true
	}
	return false
}

func serveTankStaticIndexWithMessageLink(w http.ResponseWriter, r *http.Request, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contract, err := json.Marshal(tankMessageLinkContract(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	script := fmt.Sprintf("\n<script id=%q type=\"application/json\">%s</script>\n", tankMessageLinkScriptID, contract)
	headTags := tankMessageLinkHeadTags(r)
	html := string(data)
	if strings.Contains(html, "</head>") {
		html = strings.Replace(html, "</head>", headTags+"</head>", 1)
	}
	if strings.Contains(html, "</body>") {
		html = strings.Replace(html, "</body>", script+"</body>", 1)
	} else {
		html += script
	}
	setTankMessageLinkHeaders(w, r)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

func setTankMessageLinkHeaders(w http.ResponseWriter, r *http.Request) {
	contract := tankMessageLinkContract(r)
	jsonURL, _ := contract["json_url"].(string)
	if jsonURL != "" {
		w.Header().Add("Link", fmt.Sprintf("<%s>; rel=\"alternate\"; type=\"application/json\"; title=\"Tank message link contract\"", jsonURL))
	}
	if api, ok := contract["api"].(map[string]any); ok {
		if timelineURL, _ := api["timeline_url"].(string); timelineURL != "" {
			w.Header().Add("Link", fmt.Sprintf("<%s>; rel=\"related\"; type=\"application/json\"; title=\"Tank timeline window\"", timelineURL))
		}
	}
}

func tankMessageLinkHeadTags(r *http.Request) string {
	contract := tankMessageLinkContract(r)
	jsonURL, _ := contract["json_url"].(string)
	tags := ""
	if jsonURL != "" {
		tags += fmt.Sprintf("\n<link rel=\"alternate\" type=\"application/json\" title=\"Tank message link contract\" href=\"%s\" />", html.EscapeString(jsonURL))
	}
	if api, ok := contract["api"].(map[string]any); ok {
		if timelineURL, _ := api["timeline_url"].(string); timelineURL != "" {
			tags += fmt.Sprintf("\n<link rel=\"related\" type=\"application/json\" title=\"Tank timeline window\" href=\"%s\" />", html.EscapeString(timelineURL))
		}
	}
	if tags != "" {
		tags += "\n"
	}
	return tags
}

func requestOrigin(r *http.Request) string {
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	if host == "" {
		host = "tank.romaine.life"
	}
	return proto + "://" + host
}

func absoluteURL(origin string, u *url.URL) string {
	if u == nil {
		return origin
	}
	clone := cloneURL(u)
	clone.Scheme = ""
	clone.Host = ""
	return strings.TrimRight(origin, "/") + clone.String()
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return &url.URL{}
	}
	v := *u
	return &v
}
