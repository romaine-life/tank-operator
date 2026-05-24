package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/avatarassets"
)

var tinyPNG = mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func TestAvatarAssetAdminCreateListReadDelete(t *testing.T) {
	app := &appServer{
		verifier: auth.NewVerifier(testJWT(t)),
		avatars:  avatarassets.NewMemoryStore(),
	}

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

func TestAvatarCreateRejectsInvalidKind(t *testing.T) {
	app := &appServer{
		verifier: auth.NewVerifier(testJWT(t)),
		avatars:  avatarassets.NewMemoryStore(),
	}
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

func avatarCreateRequest(t *testing.T, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"avatar", "backing"} {
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

func mustDecodeBase64(raw string) []byte {
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		panic(err)
	}
	return b
}
