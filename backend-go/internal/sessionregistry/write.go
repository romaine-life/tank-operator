package sessionregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

// counterPartitionKey is the partition value for the session-counter document.
// The profiles container is partitioned on /email; the counter has no real
// owner, so we stamp this sentinel as both the partition value and the doc's
// email field. Must match the prior Python backend's value so the existing
// counter row continues to advance monotonically across the rewrite.
const counterPartitionKey = "__tank_operator_system__"

// NextSessionID returns the next monotonic session ID, allocated from a
// per-scope counter document. Doc shape matches the Python implementation that
// previously owned this counter: id=session-counter[:scope], partition value
// __tank_operator_system__, field next_session_number is the value to return
// on the NEXT call (i.e. the doc stores "next", not "last allocated").
func (s *CosmosStore) NextSessionID(ctx context.Context) (string, error) {
	counterID := compat.SessionCounterDocID(s.scope)
	pk := azcosmos.NewPartitionKeyString(counterPartitionKey)

	for attempt := 0; attempt < 20; attempt++ {
		resp, readErr := s.container.ReadItem(ctx, pk, counterID, nil)

		var next int64
		var etag string
		exists := false
		if readErr == nil {
			var existing struct {
				NextSessionNumber int64 `json:"next_session_number"`
			}
			if err := json.Unmarshal(resp.Value, &existing); err != nil {
				return "", fmt.Errorf("decode session counter: %w", err)
			}
			next = existing.NextSessionNumber
			if next < 1 {
				next = 1
			}
			etag = string(resp.ETag)
			exists = true
		} else if !isNotFound(readErr) {
			return "", fmt.Errorf("read session counter: %w", readErr)
		} else {
			next = 1
		}

		now := time.Now().UTC().Format(time.RFC3339)
		doc := buildCounterDoc(s.scope, next, exists, now)
		raw, err := json.Marshal(doc)
		if err != nil {
			return "", err
		}

		var writeErr error
		if exists {
			opts := &azcosmos.ItemOptions{IfMatchEtag: (*azcore.ETag)(&etag)}
			_, writeErr = s.container.ReplaceItem(ctx, pk, counterID, raw, opts)
		} else {
			_, writeErr = s.container.CreateItem(ctx, pk, raw, nil)
		}
		if writeErr == nil {
			return fmt.Sprintf("%d", next), nil
		}
		if isConflict(writeErr) {
			continue
		}
		return "", fmt.Errorf("allocate session id: %w", writeErr)
	}
	return "", fmt.Errorf("could not allocate session ID after 20 retries")
}

// buildCounterDoc returns the Cosmos document body for the session counter.
// The email field MUST equal counterPartitionKey because the profiles container
// is partitioned on /email; a mismatch is a 400 BadRequest on every write.
func buildCounterDoc(scope string, currentNext int64, exists bool, now string) map[string]any {
	doc := map[string]any{
		"id":                  compat.SessionCounterDocID(scope),
		"type":                "session_counter",
		"email":               counterPartitionKey,
		"session_scope":       scope,
		"next_session_number": currentNext + 1,
		"updated_at":          now,
	}
	if !exists {
		doc["created_at"] = now
	}
	return doc
}

func isNotFound(err error) bool {
	var respErr *azcore.ResponseError
	return errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound
}

func isConflict(err error) bool {
	var respErr *azcore.ResponseError
	if !errors.As(err, &respErr) {
		return false
	}
	return respErr.StatusCode == http.StatusConflict ||
		respErr.StatusCode == http.StatusPreconditionFailed
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
