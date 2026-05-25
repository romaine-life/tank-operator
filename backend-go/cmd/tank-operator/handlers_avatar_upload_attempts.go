package main

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/avataruploads"
)

const avatarUploadRoute = "POST /api/admin/avatars"

const debugAvatarUploadAttemptsDescription = `Durable diagnostic surface for admin avatar uploads.

Each failed POST /api/admin/avatars response includes attempt_id. Query
?attempt_id=<id> to see the server-side stage, bounded result, safe request
metadata, parsed field summaries, and parser/store diagnostics without using
browser devtools. Query without attempt_id for the most recent attempts on
this replica's backing store.`

func (s *appServer) newAvatarUploadAttempt(r *http.Request, user auth.User) (avataruploads.Attempt, error) {
	if s.avatarUploads == nil {
		return avataruploads.Attempt{}, errors.New("avatar upload attempt store not configured")
	}
	now := time.Now().UTC()
	attempt := avataruploads.Attempt{
		ID:               "avu_" + auth.RandomHex(12),
		Operation:        "create",
		ActorEmail:       strings.ToLower(strings.TrimSpace(user.OwnerEmail())),
		ActorRole:        avatarUploadActorRole(user),
		Method:           r.Method,
		Route:            avatarUploadRoute,
		ContentType:      strings.TrimSpace(r.Header.Get("Content-Type")),
		ContentTypeClass: avatarUploadContentTypeClass(r.Header.Get("Content-Type")),
		ContentLength:    r.ContentLength,
		Stage:            "received",
		Result:           "started",
		Fields:           map[string]avataruploads.FieldSummary{},
		Diagnostics:      map[string]string{},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.avatarUploads.Upsert(r.Context(), attempt); err != nil {
		return avataruploads.Attempt{}, err
	}
	recordAvatarUploadAttempt(attempt.Stage, attempt.Result)
	return attempt, nil
}

func (s *appServer) recordAvatarUploadAttemptState(r *http.Request, attempt *avataruploads.Attempt, stage, result, detail string) {
	if attempt == nil || s.avatarUploads == nil {
		return
	}
	attempt.Stage = stage
	attempt.Result = result
	attempt.Detail = detail
	attempt.UpdatedAt = time.Now().UTC()
	recordAvatarUploadAttempt(stage, result)
	if err := s.avatarUploads.Upsert(r.Context(), *attempt); err != nil {
		slog.Warn("avatar upload attempt record failed",
			"attempt_id", attempt.ID,
			"stage", stage,
			"result", result,
			"error", err,
		)
	}
}

func (s *appServer) writeAvatarUploadFailure(w http.ResponseWriter, r *http.Request, attempt *avataruploads.Attempt, status int, stage, result, code, detail string) {
	s.recordAvatarUploadAttemptState(r, attempt, stage, result, detail)
	if attempt != nil {
		slog.Warn("avatar upload failed",
			"attempt_id", attempt.ID,
			"stage", stage,
			"result", result,
			"email", attempt.ActorEmail,
			"role", attempt.ActorRole,
			"content_type_class", attempt.ContentTypeClass,
			"content_length", attempt.ContentLength,
			"detail", detail,
		)
		writeJSON(w, status, map[string]string{
			"detail":     detail,
			"code":       code,
			"attempt_id": attempt.ID,
		})
		return
	}
	writeJSON(w, status, map[string]string{
		"detail": detail,
		"code":   code,
	})
}

func avatarUploadContentTypeClass(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "missing"
	}
	mediaType, params, err := mime.ParseMediaType(raw)
	if err != nil {
		return "invalid"
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch {
	case mediaType == "multipart/form-data":
		if strings.TrimSpace(params["boundary"]) == "" {
			return "multipart_missing_boundary"
		}
		return "multipart_form_data"
	case strings.HasPrefix(mediaType, "multipart/"):
		return "multipart_other"
	default:
		return "wrong_media_type"
	}
}

func classifyAvatarMultipartFailure(err error, contentTypeClass string) (result, code, detail string) {
	switch contentTypeClass {
	case "missing":
		return "wrong_media_type", "missing_content_type", "Avatar upload request must use multipart/form-data."
	case "invalid":
		return "wrong_media_type", "invalid_content_type", "Avatar upload request Content-Type is not valid."
	case "multipart_missing_boundary":
		return "missing_boundary", "missing_multipart_boundary", "Avatar upload request is multipart/form-data but is missing its boundary."
	case "multipart_other", "wrong_media_type":
		return "wrong_media_type", "wrong_content_type", "Avatar upload request must use multipart/form-data."
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "request body too large") {
		return "body_too_large", "upload_too_large", "Avatar upload exceeds the maximum allowed request size."
	}
	return "parse_error", "bad_multipart", "Avatar upload request was not valid multipart/form-data."
}

func avatarUploadReadErrorCode(result string) string {
	switch result {
	case "missing_field":
		return "missing_image_field"
	case "empty_file":
		return "empty_image"
	case "field_too_large":
		return "image_too_large"
	case "invalid_mime":
		return "invalid_image_type"
	case "read_error":
		return "image_read_failed"
	default:
		return "invalid_image"
	}
}

func avatarUploadDiagnosticValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

func (s *appServer) handleDebugAvatarUploadAttempts(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.avatarUploads == nil {
		writeError(w, http.StatusServiceUnavailable, "avatar upload attempt store not configured")
		return
	}
	attemptID := strings.TrimSpace(r.URL.Query().Get("attempt_id"))
	limit := 20
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	if limit > 100 {
		limit = 100
	}
	attempts, err := s.avatarUploads.List(r.Context(), avataruploads.Filter{
		ID:    attemptID,
		Limit: limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if attemptID != "" && len(attempts) == 0 {
		writeError(w, http.StatusNotFound, "avatar upload attempt not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"description": debugAvatarUploadAttemptsDescription,
		"attempts":    attempts,
		"count":       len(attempts),
		"fetched_at":  time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func avatarUploadActorRole(user auth.User) string {
	if hasAdminPower(user) {
		return auth.RoleAdmin
	}
	return user.Role
}
