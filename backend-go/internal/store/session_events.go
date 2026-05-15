package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
)

// SessionEventStore reads the canonical SDK events the pod-side runners write
// to the session-events Cosmos container. The orchestrator owns writes through
// the session bus persister, and the SPA consumes those same durable rows
// through timeline snapshots and the SSE stream.
type SessionEventStore interface {
	Upsert(ctx context.Context, event map[string]any) error
	ListBySession(ctx context.Context, tankSessionID string, cursor SessionEventCursor, limit int) (SessionEventPage, error)
	HasOrderKey(ctx context.Context, tankSessionID, orderKey string) (bool, error)
	FindTurnTerminal(ctx context.Context, tankSessionID, turnID string) (map[string]any, error)
	// LatestLifecycleEvents returns the most recent N lifecycle events
	// (turn.*, item.failed, tool.approval_*) for a session in ascending
	// order_key. Bounded read used by /api/sessions/activity instead of
	// folding the full ledger.
	LatestLifecycleEvents(ctx context.Context, tankSessionID string, limit int) ([]map[string]any, error)
	// UnreadOutputCount returns the number of distinct timeline_id /
	// turn_id markers that count as "unread output" strictly after the
	// caller's last_read_order_key cursor. Implemented as a Cosmos COUNT
	// query so it's O(1) RU per session regardless of history size.
	UnreadOutputCount(ctx context.Context, tankSessionID, afterOrderKey string) (int, error)
}

// LifecycleEventTypes is the set of event types that drive run-status,
// active-turn-id, and needs-input transitions in the activity summary.
// Centralized here so the Cosmos query, the stub, and the activity
// handler stay in sync. Order_key fold semantics are: ASC, last-write-
// wins per field.
var LifecycleEventTypes = []string{
	"turn.submitted",
	"turn.started",
	"turn.completed",
	"turn.failed",
	"turn.command_failed",
	"turn.interrupted",
	"item.failed",
	"tool.approval_requested",
	"tool.approval_resolved",
}

// UnreadOutputItemTypes are event types whose timeline_id contributes to
// the unread-output count. Excludes user-actor events and metadata-only
// turn lifecycle markers (turn.submitted / turn.started / turn.completed
// are not "unread output" — they're lifecycle, not content).
var UnreadOutputItemTypes = []string{
	"item.started",
	"item.delta",
	"item.completed",
	"item.failed",
	"tool.approval_requested",
	"tool.approval_resolved",
}

// UnreadOutputTurnTypes are turn-level terminal events that count as
// unread output via their turn_id (no timeline_id on these).
var UnreadOutputTurnTypes = []string{
	"turn.failed",
	"turn.command_failed",
	"turn.interrupted",
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

func (s *cosmosSessionEventStore) Upsert(ctx context.Context, event map[string]any) error {
	if err := conversation.ValidateEventMap(event); err != nil {
		return err
	}
	doc := cloneSessionEventMap(event)
	storageKey := stringField(doc, "tank_session_id")
	publicSessionID := stringField(doc, "session_id")
	if storageKey == "" {
		storageKey = compat.SessionStorageKey(s.scope, publicSessionID)
	}
	if storageKey == "" {
		return errMissingSessionEventField("tank_session_id")
	}
	id := stringField(doc, "id")
	if id == "" {
		id = stringField(doc, "uuid")
	}
	if id == "" {
		id = stringField(doc, "event_id")
	}
	if id == "" {
		return errMissingSessionEventField("id")
	}
	doc["id"] = id
	doc["tank_session_id"] = storageKey
	if _, ok := doc["tank_public_session_id"]; !ok && publicSessionID != "" {
		doc["tank_public_session_id"] = publicSessionID
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return retryOnCosmosThrottle(ctx, func() error {
		_, err := s.container.UpsertItem(ctx, azcosmos.NewPartitionKeyString(storageKey), raw, nil)
		return err
	})
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
				return SessionEventPage{}, fmt.Errorf("session-events doc is not JSON: %w", err)
			}
			if err := conversation.ValidateEventMap(doc); err != nil {
				// Per docs/migration-policy.md, the read path no longer
				// silently filters malformed docs. The producer-side cutover
				// (runner dispatch contract, persister schema-terminal NAK)
				// guarantees only Tank events land in Cosmos, and the
				// pre-deploy Cosmos audit script (scripts/audit-session-
				// events.py) clears any pre-existing rows. A failure here
				// means one of those guarantees regressed — surface it.
				return SessionEventPage{}, fmt.Errorf("session-events doc rejected by schema: %w", err)
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

func (s *cosmosSessionEventStore) FindTurnTerminal(ctx context.Context, tankSessionID, turnID string) (map[string]any, error) {
	if strings.TrimSpace(turnID) == "" {
		return nil, nil
	}
	storageKey := compat.SessionStorageKey(s.scope, tankSessionID)
	query := "SELECT TOP 1 * FROM c WHERE c.tank_session_id = @sid AND c.turn_id = @turn_id AND (c.type = @completed OR c.type = @failed OR c.type = @interrupted) ORDER BY c.order_key DESC"
	params := []azcosmos.QueryParameter{
		{Name: "@sid", Value: storageKey},
		{Name: "@turn_id", Value: turnID},
		{Name: "@completed", Value: string(conversation.EventTurnCompleted)},
		{Name: "@failed", Value: string(conversation.EventTurnFailed)},
		{Name: "@interrupted", Value: string(conversation.EventTurnInterrupted)},
	}
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(storageKey), &azcosmos.QueryOptions{QueryParameters: params})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, raw := range page.Items {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				return nil, fmt.Errorf("session-events doc is not JSON: %w", err)
			}
			if err := conversation.ValidateEventMap(doc); err != nil {
				return nil, fmt.Errorf("session-events doc rejected by schema: %w", err)
			}
			doc["tank_session_id"] = tankSessionID
			return doc, nil
		}
	}
	return nil, nil
}

