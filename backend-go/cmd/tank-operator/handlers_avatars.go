package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/avatarassets"
	"github.com/romaine-life/tank-operator/backend-go/internal/avataruploads"
)

const (
	maxAvatarCropBytes      = 1048576
	maxAvatarBackingBytes   = 8388608
	maxAvatarMultipartBytes = maxAvatarCropBytes + maxAvatarBackingBytes + 1048576
)

var allowedAvatarUploadMIMEs = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/jpg":  {},
	"image/webp": {},
	"image/gif":  {},
	"image/avif": {},
	"image/bmp":  {},
}

type avatarAssetResponse struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	AvatarURL  string            `json:"avatar_url"`
	BackingURL string            `json:"backing_url"`
	Crop       avatarassets.Crop `json:"crop"`
	CreatedBy  string            `json:"created_by"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`
	AttemptID  string            `json:"attempt_id,omitempty"`
}

type avatarDeckResponse struct {
	Owner        string                   `json:"owner"`
	SessionScope string                   `json:"session_scope"`
	Decks        []avatarDeckKindResponse `json:"decks"`
}

type avatarDeckKindResponse struct {
	Kind    string                    `json:"kind"`
	Cycle   int64                     `json:"cycle"`
	Entries []avatarDeckEntryResponse `json:"entries"`
}

type avatarDeckEntryResponse struct {
	Position      int    `json:"position"`
	AvatarID      string `json:"avatar_id"`
	Name          string `json:"name"`
	AvatarURL     string `json:"avatar_url,omitempty"`
	Used          bool   `json:"used"`
	UsedSessionID string `json:"used_session_id,omitempty"`
	UsedAt        string `json:"used_at,omitempty"`
	Available     bool   `json:"available"`
}

