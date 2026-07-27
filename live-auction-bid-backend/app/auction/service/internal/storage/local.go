package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type LocalConfig struct {
	RootDir       string
	Bucket        string
	PublicBaseURL string
}

type LocalStorage struct {
	rootDir       string
	bucket        string
	publicBaseURL string
}

func NewLocalStorage(cfg LocalConfig) (*LocalStorage, error) {
	cfg.RootDir = strings.TrimSpace(cfg.RootDir)
	if cfg.RootDir == "" {
		cfg.RootDir = "/tmp/live-auction-assets"
	}
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	if cfg.Bucket == "" {
		cfg.Bucket = "local"
	}
	if err := os.MkdirAll(cfg.RootDir, 0o755); err != nil {
		return nil, err
	}
	return &LocalStorage{
		rootDir:       cfg.RootDir,
		bucket:        cfg.Bucket,
		publicBaseURL: strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/"),
	}, nil
}

func (s *LocalStorage) PutObject(ctx context.Context, input PutObjectInput) (*StoredObject, error) {
	if input.ObjectKey == "" {
		return nil, errors.New("object key is required")
	}
	if input.Reader == nil {
		return nil, errors.New("object reader is required")
	}
	target, err := s.safePath(input.ObjectKey)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	file, err := os.Create(target)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = file.Close()
			_ = os.Remove(target)
		}
	}()
	if _, err := io.Copy(file, input.Reader); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close local object %q: %w", input.ObjectKey, err)
	}
	committed = true
	publicURL := input.ObjectKey
	if s.publicBaseURL != "" {
		publicURL = s.publicBaseURL + "/" + strings.TrimLeft(input.ObjectKey, "/")
	}
	return &StoredObject{
		Provider:  "local",
		Bucket:    s.bucket,
		ObjectKey: input.ObjectKey,
		PublicURL: publicURL,
	}, nil
}

func (s *LocalStorage) DeleteObject(ctx context.Context, objectKey string) error {
	if objectKey == "" {
		return errors.New("object key is required")
	}
	target, err := s.safePath(objectKey)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *LocalStorage) safePath(objectKey string) (string, error) {
	key := filepath.Clean(strings.TrimLeft(strings.TrimSpace(objectKey), "/\\"))
	if key == "." || key == "" || strings.HasPrefix(key, "..") || filepath.IsAbs(key) {
		return "", errors.New("invalid object key")
	}
	target := filepath.Join(s.rootDir, key)
	root, err := filepath.Abs(s.rootDir)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if absTarget != root && !strings.HasPrefix(absTarget, root+string(os.PathSeparator)) {
		return "", errors.New("object key escapes storage root")
	}
	return absTarget, nil
}
