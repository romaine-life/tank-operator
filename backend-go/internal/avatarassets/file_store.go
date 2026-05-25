package avatarassets

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type FileImageStore struct {
	baseDir string
}

func NewFileImageStore(baseDir string) (*FileImageStore, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, errors.New("avatar file store base dir is required")
	}
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	return &FileImageStore{baseDir: abs}, nil
}

func (s *FileImageStore) Put(_ context.Context, key string, img Image) error {
	target, err := s.pathForKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".avatar-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(img.Bytes); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (s *FileImageStore) Get(_ context.Context, key string) (Image, error) {
	target, err := s.pathForKey(key)
	if err != nil {
		return Image{}, err
	}
	body, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Image{}, ErrNotFound
		}
		return Image{}, err
	}
	return Image{MIME: http.DetectContentType(body), Bytes: body}, nil
}

func (s *FileImageStore) Delete(_ context.Context, key string) error {
	target, err := s.pathForKey(key)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *FileImageStore) pathForKey(key string) (string, error) {
	clean := path.Clean(strings.TrimSpace(key))
	if clean == "." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("invalid avatar image key %q", key)
	}
	target := filepath.Join(s.baseDir, filepath.FromSlash(clean))
	rel, err := filepath.Rel(s.baseDir, target)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("invalid avatar image key %q", key)
	}
	return target, nil
}
