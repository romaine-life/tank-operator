package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

// TurnQueueStatus is the lifecycle status of a queued turn.
type TurnQueueStatus string

const (
	TurnPending   TurnQueueStatus = "pending"
	TurnClaimed   TurnQueueStatus = "claimed"
	TurnCompleted TurnQueueStatus = "completed"
	TurnFailed    TurnQueueStatus = "failed"
)

// TurnRecord is a single queued turn descriptor. The orchestrator enqueues
// one per dispatch; the pod-side runner claims pending rows in created_at
// order, drives the agent, then marks them completed or failed. Recovery is
// scoped to runner-process restarts while the session pod still exists; a dead
// or deleted session pod remains terminal for that session.
type TurnRecord struct {
	TurnID         string          `json:"turn_id"`
	SessionID      string          `json:"session_id"`
	Email          string          `json:"email"`
	Provider       string          `json:"provider"`
	Source         string          `json:"source,omitempty"`
	ClientNonce    string          `json:"client_nonce,omitempty"`
	TargetTurnID   string          `json:"target_turn_id,omitempty"`
	Prompt         string          `json:"prompt"`
	Model          string          `json:"model,omitempty"`
	PermissionMode string          `json:"permission_mode,omitempty"`
	SkillName      string          `json:"skill_name,omitempty"`
	FollowUp       bool            `json:"follow_up"`
	Status         TurnQueueStatus `json:"status"`
	CreatedAt      string          `json:"created_at"`
	ClaimedAt      *string         `json:"claimed_at"`
	ClaimID        *string         `json:"claim_id"`
	ClaimedBy      *string         `json:"claimed_by"`
	ClaimExpiresAt *string         `json:"claim_expires_at"`
	AttemptCount   int             `json:"attempt_count"`
	AvailableAt    *string         `json:"available_at"`
	CompletedAt    *string         `json:"completed_at"`
	LastError      string          `json:"last_error,omitempty"`
}

// TurnQueueStore persists per-session turn descriptors. The orchestrator
// is the only producer today; the pod-side SDK runners are the consumers.
// Containers partition on the scoped storage key in session_id so a session's
// turn history is one-partition reads without cross-slot collisions.
type TurnQueueStore interface {
	Enqueue(ctx context.Context, rec TurnRecord) error
	NextPending(ctx context.Context, sessionID string) (*TurnRecord, error)
	MarkClaimed(ctx context.Context, sessionID, turnID string) error
	MarkCompleted(ctx context.Context, sessionID, turnID string) error
	MarkFailed(ctx context.Context, sessionID, turnID string) error
}

type cosmosTurnQueueStore struct {
	container *azcosmos.ContainerClient
	scope     string
}

func NewCosmosTurnQueueStore(endpoint, database, container, scope string, cred azcore.TokenCredential) (TurnQueueStore, error) {
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
	return &cosmosTurnQueueStore{container: c, scope: scope}, nil
}

func (s *cosmosTurnQueueStore) Enqueue(ctx context.Context, rec TurnRecord) error {
	if rec.TurnID == "" || rec.SessionID == "" {
		return fmt.Errorf("turn queue: turn_id and session_id are required")
	}
	if rec.Status == "" {
		rec.Status = TurnPending
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = nowISO()
	}
	rec.Email = strings.ToLower(strings.TrimSpace(rec.Email))
	storageKey := s.storageKey(rec.SessionID)
	doc := turnDocForStorageKey(rec, storageKey)
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	pk := azcosmos.NewPartitionKeyString(storageKey)
	// CreateItem so a re-dispatch with the same turn_id surfaces (409) rather
	// than silently overwriting an in-flight turn. Re-dispatches should not
	// happen in normal operation — each user message generates a fresh
	// turn_id; if the orchestrator double-fires, a conflict is the
	// right signal.
	_, err = s.container.CreateItem(ctx, pk, raw, nil)
	if err != nil {
		var re *azcore.ResponseError
		if errors.As(err, &re) && re.StatusCode == http.StatusConflict {
			// Already enqueued. Producer is at-most-once from here.
			return nil
		}
		return err
	}
	return nil
}

