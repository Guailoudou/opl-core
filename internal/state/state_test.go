package state

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicStateAndBackupRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "room.json")
	first := validState()
	if err := Save(path, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.JoinEnabled = false
	if err := Save(path, second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := Load(path)
	if err != nil || recovered.JoinEnabled != first.JoinEnabled {
		t.Fatalf("backup recovery failed: %#v, %v", recovered, err)
	}
}

func TestRejectsDuplicateLease(t *testing.T) {
	value := validState()
	value.Members = append(value.Members, value.Members[0])
	if err := Save(filepath.Join(t.TempDir(), "room.json"), value); err == nil {
		t.Fatal("duplicate lease accepted")
	}
}

func TestRejectsBlockedActiveMember(t *testing.T) {
	value := validState()
	value.BlockedMembers = []string{value.Members[0].PublicKey}
	if err := Save(filepath.Join(t.TempDir(), "room.json"), value); err == nil {
		t.Fatal("active member was also accepted as blocked")
	}
}

func validState() Room {
	key := make([]byte, 32)
	key[0] = 1
	return Room{
		SchemaVersion: SchemaVersion,
		RoomID:        "00112233445566778899aabbccddeeff",
		HostUID:       "0123456789abcdef",
		PairPort:      25673,
		WireGuardPort: 25674,
		RelayPort:     25675,
		JoinEnabled:   true,
		Members: []Member{{
			PublicKey:     base64.StdEncoding.EncodeToString(key),
			IP:            "10.0.23.2",
			Name:          "player",
			Enabled:       true,
			LeaseRevision: 1,
		}},
	}
}
