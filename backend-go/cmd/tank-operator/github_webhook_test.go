package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

func signWebhook(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestInterpretGitHubWebhook(t *testing.T) {
	cases := []struct {
		name     string
		event    string
		body     string
		wantKind string
		wantPR   int
	}{
		{"pr merged", "pull_request",
			`{"action":"closed","pull_request":{"number":12,"merged":true,"merge_commit_sha":"m1","head":{"sha":"h1"}}}`,
			"merged", 12},
		{"pr conflict", "pull_request",
			`{"action":"synchronize","pull_request":{"number":12,"mergeable_state":"dirty","head":{"sha":"h1"}}}`,
			"conflict", 12},
		{"pr opened clean -> ignore", "pull_request",
			`{"action":"opened","pull_request":{"number":12,"mergeable_state":"clean","head":{"sha":"h1"}}}`,
			"", 0},
		{"check_suite failure -> red", "check_suite",
			`{"action":"completed","check_suite":{"conclusion":"failure","head_sha":"h1","pull_requests":[{"number":12}]}}`,
			"red", 12},
		{"check_suite success -> ignore", "check_suite",
			`{"action":"completed","check_suite":{"conclusion":"success","head_sha":"h1","pull_requests":[{"number":12}]}}`,
			"", 0},
		{"workflow_run failure -> red", "workflow_run",
			`{"action":"completed","workflow_run":{"conclusion":"failure","head_sha":"h1","pull_requests":[{"number":12}]}}`,
			"red", 12},
		{"check_run not completed -> ignore", "check_run",
			`{"action":"created","check_run":{"head_sha":"h1","pull_requests":[{"number":12}]}}`,
			"", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var p githubWebhookPayload
			if err := json.Unmarshal([]byte(c.body), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			sig := interpretGitHubWebhook(c.event, &p)
			if sig.kind != c.wantKind {
				t.Fatalf("kind=%q want %q", sig.kind, c.wantKind)
			}
			if c.wantKind != "" && sig.prNumber != c.wantPR {
				t.Fatalf("prNumber=%d want %d", sig.prNumber, c.wantPR)
			}
		})
	}
}

func TestVerifyGitHubWebhookSignature(t *testing.T) {
	app := &appServer{githubWebhookSecret: "topsecret"}
	body := []byte(`{"hello":"world"}`)
	if !app.verifyGitHubWebhookSignature(signWebhook("topsecret", string(body)), body) {
		t.Fatal("valid signature rejected")
	}
	if app.verifyGitHubWebhookSignature("sha256=deadbeef", body) {
		t.Fatal("invalid signature accepted")
	}
	// Fail closed when no secret is configured.
	empty := &appServer{}
	if empty.verifyGitHubWebhookSignature(signWebhook("topsecret", string(body)), body) {
		t.Fatal("unconfigured secret should reject all deliveries")
	}
}

func webhookTestApp(t *testing.T, fake *fakeCIWatchStore) *appServer {
	t.Helper()
	app := testTurnsApp(
		t,
		&recordingSessionBus{},
		sdkSessionPod("session-47", "47", "owner@example.test", sessionmodel.ClaudeGUIMode, "claude-runner"),
	)
	app.sessionScope = "default"
	app.githubWebhookSecret = "topsecret"
	app.ciWatches = fake
	return app
}

func postWebhook(t *testing.T, app *appServer, event, body, sig string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	rec := httptest.NewRecorder()
	app.handleGitHubWebhook(rec, req)
	return rec
}

const redCheckSuiteBody = `{"action":"completed","repository":{"owner":{"login":"romaine-life"},"name":"tank-operator"},"check_suite":{"conclusion":"failure","head_sha":"abc","pull_requests":[{"number":1234}]}}`

func TestHandleGitHubWebhookRejectsBadSignature(t *testing.T) {
	app := webhookTestApp(t, &fakeCIWatchStore{})
	rec := postWebhook(t, app, "check_suite", redCheckSuiteBody, "sha256=deadbeef")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
}

func TestHandleGitHubWebhookWakesOnRed(t *testing.T) {
	fake := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", OwnerEmail: "owner@example.test",
		PROwner: "romaine-life", PRName: "tank-operator", PRNumber: 1234,
		HeadSHA: "abc", Status: pgstore.CIWatchWatching,
	}}
	app := webhookTestApp(t, fake)
	rec := postWebhook(t, app, "check_suite", redCheckSuiteBody, signWebhook("topsecret", redCheckSuiteBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// UpdateStatus(failed) runs before the wake enqueue, so it proves the red
	// path regardless of enqueue outcome in the test harness.
	if len(fake.updateStatusCalls) != 1 || fake.updateStatusCalls[0].status != pgstore.CIWatchFailed {
		t.Fatalf("updateStatus calls = %+v, want one CIWatchFailed", fake.updateStatusCalls)
	}
}

func TestHandleGitHubWebhookStaleSHAIgnored(t *testing.T) {
	fake := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", PRNumber: 1234,
		PROwner: "romaine-life", PRName: "tank-operator",
		HeadSHA: "current-sha", Status: pgstore.CIWatchWatching, // event carries head_sha "abc"
	}}
	app := webhookTestApp(t, fake)
	rec := postWebhook(t, app, "check_suite", redCheckSuiteBody, signWebhook("topsecret", redCheckSuiteBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(fake.updateStatusCalls) != 0 {
		t.Fatalf("stale-SHA delivery acted: %+v", fake.updateStatusCalls)
	}
}

const mergedPRBody = `{"action":"closed","repository":{"owner":{"login":"romaine-life"},"name":"tank-operator"},"pull_request":{"number":100,"merged":true,"merge_commit_sha":"sha-a","head":{"sha":"h1"}}}`

// TestHandleGitHubWebhookAdvancesOrchestrationPhase proves the merged-PR webhook
// walks the orchestration DAG end-to-end through the real handler: a merged
// phase PR marks the phase merged and dispatches the dependent phase, even
// though the CI-watch lookup finds nothing actionable (the two subsystems are
// independent).
func TestHandleGitHubWebhookAdvancesOrchestrationPhase(t *testing.T) {
	app := webhookTestApp(t, &fakeCIWatchStore{})
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, prNumber: 100, spoke: "spoke-a"},
		phaseSpec{key: "b", deps: []string{"a"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	app.orchestrations = newOrchestrationEngine(store, sp.spawn)

	rec := postWebhook(t, app, "pull_request", mergedPRBody, signWebhook("topsecret", mergedPRBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := store.snapshot(phaseID("a")).Status; got != pgstore.PhaseMerged {
		t.Fatalf("phase a status = %q, want merged", got)
	}
	if got := store.snapshot(phaseID("b")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("phase b status = %q, want running (dispatched)", got)
	}
	if sp.count("b") != 1 {
		t.Fatalf("phase b spawned %d times, want 1", sp.count("b"))
	}
}

// TestHandleGitHubWebhookOrchestrationDoubleMergeAdvancesOnce proves the handler
// is idempotent against GitHub's at-least-once delivery: two identical merged
// deliveries dispatch the dependent phase exactly once.
func TestHandleGitHubWebhookOrchestrationDoubleMergeAdvancesOnce(t *testing.T) {
	app := webhookTestApp(t, &fakeCIWatchStore{})
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, prNumber: 100, spoke: "spoke-a"},
		phaseSpec{key: "b", deps: []string{"a"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	app.orchestrations = newOrchestrationEngine(store, sp.spawn)

	sig := signWebhook("topsecret", mergedPRBody)
	_ = postWebhook(t, app, "pull_request", mergedPRBody, sig)
	_ = postWebhook(t, app, "pull_request", mergedPRBody, sig) // duplicate delivery

	if sp.count("b") != 1 {
		t.Fatalf("phase b spawned %d times across a double delivery, want 1", sp.count("b"))
	}
}

func TestHandleGitHubWebhookCoalescesAfterTerminal(t *testing.T) {
	fake := &fakeCIWatchStore{getByPRResult: pgstore.CIWatch{
		WatchID: "cw1", SessionID: "47", PRNumber: 1234,
		PROwner: "romaine-life", PRName: "tank-operator",
		HeadSHA: "abc", Status: pgstore.CIWatchFailed, // already terminal
	}}
	app := webhookTestApp(t, fake)
	rec := postWebhook(t, app, "check_suite", redCheckSuiteBody, signWebhook("topsecret", redCheckSuiteBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(fake.updateStatusCalls) != 0 {
		t.Fatalf("non-watching watch acted (no coalescing): %+v", fake.updateStatusCalls)
	}
}
