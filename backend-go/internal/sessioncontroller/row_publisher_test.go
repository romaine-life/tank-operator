package sessioncontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
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

// TestMarshalRowUpdateIncludesDiscoveredRepos locks the live-wire contract
// for the new field: discovered_repos rides every row-update payload, and —
// like repos — serializes as an empty array (never null/absent) so the SPA
// never has to distinguish "field missing" from "no repos observed". This
// is what lets the sidebar chips/filter converge from the SSE stream without
// a refresh (Session Bar contract: status converges without reload).
func TestMarshalRowUpdateIncludesDiscoveredRepos(t *testing.T) {
	payload, err := MarshalRowUpdate(sessionmodel.SessionRecord{
		ID:              "7",
		Email:           "u@example.com",
		Mode:            sessionmodel.ClaudeGUIMode,
		Scope:           "default",
		Visible:         true,
		Status:          "Active",
		Repos:           []string{"nelsong6/tank-operator"},
		DiscoveredRepos: []string{"nelsong6/glimmung", "nelsong6/auth"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Row struct {
			Repos           []string `json:"repos"`
			DiscoveredRepos []string `json:"discovered_repos"`
		} `json:"row"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire.Row.DiscoveredRepos) != 2 ||
		wire.Row.DiscoveredRepos[0] != "nelsong6/glimmung" ||
		wire.Row.DiscoveredRepos[1] != "nelsong6/auth" {
		t.Fatalf("discovered_repos = %v, want the two reported slugs", wire.Row.DiscoveredRepos)
	}

	// nil DiscoveredRepos must still serialize as [] on the wire.
	emptyPayload, err := MarshalRowUpdate(sessionmodel.SessionRecord{
		ID: "8", Email: "u@example.com", Mode: sessionmodel.ClaudeGUIMode, Scope: "default",
	})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if !bytes.Contains(emptyPayload, []byte(`"discovered_repos":[]`)) {
		t.Fatalf("empty record must emit discovered_repos:[]; got %s", emptyPayload)
	}
}