func (s *appServer) handleListAvatars(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	if s.avatars == nil {
		recordAvatarAssetRequest("list", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "avatar store not configured")
		return
	}
	metas, err := s.avatars.List(r.Context())
	if err != nil {
		recordAvatarAssetRequest("list", "", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordAvatarAssetRequest("list", "", "ok")
	out := make([]avatarAssetResponse, 0, len(metas))
	for _, meta := range metas {
		out = append(out, avatarAssetResponseFromMeta(meta))
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

func (s *appServer) handleGetAvatarDecks(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdmin(w, r, "deck_list")
	if !ok {
		return
	}
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	registry := s.sessionRegistryForScope(sessionScope)
	if registry == nil {
		writeJSON(w, http.StatusOK, avatarDeckResponse{
			Owner:        user.OwnerEmail(),
			SessionScope: sessionScope,
			Decks:        []avatarDeckKindResponse{},
		})
		return
	}
	owner := listSessionsOwner(user, r)
	decks, err := registry.CurrentAvatarDecks(r.Context(), owner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := avatarDeckResponse{
		Owner:        owner,
		SessionScope: sessionScope,
		Decks:        make([]avatarDeckKindResponse, 0, len(decks)),
	}
	for _, deck := range decks {
		entries := make([]avatarDeckEntryResponse, 0, len(deck.Entries))
		for _, entry := range deck.Entries {
			resp := avatarDeckEntryResponse{
				Position:      entry.Position,
				AvatarID:      entry.AvatarID,
				Name:          entry.Name,
				Used:          entry.Used,
				UsedSessionID: entry.UsedSessionID,
				UsedAt:        entry.UsedAt,
				Available:     entry.Available,
			}
			if entry.Available {
				resp.AvatarURL = "/api/avatars/" + url.PathEscape(entry.AvatarID) + "/image"
			}
			entries = append(entries, resp)
		}
		out.Decks = append(out.Decks, avatarDeckKindResponse{
			Kind:    deck.Kind,
			Cycle:   deck.Cycle,
			Entries: entries,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *appServer) handleGetAvatarImage(w http.ResponseWriter, r *http.Request) {
	s.handleGetAvatarBinary(w, r, "avatar")
}

func (s *appServer) handleGetAvatarBacking(w http.ResponseWriter, r *http.Request) {
	s.handleGetAvatarBinary(w, r, "backing")
}

func (s *appServer) handleGetAvatarBinary(w http.ResponseWriter, r *http.Request, variant string) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("avatar_id"))
	if id == "" {
		recordAvatarAssetRequest("read_image", "", "bad_request")
		writeError(w, http.StatusBadRequest, "missing avatar id")
		return
	}
	s.writeAvatarBinary(w, r, id, variant)
}

func (s *appServer) writeAvatarBinary(w http.ResponseWriter, r *http.Request, id, variant string) {
	if s.avatars == nil || s.avatarImages == nil {
		recordAvatarAssetRequest("read_image", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "avatar store not configured")
		return
	}
	meta, err := s.avatars.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, avatarassets.ErrNotFound) {
			recordAvatarAssetRequest("read_image", "", "not_found")
			writeError(w, http.StatusNotFound, "avatar not found")
			return
		}
		recordAvatarAssetRequest("read_image", "", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	key, fallbackMIME, err := meta.ImageRef(variant)
	if err != nil || strings.TrimSpace(key) == "" {
		recordAvatarAssetRequest("read_image", "", "not_found")
		writeError(w, http.StatusNotFound, "avatar not found")
		return
	}
	img, err := s.avatarImages.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, avatarassets.ErrNotFound) {
			if serveDefaultAvatarAssetImage(w, r, meta) {
				recordAvatarAssetRequest("read_image", "", "ok")
				return
			}
			recordAvatarAssetRequest("read_image", "", "not_found")
			writeError(w, http.StatusNotFound, "avatar not found")
			return
		}
		recordAvatarAssetRequest("read_image", "", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if strings.TrimSpace(img.MIME) == "" {
		img.MIME = fallbackMIME
	}
	recordAvatarAssetRequest("read_image", "", "ok")
	w.Header().Set("Content-Type", img.MIME)
	w.Header().Set("Content-Length", fmt.Sprint(len(img.Bytes)))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img.Bytes)
}

func serveDefaultAvatarAssetImage(w http.ResponseWriter, r *http.Request, meta avatarassets.Metadata) bool {
	file, ok := defaultAvatarAssetFile(meta.ID)
	if !ok {
		return false
	}
	path, ok := tankStaticFile(tankStaticRoots(), "assets", "avatars", file)
	if !ok {
		return false
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, path)
	return true
}

func (s *appServer) handleCreateAvatar(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdmin(w, r, "create")
	if !ok {
		return
	}
	attempt, err := s.newAvatarUploadAttempt(r, user)
	if err != nil {
		recordAvatarAssetRequest("create", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "avatar upload attempt store unavailable")
		return
	}
	if s.avatars == nil || s.avatarImages == nil {
		recordAvatarAssetRequest("create", "", "store_unavailable")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusServiceUnavailable, "received", "store_unavailable", "avatar_store_unavailable", "avatar store not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarMultipartBytes)
	if err := r.ParseMultipartForm(maxAvatarMultipartBytes); err != nil {
		result, code, detail := classifyAvatarMultipartFailure(err, attempt.ContentTypeClass)
		attempt.Diagnostics["parser_error"] = avatarUploadDiagnosticValue(err.Error())
		recordAvatarAssetRequest("create", "", "bad_request")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusBadRequest, "parse_multipart", result, code, detail)
		return
	}
	attempt.Diagnostics["kind_present"] = fmt.Sprintf("%t", strings.TrimSpace(r.FormValue("kind")) != "")
	attempt.Diagnostics["name_present"] = fmt.Sprintf("%t", strings.TrimSpace(r.FormValue("name")) != "")
	attempt.Diagnostics["crop_present"] = fmt.Sprintf("%t", strings.TrimSpace(r.FormValue("crop")) != "")
	kind, ok := avatarassets.NormalizeKind(r.FormValue("kind"))
	if !ok {
		recordAvatarAssetRequest("create", "", "bad_request")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusBadRequest, "validate_kind", "invalid_kind", "invalid_kind", "kind must be agent or system")
		return
	}
	attempt.Kind = kind
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 80 {
		recordAvatarAssetRequest("create", kind, "bad_request")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusBadRequest, "validate_name", "invalid_name", "invalid_name", "name is required and must be 80 characters or fewer")
		return
	}
	crop, err := parseAvatarCrop(r.FormValue("crop"))
	if err != nil {
		recordAvatarAssetRequest("create", kind, "bad_request")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusBadRequest, "validate_crop", "invalid_crop", "invalid_crop", err.Error())
		return
	}
	avatarBytes, avatarMIME, avatarSummary, result, err := readAvatarUploadField(r, "avatar", maxAvatarCropBytes)
	attempt.Fields["avatar"] = avatarSummary
	if err != nil {
		recordAvatarAssetRequest("create", kind, "bad_request")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusBadRequest, "read_avatar", result, avatarUploadReadErrorCode(result), err.Error())
		return
	}
	backingBytes, backingMIME, backingSummary, result, err := readAvatarUploadField(r, "backing", maxAvatarBackingBytes)
	attempt.Fields["backing"] = backingSummary
	if err != nil {
		recordAvatarAssetRequest("create", kind, "bad_request")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusBadRequest, "read_backing", result, avatarUploadReadErrorCode(result), err.Error())
		return
	}

	id := "av_" + auth.RandomHex(12)
	attempt.AvatarID = id
	avatarKey := newUploadedAvatarBlobKey(id, avatarassets.VariantAvatar, avatarMIME)
	backingKey := newUploadedAvatarBlobKey(id, avatarassets.VariantBacking, backingMIME)
	if err := s.avatarImages.Put(r.Context(), avatarKey, avatarassets.Image{MIME: avatarMIME, Bytes: avatarBytes}); err != nil {
		attempt.Diagnostics["store_error"] = avatarUploadDiagnosticValue(err.Error())
		recordAvatarAssetRequest("create", kind, "store_error")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusInternalServerError, "store_avatar", "store_error", "avatar_store_failed", "failed to store avatar image")
		return
	}
	if err := s.avatarImages.Put(r.Context(), backingKey, avatarassets.Image{MIME: backingMIME, Bytes: backingBytes}); err != nil {
		attempt.Diagnostics["store_error"] = avatarUploadDiagnosticValue(err.Error())
		cleanupAvatarImageKeys(contextWithoutCancel(r.Context()), s.avatarImages, avatarKey)
		recordAvatarAssetRequest("create", kind, "store_error")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusInternalServerError, "store_backing", "store_error", "backing_store_failed", "failed to store backing image")
		return
	}

	meta, err := s.avatars.Create(r.Context(), avatarassets.NewAsset{
		ID:             id,
		Kind:           kind,
		Name:           name,
		Crop:           crop,
		AvatarMIME:     avatarMIME,
		AvatarBlobKey:  avatarKey,
		BackingMIME:    backingMIME,
		BackingBlobKey: backingKey,
		CreatedBy:      user.OwnerEmail(),
	})
	if err != nil {
		attempt.Diagnostics["store_error"] = avatarUploadDiagnosticValue(err.Error())
		cleanupAvatarImageKeys(contextWithoutCancel(r.Context()), s.avatarImages, avatarKey, backingKey)
		recordAvatarAssetRequest("create", kind, "store_error")
		s.writeAvatarUploadFailure(w, r, &attempt, http.StatusInternalServerError, "create_metadata", "store_error", "avatar_metadata_store_failed", "failed to store avatar metadata")
		return
	}
	s.recordAvatarUploadAttemptState(r, &attempt, "complete", "ok", "avatar saved")
	recordAvatarAssetRequest("create", kind, "ok")
	resp := avatarAssetResponseFromMeta(meta)
	resp.AttemptID = attempt.ID
	writeJSON(w, http.StatusCreated, resp)
}

