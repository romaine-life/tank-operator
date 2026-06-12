package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/avatarassets"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

type fakeMessageLinkShareStore struct {
	created pgstore.MessageLinkShare
	shares  map[string]pgstore.MessageLinkShare
}

func (s *fakeMessageLinkShareStore) Create(_ context.Context, share pgstore.MessageLinkShare) error {
	s.created = share
	if s.shares == nil {
		s.shares = map[string]pgstore.MessageLinkShare{}
	}
	s.shares[share.Token] = share
	return nil
}

func (s *fakeMessageLinkShareStore) Get(_ context.Context, token string) (pgstore.MessageLinkShare, error) {
	share, ok := s.shares[token]
	if !ok {
		return pgstore.MessageLinkShare{}, pgstore.ErrMessageLinkShareInvalid
	}
	share.Token = token
	return share, nil
}

func messageLinkShareTestApp(t *testing.T, shares *fakeMessageLinkShareStore, rows *fakeSessionTranscriptRowStore, events fakeSessionEventStore) *appServer {
	t.Helper()
	registry := newTestSessionRegistry(sessionmodel.SessionRecord{
		Email:           otherUser,
		ID:              "63",
		Scope:           "default",
		Visible:         true,
		Status:          "Active",
		Mode:            sessionmodel.ClaudeGUIMode,
		AgentAvatarID:   "jp1-grant",
		SystemAvatarID:  "",
		SidebarPosition: 1,
		RowVersion:      1,
	})
	return &appServer{
		verifier:          auth.NewVerifier(testJWT(t)),
		mgr:               sessions.NewManager(fake.NewSimpleClientset(), nil, sessionmodel.SessionsNamespace, registry, nil, sessions.ManagerOptions{}),
		messageLinkShares: shares,
		transcriptRows:    rows,
		sessionEvents:     events,
		readStates:        store.NewStubConversationReadStateStore(),
	}
}

