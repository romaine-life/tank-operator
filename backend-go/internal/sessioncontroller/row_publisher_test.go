package sessioncontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type fakeRowFetcher struct {
	record sessionmodel.SessionRecord
	ok     bool
	err    error
}

func (f fakeRowFetcher) Get(context.Context, string, string) (sessionmodel.SessionRecord, bool, error) {
	return f.record, f.ok, f.err
}

type recordedRowPublish struct {
	email   string
	scope   string
	payload []byte
}

type recordingRowPublisher struct {
	rows  []recordedRowPublish
	wakes []string
}

func (p *recordingRowPublisher) PublishSessionRowUpdate(_ context.Context, email, scope string, payload []byte) error {
	p.rows = append(p.rows, recordedRowPublish{
		email:   email,
		scope:   scope,
		payload: append([]byte(nil), payload...),
	})
	return nil
}

func (p *recordingRowPublisher) PublishSessionEventWake(_ context.Context, storageKey string) error {
	p.wakes = append(p.wakes, storageKey)
	return nil
}

func TestMarshalRowUpdateIncludesName(t *testing.T) {
	decodeName := func(t *testing.T, record sessionmodel.SessionRecord) string {
		t.Helper()
		payload, err := MarshalRowUpdate(record)
		if err != nil {
			t.Fatalf("MarshalRowUpdate: %v", err)
		}
		// The redundant display_name field must no longer ride the row wire.
		if bytes.Contains(payload, []byte(`"display_name"`)) {
			t.Fatalf("row payload still carries display_name: %s", payload)
		}
		var decoded struct {
			Row struct {
				Name string `json:"name"`
			} `json:"row"`
		}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatalf("unmarshal row payload: %v", err)
		}
		return decoded.Row.Name
	}

	if got, want := decodeName(t, sessionmodel.SessionRecord{
		ID:      "8",
		PodName: "session-8",
		Name:    "Launch draft",
	}), "Launch draft"; got != want {
		t.Fatalf("named row name = %q, want %q", got, want)
	}

	// Name is NON-NULL now: an "unnamed" session carries the canonical
	// default that Create assigns / the migration backfills (the short id).
	if got, want := decodeName(t, sessionmodel.SessionRecord{
		ID:      "8",
		PodName: "session-8",
		Name:    "8",
	}), "8"; got != want {
		t.Fatalf("default-name row name = %q, want %q", got, want)
	}
}

func TestMarshalRowUpdateIncludesSessionImage(t *testing.T) {
	payload, err := MarshalRowUpdate(sessionmodel.SessionRecord{
		ID:           "42",
		Email:        "user@example.com",
		Mode:         sessionmodel.CodexGUIMode,
		Scope:        "tank-operator-slot-1",
		PodName:      "session-42",
		Name:         "42",
		SessionImage: "romainecr.azurecr.io/codex-container:codex-BRANCH",
		Visible:      true,
		Status:       "Active",
		Repos:        []string{},
		Capabilities: []string{},
	})
	if err != nil {
		t.Fatalf("MarshalRowUpdate: %v", err)
	}
	var decoded struct {
		Row struct {
			SessionImage string `json:"session_image"`
		} `json:"row"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal row payload: %v", err)
	}
	if got, want := decoded.Row.SessionImage, "romainecr.azurecr.io/codex-container:codex-BRANCH"; got != want {
		t.Fatalf("session_image = %q, want %q", got, want)
	}
}

func TestPublishCurrentRowWakesTranscriptStream(t *testing.T) {
	publisher := &recordingRowPublisher{}
	rowPublisher := &RowPublisher{
		Fetcher: fakeRowFetcher{
			ok: true,
			record: sessionmodel.SessionRecord{
				ID:        "42",
				Email:     "user@example.com",
				Mode:      sessionmodel.ClaudeGUIMode,
				Scope:     "team-a",
				Visible:   true,
				Status:    "Active",
				CreatedAt: "2026-05-21T00:00:00Z",
				UpdatedAt: "2026-05-21T00:00:01Z",
			},
		},
		Publisher: publisher,
		Scope:     "team-a",
	}

	rowPublisher.PublishCurrentRow(context.Background(), "User@Example.COM", "42")

	if len(publisher.rows) != 1 {
		t.Fatalf("row publishes = %d, want 1", len(publisher.rows))
	}
	if got := publisher.rows[0].email; got != "user@example.com" {
		t.Fatalf("row email = %q, want lowercase owner", got)
	}
	if len(publisher.wakes) != 1 {
		t.Fatalf("event wakes = %d, want 1", len(publisher.wakes))
	}
	if want := sessionmodel.SessionStorageKey("team-a", "42"); publisher.wakes[0] != want {
		t.Fatalf("event wake storage key = %q, want %q", publisher.wakes[0], want)
	}
}