func (s *appServer) handleUpdateAvatar(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireAdmin(w, r, "update")
	if !ok {
		return
	}
	if s.avatars == nil || s.avatarImages == nil {
		recordAvatarAssetRequest("update", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "avatar store not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("avatar_id"))
	if id == "" {
		recordAvatarAssetRequest("update", "", "bad_request")
		writeError(w, http.StatusBadRequest, "missing avatar id")
		return
	}
	current, err := s.avatars.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, avatarassets.ErrNotFound) {
			recordAvatarAssetRequest("update", "", "not_found")
			writeError(w, http.StatusNotFound, "avatar not found")
			return
		}
		recordAvatarAssetRequest("update", "", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if strings.TrimSpace(current.AvatarBlobKey) == "" {
		recordAvatarAssetRequest("update", current.Kind, "not_found")
		writeError(w, http.StatusNotFound, "avatar image not found")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarMultipartBytes)
	if err := r.ParseMultipartForm(maxAvatarMultipartBytes); err != nil {
		_, _, detail := classifyAvatarMultipartFailure(err, avatarUploadContentTypeClass(r.Header.Get("Content-Type")))
		recordAvatarAssetRequest("update", current.Kind, "bad_request")
		writeError(w, http.StatusBadRequest, detail)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 80 {
		recordAvatarAssetRequest("update", current.Kind, "bad_request")
		writeError(w, http.StatusBadRequest, "name is required and must be 80 characters or fewer")
		return
	}
	crop, err := parseAvatarCrop(r.FormValue("crop"))
	if err != nil {
		recordAvatarAssetRequest("update", current.Kind, "bad_request")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	avatarBytes, avatarMIME, _, _, err := readAvatarUploadField(r, "avatar", maxAvatarCropBytes)
	if err != nil {
		recordAvatarAssetRequest("update", current.Kind, "bad_request")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.avatarImages.Put(r.Context(), current.AvatarBlobKey, avatarassets.Image{MIME: avatarMIME, Bytes: avatarBytes}); err != nil {
		recordAvatarAssetRequest("update", current.Kind, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	meta, err := s.avatars.Update(r.Context(), id, avatarassets.UpdateAsset{
		Name:       name,
		Crop:       crop,
		AvatarMIME: avatarMIME,
	})
	if err != nil {
		if errors.Is(err, avatarassets.ErrNotFound) {
			recordAvatarAssetRequest("update", current.Kind, "not_found")
			writeError(w, http.StatusNotFound, "avatar not found")
			return
		}
		recordAvatarAssetRequest("update", current.Kind, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordAvatarAssetRequest("update", meta.Kind, "ok")
	writeJSON(w, http.StatusOK, avatarAssetResponseFromMeta(meta))
}

func (s *appServer) handleUpdateAvatarKind(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireAdmin(w, r, "update_kind")
	if !ok {
		return
	}
	if s.avatars == nil {
		recordAvatarAssetRequest("update_kind", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "avatar store not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("avatar_id"))
	if id == "" {
		recordAvatarAssetRequest("update_kind", "", "bad_request")
		writeError(w, http.StatusBadRequest, "missing avatar id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		recordAvatarAssetRequest("update_kind", "", "bad_request")
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	newKind, ok := avatarassets.NormalizeKind(body.Kind)
	if !ok {
		recordAvatarAssetRequest("update_kind", "", "bad_request")
		writeError(w, http.StatusBadRequest, "kind must be agent or system")
		return
	}
	meta, err := s.avatars.UpdateKind(r.Context(), id, newKind)
	if err != nil {
		switch {
		case errors.Is(err, avatarassets.ErrNotFound):
			recordAvatarAssetRequest("update_kind", "", "not_found")
			writeError(w, http.StatusNotFound, "avatar not found")
		case errors.Is(err, avatarassets.ErrKindUnchanged):
			recordAvatarAssetRequest("update_kind", newKind, "bad_request")
			writeError(w, http.StatusConflict, "avatar already has the requested kind")
		default:
			recordAvatarAssetRequest("update_kind", newKind, "store_error")
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	recordAvatarAssetRequest("update_kind", meta.Kind, "ok")
	writeJSON(w, http.StatusOK, avatarAssetResponseFromMeta(meta))
}

func (s *appServer) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireAdmin(w, r, "delete")
	if !ok {
		return
	}
	if s.avatars == nil || s.avatarImages == nil {
		recordAvatarAssetRequest("delete", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "avatar store not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("avatar_id"))
	if id == "" {
		recordAvatarAssetRequest("delete", "", "bad_request")
		writeError(w, http.StatusBadRequest, "missing avatar id")
		return
	}
	meta, err := s.avatars.Delete(r.Context(), id)
	if err != nil {
		if errors.Is(err, avatarassets.ErrNotFound) {
			recordAvatarAssetRequest("delete", "", "not_found")
			writeError(w, http.StatusNotFound, "avatar not found")
			return
		}
		recordAvatarAssetRequest("delete", "", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cleanupAvatarImageKeys(contextWithoutCancel(r.Context()), s.avatarImages, meta.AvatarBlobKey, meta.BackingBlobKey)
	recordAvatarAssetRequest("delete", "", "ok")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *appServer) requireAdmin(w http.ResponseWriter, r *http.Request, operation string) (auth.User, bool) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return auth.User{}, false
	}
	if !hasAdminPower(user) {
		recordAvatarAssetRequest(operation, "", "forbidden")
		writeError(w, http.StatusForbidden, "route requires admin access")
		return auth.User{}, false
	}
	return user, true
}

func avatarAssetResponseFromMeta(meta avatarassets.Metadata) avatarAssetResponse {
	escapedID := url.PathEscape(meta.ID)
	avatarURL := "/api/avatars/" + escapedID + "/image"
	if !meta.UpdatedAt.IsZero() {
		avatarURL += "?v=" + fmt.Sprint(meta.UpdatedAt.UTC().UnixNano())
	}
	return avatarAssetResponse{
		ID:         meta.ID,
		Kind:       meta.Kind,
		Name:       meta.Name,
		AvatarURL:  avatarURL,
		BackingURL: "/api/avatars/" + escapedID + "/backing",
		Crop:       meta.Crop,
		CreatedBy:  meta.CreatedBy,
		CreatedAt:  meta.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  meta.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func parseAvatarCrop(raw string) (avatarassets.Crop, error) {
	if strings.TrimSpace(raw) == "" {
		return avatarassets.Crop{CenterX: 0.5, CenterY: 0.5, Size: 1}, nil
	}
	if len(raw) > 1024 {
		return avatarassets.Crop{}, errors.New("crop payload is too large")
	}
	var crop avatarassets.Crop
	if err := json.Unmarshal([]byte(raw), &crop); err != nil {
		return avatarassets.Crop{}, errors.New("invalid crop payload")
	}
	if crop.CenterX < 0 || crop.CenterX > 1 || crop.CenterY < 0 || crop.CenterY > 1 {
		return avatarassets.Crop{}, errors.New("crop center must be between 0 and 1")
	}
	if crop.Size <= 0 || crop.Size > 1 {
		return avatarassets.Crop{}, errors.New("crop size must be between 0 and 1")
	}
	if crop.SourceWidth < 0 || crop.SourceHeight < 0 {
		return avatarassets.Crop{}, errors.New("crop source dimensions must be positive")
	}
	return crop, nil
}

func readAvatarUploadField(r *http.Request, field string, limit int64) ([]byte, string, avataruploads.FieldSummary, string, error) {
	summary := avataruploads.FieldSummary{}
	file, header, err := r.FormFile(field)
	if err != nil {
		return nil, "", summary, "missing_field", fmt.Errorf("%s image is required", field)
	}
	defer file.Close()
	summary.Present = true
	summary.HeaderMIME = strings.TrimSpace(header.Header.Get("Content-Type"))
	var buf bytes.Buffer
	written, err := io.Copy(&buf, io.LimitReader(file, limit+1))
	if err != nil {
		return nil, "", summary, "read_error", fmt.Errorf("failed to read %s image", field)
	}
	summary.SizeBytes = written
	if written > limit {
		return nil, "", summary, "field_too_large", fmt.Errorf("%s image exceeds %d bytes", field, limit)
	}
	raw := buf.Bytes()
	if len(raw) == 0 {
		return nil, "", summary, "empty_file", fmt.Errorf("%s image is empty", field)
	}
	summary.DetectedMIME = http.DetectContentType(raw)
	mime := normalizeAvatarUploadMIME(header.Header.Get("Content-Type"), raw)
	if mime == "" {
		return nil, "", summary, "invalid_mime", fmt.Errorf("%s image must be png, jpeg, webp, gif, avif, or bmp", field)
	}
	summary.MIME = mime
	return raw, mime, summary, "ok", nil
}

func normalizeAvatarUploadMIME(header string, body []byte) string {
	candidates := []string{header, http.DetectContentType(body)}
	for _, candidate := range candidates {
		mime := strings.ToLower(strings.TrimSpace(candidate))
		if i := strings.IndexByte(mime, ';'); i >= 0 {
			mime = strings.TrimSpace(mime[:i])
		}
		if mime == "image/jpg" {
			mime = "image/jpeg"
		}
		if _, ok := allowedAvatarUploadMIMEs[mime]; ok {
			return mime
		}
	}
	return ""
}

func contextWithoutCancel(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}
