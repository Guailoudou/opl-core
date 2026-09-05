package secret

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSecureStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.bin")
	value := []byte("room-key-and-private-key")
	err := Save(path, value)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || !bytes.Equal(loaded, value) {
		t.Fatalf("secure store mismatch: %q, %v", loaded, err)
	}
}
