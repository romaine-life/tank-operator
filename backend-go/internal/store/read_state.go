package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

// ConversationReadStateStore persists the per-user render cursor for a Tank
// conversation. The cursor is the same render-order value used by /timeline,
// not a Cosmos document id.
type ConversationReadStateStore interface {
	Get(ctx context.Context, email, sessionID string) (*ConversationReadStateRecord, error)
	Set(ctx context.Context, email, sessionID, lastReadOrderKey string) (ConversationReadStateRecord, error)
}

type ConversationReadStateRecord struct {
	Email            string `json:"email"`
	SessionID        string `json:"session_id"`
	LastReadOrderKey string `json:"last_read_order_key"`
	UpdatedAt        string `json:"updated_at"`
}

type cosmosConversationReadStateStore struct {
	container *azcosmos.ContainerClient
	scope     string
}

func NewCosmosConversationReadStateStore(endpoint, database, container, scope string, cred azcore.TokenCredential) (ConversationReadStateStore, error) {
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
	return &cosmosConversationReadStateStore{container: c, scope: scope}, nil
}

func (s *cosmosConversationReadStateStore) Get(ctx context.Context, email, sessionID string) (*ConversationReadStateRecord, error) {
	normalized := normalizeReadStateEmail(email)
	sessionID = strings.TrimSpace(sessionID)
	if normalized == "" || sessionID == "" {
		return nil, nil
	}
	resp, err := s.container.ReadItem(ctx, azcosmos.NewPartitionKeyString(normalized), readStateDocID(s.scope, sessionID), nil)
	if err != nil {
		if isCosmosNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	rec, err := readStateRecordFromDoc(resp.Value)
	if err != nil {
		return nil, err
	}
	if rec.Email == "" {
		rec.Email = normalized
	}
	if rec.SessionID == "" {
		rec.SessionID = sessionID
	}
	return &rec, nil
}

func (s *cosmosConversationReadStateStore) Set(ctx context.Context, email, sessionID, lastReadOrderKey string) (ConversationReadStateRecord, error) {
	normalized := normalizeReadStateEmail(email)
	sessionID = strings.TrimSpace(sessionID)
	lastReadOrderKey = strings.TrimSpace(lastReadOrderKey)
	if normalized == "" || sessionID == "" || lastReadOrderKey == "" {
		return ConversationReadStateRecord{}, nil
	}

	docID := readStateDocID(s.scope, sessionID)
	pk := azcosmos.NewPartitionKeyString(normalized)
	for attempt := 0; attempt < 20; attempt++ {
		resp, readErr := s.container.ReadItem(ctx, pk, docID, nil)

		var existing *ConversationReadStateRecord
		var etag string
		exists := false
		if readErr == nil {
			rec, err := readStateRecordFromDoc(resp.Value)
			if err != nil {
				return ConversationReadStateRecord{}, fmt.Errorf("decode read state: %w", err)
			}
			if rec.LastReadOrderKey != "" && rec.LastReadOrderKey >= lastReadOrderKey {
				return rec, nil
			}
			existing = &rec
			etag = string(resp.ETag)
			exists = true
		} else if !isCosmosNotFound(readErr) {
			return ConversationReadStateRecord{}, fmt.Errorf("read read state: %w", readErr)
		}

		rec := ConversationReadStateRecord{
			Email:            normalized,
			SessionID:        sessionID,
			LastReadOrderKey: lastReadOrderKey,
			UpdatedAt:        nowISO(),
		}
		if existing != nil && existing.UpdatedAt != "" && existing.LastReadOrderKey == lastReadOrderKey {
			rec.UpdatedAt = existing.UpdatedAt
		}
		raw, err := json.Marshal(readStateDoc(s.scope, rec))
		if err != nil {
			return ConversationReadStateRecord{}, err
		}

		var writeErr error
		if exists {
			opts := &azcosmos.ItemOptions{IfMatchEtag: (*azcore.ETag)(&etag)}
			_, writeErr = s.container.ReplaceItem(ctx, pk, docID, raw, opts)
		} else {
			_, writeErr = s.container.CreateItem(ctx, pk, raw, nil)
		}
		if writeErr == nil {
			return rec, nil
		}
		if isCosmosConflict(writeErr) {
			continue
		}
		return ConversationReadStateRecord{}, fmt.Errorf("write read state: %w", writeErr)
	}
	return ConversationReadStateRecord{}, fmt.Errorf("could not update read state after 20 retries")
}

type StubConversationReadStateStore struct {
	mu      sync.Mutex
	records map[string]ConversationReadStateRecord
}

func NewStubConversationReadStateStore() *StubConversationReadStateStore {
	return &StubConversationReadStateStore{records: map[string]ConversationReadStateRecord{}}
}

func (s *StubConversationReadStateStore) Get(_ context.Context, email, sessionID string) (*ConversationReadStateRecord, error) {
	if s == nil {
		return nil, nil
	}
	key, _, _ := readStateMemoryKey(email, sessionID)
	if key == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[key]
	if !ok {
		return nil, nil
	}
	return &rec, nil
}

func (s *StubConversationReadStateStore) Set(_ context.Context, email, sessionID, lastReadOrderKey string) (ConversationReadStateRecord, error) {
	if s == nil {
		return ConversationReadStateRecord{}, nil
	}
	key, normalized, trimmedSessionID := readStateMemoryKey(email, sessionID)
	lastReadOrderKey = strings.TrimSpace(lastReadOrderKey)
	if key == "" || lastReadOrderKey == "" {
		return ConversationReadStateRecord{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[key]; ok && existing.LastReadOrderKey >= lastReadOrderKey {
		return existing, nil
	}
	rec := ConversationReadStateRecord{
		Email:            normalized,
		SessionID:        trimmedSessionID,
		LastReadOrderKey: lastReadOrderKey,
		UpdatedAt:        nowISO(),
	}
	s.records[key] = rec
	return rec, nil
}

func readStateMemoryKey(email, sessionID string) (string, string, string) {
	normalized := normalizeReadStateEmail(email)
	trimmedSessionID := strings.TrimSpace(sessionID)
	if normalized == "" || trimmedSessionID == "" {
		return "", "", ""
	}
	return normalized + "\x1f" + trimmedSessionID, normalized, trimmedSessionID
}

func readStateDocID(scope, sessionID string) string {
	return "read-state:" + compat.SessionStorageKey(scope, sessionID)
}

func readStateDoc(scope string, rec ConversationReadStateRecord) map[string]any {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	storageKey := compat.SessionStorageKey(scope, rec.SessionID)
	return map[string]any{
		"id":                  readStateDocID(scope, rec.SessionID),
		"type":                "conversation_read_state",
		"email":               normalizeReadStateEmail(rec.Email),
		"session_id":          strings.TrimSpace(rec.SessionID),
		"session_scope":       strings.TrimSpace(scope),
		"session_storage_key": storageKey,
		"last_read_order_key": strings.TrimSpace(rec.LastReadOrderKey),
		"updated_at":          rec.UpdatedAt,
	}
}

func readStateRecordFromDoc(data []byte) (ConversationReadStateRecord, error) {
	var doc struct {
		Email            string `json:"email"`
		SessionID        string `json:"session_id"`
		LastReadOrderKey string `json:"last_read_order_key"`
		UpdatedAt        string `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ConversationReadStateRecord{}, err
	}
	return ConversationReadStateRecord{
		Email:            normalizeReadStateEmail(doc.Email),
		SessionID:        strings.TrimSpace(doc.SessionID),
		LastReadOrderKey: strings.TrimSpace(doc.LastReadOrderKey),
		UpdatedAt:        doc.UpdatedAt,
	}, nil
}

func normalizeReadStateEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func isCosmosNotFound(err error) bool {
	var respErr *azcore.ResponseError
	return errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound
}

func isCosmosConflict(err error) bool {
	var respErr *azcore.ResponseError
	if !errors.As(err, &respErr) {
		return false
	}
	return respErr.StatusCode == http.StatusConflict ||
		respErr.StatusCode == http.StatusPreconditionFailed
}
