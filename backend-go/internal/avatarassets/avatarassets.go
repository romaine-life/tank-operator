package avatarassets

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	KindAgent      = "agent"
	KindSystem     = "system"
	VariantAvatar  = "avatar"
	VariantBacking = "backing"
)

var (
	ErrNotFound       = errors.New("avatar asset not found")
	ErrInvalidVariant = errors.New("invalid avatar image variant")
)

type Crop struct {
	CenterX      float64 `json:"center_x"`
	CenterY      float64 `json:"center_y"`
	Size         float64 `json:"size"`
	SourceWidth  int     `json:"source_width,omitempty"`
	SourceHeight int     `json:"source_height,omitempty"`
}

type Metadata struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	Crop           Crop      `json:"crop"`
	AvatarMIME     string    `json:"-"`
	AvatarBlobKey  string    `json:"-"`
	BackingMIME    string    `json:"-"`
	BackingBlobKey string    `json:"-"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type NewAsset struct {
	ID             string
	Kind           string
	Name           string
	Crop           Crop
	AvatarMIME     string
	AvatarBlobKey  string
	BackingMIME    string
	BackingBlobKey string
	CreatedBy      string
}

type Image struct {
	MIME  string
	Bytes []byte
}

type Store interface {
	List(ctx context.Context) ([]Metadata, error)
	Get(ctx context.Context, id string) (Metadata, error)
	Create(ctx context.Context, asset NewAsset) (Metadata, error)
	Ensure(ctx context.Context, asset NewAsset) error
	Delete(ctx context.Context, id string) (Metadata, error)
}

type ImageStore interface {
	Put(ctx context.Context, key string, img Image) error
	Get(ctx context.Context, key string) (Image, error)
	Delete(ctx context.Context, key string) error
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

func (m Metadata) ImageRef(variant string) (key, mime string, err error) {
	switch variant {
	case VariantAvatar:
		return m.AvatarBlobKey, m.AvatarMIME, nil
	case VariantBacking:
		return m.BackingBlobKey, m.BackingMIME, nil
	default:
		return "", "", ErrInvalidVariant
	}
}

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Metadata
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]Metadata{}}
}

func (s *MemoryStore) List(_ context.Context) ([]Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Metadata, 0, len(s.records))
	for _, meta := range s.records {
		out = append(out, meta)
	}
	return out, nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.records[id]
	if !ok {
		return Metadata{}, ErrNotFound
	}
	return meta, nil
}

func (s *MemoryStore) Create(_ context.Context, asset NewAsset) (Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createLocked(asset), nil
}

func (s *MemoryStore) Ensure(_ context.Context, asset NewAsset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[asset.ID]; ok {
		return nil
	}
	s.createLocked(asset)
	return nil
}

func (s *MemoryStore) createLocked(asset NewAsset) Metadata {
	now := time.Now().UTC()
	meta := Metadata{
		ID:             asset.ID,
		Kind:           asset.Kind,
		Name:           asset.Name,
		Crop:           asset.Crop,
		AvatarMIME:     asset.AvatarMIME,
		AvatarBlobKey:  asset.AvatarBlobKey,
		BackingMIME:    asset.BackingMIME,
		BackingBlobKey: asset.BackingBlobKey,
		CreatedBy:      strings.ToLower(strings.TrimSpace(asset.CreatedBy)),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.records[asset.ID] = meta
	return meta
}

func (s *MemoryStore) Delete(_ context.Context, id string) (Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.records[id]
	if !ok {
		return Metadata{}, ErrNotFound
	}
	delete(s.records, id)
	return meta, nil
}

type MemoryImageStore struct {
	mu     sync.Mutex
	images map[string]Image
}

func NewMemoryImageStore() *MemoryImageStore {
	return &MemoryImageStore{images: map[string]Image{}}
}

func (s *MemoryImageStore) Put(_ context.Context, key string, img Image) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.images[key] = Image{MIME: img.MIME, Bytes: append([]byte(nil), img.Bytes...)}
	return nil
}

func (s *MemoryImageStore) Get(_ context.Context, key string) (Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	img, ok := s.images[key]
	if !ok {
		return Image{}, ErrNotFound
	}
	return Image{MIME: img.MIME, Bytes: append([]byte(nil), img.Bytes...)}, nil
}

func (s *MemoryImageStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.images[key]; !ok {
		return ErrNotFound
	}
	delete(s.images, key)
	return nil
}
