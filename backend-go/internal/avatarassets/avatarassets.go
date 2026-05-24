package avatarassets

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	KindAgent  = "agent"
	KindSystem = "system"
)

var ErrNotFound = errors.New("avatar asset not found")

type Crop struct {
	CenterX      float64 `json:"center_x"`
	CenterY      float64 `json:"center_y"`
	Size         float64 `json:"size"`
	SourceWidth  int     `json:"source_width,omitempty"`
	SourceHeight int     `json:"source_height,omitempty"`
}

type Metadata struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Crop      Crop      `json:"crop"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type NewAsset struct {
	ID           string
	Kind         string
	Name         string
	Crop         Crop
	AvatarMIME   string
	AvatarBytes  []byte
	BackingMIME  string
	BackingBytes []byte
	CreatedBy    string
}

type Image struct {
	MIME  string
	Bytes []byte
}

type Store interface {
	List(ctx context.Context) ([]Metadata, error)
	Create(ctx context.Context, asset NewAsset) (Metadata, error)
	GetImage(ctx context.Context, id, variant string) (Image, error)
	Delete(ctx context.Context, id string) error
}

func NormalizeKind(kind string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case KindAgent:
		return KindAgent, true
	case KindSystem:
		return KindSystem, true
	default:
		return "", false
	}
}

type memoryRecord struct {
	meta         Metadata
	avatarMIME   string
	avatarBytes  []byte
	backingMIME  string
	backingBytes []byte
}

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]memoryRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]memoryRecord{}}
}

func (s *MemoryStore) List(_ context.Context) ([]Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Metadata, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec.meta)
	}
	return out, nil
}

func (s *MemoryStore) Create(_ context.Context, asset NewAsset) (Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	meta := Metadata{
		ID:        asset.ID,
		Kind:      asset.Kind,
		Name:      asset.Name,
		Crop:      asset.Crop,
		CreatedBy: strings.ToLower(strings.TrimSpace(asset.CreatedBy)),
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.records[asset.ID] = memoryRecord{
		meta:         meta,
		avatarMIME:   asset.AvatarMIME,
		avatarBytes:  append([]byte(nil), asset.AvatarBytes...),
		backingMIME:  asset.BackingMIME,
		backingBytes: append([]byte(nil), asset.BackingBytes...),
	}
	return meta, nil
}

func (s *MemoryStore) GetImage(_ context.Context, id, variant string) (Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return Image{}, ErrNotFound
	}
	switch variant {
	case "avatar":
		return Image{MIME: rec.avatarMIME, Bytes: append([]byte(nil), rec.avatarBytes...)}, nil
	case "backing":
		return Image{MIME: rec.backingMIME, Bytes: append([]byte(nil), rec.backingBytes...)}, nil
	default:
		return Image{}, ErrNotFound
	}
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; !ok {
		return ErrNotFound
	}
	delete(s.records, id)
	return nil
}
