package store

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

// SessionEventStore reads the canonical SDK events the pod-side
// agent-runner writes to the `session-events` Cosmos container. The
// orchestrator is a reader only here — the runner owns the write side
// (see agent-runner/src/cosmos.ts). This split is the producer contract:
// one producer, two consumers (DB reader = SPA history-replay,
// WS subscriber = SPA live tap).
//
// Container partition key is /tank_session_id (Tank's scoped storage key, not
// the SDK's UUID). New clients page by the same render-order cursor the SPA
// sorts on; the document-id cursor remains accepted for older tabs.
type SessionEventStore interface {
	ListBySession(ctx context.Context, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error)
}

type SessionEventCursor struct {
	AfterOrderKey string
	AfterID       string
}

type SessionEventPage struct {
	Events       []map[string]any
	NextOrderKey string
	HasMore      bool
}

type cosmosSessionEventStore struct {
	container *azcosmos.ContainerClient
	scope     string
}

func NewCosmosSessionEventStore(endpoint, database, container, scope string, cred azcore.TokenCredential) (SessionEventStore, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	client, err := azcosmos.NewClient(endpoint, cred, nil)
	if err != nil {
		return nil, err
	}
	c, err := client.NewContainer(database, container)
	if err != nil {
		return nil, err
	}
	return &cosmosSessionEventStore{container: c, scope: scope}, nil
}

// ListBySession returns events for one session, strictly after the
// given render-order cursor. An empty cursor returns from the beginning.
// limit caps the page size.
//
// Single-partition read (tank_session_id is the scoped partition key) so the
// query is one RU per ~1KB doc — fast and cheap. The /message/* path
// is excluded from indexing (see infra/cosmos.tf) so large assistant
// content doesn't inflate index size. Page slicing happens after the Go sort
// because older docs may not have tank_order_key; filtering by id first can
// skip events when id order and render order diverge.
func (s *cosmosSessionEventStore) ListBySession(ctx context.Context, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error) {
	limit = normalizeSessionEventLimit(limit)
	storageKey := compat.SessionStorageKey(s.scope, tankSessionID)
	query := "SELECT * FROM c WHERE c.tank_session_id = @sid"
	params := []azcosmos.QueryParameter{
		{Name: "@sid", Value: storageKey},
	}
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(storageKey), &azcosmos.QueryOptions{QueryParameters: params})
	out := make([]map[string]any, 0, limit)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return SessionEventPage{}, err
		}
		for _, raw := range page.Items {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				continue
			}
			doc["tank_session_id"] = tankSessionID
			out = append(out, doc)
		}
	}
	// New runner events carry tank_order_key (producer write time + local
	// sequence + event id). Older Claude docs use UUIDv7 and old Codex docs
	// used UUIDv4, so fall back through write timestamps before id to avoid
	// reshuffling existing sessions on every history refresh.
	sortSessionEvents(out)
	return paginateSessionEvents(out, cursor, limit), nil
}

func sortSessionEvents(out []map[string]any) {
	sort.SliceStable(out, func(i, j int) bool {
		ki := eventOrderKey(out[i])
		kj := eventOrderKey(out[j])
		if ki != "" && kj != "" && ki != kj {
			return ki < kj
		}
		if ki != kj {
			return kj == ""
		}
		ti := eventOrderTime(out[i])
		tj := eventOrderTime(out[j])
		if ti != "" && tj != "" && ti != tj {
			return ti < tj
		}
		if ti != tj {
			return tj == ""
		}
		return eventDocumentID(out[i]) < eventDocumentID(out[j])
	})
}

func eventOrderKey(doc map[string]any) string {
	for _, field := range []string{"order_key", "tank_order_key"} {
		if value, ok := doc[field].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func eventSequence(doc map[string]any) string {
	for _, field := range []string{"sequence", "tank_event_seq"} {
		switch value := doc[field].(type) {
		case int:
			return strconv.FormatInt(int64(value), 10)
		case int64:
			return strconv.FormatInt(value, 10)
		case float64:
			if value == float64(int64(value)) {
				return strconv.FormatInt(int64(value), 10)
			}
		case string:
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func eventOrderTime(doc map[string]any) string {
	for _, field := range []string{"written_at", "timestamp", "time", "created_at"} {
		if value, ok := doc[field].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func paginateSessionEvents(events []map[string]any, cursor SessionEventCursor, limit int) SessionEventPage {
	limit = normalizeSessionEventLimit(limit)
	start := sessionEventStartIndex(events, cursor)
	if start > len(events) {
		start = len(events)
	}
	end := start + limit
	if end > len(events) {
		end = len(events)
	}
	pageEvents := append([]map[string]any(nil), events[start:end]...)
	nextOrderKey := ""
	if len(pageEvents) > 0 {
		nextOrderKey = eventOrderCursor(pageEvents[len(pageEvents)-1])
	}
	return SessionEventPage{
		Events:       pageEvents,
		NextOrderKey: nextOrderKey,
		HasMore:      end < len(events),
	}
}

func sessionEventStartIndex(events []map[string]any, cursor SessionEventCursor) int {
	if cursor.AfterOrderKey != "" {
		for i, doc := range events {
			if eventOrderCursor(doc) == cursor.AfterOrderKey {
				return i + 1
			}
		}
		return 0
	}
	if cursor.AfterID != "" {
		for i, doc := range events {
			if eventDocumentID(doc) == cursor.AfterID {
				return i + 1
			}
		}
		return 0
	}
	return 0
}

func eventOrderCursor(doc map[string]any) string {
	parts := []string{
		eventOrderKey(doc),
		eventOrderTime(doc),
		eventSequence(doc),
		eventDocumentID(doc),
	}
	return strings.Join(parts, "\x1f")
}

func eventDocumentID(doc map[string]any) string {
	for _, field := range []string{"id", "uuid", "event_id"} {
		if value, ok := doc[field].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func normalizeSessionEventLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 200
	}
	return limit
}

// Stub for local dev where Cosmos isn't configured.
type StubSessionEventStore struct{}

func (StubSessionEventStore) ListBySession(_ context.Context, _ string, _ SessionEventCursor, _ int) (SessionEventPage, error) {
	return SessionEventPage{Events: []map[string]any{}}, nil
}
