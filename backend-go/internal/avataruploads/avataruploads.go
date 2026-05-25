package avataruploads

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("avatar upload attempt not found")

type FieldSummary struct {
	Present      bool   `json:"present"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	HeaderMIME   string `json:"header_mime,omitempty"`
	DetectedMIME string `json:"detected_mime,omitempty"`
	MIME         string `json:"mime,omitempty"`
}

type Attempt struct {
	ID               string                  `json:"attempt_id"`
	Operation        string                  `json:"operation"`
	ActorEmail       string                  `json:"actor_email"`
	ActorRole        string                  `json:"actor_role"`
	Method           string                  `json:"method"`
	Route            string                  `json:"route"`
	ContentType      string                  `json:"content_type"`
	ContentTypeClass string                  `json:"content_type_class"`
	ContentLength    int64                   `json:"content_length"`
	Stage            string                  `json:"stage"`
	Result           string                  `json:"result"`
	Detail           string                  `json:"detail"`
	Kind             string                  `json:"kind,omitempty"`
	AvatarID         string                  `json:"avatar_id,omitempty"`
	Fields           map[string]FieldSummary `json:"fields,omitempty"`
	Diagnostics      map[string]string       `json:"diagnostics,omitempty"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
}

type Filter struct {
	ID    string
	Limit int
}

type Store interface {
	Upsert(ctx context.Context, attempt Attempt) error
	Get(ctx context.Context, id string) (Attempt, error)
	List(ctx context.Context, filter Filter) ([]Attempt, error)
}

type MemoryStore struct {
	mu       sync.Mutex
	attempts map[string]Attempt
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{attempts: map[string]Attempt{}}
}

func (s *MemoryStore) Upsert(_ context.Context, attempt Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if attempt.CreatedAt.IsZero() {
		attempt.CreatedAt = time.Now().UTC()
	}
	if attempt.UpdatedAt.IsZero() {
		attempt.UpdatedAt = attempt.CreatedAt
	}
	s.attempts[attempt.ID] = cloneAttempt(attempt)
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.attempts[id]
	if !ok {
		return Attempt{}, ErrNotFound
	}
	return cloneAttempt(attempt), nil
}

func (s *MemoryStore) List(_ context.Context, filter Filter) ([]Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	out := make([]Attempt, 0, min(limit, len(s.attempts)))
	if filter.ID != "" {
		attempt, ok := s.attempts[filter.ID]
		if !ok {
			return []Attempt{}, nil
		}
		return []Attempt{cloneAttempt(attempt)}, nil
	}
	for _, attempt := range s.attempts {
		out = append(out, cloneAttempt(attempt))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func cloneAttempt(in Attempt) Attempt {
	out := in
	if in.Fields != nil {
		out.Fields = make(map[string]FieldSummary, len(in.Fields))
		for key, value := range in.Fields {
			out.Fields[key] = value
		}
	}
	if in.Diagnostics != nil {
		out.Diagnostics = make(map[string]string, len(in.Diagnostics))
		for key, value := range in.Diagnostics {
			out.Diagnostics[key] = value
		}
	}
	return out
}
