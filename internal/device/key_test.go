package device

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFixedAndTemporaryKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.secret")
	first, err := LoadOrCreate(path, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(path, true)
	if err != nil || second != first {
		t.Fatal("fixed key was not restored")
	}
	temporary, err := LoadOrCreate(path, false)
	if err != nil || temporary == first {
		t.Fatal("temporary key reused fixed identity")
	}

}

func TestTemporaryKeysAreEphemeral(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "device.secret")
	first, err := LoadOrCreate(path, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(path, false)
	if err != nil || second == first {
		t.Fatal("temporary identity survived restart")
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary identity was persisted: %v, %v", files, err)
	}
}
