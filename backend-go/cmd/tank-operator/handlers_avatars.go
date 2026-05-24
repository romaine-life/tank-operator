package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/avatarassets"
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
	if s.avatars == nil {
		recordAvatarAssetRequest("read_image", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "avatar store not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("avatar_id"))
	if id == "" {
		recordAvatarAssetRequest("read_image", "", "bad_request")
		writeError(w, http.StatusBadRequest, "missing avatar id")
		return
	}
	img, err := s.avatars.GetImage(r.Context(), id, variant)
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
	recordAvatarAssetRequest("read_image", "", "ok")
	w.Header().Set("Content-Type", img.MIME)
	w.Header().Set("Content-Length", fmt.Sprint(len(img.Bytes)))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img.Bytes)
}

func (s *appServer) handleCreateAvatar(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdmin(w, r, "create")
	if !ok {
		return
	}
	if s.avatars == nil {
		recordAvatarAssetRequest("create", "", "store_unavailable")
		writeError(w, http.StatusServiceUnavailable, "avatar store not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarMultipartBytes)
	if err := r.ParseMultipartForm(maxAvatarMultipartBytes); err != nil {
		recordAvatarAssetRequest("create", "", "bad_request")
		writeError(w, http.StatusBadRequest, "invalid multipart avatar upload")
		return
	}
	kind, ok := avatarassets.NormalizeKind(r.FormValue("kind"))
	if !ok {
		recordAvatarAssetRequest("create", "", "bad_request")
		writeError(w, http.StatusBadRequest, "kind must be agent or system")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 80 {
		recordAvatarAssetRequest("create", kind, "bad_request")
		writeError(w, http.StatusBadRequest, "name is required and must be 80 characters or fewer")
		return
	}
	crop, err := parseAvatarCrop(r.FormValue("crop"))
	if err != nil {
		recordAvatarAssetRequest("create", kind, "bad_request")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	avatarBytes, avatarMIME, err := readAvatarUploadField(r, "avatar", maxAvatarCropBytes)
	if err != nil {
		recordAvatarAssetRequest("create", kind, "bad_request")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	backingBytes, backingMIME, err := readAvatarUploadField(r, "backing", maxAvatarBackingBytes)
	if err != nil {
		recordAvatarAssetRequest("create", kind, "bad_request")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	meta, err := s.avatars.Create(r.Context(), avatarassets.NewAsset{
		ID:           "av_" + auth.RandomHex(12),
		Kind:         kind,
		Name:         name,
		Crop:         crop,
		AvatarMIME:   avatarMIME,
		AvatarBytes:  avatarBytes,
		BackingMIME:  backingMIME,
		BackingBytes: backingBytes,
		CreatedBy:    user.Email,
	})
	if err != nil {
		recordAvatarAssetRequest("create", kind, "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordAvatarAssetRequest("create", kind, "ok")
	writeJSON(w, http.StatusCreated, avatarAssetResponseFromMeta(meta))
}

func (s *appServer) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireAdmin(w, r, "delete")
	if !ok {
		return
	}
	if s.avatars == nil {
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
	if err := s.avatars.Delete(r.Context(), id); err != nil {
		if errors.Is(err, avatarassets.ErrNotFound) {
			recordAvatarAssetRequest("delete", "", "not_found")
			writeError(w, http.StatusNotFound, "avatar not found")
			return
		}
		recordAvatarAssetRequest("delete", "", "store_error")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordAvatarAssetRequest("delete", "", "ok")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *appServer) requireAdmin(w http.ResponseWriter, r *http.Request, operation string) (auth.User, bool) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return auth.User{}, false
	}
	if user.Role != auth.RoleAdmin {
		recordAvatarAssetRequest(operation, "", "forbidden")
		writeError(w, http.StatusForbidden, "route requires role=admin")
		return auth.User{}, false
	}
	return user, true
}

func avatarAssetResponseFromMeta(meta avatarassets.Metadata) avatarAssetResponse {
	escapedID := url.PathEscape(meta.ID)
	return avatarAssetResponse{
		ID:         meta.ID,
		Kind:       meta.Kind,
		Name:       meta.Name,
		AvatarURL:  "/api/avatars/" + escapedID + "/image",
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

func readAvatarUploadField(r *http.Request, field string, limit int64) ([]byte, string, error) {
	file, header, err := r.FormFile(field)
	if err != nil {
		return nil, "", fmt.Errorf("%s image is required", field)
	}
	defer file.Close()
	var buf bytes.Buffer
	written, err := io.Copy(&buf, io.LimitReader(file, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read %s image", field)
	}
	if written > limit {
		return nil, "", fmt.Errorf("%s image exceeds %d bytes", field, limit)
	}
	raw := buf.Bytes()
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("%s image is empty", field)
	}
	mime := normalizeAvatarUploadMIME(header.Header.Get("Content-Type"), raw)
	if mime == "" {
		return nil, "", fmt.Errorf("%s image must be png, jpeg, webp, gif, avif, or bmp", field)
	}
	return raw, mime, nil
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
