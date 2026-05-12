package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

type recordingTurnQueue struct {
	records []store.TurnRecord
	err     error
}

func (q *recordingTurnQueue) Enqueue(_ context.Context, rec store.TurnRecord) error {
	if q.err != nil {
		return q.err
	}
	q.records = append(q.records, rec)
	return nil
}

func (*recordingTurnQueue) NextPending(context.Context, string) (*store.TurnRecord, error) {
	return nil, nil
}
func (*recordingTurnQueue) MarkClaimed(context.Context, string, string) error   { return nil }
func (*recordingTurnQueue) MarkCompleted(context.Context, string, string) error { return nil }
func (*recordingTurnQueue) MarkFailed(context.Context, string, string) error    { return nil }

func TestEnqueueSessionTurnWritesSDKTurnQueueRecord(t *testing.T) {
	queue := &recordingTurnQueue{}
	app := testTurnsApp(t, queue, sdkSessionPod("session-63", "63", "user@example.com", compat.ClaudeGUIMode, "agent-runner"))
	body := `{"client_nonce":"run-abc_123","prompt":"  hello sdk  ","model":"claude-sonnet-4-6","permission_mode":"bypassPermissions","skill_name":"test","follow_up":true}`
	req := authedTurnRequest(t, "63", body)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(queue.records) != 1 {
		t.Fatalf("enqueued records = %d, want 1", len(queue.records))
	}
	got := queue.records[0]
	if got.RunID != "run-abc_123" || got.ClientNonce != "run-abc_123" {
		t.Fatalf("run/client nonce = %q/%q", got.RunID, got.ClientNonce)
	}
	if got.Source != "sdk" || got.Provider != "claude" || got.SessionID != "63" || got.Email != "user@example.com" {
		t.Fatalf("record routing fields = %#v", got)
	}
	if got.Prompt != "hello sdk" || got.Model != "claude-sonnet-4-6" || got.PermissionMode != "bypassPermissions" || got.SkillName != "test" || !got.FollowUp {
		t.Fatalf("record payload fields = %#v", got)
	}
}

func TestEnqueueSessionTurnRoutesCodexProvider(t *testing.T) {
	queue := &recordingTurnQueue{}
	app := testTurnsApp(t, queue, sdkSessionPod("session-64", "64", "user@example.com", compat.CodexGUIMode, "codex-runner"))
	req := authedTurnRequest(t, "64", `{"client_nonce":"run-codex","prompt":"hello"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if got := queue.records[0].Provider; got != "codex" {
		t.Fatalf("provider = %q, want codex", got)
	}
}

func TestEnqueueSessionTurnRejectsLegacyRuntime(t *testing.T) {
	queue := &recordingTurnQueue{}
	app := testTurnsApp(t, queue, sdkSessionPod("session-65", "65", "user@example.com", compat.ClaudeGUIMode, "claude"))
	req := authedTurnRequest(t, "65", `{"client_nonce":"run-legacy","prompt":"hello"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(queue.records) != 0 {
		t.Fatalf("enqueued legacy runtime record: %#v", queue.records)
	}
}

func TestEnqueueSessionTurnValidatesClientNonce(t *testing.T) {
	queue := &recordingTurnQueue{}
	app := testTurnsApp(t, queue, sdkSessionPod("session-66", "66", "user@example.com", compat.ClaudeGUIMode, "agent-runner"))
	req := authedTurnRequest(t, "66", `{"client_nonce":"bad/slash","prompt":"hello"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
}

func TestEnqueueSessionTurnSurfacesQueueFailure(t *testing.T) {
	queue := &recordingTurnQueue{err: errors.New("cosmos down")}
	app := testTurnsApp(t, queue, sdkSessionPod("session-67", "67", "user@example.com", compat.ClaudeGUIMode, "agent-runner"))
	req := authedTurnRequest(t, "67", `{"client_nonce":"run-fail","prompt":"hello"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
}

func testTurnsApp(t *testing.T, queue store.TurnQueueStore, pods ...*corev1.Pod) *appServer {
	t.Helper()
	clientObjects := make([]runtime.Object, 0, len(pods))
	for _, pod := range pods {
		clientObjects = append(clientObjects, pod)
	}
	k8s := fake.NewSimpleClientset(clientObjects...)
	ns := compat.SessionsNamespace
	return &appServer{
		k8s:       k8s,
		mgr:       sessions.NewManager(k8s, nil, ns, nil, nil, sessions.ManagerOptions{}),
		turnQueue: queue,
		verifier:  auth.NewVerifier(testJWT(t), "user@example.com"),
		namespace: ns,
	}
}

func sdkSessionPod(name, sessionID, email, mode, runnerContainer string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: compat.SessionsNamespace,
			Labels: map[string]string{
				"tank-operator/session-id": sessionID,
				"tank-operator/owner":      compat.OwnerLabel(email),
				"tank-operator/mode":       mode,
			},
			Annotations: map[string]string{
				"tank-operator/owner-email": email,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sandbox-agent"}, {Name: runnerContainer}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func authedTurnRequest(t *testing.T, sessionID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/turns", strings.NewReader(body))
	req.SetPathValue("session_id", sessionID)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	return req
}
