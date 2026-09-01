//go:build windows

package core

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVerifyFileHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "component.bin")
	data := []byte("release component")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if err := verifyFileHash(path, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	if err := verifyFileHash(path, wintunSHA256[runtime.GOARCH]); err == nil {
		t.Fatal("wrong component hash was accepted")
	}
}