func (s *cosmosTurnQueueStore) NextPending(ctx context.Context, sessionID string) (*TurnRecord, error) {
	storageKey := s.storageKey(sessionID)
	query := "SELECT TOP 1 * FROM c WHERE c.session_id = @session_id AND c.status = @status ORDER BY c.created_at ASC"
	params := []azcosmos.QueryParameter{
		{Name: "@session_id", Value: storageKey},
		{Name: "@status", Value: string(TurnPending)},
	}
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(storageKey), &azcosmos.QueryOptions{QueryParameters: params})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			rec, err := turnFromDoc(item)
			if err != nil {
				continue
			}
			return &rec, nil
		}
	}
	return nil, nil
}

func (s *cosmosTurnQueueStore) MarkClaimed(ctx context.Context, sessionID, turnID string) error {
	now := nowISO()
	return s.patchStatus(ctx, sessionID, turnID, TurnClaimed, &now, nil)
}

func (s *cosmosTurnQueueStore) MarkCompleted(ctx context.Context, sessionID, turnID string) error {
	now := nowISO()
	return s.patchStatus(ctx, sessionID, turnID, TurnCompleted, nil, &now)
}

func (s *cosmosTurnQueueStore) MarkFailed(ctx context.Context, sessionID, turnID string) error {
	now := nowISO()
	return s.patchStatus(ctx, sessionID, turnID, TurnFailed, nil, &now)
}

