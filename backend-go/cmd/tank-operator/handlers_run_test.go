package main

import (
	"context"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

type recordingRunEventStore struct {
	events []store.RunEventRecord
}

func (s *recordingRunEventStore) Append(_ context.Context, email, sessionID, runID, eventType string, payload map[string]any) (store.RunEventRecord, error) {
	rec := store.RunEventRecord{
		RunID:     runID,
		SessionID: sessionID,
		Email:     email,
		EventID:   int64(len(s.events) + 1),
		Type:      eventType,
		Payload:   payload,
		CreatedAt: "2026-05-12T00:00:00Z",
	}
	s.events = append(s.events, rec)
	return rec, nil
}

func (s *recordingRunEventStore) ListAfter(_ context.Context, _, _ string, _ int64, _ int) ([]store.RunEventRecord, error) {
	return nil, nil
}

func TestBuildStdoutObserverEmitsFrontendLifecyclePayloads(t *testing.T) {
	recorder := &recordingRunEventStore{}
	observe := buildStdoutObserver(context.Background(), recorder, "user@example.com", "session-1", "run-1", "claude")
	if observe == nil {
		t.Fatal("observer is nil")
	}

	observe(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]}}` + "\n")
	observe(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"ok"}],"is_error":false}]}}` + "\n")
	observe(`{"type":"result","result":"done"}` + "\n")

	var toolStarted, toolCompleted, messageCreated *store.RunEventRecord
	for i := range recorder.events {
		switch recorder.events[i].Type {
		case "run.tool.started":
			toolStarted = &recorder.events[i]
		case "run.tool.completed":
			toolCompleted = &recorder.events[i]
		case "run.message.created":
			messageCreated = &recorder.events[i]
		}
	}
	if toolStarted == nil {
		t.Fatal("missing run.tool.started")
	}
	if got := toolStarted.Payload["name"]; got != "Bash" {
		t.Fatalf("run.tool.started name = %v, want Bash", got)
	}
	if got := toolStarted.Payload["tool_use_id"]; got != "toolu_1" {
		t.Fatalf("run.tool.started tool_use_id = %v, want toolu_1", got)
	}
	if toolCompleted == nil {
		t.Fatal("missing run.tool.completed")
	}
	if got := toolCompleted.Payload["tool_use_id"]; got != "toolu_1" {
		t.Fatalf("run.tool.completed tool_use_id = %v, want toolu_1", got)
	}
	if got := toolCompleted.Payload["output"]; got != "ok" {
		t.Fatalf("run.tool.completed output = %v, want ok", got)
	}
	if messageCreated == nil {
		t.Fatal("missing run.message.created")
	}
	if got := messageCreated.Payload["role"]; got != "assistant" {
		t.Fatalf("run.message.created role = %v, want assistant", got)
	}
	if got := messageCreated.Payload["text"]; got != "done" {
		t.Fatalf("run.message.created text = %v, want done", got)
	}
}

func TestBuildStdoutObserverWaitsForNewlineBeforeParsing(t *testing.T) {
	recorder := &recordingRunEventStore{}
	observe := buildStdoutObserver(context.Background(), recorder, "user@example.com", "session-1", "run-1", "claude")
	if observe == nil {
		t.Fatal("observer is nil")
	}

	observe(`{"type":"result","result":"done"}`)
	if countEvents(recorder.events, "run.message.created") != 0 {
		t.Fatal("parsed JSON before line was complete")
	}
	observe("\n")
	if got := countEvents(recorder.events, "run.message.created"); got != 1 {
		t.Fatalf("run.message.created count = %d, want 1", got)
	}
}

func countEvents(events []store.RunEventRecord, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}
