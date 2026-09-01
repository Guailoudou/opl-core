package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	ErrUnavailable = errors.New("secure storage is unavailable on this platform")
	ErrStore       = errors.New("secure storage failed")
)

func Save(path string, plaintext []byte) error {
	if len(plaintext) == 0 {
		return fmt.Errorf("%w: secret is empty", ErrStore)
	}
	protected, err := protect(plaintext)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(protected)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	return nil
}

func Load(path string) ([]byte, error) {
	protected, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	plaintext, err := unprotect(protected)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	return plaintext, nil
}