func TestHandleCreateMessageLinkShareMintsBearerURLForOwnedTimelineTarget(t *testing.T) {
	shares := &fakeMessageLinkShareStore{}
	rows := &fakeSessionTranscriptRowStore{
		resolveTimeline: map[string]string{
			"turn-1:item:msg-1": store.EncodeTranscriptRowCursor("order-001\x1fturn-1:item:msg-1"),
		},
	}
	app := messageLinkShareTestApp(t, shares, rows, fakeSessionEventStore{})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/63/message-links", strings.NewReader(`{"timeline_id":"turn-1:item:msg-1"}`))
	req.Host = "tank.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	req.SetPathValue("session_id", "63")
	rec := httptest.NewRecorder()

	app.handleCreateMessageLinkShare(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if shares.created.Token == "" {
		t.Fatal("share token was not created")
	}
	if shares.created.OwnerEmail != otherUser || shares.created.SessionID != "63" || shares.created.TimelineID != "turn-1:item:msg-1" {
		t.Fatalf("created share = %#v", shares.created)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	browserURL, _ := body["browser_url"].(string)
	if !strings.Contains(browserURL, "https://tank.example.test/?") ||
		!strings.Contains(browserURL, "session=63") ||
		!strings.Contains(browserURL, "message=turn-1%3Aitem%3Amsg-1") ||
		!strings.Contains(browserURL, "share="+shares.created.Token) {
		t.Fatalf("browser_url = %q", browserURL)
	}
}

func TestHandlePublicMessageLinkTimelineReadsThroughShareToken(t *testing.T) {
	token := "share-token"
	shares := &fakeMessageLinkShareStore{shares: map[string]pgstore.MessageLinkShare{
		token: {
			Token:        token,
			OwnerEmail:   otherUser,
			SessionScope: "default",
			SessionID:    "63",
			TimelineID:   "turn-1:item:msg-1",
		},
	}}
	rows := &fakeSessionTranscriptRowStore{
		resolveTimeline: map[string]string{
			"turn-1:item:msg-1": store.EncodeTranscriptRowCursor("order-001\x1fturn-1:item:msg-1"),
		},
		pages: map[string]store.TranscriptRowPage{
			"around": {
				Rows: []map[string]any{{
					"id":       "turn-1:item:msg-1",
					"kind":     "message",
					"role":     "assistant",
					"text":     "linked",
					"orderKey": "order-001",
				}},
				FoundOldest: true,
				FoundNewest: true,
			},
		},
	}
	events := fakeSessionEventStore{pages: map[string]store.SessionEventPage{
		"": {Events: []map[string]any{{"order_key": "order-001"}}},
	}}
	app := messageLinkShareTestApp(t, shares, rows, events)
	req := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token+"/timeline", nil)
	req.SetPathValue("share_token", token)
	rec := httptest.NewRecorder()

	app.handlePublicMessageLinkTimeline(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["public"] != true || body["target_timeline_id"] != "turn-1:item:msg-1" {
		t.Fatalf("unexpected body = %#v", body)
	}
	rowsJSON, ok := body["rows"].([]any)
	if !ok || len(rowsJSON) != 1 {
		t.Fatalf("rows = %#v", body["rows"])
	}
	if _, ok := body["read_state"]; !ok || body["read_state"] != nil {
		t.Fatalf("read_state = %#v", body["read_state"])
	}
}

func TestHandlePublicMessageLinkTurnActivityIncludesTurnContext(t *testing.T) {
	token := "share-token"
	shares := &fakeMessageLinkShareStore{shares: map[string]pgstore.MessageLinkShare{
		token: {
			Token:        token,
			OwnerEmail:   otherUser,
			SessionScope: "default",
			SessionID:    "63",
			TimelineID:   "turn-1:item:msg-1",
		},
	}}
	events := []map[string]any{
		projectionTestEvent("u", "00000001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "show this prompt in turns",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", "00000002", "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("tool", "00000003", "item.completed", "tool", "claude", "turn-1", "turn-1:item:tool", map[string]any{
			"kind": "tool_result", "name": "Read", "output": "ok",
		}),
	}
	app := messageLinkShareTestApp(t, shares, &fakeSessionTranscriptRowStore{}, fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: events, FoundOldest: true, FoundNewest: true},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token+"/turns/turn-1/activity", nil)
	req.SetPathValue("share_token", token)
	req.SetPathValue("turn_id", "turn-1")
	rec := httptest.NewRecorder()

	app.handlePublicMessageLinkTurnActivity(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["projection"] != "server_turn_activity_v3" {
		t.Fatalf("projection = %#v", body["projection"])
	}
	context, _ := body["turn_context"].(map[string]any)
	if got, _ := context["text"].(string); got != "show this prompt in turns" {
		t.Fatalf("turn_context.text = %q: %#v", got, body["turn_context"])
	}
	entries, _ := body["entries"].([]any)
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry["kind"] == "message" && entry["role"] == "user" {
			t.Fatalf("human user message leaked into public activity body: %#v", entries)
		}
	}
}

