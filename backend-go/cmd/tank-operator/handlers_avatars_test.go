package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/avatarassets"
	"github.com/nelsong6/tank-operator/backend-go/internal/avataruploads"
)

var tinyPNG = mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func TestAvatarAssetAdminCreateListReadDelete(t *testing.T) {
	app := newAvatarTestServer(t)

	createReq := avatarCreateRequest(t, map[string]string{
		"kind": "agent",
		"name": "Ada",
		"crop": `{"center_x":0.4,"center_y":0.45,"size":0.5,"source_width":800,"source_height":600}`,
	})
	createReq.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	createResp := httptest.NewRecorder()
	app.handleCreateAvatar(createResp, createReq)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", createResp.Code, createResp.Body.String())
	}
	var created avatarAssetResponse
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Kind != "agent" || created.Name != "Ada" {
		t.Fatalf("created = %#v", created)
	}
	if created.AttemptID == "" {
		t.Fatalf("missing upload attempt id: %#v", created)
	}
	if created.AvatarURL == "" || created.BackingURL == "" {
		t.Fatalf("missing image urls: %#v", created)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/avatars", nil)
	listReq.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	listResp := httptest.NewRecorder()
	app.handleListAvatars(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", listResp.Code, listResp.Body.String())
	}
	var listBody struct {
		Entries []avatarAssetResponse `json:"entries"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	if len(listBody.Entries) != 1 || listBody.Entries[0].ID != created.ID {
		t.Fatalf("list entries = %#v", listBody.Entries)
	}

	imageReq := httptest.NewRequest(http.MethodGet, "/api/avatars/"+created.ID+"/image", nil)
	imageReq.SetPathValue("avatar_id", created.ID)
	imageReq.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	imageResp := httptest.NewRecorder()
	app.handleGetAvatarImage(imageResp, imageReq)
	if imageResp.Code != http.StatusOK {
		t.Fatalf("image status = %d body = %s", imageResp.Code, imageResp.Body.String())
	}
	if got := imageResp.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("image content-type = %q", got)
	}
	if !bytes.Equal(imageResp.Body.Bytes(), tinyPNG) {
		t.Fatalf("image body did not round-trip")
	}

	userDeleteReq := httptest.NewRequest(http.MethodDelete, "/api/admin/avatars/"+created.ID, nil)
	userDeleteReq.SetPathValue("avatar_id", created.ID)
	userDeleteReq.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	userDeleteResp := httptest.NewRecorder()
	app.handleDeleteAvatar(userDeleteResp, userDeleteReq)
	if userDeleteResp.Code != http.StatusForbidden {
		t.Fatalf("user delete status = %d body = %s", userDeleteResp.Code, userDeleteResp.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/admin/avatars/"+created.ID, nil)
	deleteReq.SetPathValue("avatar_id", created.ID)
	deleteReq.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	deleteResp := httptest.NewRecorder()
	app.handleDeleteAvatar(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", deleteResp.Code, deleteResp.Body.String())
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/api/avatars/"+created.ID+"/image", nil)
	missingReq.SetPathValue("avatar_id", created.ID)
	missingReq.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	missingResp := httptest.NewRecorder()
	app.handleGetAvatarImage(missingResp, missingReq)
	if missingResp.Code != http.StatusNotFound {
		t.Fatalf("post-delete image status = %d body = %s", missingResp.Code, missingResp.Body.String())
	}
}

func TestAvatarCreateAllowsSuperAdminServiceActor(t *testing.T) {
	t.Setenv("SUPER_ADMIN_EMAILS", adminEmail)
	app := newAvatarTestServer(t)
	req := avatarCreateRequest(t, map[string]string{
		"kind": "agent",
		"name": "Ada",
	})
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-200@service.tank.romaine.life", adminEmail))
	resp := httptest.NewRecorder()

	app.handleCreateAvatar(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var created avatarAssetResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.CreatedBy != adminEmail {
		t.Fatalf("created_by = %q, want actor email %q", created.CreatedBy, adminEmail)
	}
}

func TestAvatarCreateRejectsRegularServiceActor(t *testing.T) {
	t.Setenv("SUPER_ADMIN_EMAILS", adminEmail)
	app := newAvatarTestServer(t)
	req := avatarCreateRequest(t, map[string]string{
		"kind": "agent",
		"name": "Ada",
	})
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-200@service.tank.romaine.life", otherUser))
	resp := httptest.NewRecorder()

	app.handleCreateAvatar(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
}

func TestDefaultAvatarImageFallsBackToBundledStatic(t *testing.T) {
	root := t.TempDir()
	avatarDir := filepath.Join(root, "assets", "avatars")
	if err := os.MkdirAll(avatarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	staticBody := []byte("bundled-default-avatar")
	if err := os.WriteFile(filepath.Join(avatarDir, "jp1-raptor.png"), staticBody, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TANK_OPERATOR_STATIC_DIR", root)
	t.Setenv("TANK_OPERATOR_STATIC_OVERRIDE_DIR", "")

	store := avatarassets.NewMemoryStore()
	if err := store.Ensure(t.Context(), avatarassets.NewAsset{
		ID:             "jp1-raptor",
		Kind:           avatarassets.KindAgent,
		Name:           "Velociraptor",
		Crop:           avatarassets.Crop{CenterX: 0.5, CenterY: 0.5, Size: 1},
		AvatarMIME:     "image/png",
		AvatarBlobKey:  "avatars/legacy/jp1-raptor/avatar.png",
		BackingMIME:    "image/png",
		BackingBlobKey: "avatars/legacy/jp1-raptor/avatar.png",
		CreatedBy:      "tank-operator",
	}); err != nil {
		t.Fatal(err)
	}
	app := &appServer{
		verifier:     auth.NewVerifier(testJWT(t)),
		avatars:      store,
		avatarImages: avatarassets.NewMemoryImageStore(),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/avatars/jp1-raptor/image", nil)
	req.SetPathValue("avatar_id", "jp1-raptor")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	resp := httptest.NewRecorder()

	app.handleGetAvatarImage(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !bytes.Equal(resp.Body.Bytes(), staticBody) {
		t.Fatalf("body = %q, want %q", resp.Body.Bytes(), staticBody)
	}
}

func TestAvatarCreateRejectsInvalidKind(t *testing.T) {
	app := newAvatarTestServer(t)
	req := avatarCreateRequest(t, map[string]string{
		"kind": "personal",
		"name": "Ada",
	})
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleCreateAvatar(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "kind must be agent or system") {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestAvatarCreateMalformedMultipartRecordsDebugAttempt(t *testing.T) {
	app := newAvatarTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/avatars", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleCreateAvatar(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Detail    string `json:"detail"`
		Code      string `json:"code"`
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "wrong_content_type" || body.AttemptID == "" {
		t.Fatalf("body = %#v", body)
	}

	debugReq := httptest.NewRequest(http.MethodGet, "/api/debug/avatar-upload-attempts?attempt_id="+body.AttemptID, nil)
	debugReq.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	debugResp := httptest.NewRecorder()
	app.handleDebugAvatarUploadAttempts(debugResp, debugReq)
	if debugResp.Code != http.StatusOK {
		t.Fatalf("debug status = %d body = %s", debugResp.Code, debugResp.Body.String())
	}
	var debugBody struct {
		Attempts []avataruploads.Attempt `json:"attempts"`
	}
	if err := json.Unmarshal(debugResp.Body.Bytes(), &debugBody); err != nil {
		t.Fatal(err)
	}
	if len(debugBody.Attempts) != 1 {
		t.Fatalf("attempts = %#v", debugBody.Attempts)
	}
	got := debugBody.Attempts[0]
	if got.ID != body.AttemptID || got.Stage != "parse_multipart" || got.Result != "wrong_media_type" {
		t.Fatalf("attempt = %#v", got)
	}
	if got.ContentTypeClass != "wrong_media_type" || got.Diagnostics["parser_error"] == "" {
		t.Fatalf("attempt diagnostics = %#v", got)
	}
}

func TestAvatarCreateMissingBackingRecordsFieldSummary(t *testing.T) {
	app := newAvatarTestServer(t)
	req := avatarCreateRequestWithFiles(t, map[string]string{
		"kind": "agent",
		"name": "Ada",
	}, []string{"avatar"})
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleCreateAvatar(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Code      string `json:"code"`
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "missing_image_field" || body.AttemptID == "" {
		t.Fatalf("body = %#v", body)
	}
	attempt, err := app.avatarUploads.Get(t.Context(), body.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Stage != "read_backing" || attempt.Result != "missing_field" {
		t.Fatalf("attempt = %#v", attempt)
	}
	if !attempt.Fields["avatar"].Present {
		t.Fatalf("avatar field summary missing: %#v", attempt.Fields)
	}
	if attempt.Fields["backing"].Present {
		t.Fatalf("backing field should be absent: %#v", attempt.Fields)
	}
}

func avatarCreateRequest(t *testing.T, fields map[string]string) *http.Request {
	return avatarCreateRequestWithFiles(t, fields, []string{"avatar", "backing"})
}

func avatarCreateRequestWithFiles(t *testing.T, fields map[string]string, fileFields []string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range fileFields {
		part, err := writer.CreateFormFile(field, field+".png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(tinyPNG); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/avatars", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func newAvatarTestServer(t *testing.T) *appServer {
	t.Helper()
	return &appServer{
		verifier:      auth.NewVerifier(testJWT(t)),
		avatars:       avatarassets.NewMemoryStore(),
		avatarImages:  avatarassets.NewMemoryImageStore(),
		avatarUploads: avataruploads.NewMemoryStore(),
	}
}

func mustDecodeBase64(raw string) []byte {
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		panic(err)
	}
	return b
}
