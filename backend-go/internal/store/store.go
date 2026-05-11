// Package store provides Cosmos-backed (and stub) stores for active runs and
// run events. Document shapes are kept wire-compatible with the Python backend.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

// ActiveRunRecord mirrors Python's ActiveRunRecord dataclass.
type ActiveRunRecord struct {
	SessionID   string  `json:"session_id"`
	Email       string  `json:"email"`
	RunID       string  `json:"run_id"`
	PodName     string  `json:"pod_name"`
	Provider    string  `json:"provider"`
	Status      string  `json:"status"`
	StreamPath  string  `json:"stream_path"`
	PIDPath     string  `json:"pid_path"`
	StartedAt   string  `json:"started_at"`
	UpdatedAt   string  `json:"updated_at"`
	CompletedAt *string `json:"completed_at"`
}

// RunEventRecord mirrors Python's RunEventRecord dataclass.
type RunEventRecord struct {
	RunID     string         `json:"run_id"`
	SessionID string         `json:"session_id"`
	Email     string         `json:"email"`
	EventID   int64          `json:"event_id"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"created_at"`
}

// ActiveRunStore persists active/completed run state per session.
type ActiveRunStore interface {
	Start(ctx context.Context, email, sessionID, runID, podName, provider, streamPath, pidPath string) (ActiveRunRecord, error)
	GetActive(ctx context.Context, sessionID string) (*ActiveRunRecord, error)
	GetLatest(ctx context.Context, sessionID string) (*ActiveRunRecord, error)
	MarkCompleted(ctx context.Context, sessionID, runID string) error
	MarkStale(ctx context.Context, sessionID, runID string) error
}

// RunEventStore appends and queries run lifecycle events.
type RunEventStore interface {
	Append(ctx context.Context, email, sessionID, runID, eventType string, payload map[string]any) (RunEventRecord, error)
	ListAfter(ctx context.Context, runID, sessionID string, afterEventID int64, limit int) ([]RunEventRecord, error)
}

// ─── Cosmos implementations ──────────────────────────────────────────────────

type cosmosActiveRunStore struct {
	container *azcosmos.ContainerClient
}

func NewCosmosActiveRunStore(endpoint, database, container string, cred azcore.TokenCredential) (ActiveRunStore, error) {
	client, err := azcosmos.NewClient(endpoint, cred, nil)
	if err != nil {
		return nil, err
	}
	c, err := client.NewContainer(database, container)
	if err != nil {
		return nil, err
	}
	return &cosmosActiveRunStore{container: c}, nil
}

func (s *cosmosActiveRunStore) Start(ctx context.Context, email, sessionID, runID, podName, provider, streamPath, pidPath string) (ActiveRunRecord, error) {
	now := nowISO()
	rec := ActiveRunRecord{
		SessionID:  sessionID,
		Email:      strings.ToLower(email),
		RunID:      runID,
		PodName:    podName,
		Provider:   provider,
		Status:     "running",
		StreamPath: streamPath,
		PIDPath:    pidPath,
		StartedAt:  now,
		UpdatedAt:  now,
	}
	doc := activeRunDoc(rec)
	raw, err := json.Marshal(doc)
	if err != nil {
		return ActiveRunRecord{}, err
	}
	pk := azcosmos.NewPartitionKeyString(sessionID)
	_, err = s.container.UpsertItem(ctx, pk, raw, nil)
	if err != nil {
		return ActiveRunRecord{}, err
	}
	return rec, nil
}

func (s *cosmosActiveRunStore) GetActive(ctx context.Context, sessionID string) (*ActiveRunRecord, error) {
	rec, err := s.read(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, nil
	}
	if rec.Status != "starting" && rec.Status != "running" {
		return nil, nil
	}
	return rec, nil
}

func (s *cosmosActiveRunStore) GetLatest(ctx context.Context, sessionID string) (*ActiveRunRecord, error) {
	return s.read(ctx, sessionID)
}