func (s *cosmosSessionEventStore) LatestLifecycleEvents(ctx context.Context, tankSessionID string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	storageKey := compat.SessionStorageKey(s.scope, tankSessionID)
	placeholders := make([]string, len(LifecycleEventTypes))
	params := []azcosmos.QueryParameter{
		{Name: "@sid", Value: storageKey},
		{Name: "@limit", Value: limit},
	}
	for i, t := range LifecycleEventTypes {
		name := fmt.Sprintf("@t%d", i)
		placeholders[i] = name
		params = append(params, azcosmos.QueryParameter{Name: name, Value: t})
	}
	query := fmt.Sprintf(
		"SELECT TOP @limit * FROM c WHERE c.tank_session_id = @sid AND c.type IN (%s) AND IS_DEFINED(c.order_key) AND c.order_key != '' ORDER BY c.order_key DESC",
		strings.Join(placeholders, ", "),
	)
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(storageKey), &azcosmos.QueryOptions{QueryParameters: params})
	out := make([]map[string]any, 0, limit)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, raw := range page.Items {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				return nil, fmt.Errorf("session-events doc is not JSON: %w", err)
			}
			if err := conversation.ValidateEventMap(doc); err != nil {
				return nil, fmt.Errorf("session-events doc rejected by schema: %w", err)
			}
			doc["tank_session_id"] = tankSessionID
			out = append(out, doc)
		}
	}
	// Cosmos returns DESC; the activity fold expects ASC.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *cosmosSessionEventStore) UnreadOutputCount(ctx context.Context, tankSessionID, afterOrderKey string) (int, error) {
	storageKey := compat.SessionStorageKey(s.scope, tankSessionID)
	itemCount, err := s.countDistinctField(
		ctx, storageKey, "timeline_id", UnreadOutputItemTypes, afterOrderKey,
	)
	if err != nil {
		return 0, err
	}
	turnCount, err := s.countDistinctField(
		ctx, storageKey, "turn_id", UnreadOutputTurnTypes, afterOrderKey,
	)
	if err != nil {
		return 0, err
	}
	return itemCount + turnCount, nil
}

func (s *cosmosSessionEventStore) countDistinctField(
	ctx context.Context, storageKey, field string, types []string, afterOrderKey string,
) (int, error) {
	if len(types) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(types))
	params := []azcosmos.QueryParameter{
		{Name: "@sid", Value: storageKey},
	}
	for i, t := range types {
		name := fmt.Sprintf("@t%d", i)
		placeholders[i] = name
		params = append(params, azcosmos.QueryParameter{Name: name, Value: t})
	}
	where := fmt.Sprintf(
		"c.tank_session_id = @sid AND c.type IN (%s) AND (NOT IS_DEFINED(c.actor) OR c.actor != 'user') AND IS_DEFINED(c.%s) AND c.%s != ''",
		strings.Join(placeholders, ", "), field, field,
	)
	if strings.TrimSpace(afterOrderKey) != "" {
		where += " AND c.order_key > @after"
		params = append(params, azcosmos.QueryParameter{Name: "@after", Value: afterOrderKey})
	}
	query := fmt.Sprintf(
		"SELECT VALUE COUNT(1) FROM (SELECT DISTINCT VALUE c.%s FROM c WHERE %s)",
		field, where,
	)
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(storageKey), &azcosmos.QueryOptions{QueryParameters: params})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return 0, err
		}
		for _, raw := range page.Items {
			var n int
			if err := json.Unmarshal(raw, &n); err != nil {
				return 0, fmt.Errorf("count result not int: %w", err)
			}
			return n, nil
		}
	}
	return 0, nil
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

func (StubSessionEventStore) Upsert(_ context.Context, _ map[string]any) error { return nil }

func (StubSessionEventStore) ListBySession(_ context.Context, _ string, _ SessionEventCursor, _ int) (SessionEventPage, error) {
	return SessionEventPage{Events: []map[string]any{}}, nil
}

func (StubSessionEventStore) HasOrderKey(_ context.Context, _, orderKey string) (bool, error) {
	return strings.TrimSpace(orderKey) == "", nil
}

func (StubSessionEventStore) FindTurnTerminal(_ context.Context, _, _ string) (map[string]any, error) {
	return nil, nil
}

func (StubSessionEventStore) LatestLifecycleEvents(_ context.Context, _ string, _ int) ([]map[string]any, error) {
	return nil, nil
}

func (StubSessionEventStore) UnreadOutputCount(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}

func cloneSessionEventMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func stringField(doc map[string]any, key string) string {
	value, _ := doc[key].(string)
	return strings.TrimSpace(value)
}

func errMissingSessionEventField(field string) error {
	return &sessionEventFieldError{field: field}
}

type sessionEventFieldError struct {
	field string
}

func (e *sessionEventFieldError) Error() string {
	return "session event " + e.field + " is required"
}
