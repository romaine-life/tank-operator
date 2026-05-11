package sessionregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

// NextSessionID returns the next monotonic session ID using a per-scope counter
// document. The counter is a simple integer stored as a Cosmos item.
func (s *CosmosStore) NextSessionID(ctx context.Context) (string, error) {
	counterID := "counter:" + s.scope
	pk := azcosmos.NewPartitionKeyString(counterID)

	for attempt := 0; attempt < 5; attempt++ {
		resp, err := s.container.ReadItem(ctx, pk, counterID, nil)
		var current int64
		var etag string
		if err != nil {
			current = 0
			etag = ""
		} else {
			var doc struct {
				Value int64 `json:"value"`
			}
			if jsonErr := json.Unmarshal(resp.Value, &doc); jsonErr == nil {
				current = doc.Value
			}
			etag = string(resp.ETag)
		}

		next := current + 1
		doc := map[string]any{
			"id":    counterID,
			"scope": s.scope,
			"type":  "session_counter",
			"value": next,
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return "", err
		}

		var upsertErr error
		if etag == "" {
			_, upsertErr = s.container.CreateItem(ctx, pk, raw, nil)
		} else {
			opts := &azcosmos.ItemOptions{IfMatchEtag: (*azcore.ETag)(&etag)}
			_, upsertErr = s.container.ReplaceItem(ctx, pk, counterID, raw, opts)
		}
		if upsertErr == nil {
			return fmt.Sprintf("%d", next), nil
		}
		// Conflict (412) or already exists (409): retry.
	}
	return "", fmt.Errorf("could not allocate session ID after retries")
}

// Upsert writes or overwrites a session record in the registry.
func (s *CosmosStore) Upsert(ctx context.Context, record compat.SessionRecord) error {
	normalized := strings.ToLower(record.Email)
	docID := "session:" + record.ID
	scope := record.Scope
	if scope == "" {
		scope = s.scope
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if record.UpdatedAt == "" {
		record.UpdatedAt = now
	}
	if record.CreatedAt == "" {
		record.CreatedAt = now
	}
	doc := map[string]any{
		"id":           docID,
		"type":         "session",
		"email":        normalized,
		"session_id":   record.ID,
		"mode":         record.Mode,
		"session_scope": scope,
		"pod_name":     record.PodName,
		"name":         record.Name,
		"visible":      record.Visible,
		"requested_at": record.RequestedAt,
		"created_at":   record.CreatedAt,
		"updated_at":   record.UpdatedAt,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	pk := azcosmos.NewPartitionKeyString(normalized)
	_, err = s.container.UpsertItem(ctx, pk, raw, nil)
	return err
}

// SetName updates the display name for a session.
func (s *CosmosStore) SetName(ctx context.Context, email, sessionID string, name *string) error {
	normalized := strings.ToLower(email)
	docID := "session:" + sessionID
	pk := azcosmos.NewPartitionKeyString(normalized)

	resp, err := s.container.ReadItem(ctx, pk, docID, nil)
	if err != nil {
		return nil // not found → no-op
	}
	var doc map[string]any
	if err := json.Unmarshal(resp.Value, &doc); err != nil {
		return err
	}
	if name == nil {
		doc["name"] = nil
	} else {
		doc["name"] = *name
	}
	doc["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = s.container.ReplaceItem(ctx, pk, docID, raw, nil)
	return err
}

// MarkDeleted sets visible=false on the session registry record.
func (s *CosmosStore) MarkDeleted(ctx context.Context, email, sessionID string) error {
	normalized := strings.ToLower(email)
	docID := "session:" + sessionID
	pk := azcosmos.NewPartitionKeyString(normalized)

	resp, err := s.container.ReadItem(ctx, pk, docID, nil)
	if err != nil {
		return nil // not found → no-op
	}
	var doc map[string]any
	if err := json.Unmarshal(resp.Value, &doc); err != nil {
		return err
	}
	doc["visible"] = false
	doc["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = s.container.ReplaceItem(ctx, pk, docID, raw, nil)
	return err
}