func (s *cosmosActiveRunStore) read(ctx context.Context, sessionID string) (*ActiveRunRecord, error) {
	pk := azcosmos.NewPartitionKeyString(sessionID)
	resp, err := s.container.ReadItem(ctx, pk, sessionID, nil)
	if err != nil {
		var re *azcore.ResponseError
		if errors.As(err, &re) && re.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	rec, err := activeRunFromDoc(resp.Value)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *cosmosActiveRunStore) MarkCompleted(ctx context.Context, sessionID, runID string) error {
	return s.setStatus(ctx, sessionID, runID, "completed")
}

func (s *cosmosActiveRunStore) MarkStale(ctx context.Context, sessionID, runID string) error {
	return s.setStatus(ctx, sessionID, runID, "stale")
}

func (s *cosmosActiveRunStore) setStatus(ctx context.Context, sessionID, runID, status string) error {
	rec, err := s.read(ctx, sessionID)
	if err != nil || rec == nil {
		return err
	}
	if rec.RunID != runID {
		return nil
	}
	now := nowISO()
	rec.Status = status
	rec.UpdatedAt = now
	if status == "completed" || status == "stale" {
		rec.CompletedAt = &now
	}
	doc := activeRunDoc(*rec)
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	pk := azcosmos.NewPartitionKeyString(sessionID)
	_, err = s.container.UpsertItem(ctx, pk, raw, nil)
	return err
}

// ─── Run Events ──────────────────────────────────────────────────────────────

type cosmosRunEventStore struct {
	container *azcosmos.ContainerClient
}

func NewCosmosRunEventStore(endpoint, database, container string, cred azcore.TokenCredential) (RunEventStore, error) {
	client, err := azcosmos.NewClient(endpoint, cred, nil)
	if err != nil {
		return nil, err
	}
	c, err := client.NewContainer(database, container)
	if err != nil {
		return nil, err
	}
	return &cosmosRunEventStore{container: c}, nil
}

func (s *cosmosRunEventStore) Append(ctx context.Context, email, sessionID, runID, eventType string, payload map[string]any) (RunEventRecord, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	eventID := time.Now().UnixNano()
	now := nowISO()
	rec := RunEventRecord{
		RunID:     runID,
		SessionID: sessionID,
		Email:     strings.ToLower(email),
		EventID:   eventID,
		Type:      eventType,
		Payload:   payload,
		CreatedAt: now,
	}
	docID := fmt.Sprintf("%s:%d", runID, eventID)
	doc := map[string]any{
		"id":         docID,
		"run_id":     runID,
		"session_id": sessionID,
		"email":      strings.ToLower(email),
		"event_id":   eventID,
		"type":       eventType,
		"payload":    payload,
		"created_at": now,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return RunEventRecord{}, err
	}
	pk := azcosmos.NewPartitionKeyString(runID)
	_, err = s.container.CreateItem(ctx, pk, raw, nil)
	if err != nil {
		return RunEventRecord{}, err
	}
	return rec, nil
}

func (s *cosmosRunEventStore) ListAfter(ctx context.Context, runID, sessionID string, afterEventID int64, limit int) ([]RunEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	query := "SELECT * FROM c WHERE c.run_id = @run_id AND c.session_id = @session_id AND c.event_id > @after_id ORDER BY c.event_id ASC OFFSET 0 LIMIT @limit"
	params := []azcosmos.QueryParameter{
		{Name: "@run_id", Value: runID},
		{Name: "@session_id", Value: sessionID},
		{Name: "@after_id", Value: afterEventID},
		{Name: "@limit", Value: limit},
	}
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(runID), &azcosmos.QueryOptions{QueryParameters: params})
	var records []RunEventRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			rec, err := runEventFromDoc(item)
			if err != nil {
				continue
			}
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].EventID < records[j].EventID })
	return records, nil
}

// ─── Stub implementations ─────────────────────────────────────────────────────

