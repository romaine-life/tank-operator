package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
)

// SessionEventStore reads the canonical SDK events the pod-side runners write
// to the session-events Cosmos container. The orchestrator is a reader only:
// runners write durable events, and the SPA consumes those same durable rows
// through timeline snapshots and the SSE stream.
type SessionEventStore interface {
	ListBySession(ctx context.Context, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error)
	HasOrderKey(ctx context.Context, tankSessionID, orderKey string) (bool, error)
}

type SessionEventCursor struct {
	AfterOrderKey string
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

// ListBySession returns events for one session strictly after the canonical
// Tank order_key cursor. The query stays within one partition and pages by
// order_key in Cosmos, avoiding full-session reads on every replay or stream
// tick.
func (s *cosmosSessionEventStore) ListBySession(ctx context.Context, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error) {
	limit = normalizeSessionEventLimit(limit)
	queryLimit := limit + 1
	storageKey := compat.SessionStorageKey(s.scope, tankSessionID)

	query := "SELECT * FROM c WHERE c.tank_session_id = @sid AND IS_DEFINED(c.order_key) AND c.order_key != ''"
	params := []azcosmos.QueryParameter{
		{Name: "@sid", Value: storageKey},
		{Name: "@limit", Value: queryLimit},
	}
	if cursor.AfterOrderKey != "" {
		query += " AND c.order_key > @after"
		params = append(params, azcosmos.QueryParameter{Name: "@after", Value: cursor.AfterOrderKey})
	}
	query += " ORDER BY c.order_key ASC OFFSET 0 LIMIT @limit"

	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(storageKey), &azcosmos.QueryOptions{QueryParameters: params})
	out := make([]map[string]any, 0, queryLimit)
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
			if err := conversation.ValidateEventMap(doc); err != nil {
				continue
			}
			doc["tank_session_id"] = tankSessionID
			out = append(out, doc)
		}
	}
	return sessionEventPageFromOrdered(out, limit), nil
}

func (s *cosmosSessionEventStore) HasOrderKey(ctx context.Context, tankSessionID, orderKey string) (bool, error) {
	if strings.TrimSpace(orderKey) == "" {
		return true, nil
	}
	storageKey := compat.SessionStorageKey(s.scope, tankSessionID)
	query := "SELECT TOP 1 VALUE c.order_key FROM c WHERE c.tank_session_id = @sid AND c.order_key = @order_key"
	params := []azcosmos.QueryParameter{
		{Name: "@sid", Value: storageKey},
		{Name: "@order_key", Value: orderKey},
	}
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(storageKey), &azcosmos.QueryOptions{QueryParameters: params})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return false, err
		}
		if len(page.Items) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func sessionEventPageFromOrdered(events []map[string]any, limit int) SessionEventPage {
	limit = normalizeSessionEventLimit(limit)
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	nextOrderKey := ""
	if len(events) > 0 {
		nextOrderKey = eventOrderKey(events[len(events)-1])
	}
	return SessionEventPage{
		Events:       append([]map[string]any(nil), events...),
		NextOrderKey: nextOrderKey,
		HasMore:      hasMore,
	}
}

func eventOrderKey(doc map[string]any) string {
	if value, ok := doc["order_key"].(string); ok && value != "" {
		return value
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

func (StubSessionEventStore) HasOrderKey(_ context.Context, _, orderKey string) (bool, error) {
	return strings.TrimSpace(orderKey) == "", nil
}
