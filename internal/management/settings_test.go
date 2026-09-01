package management

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreDefaultsToLoopbackAndPersistsSelection(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "set.json"))
	if address, err := store.Load(); err != nil || address != Loopback {
		t.Fatalf("default address = %q, %v", address, err)
	}
	if err := store.Save(Loopback); err != nil {
		t.Fatal(err)
	}
	if address, err := store.Load(); err != nil || address != Loopback {
		t.Fatalf("saved address = %q, %v", address, err)
	}
}

func TestAccessListDefaultsToLocalAndPreventsLockout(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "set.json"))
	allowed, err := store.AllowedIPs(Loopback)
	if err != nil || !contains(allowed, Loopback) {
		t.Fatalf("default access list = %#v, %v", allowed, err)
	}
	allowed, err = store.SaveAllowedIPs([]string{"192.0.2.10"}, "192.168.1.20")
	if err != nil || !contains(allowed, Loopback) || !contains(allowed, "192.168.1.20") || !contains(allowed, "192.0.2.10") {
		t.Fatalf("protected access list = %#v, %v", allowed, err)
	}
	if _, err := store.SaveAllowedIPs([]string{"not-an-ip"}, Loopback); err == nil {
		t.Fatal("invalid client address accepted")
	}
}

func TestStoreAcceptsLegacyWPFSetFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "set.json")
	legacy := `{"Theme":"dark","minimize":false}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	if address, err := store.Load(); err != nil || address != Loopback {
		t.Fatalf("legacy address = %q, %v", address, err)
	}
	if err := store.Save(Loopback); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"Theme": "dark"`) || !strings.Contains(string(data), `"managementAddress"`) {
		t.Fatalf("legacy settings were not preserved: %s", data)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