func (s *cosmosTurnQueueStore) patchStatus(ctx context.Context, sessionID, turnID string, status TurnQueueStatus, claimedAt, completedAt *string) error {
	storageKey := s.storageKey(sessionID)
	pk := azcosmos.NewPartitionKeyString(storageKey)
	resp, err := s.container.ReadItem(ctx, pk, "turn:"+turnID, nil)
	if err != nil {
		var re *azcore.ResponseError
		if errors.As(err, &re) && re.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	rec, err := turnFromDoc(resp.Value)
	if err != nil {
		return err
	}
	rec.Status = status
	if claimedAt != nil {
		rec.ClaimedAt = claimedAt
	}
	if completedAt != nil {
		rec.CompletedAt = completedAt
	}
	raw, err := json.Marshal(turnDocForStorageKey(rec, storageKey))
	if err != nil {
		return err
	}
	_, err = s.container.ReplaceItem(ctx, pk, "turn:"+turnID, raw, nil)
	return err
}

func turnDoc(r TurnRecord) map[string]any {
	return turnDocForStorageKey(r, r.SessionID)
}

// turnDocForStorageKey shapes the wire JSON. Doc id is `turn:<turn_id>` so the
// partition (session_id) can hold sibling kinds of docs later without collision.
func turnDocForStorageKey(r TurnRecord, storageKey string) map[string]any {
	if storageKey == "" {
		storageKey = r.SessionID
	}
	doc := map[string]any{
		"id":                     "turn:" + r.TurnID,
		"type":                   "turn",
		"turn_id":                r.TurnID,
		"session_id":             storageKey,
		"tank_public_session_id": r.SessionID,
		"email":                  r.Email,
		"provider":               r.Provider,
		"source":                 r.Source,
		"client_nonce":           r.ClientNonce,
		"target_turn_id":         r.TargetTurnID,
		"prompt":                 r.Prompt,
		"follow_up":              r.FollowUp,
		"status":                 string(r.Status),
		"created_at":             r.CreatedAt,
		"claimed_at":             r.ClaimedAt,
		"claim_id":               r.ClaimID,
		"claimed_by":             r.ClaimedBy,
		"claim_expires_at":       r.ClaimExpiresAt,
		"attempt_count":          r.AttemptCount,
		"available_at":           r.AvailableAt,
		"completed_at":           r.CompletedAt,
		"last_error":             r.LastError,
		"model":                  r.Model,
		"permission_mode":        r.PermissionMode,
		"skill_name":             r.SkillName,
	}
	return doc
}

func turnFromDoc(data []byte) (TurnRecord, error) {
	var d struct {
		TurnID          string  `json:"turn_id"`
		SessionID       string  `json:"session_id"`
		PublicSessionID string  `json:"tank_public_session_id"`
		Email           string  `json:"email"`
		Provider        string  `json:"provider"`
		Source          string  `json:"source"`
		ClientNonce     string  `json:"client_nonce"`
		TargetTurnID    string  `json:"target_turn_id"`
		Prompt          string  `json:"prompt"`
		Model           string  `json:"model"`
		PermissionMode  string  `json:"permission_mode"`
		SkillName       string  `json:"skill_name"`
		FollowUp        bool    `json:"follow_up"`
		Status          string  `json:"status"`
		CreatedAt       string  `json:"created_at"`
		ClaimedAt       *string `json:"claimed_at"`
		ClaimID         *string `json:"claim_id"`
		ClaimedBy       *string `json:"claimed_by"`
		ClaimExpiresAt  *string `json:"claim_expires_at"`
		AttemptCount    int     `json:"attempt_count"`
		AvailableAt     *string `json:"available_at"`
		CompletedAt     *string `json:"completed_at"`
		LastError       string  `json:"last_error"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return TurnRecord{}, err
	}
	sessionID := d.PublicSessionID
	if sessionID == "" {
		sessionID = d.SessionID
	}
	return TurnRecord{
		TurnID:         d.TurnID,
		SessionID:      sessionID,
		Email:          d.Email,
		Provider:       d.Provider,
		Source:         d.Source,
		ClientNonce:    d.ClientNonce,
		TargetTurnID:   d.TargetTurnID,
		Prompt:         d.Prompt,
		Model:          d.Model,
		PermissionMode: d.PermissionMode,
		SkillName:      d.SkillName,
		FollowUp:       d.FollowUp,
		Status:         TurnQueueStatus(d.Status),
		CreatedAt:      d.CreatedAt,
		ClaimedAt:      d.ClaimedAt,
		ClaimID:        d.ClaimID,
		ClaimedBy:      d.ClaimedBy,
		ClaimExpiresAt: d.ClaimExpiresAt,
		AttemptCount:   d.AttemptCount,
		AvailableAt:    d.AvailableAt,
		CompletedAt:    d.CompletedAt,
		LastError:      d.LastError,
	}, nil
}

// Listing helper for tests / future admin surfaces.
func (s *cosmosTurnQueueStore) ListBySession(ctx context.Context, sessionID string, limit int) ([]TurnRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	storageKey := s.storageKey(sessionID)
	query := "SELECT * FROM c WHERE c.session_id = @session_id ORDER BY c.created_at ASC OFFSET 0 LIMIT @limit"
	params := []azcosmos.QueryParameter{
		{Name: "@session_id", Value: storageKey},
		{Name: "@limit", Value: limit},
	}
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(storageKey), &azcosmos.QueryOptions{QueryParameters: params})
	var out []TurnRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			rec, err := turnFromDoc(item)
			if err != nil {
				continue
			}
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

func (s *cosmosTurnQueueStore) storageKey(sessionID string) string {
	return compat.SessionStorageKey(s.scope, sessionID)
}

// StubTurnQueueStore satisfies the interface when Cosmos is unavailable
// (first-install ordering, local dev without endpoint). Writes are no-ops;
// reads return nothing, never error.
type StubTurnQueueStore struct{}

func (StubTurnQueueStore) Enqueue(_ context.Context, _ TurnRecord) error { return nil }
func (StubTurnQueueStore) NextPending(_ context.Context, _ string) (*TurnRecord, error) {
	return nil, nil
}
func (StubTurnQueueStore) MarkClaimed(_ context.Context, _, _ string) error   { return nil }
func (StubTurnQueueStore) MarkCompleted(_ context.Context, _, _ string) error { return nil }
func (StubTurnQueueStore) MarkFailed(_ context.Context, _, _ string) error    { return nil }
