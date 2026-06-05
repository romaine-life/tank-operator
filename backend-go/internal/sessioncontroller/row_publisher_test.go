package sessioncontroller

import (
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

func TestMarshalRowUpdateIncludesDisplayName(t *testing.T) {
	decodeDisplayName := func(t *testing.T, record sessionmodel.SessionRecord) string {
		t.Helper()
		payload, err := MarshalRowUpdate(record)
		if err != nil {
			t.Fatalf("MarshalRowUpdate: %v", err)
		}
		var decoded struct {
			Row struct {
				DisplayName string `json:"display_name"`
			} `json:"row"`
		}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatalf("unmarshal row payload: %v", err)
		}
		return decoded.Row.DisplayName
	}

	name := "Launch draft"
	if got, want := decodeDisplayName(t, sessionmodel.SessionRecord{
		ID:      "8",
		PodName: "session-8",
		Name:    &name,
	}), "Launch draft"; got != want {
		t.Fatalf("named row display_name = %q, want %q", got, want)
	}

	if got, want := decodeDisplayName(t, sessionmodel.SessionRecord{
		ID:      "8",
		PodName: "session-8",
		Name:    nil,
	}), "8"; got != want {
		t.Fatalf("nil-name row display_name = %q, want %q (derived from pod_name)", got, want)
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
