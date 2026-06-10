package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// fakePendingLaunchStore implements the pendingLaunchStore interface for
// handler tests: Get returns a configured launch (or not-found), StageAttachment
// records the blob and flips to ready once the staged count meets the launch's
// attachment_count. The reconciler-side methods are unused here.
type fakePendingLaunchStore struct {
	launch    *pgstore.PendingLaunchTurn
	staged    []pgstore.LaunchAttachmentBlob
	notAccept bool
	// reconciler-side knobs
	claimRows    []pgstore.PendingLaunchTurn
	staleRows    []pgstore.PendingLaunchTurn
	loadBlobs    []pgstore.LaunchAttachmentBlob
	failedTurn   string
	failReason   string
	dispatchTurn string
}

func (f *fakePendingLaunchStore) Register(context.Context, pgstore.RegisterPendingLaunchRequest) (pgstore.PendingLaunchTurn, error) {
	return pgstore.PendingLaunchTurn{}, nil
}

func (f *fakePendingLaunchStore) Get(_ context.Context, _, turnID string) (pgstore.PendingLaunchTurn, error) {
	if f.launch == nil || f.launch.TurnID != turnID {
		return pgstore.PendingLaunchTurn{}, pgstore.ErrPendingLaunchNotFound
	}
	return *f.launch, nil
}

func (f *fakePendingLaunchStore) StageAttachment(_ context.Context, _, _ string, blob pgstore.LaunchAttachmentBlob) (pgstore.PendingLaunchStatus, error) {
	if f.notAccept {
		return pgstore.PendingLaunchClaiming, pgstore.ErrPendingLaunchNotAcceptingBytes
	}
	f.staged = append(f.staged, blob)
	if f.launch != nil && len(f.staged) >= f.launch.AttachmentCount {
		return pgstore.PendingLaunchReady, nil
	}
	return pgstore.PendingLaunchAwaitingBytes, nil
}

func (f *fakePendingLaunchStore) ClaimReady(context.Context, time.Time, int, time.Duration) ([]pgstore.PendingLaunchTurn, error) {
	return f.claimRows, nil
}
func (f *fakePendingLaunchStore) FindStale(context.Context, time.Time, int) ([]pgstore.PendingLaunchTurn, error) {
	return f.staleRows, nil
}
func (f *fakePendingLaunchStore) LoadAttachments(context.Context, string, string) ([]pgstore.LaunchAttachmentBlob, error) {
	return f.loadBlobs, nil
}
func (f *fakePendingLaunchStore) MarkDispatched(_ context.Context, _, turnID, dispatchedTurnID string) error {
	f.dispatchTurn = dispatchedTurnID
	return nil
}
func (f *fakePendingLaunchStore) MarkFailed(_ context.Context, _, turnID, reason string) error {
	f.failedTurn = turnID
	f.failReason = reason
	return nil
}

func stageReq(t *testing.T, sessionID, ordinal, clientNonce, name, body string) *http.Request {
	t.Helper()
	url := "/api/sessions/" + sessionID + "/launch-attachments/" + ordinal
	if clientNonce != "" || name != "" {
		url += "?client_nonce=" + clientNonce + "&name=" + name
	}
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewBufferString(body))
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("ordinal", ordinal)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/octet-stream")
	return req
}

func TestStageLaunchAttachmentStagesAndFlipsReady(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{}, sdkSessionPod("session-700", "700", "user@example.com", "claude_gui", "claude-runner"))
	fake := &fakePendingLaunchStore{launch: &pgstore.PendingLaunchTurn{TurnID: conversation.TurnIDForClientNonce("launchseven00"), AttachmentCount: 2}}
	app.pendingLaunch = fake

	// First of two — awaiting.
	rec := httptest.NewRecorder()
	app.handleStageLaunchAttachment(rec, stageReq(t, "700", "0", "launchseven00", "a.zip", "aaa"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var first map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first["status"] != "awaiting_bytes" {
		t.Fatalf("status after 1/2 = %v, want awaiting_bytes", first["status"])
	}

	// Second — ready.
	rec = httptest.NewRecorder()
	app.handleStageLaunchAttachment(rec, stageReq(t, "700", "1", "launchseven00", "b.png", "bbbb"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var second map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &second)
	if second["status"] != "ready" {
		t.Fatalf("status after 2/2 = %v, want ready", second["status"])
	}
	if len(fake.staged) != 2 || !bytes.Equal(fake.staged[0].Bytes, []byte("aaa")) {
		t.Fatalf("staged blobs not recorded: %+v", fake.staged)
	}
}

func TestStageLaunchAttachmentValidationAndErrors(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{}, sdkSessionPod("session-700", "700", "user@example.com", "claude_gui", "claude-runner"))
	app.pendingLaunch = &fakePendingLaunchStore{launch: &pgstore.PendingLaunchTurn{TurnID: conversation.TurnIDForClientNonce("launchseven00"), AttachmentCount: 2}}

	cases := []struct {
		name    string
		ordinal string
		turnID  string
		fname   string
		want    int
	}{
		{"missing client_nonce", "0", "", "a.zip", http.StatusBadRequest},
		{"bad ordinal", "99", "launchseven00", "a.zip", http.StatusBadRequest},
		{"ordinal beyond count", "5", "launchseven00", "a.zip", http.StatusBadRequest},
		{"unknown launch", "0", "othernonce", "a.zip", http.StatusNotFound},
		{"missing name", "0", "launchseven00", "", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			app.handleStageLaunchAttachment(rec, stageReq(t, "700", tc.ordinal, tc.turnID, tc.fname, "x"))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestStageLaunchAttachmentConflictWhenNotAccepting(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{}, sdkSessionPod("session-700", "700", "user@example.com", "claude_gui", "claude-runner"))
	app.pendingLaunch = &fakePendingLaunchStore{
		launch:    &pgstore.PendingLaunchTurn{TurnID: conversation.TurnIDForClientNonce("launchseven00"), AttachmentCount: 2},
		notAccept: true,
	}
	rec := httptest.NewRecorder()
	app.handleStageLaunchAttachment(rec, stageReq(t, "700", "0", "launchseven00", "a.zip", "aaa"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestStageLaunchAttachmentUnavailableWhenStoreNil(t *testing.T) {
	app := testTurnsApp(t, &recordingSessionBus{}, sdkSessionPod("session-700", "700", "user@example.com", "claude_gui", "claude-runner"))
	app.pendingLaunch = nil
	rec := httptest.NewRecorder()
	app.handleStageLaunchAttachment(rec, stageReq(t, "700", "0", "launchseven00", "a.zip", "aaa"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
}