func TestHandleGetPublicMessageLinkIncludesOwnerAvatarWithoutOwnerEmail(t *testing.T) {
	token := "share-token"
	shares := &fakeMessageLinkShareStore{shares: map[string]pgstore.MessageLinkShare{
		token: {
			Token:        token,
			OwnerEmail:   otherUser,
			SessionScope: "default",
			SessionID:    "63",
			TimelineID:   "turn-1:item:msg-1",
		},
	}}
	app := messageLinkShareTestApp(t, shares, &fakeSessionTranscriptRowStore{}, fakeSessionEventStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token, nil)
	req.SetPathValue("share_token", token)
	rec := httptest.NewRecorder()

	app.handleGetPublicMessageLink(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	session, ok := body["session"].(map[string]any)
	if !ok {
		t.Fatalf("session = %#v", body["session"])
	}
	if session["owner"] != "" {
		t.Fatalf("public session owner = %#v, want redacted", session["owner"])
	}
	user, ok := body["user"].(map[string]any)
	if !ok {
		t.Fatalf("user = %#v", body["user"])
	}
	if user["email"] != nil {
		t.Fatalf("public user should not include email: %#v", user)
	}
	avatarURL, _ := user["avatar_url"].(string)
	if avatarURL == "" || !strings.Contains(avatarURL, "gravatar.com/avatar/") || strings.Contains(avatarURL, otherUser) {
		t.Fatalf("avatar_url = %q", avatarURL)
	}
}

func TestHandlePublicMessageLinkAvatarsExposeOnlySessionAssignedAvatars(t *testing.T) {
	token := "share-token"
	shares := &fakeMessageLinkShareStore{shares: map[string]pgstore.MessageLinkShare{
		token: {
			Token:        token,
			OwnerEmail:   otherUser,
			SessionScope: "default",
			SessionID:    "63",
			TimelineID:   "turn-1:item:msg-1",
		},
	}}
	app := messageLinkShareTestApp(t, shares, &fakeSessionTranscriptRowStore{}, fakeSessionEventStore{})
	avatars := avatarassets.NewMemoryStore()
	images := avatarassets.NewMemoryImageStore()
	if err := avatars.Ensure(t.Context(), avatarassets.NewAsset{
		ID:             "jp1-grant",
		Kind:           avatarassets.KindAgent,
		Name:           "Dr. Grant",
		Crop:           avatarassets.Crop{CenterX: 0.5, CenterY: 0.5, Size: 1},
		AvatarMIME:     "image/png",
		AvatarBlobKey:  "avatars/jp1-grant/avatar.png",
		BackingMIME:    "image/png",
		BackingBlobKey: "avatars/jp1-grant/backing.png",
		CreatedBy:      adminEmail,
	}); err != nil {
		t.Fatal(err)
	}
	if err := avatars.Ensure(t.Context(), avatarassets.NewAsset{
		ID:             "unassigned",
		Kind:           avatarassets.KindAgent,
		Name:           "Unassigned",
		Crop:           avatarassets.Crop{CenterX: 0.5, CenterY: 0.5, Size: 1},
		AvatarMIME:     "image/png",
		AvatarBlobKey:  "avatars/unassigned/avatar.png",
		BackingMIME:    "image/png",
		BackingBlobKey: "avatars/unassigned/backing.png",
		CreatedBy:      adminEmail,
	}); err != nil {
		t.Fatal(err)
	}
	if err := images.Put(t.Context(), "avatars/jp1-grant/avatar.png", avatarassets.Image{MIME: "image/png", Bytes: tinyPNG}); err != nil {
		t.Fatal(err)
	}
	if err := images.Put(t.Context(), "avatars/jp1-grant/backing.png", avatarassets.Image{MIME: "image/png", Bytes: []byte("backing")}); err != nil {
		t.Fatal(err)
	}
	app.avatars = avatars
	app.avatarImages = images

	listReq := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token+"/avatars", nil)
	listReq.SetPathValue("share_token", token)
	listResp := httptest.NewRecorder()
	app.handlePublicMessageLinkAvatars(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResp.Code, listResp.Body.String())
	}
	var listBody struct {
		Entries []avatarAssetResponse `json:"entries"`
		Public  bool                  `json:"public"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	if !listBody.Public || len(listBody.Entries) != 1 {
		t.Fatalf("list body = %#v", listBody)
	}
	entry := listBody.Entries[0]
	if entry.ID != "jp1-grant" || entry.Kind != avatarassets.KindAgent || entry.CreatedBy != "" {
		t.Fatalf("entry = %#v", entry)
	}
	if !strings.Contains(entry.AvatarURL, "/api/public/message-links/"+token+"/avatars/jp1-grant/image") {
		t.Fatalf("avatar_url = %q", entry.AvatarURL)
	}
	if !strings.Contains(entry.BackingURL, "/api/public/message-links/"+token+"/avatars/jp1-grant/backing") {
		t.Fatalf("backing_url = %q", entry.BackingURL)
	}

	imageReq := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token+"/avatars/jp1-grant/image", nil)
	imageReq.SetPathValue("share_token", token)
	imageReq.SetPathValue("avatar_id", "jp1-grant")
	imageResp := httptest.NewRecorder()
	app.handlePublicMessageLinkAvatarImage(imageResp, imageReq)
	if imageResp.Code != http.StatusOK {
		t.Fatalf("image status=%d body=%s", imageResp.Code, imageResp.Body.String())
	}
	if got := imageResp.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("image content-type = %q", got)
	}
	if !bytes.Equal(imageResp.Body.Bytes(), tinyPNG) {
		t.Fatalf("image body did not round-trip")
	}

	unassignedReq := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token+"/avatars/unassigned/image", nil)
	unassignedReq.SetPathValue("share_token", token)
	unassignedReq.SetPathValue("avatar_id", "unassigned")
	unassignedResp := httptest.NewRecorder()
	app.handlePublicMessageLinkAvatarImage(unassignedResp, unassignedReq)
	if unassignedResp.Code != http.StatusNotFound {
		t.Fatalf("unassigned image status=%d body=%s", unassignedResp.Code, unassignedResp.Body.String())
	}
}

// A share token grants the WHOLE session read-only (owner decision on #1077),
// so the public timeline must serve a freshly re-projected session after a
// projection-version bump instead of stale/empty rows that only an
// authenticated read would repair. Mirrors
// TestSessionTimelineMaterializesStaleTranscriptRowsBeforeRead.
func TestHandlePublicMessageLinkTimelineMaterializesStaleTranscriptRowsBeforeRead(t *testing.T) {
	token := "share-token"
	shares := &fakeMessageLinkShareStore{shares: map[string]pgstore.MessageLinkShare{
		token: {
			Token:        token,
			OwnerEmail:   otherUser,
			SessionScope: "default",
			SessionID:    "63",
			TimelineID:   "turn-1:item:msg-1",
		},
	}}
	events := fakeSessionEventStore{pages: map[string]store.SessionEventPage{
		"": {
			Events: []map[string]any{
				projectionTestEvent("turn-1:user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
					"text": "hello",
				}),
			},
			FoundOldest: true,
			FoundNewest: true,
		},
	}}
	rows := &fakeSessionTranscriptRowStore{needsBackfill: true}
	app := messageLinkShareTestApp(t, shares, rows, events)

	// anchor=newest is a whole-session tail read, not the shared message's
	// window — the share grants the full transcript.
	req := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token+"/timeline?anchor=newest&rows=24", nil)
	req.SetPathValue("share_token", token)
	rec := httptest.NewRecorder()

	app.handlePublicMessageLinkTimeline(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rows.needsCalls < 2 {
		t.Fatalf("NeedsBackfill calls = %d, want initial check and pre-replace recheck", rows.needsCalls)
	}
	if len(rows.replaceSessions) != 1 || rows.replaceSessions[0] != "63" {
		t.Fatalf("ReplaceForSession sessions = %#v, want [63]", rows.replaceSessions)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rowsJSON, ok := body["rows"].([]any)
	if !ok || len(rowsJSON) != 1 {
		t.Fatalf("rows = %#v", body["rows"])
	}
	row, _ := rowsJSON[0].(map[string]any)
	if row["id"] != "turn-1:user" {
		t.Fatalf("row = %#v", row)
	}
}

func TestHandlePublicMessageLinkTimelineFailsClosedWhenMaterializationFails(t *testing.T) {
	token := "share-token"
	shares := &fakeMessageLinkShareStore{shares: map[string]pgstore.MessageLinkShare{
		token: {
			Token:        token,
			OwnerEmail:   otherUser,
			SessionScope: "default",
			SessionID:    "63",
			TimelineID:   "turn-1:item:msg-1",
		},
	}}
	rows := &fakeSessionTranscriptRowStore{
		needsBackfill: true,
		needsErr:      errors.New("row marker unavailable"),
	}
	app := messageLinkShareTestApp(t, shares, rows, fakeSessionEventStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token+"/timeline?anchor=newest&rows=24", nil)
	req.SetPathValue("share_token", token)
	rec := httptest.NewRecorder()

	app.handlePublicMessageLinkTimeline(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "transcript materialization failed") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// The share grants every turn in the session, not just the linked message's
// turn, and the activity read materializes the projection first so the share
// surface is current without any authenticated read.
func TestHandlePublicMessageLinkTurnActivityMaterializesAndServesArbitraryTurn(t *testing.T) {
	token := "share-token"
	shares := &fakeMessageLinkShareStore{shares: map[string]pgstore.MessageLinkShare{
		token: {
			Token:        token,
			OwnerEmail:   otherUser,
			SessionScope: "default",
			SessionID:    "63",
			TimelineID:   "turn-1:item:msg-1",
		},
	}}
	events := []map[string]any{
		projectionTestEvent("u1", "00000001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text":    "first prompt",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("u2", "00000002", "user_message.created", "user", "tank", "turn-2", "turn-2:user", map[string]any{
			"text":    "second prompt",
			"display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("tool2", "00000003", "item.completed", "tool", "claude", "turn-2", "turn-2:item:tool", map[string]any{
			"kind": "tool_result", "name": "Read", "output": "ok",
		}),
	}
	rows := &fakeSessionTranscriptRowStore{needsBackfill: true}
	app := messageLinkShareTestApp(t, shares, rows, fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {Events: events, FoundOldest: true, FoundNewest: true},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token+"/turns/turn-2/activity", nil)
	req.SetPathValue("share_token", token)
	req.SetPathValue("turn_id", "turn-2")
	rec := httptest.NewRecorder()

	app.handlePublicMessageLinkTurnActivity(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rows.needsCalls < 2 {
		t.Fatalf("NeedsBackfill calls = %d, want initial check and pre-replace recheck", rows.needsCalls)
	}
	if len(rows.replaceSessions) != 1 || rows.replaceSessions[0] != "63" {
		t.Fatalf("ReplaceForSession sessions = %#v, want [63]", rows.replaceSessions)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["turn_id"] != "turn-2" || body["public"] != true {
		t.Fatalf("body = %#v", body)
	}
	context, _ := body["turn_context"].(map[string]any)
	if got, _ := context["text"].(string); got != "second prompt" {
		t.Fatalf("turn_context.text = %q: %#v", got, body["turn_context"])
	}
}

func TestHandlePublicMessageLinkTurnActivityFailsClosedWhenMaterializationFails(t *testing.T) {
	token := "share-token"
	shares := &fakeMessageLinkShareStore{shares: map[string]pgstore.MessageLinkShare{
		token: {
			Token:        token,
			OwnerEmail:   otherUser,
			SessionScope: "default",
			SessionID:    "63",
			TimelineID:   "turn-1:item:msg-1",
		},
	}}
	rows := &fakeSessionTranscriptRowStore{
		needsBackfill: true,
		needsErr:      errors.New("row marker unavailable"),
	}
	app := messageLinkShareTestApp(t, shares, rows, fakeSessionEventStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/public/message-links/"+token+"/turns/turn-1/activity", nil)
	req.SetPathValue("share_token", token)
	req.SetPathValue("turn_id", "turn-1")
	rec := httptest.NewRecorder()

	app.handlePublicMessageLinkTurnActivity(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "transcript materialization failed") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// Read-only audit guard: the public message-link surface registers GET
// handlers only, so any mutating method on a share-token route is rejected by
// the mux itself (405), independent of handler logic. If a future change adds
// a write-method registration under /api/public/message-links/, this test
// names the regression.
func TestPublicMessageLinkRoutesAreGETOnly(t *testing.T) {
	mux := http.NewServeMux()
	(&appServer{}).registerRoutes(mux)
	paths := []string{
		"/api/public/message-links/tok",
		"/api/public/message-links/tok/avatars",
		"/api/public/message-links/tok/avatars/a1/image",
		"/api/public/message-links/tok/avatars/a1/backing",
		"/api/public/message-links/tok/timeline",
		"/api/public/message-links/tok/turns/turn-1/activity",
		"/api/public/session-report-shares/tok",
	}
	for _, path := range paths {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			req := httptest.NewRequest(method, path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s status=%d, want 405", method, path, rec.Code)
			}
		}
	}
}

func TestHandlePublicMessageLinkRejectsUnknownTokenWithoutAuth(t *testing.T) {
	app := messageLinkShareTestApp(
		t,
		&fakeMessageLinkShareStore{shares: map[string]pgstore.MessageLinkShare{}},
		&fakeSessionTranscriptRowStore{},
		fakeSessionEventStore{},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/public/message-links/missing", nil)
	req.SetPathValue("share_token", "missing")
	rec := httptest.NewRecorder()

	app.handleGetPublicMessageLink(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
