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
	shareToken := tankMessageLinkShareToken(r)
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
	timelineQuery.Set("rows_before", "12")
	timelineQuery.Set("rows_after", "12")
	timelineURL.RawQuery = timelineQuery.Encode()
	sessionURL := &url.URL{Path: "/api/sessions/" + url.PathEscape(sessionID)}
	beforeURL := &url.URL{
		Path: "/api/sessions/" + url.PathEscape(sessionID) + "/timeline",
	}
	beforeQuery := url.Values{}
	beforeQuery.Set("before_cursor", "<prev_cursor>")
	beforeQuery.Set("rows", "8")
	beforeURL.RawQuery = beforeQuery.Encode()

	body := map[string]any{
		"kind":        "tank.message_link",
		"version":     1,
		"session_id":  sessionID,
		"timeline_id": timelineID,
		"message":     timelineID,
		"browser_url": browserURL,
		"json_url":    absoluteURL(origin, jsonURL),
		"api": map[string]any{
			"session_url":     absoluteURL(origin, sessionURL),
			"timeline_url":    absoluteURL(origin, timelineURL),
			"page_before_url": absoluteURL(origin, beforeURL),
		},
		"agent_recipe": []map[string]string{
			{
				"step":    "1",
				"purpose": "Fetch this URL. Generic agent fetches receive this JSON contract; browser navigations receive HTML with the same contract in script#tank-message-link and Link headers.",
				"curl":    "curl -fsS " + shellQuoteForDocs(browserURL),
			},
			{
				"step":    "2",
				"purpose": "If auth_required is true and you are in a Tank session pod, exchange the projected auth.romaine.life service-account token. The token must be sent as an Authorization bearer header; do not send it as JSON.",
				"curl":    "AUTH_JWT=$(curl -fsS -X POST https://auth.romaine.life/api/auth/exchange/k8s -H \"Authorization: Bearer $(cat /run/secrets/auth.romaine.life/token)\" -H 'Content-Type: application/json' -d '{}' | jq -r .token)",
			},
			{
				"step":    "3",
				"purpose": "Fetch the resolved transcript window around the linked message.",
				"curl":    "curl -fsS " + shellQuoteForDocs(absoluteURL(origin, jsonURL)) + " -H \"Authorization: Bearer $AUTH_JWT\"",
			},
			{
				"step":    "4",
				"purpose": "If the returned timeline has found_oldest=false and you need earlier context, keep paging backward with prev_cursor until found_oldest=true or you have enough context.",
				"curl":    "curl -fsS " + shellQuoteForDocs(absoluteURL(origin, beforeURL)) + " -H \"Authorization: Bearer $AUTH_JWT\"",
			},
		},
		"usage": []string{
			"Use the timeline_url with Tank authentication to fetch a bounded durable transcript page around the linked message.",
			"Equivalently, request this same URL with Accept: application/json or ?format=json; authenticated callers receive the resolved timeline payload inline.",
			"From a Tank session pod, exchange /run/secrets/auth.romaine.life/token at https://auth.romaine.life/api/auth/exchange/k8s using Authorization: Bearer <service-account-token>, then call timeline_url with Authorization: Bearer <auth-token>.",
			"If found_oldest is false, use prev_cursor as before_cursor to page backward for earlier transcript context.",
		},
	}
	if shareToken != "" {
		publicURL := &url.URL{Path: "/api/public/message-links/" + url.PathEscape(shareToken)}
		publicTimelineURL := &url.URL{
			Path: "/api/public/message-links/" + url.PathEscape(shareToken) + "/timeline",
		}
		publicTimelineQuery := url.Values{}
		publicTimelineQuery.Set("message", timelineID)
		publicTimelineQuery.Set("rows_before", "12")
		publicTimelineQuery.Set("rows_after", "12")
		publicTimelineURL.RawQuery = publicTimelineQuery.Encode()
		body["share_token_present"] = true
		body["public_api"] = map[string]any{
			"share_url":    absoluteURL(origin, publicURL),
			"timeline_url": absoluteURL(origin, publicTimelineURL),
		}
		body["usage"] = append(body["usage"].([]string),
			"When share_token_present is true, public_api.timeline_url resolves a read-only transcript page without Tank authentication.",
		)
	}
	return body
}

func (s *appServer) handleTankMessageLink(w http.ResponseWriter, r *http.Request) {
	setTankMessageLinkHeaders(w, r)
	w.Header().Set("Cache-Control", "no-store")
	body := tankMessageLinkContract(r)
	body["authenticated"] = false
	body["resolved"] = false

	if shareToken := tankMessageLinkShareToken(r); shareToken != "" {
		share, info, status, err := s.resolvePublicMessageLink(r.Context(), shareToken)
		if err != nil {
			recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
			body["detail"] = err.Error()
			writeJSON(w, status, body)
			return
		}
		timeline, status, err := s.publicMessageLinkTimelineBody(r.Context(), r, share, info)
		if err != nil {
			recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
			body["detail"] = err.Error()
			writeJSON(w, status, body)
			return
		}
		recordMessageLinkShare("resolve", "ok")
		body["auth_required"] = false
		body["public"] = true
		body["resolved"] = true
		body["session"] = publicMessageLinkSessionBody(info)
		body["timeline"] = timeline
		if v, ok := timeline["target_cursor"]; ok {
			body["target_cursor"] = v
		}
		writeJSON(w, http.StatusOK, body)
		return
	}

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
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		body["detail"] = scopeErr.Error()
		writeJSON(w, status, body)
		return
	}
	timeline, status, err := s.sessionTimelineBody(r.Context(), r, user, sessionID, sessionScope)
	if err != nil {
		body["detail"] = err.Error()
		writeJSON(w, status, body)
		return
	}
	body["authenticated"] = true
	body["resolved"] = true
	body["timeline"] = timeline
	if v, ok := timeline["target_cursor"]; ok {
		body["target_cursor"] = v
	}
	writeJSON(w, http.StatusOK, body)
}

func tankMessageLinkHasCredentials(r *http.Request) bool {
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" {
		return true
	}
	return false
}

func tankMessageLinkShareToken(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return strings.TrimSpace(r.URL.Query().Get("share"))
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
	if publicAPI, ok := contract["public_api"].(map[string]any); ok {
		if timelineURL, _ := publicAPI["timeline_url"].(string); timelineURL != "" {
			w.Header().Add("Link", fmt.Sprintf("<%s>; rel=\"related\"; type=\"application/json\"; title=\"Tank public timeline window\"", timelineURL))
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
	if publicAPI, ok := contract["public_api"].(map[string]any); ok {
		if timelineURL, _ := publicAPI["timeline_url"].(string); timelineURL != "" {
			tags += fmt.Sprintf("\n<link rel=\"related\" type=\"application/json\" title=\"Tank public timeline window\" href=\"%s\" />", html.EscapeString(timelineURL))
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

func shellQuoteForDocs(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
