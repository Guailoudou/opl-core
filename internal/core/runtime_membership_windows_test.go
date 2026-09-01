//go:build windows

package core

import (
	"crypto/sha256"
	"path/filepath"
	"testing"

	"github.com/Guailoudou/opl-core/internal/secret"
)

func TestFixedMembershipKeyRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "membership.secret")
	want := sha256.Sum256([]byte("fixed-membership"))
	if err := secret.Save(path, want[:]); err != nil {
		t.Fatal(err)
	}
	got, exists, err := loadMembershipKey(path, true)
	if err != nil || !exists || got != want {
		t.Fatalf("membership secret mismatch: exists=%v got=%x err=%v", exists, got, err)
	}
	if _, exists, err := loadMembershipKey(path, false); err != nil || exists {
		t.Fatalf("temporary device loaded a fixed membership secret: exists=%v err=%v", exists, err)
	}
}
