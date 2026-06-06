package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/avatarassets"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

const messageLinkShareTokenBytes = 32

func (s *appServer) handleCreateMessageLinkShare(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if s.messageLinkShares == nil {
		recordMessageLinkShare("create", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "message link share store not configured")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		recordMessageLinkShare("create", "denied")
		writeError(w, status, scopeErr.Error())
		return
	}
	var body struct {
		TimelineID string `json:"timeline_id"`
		Message    string `json:"message"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	}
	timelineID := strings.TrimSpace(body.TimelineID)
	if timelineID == "" {
		timelineID = strings.TrimSpace(body.Message)
	}
	if timelineID == "" {
		timelineID = strings.TrimSpace(r.URL.Query().Get("timeline_id"))
	}
	if timelineID == "" {
		timelineID = strings.TrimSpace(r.URL.Query().Get("message"))
	}
	if timelineID == "" {
		recordMessageLinkShare("create", "bad_request")
		writeError(w, http.StatusBadRequest, "timeline_id is required")
		return
	}

	owner := user.OwnerEmail()
	info, err := s.getRegisteredByOwnerInScope(r.Context(), owner, sessionID, sessionScope)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			recordMessageLinkShare("create", "not_found")
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		recordMessageLinkShare("create", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	targetCursor, err := s.sessionTranscriptRowStoreForScope(sessionScope).ResolveCursorForTimelineID(r.Context(), sessionID, timelineID)
	if err != nil {
		recordMessageLinkShare("create", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if targetCursor == "" {
		recordMessageLinkShare("create", "not_found")
		writeError(w, http.StatusNotFound, "timeline target not found")
		return
	}

	var token string
	for attempt := 0; attempt < 3; attempt++ {
		token = auth.RandomHex(messageLinkShareTokenBytes)
		err = s.messageLinkShares.Create(r.Context(), pgstore.MessageLinkShare{
			Token:        token,
			CreatedBy:    user.OwnerEmail(),
			OwnerEmail:   owner,
			SessionScope: sessionScope,
			SessionID:    sessionID,
			TimelineID:   timelineID,
		})
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "collision") {
			break
		}
	}
	if err != nil {
		recordMessageLinkShare("create", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordMessageLinkShare("create", "ok")
	writeJSON(w, http.StatusCreated, map[string]any{
		"kind":        "tank.message_link_share",
		"version":     1,
		"token":       token,
		"session":     publicMessageLinkSessionBody(info),
		"user":        publicMessageLinkUserBody(info),
		"session_id":  sessionID,
		"timeline_id": timelineID,
		"message":     timelineID,
		"browser_url": publicMessageLinkBrowserURL(r, sessionID, timelineID, token),
		"api": map[string]string{
			"public_url":   absoluteURL(requestOrigin(r), &url.URL{Path: "/api/public/message-links/" + url.PathEscape(token)}),
			"timeline_url": absoluteURL(requestOrigin(r), &url.URL{Path: "/api/public/message-links/" + url.PathEscape(token) + "/timeline"}),
		},
	})
}

func (s *appServer) handleGetPublicMessageLink(w http.ResponseWriter, r *http.Request) {
	share, info, status, err := s.resolvePublicMessageLink(r.Context(), r.PathValue("share_token"))
	if err != nil {
		recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
		writeError(w, status, err.Error())
		return
	}
	recordMessageLinkShare("resolve", "ok")
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":        "tank.message_link_public",
		"version":     1,
		"session":     publicMessageLinkSessionBody(info),
		"user":        publicMessageLinkUserBody(info),
		"session_id":  share.SessionID,
		"timeline_id": share.TimelineID,
		"message":     share.TimelineID,
	})
}

func (s *appServer) handlePublicMessageLinkTimeline(w http.ResponseWriter, r *http.Request) {
	share, info, status, err := s.resolvePublicMessageLink(r.Context(), r.PathValue("share_token"))
	if err != nil {
		recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
		writeError(w, status, err.Error())
		return
	}
	body, status, err := s.publicMessageLinkTimelineBody(r.Context(), r, share, info)
	if err != nil {
		recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
		writeError(w, status, err.Error())
		return
	}
	recordMessageLinkShare("resolve", "ok")
	writeJSON(w, http.StatusOK, body)
}

func (s *appServer) handlePublicMessageLinkTurnActivity(w http.ResponseWriter, r *http.Request) {
	share, _, status, err := s.resolvePublicMessageLink(r.Context(), r.PathValue("share_token"))
	if err != nil {
		recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
		writeError(w, status, err.Error())
		return
	}
	turnID := strings.TrimSpace(r.PathValue("turn_id"))
	if turnID == "" {
		recordMessageLinkShare("resolve", "bad_request")
		writeError(w, http.StatusBadRequest, "turn_id is required")
		return
	}
	events, err := readUserFacingTurnEvents(r.Context(), s.sessionEventStoreForScope(share.SessionScope), share.SessionID, turnID)
	if err != nil {
		recordMessageLinkShare("resolve", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pages := projectTurnPages(turnID, events)
	selected := defaultTurnActivityPageNumber(pages)
	if requested := strings.TrimSpace(r.URL.Query().Get("page")); requested != "" {
		if n, convErr := strconv.Atoi(requested); convErr == nil {
			selected = n
		}
	}
	if selected < 1 {
		selected = 1
	}
	if selected > len(pages.Pages) {
		selected = len(pages.Pages)
	}
	directory := pages.Shell["pages"]
	if directory == nil {
		directory = []map[string]any{}
	}
	body := map[string]any{
		"session_id":          share.SessionID,
		"turn_id":             turnID,
		"entries":             []map[string]any{},
		"compacted_entry_ids": []string{},
		"summary":             pages.Shell,
		"turn_context":        pages.TurnContext,
		"page":                selected,
		"page_count":          len(pages.Pages),
		"pages":               directory,
		"total_event_count":   pages.TotalEventCount,
		"has_more":            false,
		"cursor_semantic":     "order_key",
		"projection":          "server_turn_activity_v3",
		"public":              true,
	}
	if selected >= 1 && selected <= len(pages.Pages) {
		current := pages.Pages[selected-1]
		entries := current.Entries
		if entries == nil {
			entries = []map[string]any{}
		}
		body["entries"] = entries
		body["sealed"] = current.Sealed
		body["page_kind"] = current.Kind
		if current.Kind == "question" {
			body["question_count"] = current.QuestionCount
			body["question_index"] = current.QuestionIndex
			body["question_set"] = current.QuestionSet
			body["answered"] = current.Answered
		}
		body["page_start_order_key"] = current.StartOrderKey
		body["page_end_order_key"] = current.EndOrderKey
		body["has_more"] = selected < len(pages.Pages)
	}
	recordMessageLinkShare("resolve", "ok")
	writeJSON(w, http.StatusOK, body)
}

func (s *appServer) handlePublicMessageLinkAvatars(w http.ResponseWriter, r *http.Request) {
	share, info, status, err := s.resolvePublicMessageLink(r.Context(), r.PathValue("share_token"))
	if err != nil {
		recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
		writeError(w, status, err.Error())
		return
	}
	entries := []avatarAssetResponse{}
	if s.avatars != nil {
		for _, id := range publicMessageLinkAvatarIDs(info) {
			meta, err := s.avatars.Get(r.Context(), id)
			if err != nil {
				if errors.Is(err, avatarassets.ErrNotFound) {
					continue
				}
				recordAvatarAssetRequest("public_list", "", "store_error")
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			entries = append(entries, publicMessageLinkAvatarResponseFromMeta(share.Token, meta))
		}
	}
	recordAvatarAssetRequest("public_list", "", "ok")
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"public":  true,
	})
}

func (s *appServer) handlePublicMessageLinkAvatarImage(w http.ResponseWriter, r *http.Request) {
	s.handlePublicMessageLinkAvatarBinary(w, r, avatarassets.VariantAvatar)
}

func (s *appServer) handlePublicMessageLinkAvatarBacking(w http.ResponseWriter, r *http.Request) {
	s.handlePublicMessageLinkAvatarBinary(w, r, avatarassets.VariantBacking)
}

func (s *appServer) handlePublicMessageLinkAvatarBinary(w http.ResponseWriter, r *http.Request, variant string) {
	_, info, status, err := s.resolvePublicMessageLink(r.Context(), r.PathValue("share_token"))
	if err != nil {
		recordMessageLinkShare("resolve", messageLinkShareResolveResult(status, err))
		writeError(w, status, err.Error())
		return
	}
	id := strings.TrimSpace(r.PathValue("avatar_id"))
	if id == "" {
		recordAvatarAssetRequest("public_read_image", "", "bad_request")
		writeError(w, http.StatusBadRequest, "missing avatar id")
		return
	}
	if !publicMessageLinkSessionAllowsAvatar(info, id) {
		recordAvatarAssetRequest("public_read_image", "", "not_found")
		writeError(w, http.StatusNotFound, "avatar not found")
		return
	}
	s.writeAvatarBinary(w, r, id, variant)
}

func (s *appServer) resolvePublicMessageLink(ctx context.Context, token string) (pgstore.MessageLinkShare, sessions.Info, int, error) {
	if s.messageLinkShares == nil {
		return pgstore.MessageLinkShare{}, sessions.Info{}, http.StatusServiceUnavailable, errors.New("message link share store not configured")
	}
	share, err := s.messageLinkShares.Get(ctx, token)
	if err != nil {
		if errors.Is(err, pgstore.ErrMessageLinkShareInvalid) {
			return pgstore.MessageLinkShare{}, sessions.Info{}, http.StatusNotFound, errors.New("message link not found")
		}
		return pgstore.MessageLinkShare{}, sessions.Info{}, http.StatusInternalServerError, err
	}
	info, err := s.getRegisteredByOwnerInScope(ctx, share.OwnerEmail, share.SessionID, share.SessionScope)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			return pgstore.MessageLinkShare{}, sessions.Info{}, http.StatusNotFound, errors.New("session not found")
		}
		return pgstore.MessageLinkShare{}, sessions.Info{}, http.StatusInternalServerError, err
	}
	return share, info, http.StatusOK, nil
}

func (s *appServer) publicMessageLinkTimelineBody(ctx context.Context, r *http.Request, share pgstore.MessageLinkShare, info sessions.Info) (map[string]any, int, error) {
	timelineRequest := requestWithDefaultMessageLinkTimeline(r, share.TimelineID)
	intent, status, err := sessionTranscriptReadIntentFromRequest(timelineRequest)
	if err != nil {
		return nil, status, err
	}
	recordSessionEventTimelineRequest(intent.metricLabel)
	rowStore := s.sessionTranscriptRowStoreForScope(share.SessionScope)
	page, targetCursor, status, err := runSessionTranscriptRowRead(ctx, rowStore, share.SessionID, intent)
	if err != nil {
		return nil, status, err
	}
	liveOrderKey := ""
	if live, err := s.sessionEventStoreForScope(share.SessionScope).LatestEvents(ctx, share.SessionID, 1); err == nil && len(live.Events) > 0 {
		liveOrderKey = transcriptString(live.Events[len(live.Events)-1], "order_key")
	} else if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	body := map[string]any{
		"session_id":        share.SessionID,
		"rows":              page.Rows,
		"projection":        "server_transcript_rows_v1",
		"next_cursor":       page.NextCursor,
		"prev_cursor":       page.PrevCursor,
		"found_oldest":      page.FoundOldest,
		"found_newest":      page.FoundNewest,
		"anchor":            intent.responseAnchor,
		"cursor_semantic":   "transcript_row",
		"live_order_key":    liveOrderKey,
		"activity":          info.Activity,
		"read_state":        nil,
		"public":            true,
		"user":              publicMessageLinkUserBody(info),
		"share_timeline_id": share.TimelineID,
	}
	if intent.timelineID != "" {
		body["target_timeline_id"] = intent.timelineID
		body["target_cursor"] = targetCursor
	}
	return body, http.StatusOK, nil
}

func requestWithDefaultMessageLinkTimeline(r *http.Request, timelineID string) *http.Request {
	q := r.URL.Query()
	if strings.TrimSpace(q.Get("anchor")) != "" ||
		strings.TrimSpace(q.Get("before_cursor")) != "" ||
		strings.TrimSpace(q.Get("timeline_id")) != "" ||
		strings.TrimSpace(q.Get("message_id")) != "" ||
		strings.TrimSpace(q.Get("message")) != "" {
		return r
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	next := cloneURL(&u)
	q = next.Query()
	q.Set("timeline_id", timelineID)
	q.Set("rows_before", fmt.Sprint(sessionTranscriptAroundRowsDefault))
	q.Set("rows_after", fmt.Sprint(sessionTranscriptAroundRowsDefault))
	next.RawQuery = q.Encode()
	clone.URL = next
	return clone
}

func publicMessageLinkSessionBody(info sessions.Info) map[string]any {
	return map[string]any{
		"id":                    info.ID,
		"session_scope":         info.SessionScope,
		"pod_name":              nil,
		"owner":                 "",
		"status":                info.Status,
		"mode":                  info.Mode,
		"requested_at":          info.RequestedAt,
		"created_at":            info.CreatedAt,
		"ready_at":              info.ReadyAt,
		"name":                  info.Name,
		"test_state":            nil,
		"rollout_state":         nil,
		"repos":                 []string{},
		"clone_state":           nil,
		"row_version":           int64(0),
		"sidebar_position":      int64(0),
		"activity":              info.Activity,
		"model":                 info.Model,
		"effort":                info.Effort,
		"runtime_model":         info.RuntimeModel,
		"runtime_effort":        info.RuntimeEffort,
		"runtime_configured_at": info.RuntimeConfiguredAt,
		"agent_avatar_id":       info.AgentAvatarID,
		"system_avatar_id":      info.SystemAvatarID,
	}
}

func publicMessageLinkUserBody(info sessions.Info) map[string]any {
	owner := strings.TrimSpace(info.Owner)
	avatarURL := ""
	if owner != "" {
		avatarURL = auth.GravatarURL(owner, 64)
	}
	return map[string]any{
		"name":       "Session owner",
		"avatar_url": avatarURL,
	}
}

func publicMessageLinkAvatarIDs(info sessions.Info) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, id := range []string{info.AgentAvatarID, info.SystemAvatarID} {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func publicMessageLinkSessionAllowsAvatar(info sessions.Info, avatarID string) bool {
	avatarID = strings.TrimSpace(avatarID)
	if avatarID == "" {
		return false
	}
	for _, allowed := range publicMessageLinkAvatarIDs(info) {
		if allowed == avatarID {
			return true
		}
	}
	return false
}

func publicMessageLinkAvatarResponseFromMeta(token string, meta avatarassets.Metadata) avatarAssetResponse {
	escapedToken := url.PathEscape(token)
	escapedID := url.PathEscape(meta.ID)
	basePath := "/api/public/message-links/" + escapedToken + "/avatars/" + escapedID
	avatarURL := basePath + "/image"
	if !meta.UpdatedAt.IsZero() {
		avatarURL += "?v=" + fmt.Sprint(meta.UpdatedAt.UTC().UnixNano())
	}
	return avatarAssetResponse{
		ID:         meta.ID,
		Kind:       meta.Kind,
		Name:       meta.Name,
		AvatarURL:  avatarURL,
		BackingURL: basePath + "/backing",
		Crop:       meta.Crop,
		CreatedAt:  meta.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  meta.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func publicMessageLinkBrowserURL(r *http.Request, sessionID, timelineID, token string) string {
	u := &url.URL{Path: "/"}
	q := url.Values{}
	q.Set("session", sessionID)
	q.Set("message", timelineID)
	q.Set("share", token)
	u.RawQuery = q.Encode()
	return absoluteURL(requestOrigin(r), u)
}

func messageLinkShareResolveResult(status int, err error) string {
	if status == http.StatusBadRequest {
		return "bad_request"
	}
	if status == http.StatusNotFound {
		return "not_found"
	}
	if status == http.StatusServiceUnavailable {
		return "store_unavailable"
	}
	if err != nil {
		return "store_error"
	}
	return "ok"
}