type StubActiveRunStore struct{}

func (StubActiveRunStore) Start(_ context.Context, _, sessionID, runID, podName, provider, streamPath, pidPath string) (ActiveRunRecord, error) {
	now := nowISO()
	return ActiveRunRecord{SessionID: sessionID, RunID: runID, PodName: podName, Provider: provider, Status: "running", StreamPath: streamPath, PIDPath: pidPath, StartedAt: now, UpdatedAt: now}, nil
}
func (StubActiveRunStore) GetActive(_ context.Context, _ string) (*ActiveRunRecord, error)  { return nil, nil }
func (StubActiveRunStore) GetLatest(_ context.Context, _ string) (*ActiveRunRecord, error)  { return nil, nil }
func (StubActiveRunStore) MarkCompleted(_ context.Context, _, _ string) error               { return nil }
func (StubActiveRunStore) MarkStale(_ context.Context, _, _ string) error                   { return nil }

type StubRunEventStore struct{}

func (StubRunEventStore) Append(_ context.Context, _, sessionID, runID, eventType string, payload map[string]any) (RunEventRecord, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	return RunEventRecord{RunID: runID, SessionID: sessionID, EventID: time.Now().UnixNano(), Type: eventType, Payload: payload, CreatedAt: nowISO()}, nil
}
func (StubRunEventStore) ListAfter(_ context.Context, _, _ string, _ int64, _ int) ([]RunEventRecord, error) {
	return nil, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func activeRunDoc(r ActiveRunRecord) map[string]any {
	return map[string]any{
		"id":           r.SessionID,
		"type":         "active_run",
		"session_id":   r.SessionID,
		"email":        r.Email,
		"run_id":       r.RunID,
		"pod_name":     r.PodName,
		"provider":     r.Provider,
		"status":       r.Status,
		"stream_path":  r.StreamPath,
		"pid_path":     r.PIDPath,
		"started_at":   r.StartedAt,
		"updated_at":   r.UpdatedAt,
		"completed_at": r.CompletedAt,
	}
}

func activeRunFromDoc(data []byte) (ActiveRunRecord, error) {
	var doc struct {
		SessionID   string  `json:"session_id"`
		Email       string  `json:"email"`
		RunID       string  `json:"run_id"`
		PodName     string  `json:"pod_name"`
		Provider    string  `json:"provider"`
		Status      string  `json:"status"`
		StreamPath  string  `json:"stream_path"`
		PIDPath     string  `json:"pid_path"`
		StartedAt   string  `json:"started_at"`
		UpdatedAt   string  `json:"updated_at"`
		CompletedAt *string `json:"completed_at"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ActiveRunRecord{}, err
	}
	return ActiveRunRecord{
		SessionID: doc.SessionID, Email: doc.Email, RunID: doc.RunID,
		PodName: doc.PodName, Provider: doc.Provider, Status: doc.Status,
		StreamPath: doc.StreamPath, PIDPath: doc.PIDPath,
		StartedAt: doc.StartedAt, UpdatedAt: doc.UpdatedAt, CompletedAt: doc.CompletedAt,
	}, nil
}

func runEventFromDoc(data []byte) (RunEventRecord, error) {
	var doc struct {
		RunID     string         `json:"run_id"`
		SessionID string         `json:"session_id"`
		Email     string         `json:"email"`
		EventID   int64          `json:"event_id"`
		Type      string         `json:"type"`
		Payload   map[string]any `json:"payload"`
		CreatedAt string         `json:"created_at"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return RunEventRecord{}, err
	}
	if doc.Payload == nil {
		doc.Payload = map[string]any{}
	}
	return RunEventRecord{
		RunID: doc.RunID, SessionID: doc.SessionID, Email: doc.Email,
		EventID: doc.EventID, Type: doc.Type, Payload: doc.Payload, CreatedAt: doc.CreatedAt,
	}, nil
}
