package secret

import (
	"bytes"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSecureStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.bin")
	value := []byte("room-key-and-private-key")
	err := Save(path, value)
	if runtime.GOOS != "windows" {
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("expected unavailable store, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || !bytes.Equal(loaded, value) {
		t.Fatalf("secure store mismatch: %q, %v", loaded, err)
	}
}
