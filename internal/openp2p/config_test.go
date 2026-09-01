package openp2p

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStorePreservesUserAppsAndHidesInternal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := testConfig()
	config.Apps = []App{{AppName: "game", Protocol: "tcp", SrcPort: 7777, DstPort: 7777, DstHost: "localhost", Enabled: 1}}
	if err := save(path, config); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	pairing := PairingApp("fedcba9876543210", 25673, 30001)
	wireGuard := WireGuardApp("fedcba9876543210", 25674, 30002)
	if err := store.SetInternal([]App{pairing, wireGuard}); err != nil {
		t.Fatal(err)
	}
	user, err := store.UserApps()
	if err != nil || len(user) != 1 || user[0].AppName != "game" {
		t.Fatalf("user app changed: %#v, %v", user, err)
	}
	if err := store.ReplaceUserApps([]App{{AppName: "new", Protocol: "udp", SrcPort: 9000, DstPort: 9000, DstHost: "127.0.0.1", Enabled: 1}}); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.Load()
	if len(loaded.Apps) != 3 || loaded.Apps[0].AppName != "new" || loaded.Apps[1].AppName != pairing.AppName || loaded.Apps[2].AppName != wireGuard.AppName {
		t.Fatalf("replacement lost internal app: %#v", loaded.Apps)
	}
	public, err := store.PublicConfig()
	if err != nil || len(public.Apps) != 1 || public.Apps[0].AppName != "new" {
		t.Fatalf("internal app escaped public config: %#v, %v", public.Apps, err)
	}
	if err := store.ClearInternal(); err != nil {
		t.Fatal(err)
	}
	loaded, _ = store.Load()
	if len(loaded.Apps) != 1 || loaded.Apps[0].AppName != "new" {
		t.Fatalf("internal cleanup failed: %#v", loaded.Apps)
	}
}

func TestTokenMarshalsWithoutJavaScriptPrecisionLoss(t *testing.T) {
	data, err := testConfig().Network.Token.MarshalJSON()
	if err != nil || !bytes.Equal(data, []byte(`"11602319472897248650"`)) {
		t.Fatalf("token JSON = %s, %v", data, err)
	}
}

func TestStoreWritesEngineCompatibleNumericToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := save(path, testConfig()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"Token": "`)) || !bytes.Contains(data, []byte(`"Token": 11602319472897248650`)) {
		t.Fatalf("disk token is not engine compatible: %s", data)
	}
}

func TestStoreRejectsCorruptionAndPortConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).Load(); err == nil {
		t.Fatal("corrupt config accepted")
	}
	config := testConfig()
	config.Apps = []App{
		{AppName: "a", Protocol: "udp", SrcPort: 1, DstPort: 1, DstHost: "localhost", Enabled: 1},
		{AppName: "b", Protocol: "udp", SrcPort: 1, DstPort: 2, DstHost: "localhost", Enabled: 1},
	}
	if err := save(path, config); err == nil {
		t.Fatal("conflicting local ports accepted")
	}
}

func TestReplaceConfigPreservesInternalApps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewStore(path)
	initial := testConfig()
	initial.Apps = []App{PairingApp("fedcba9876543210", 25673, 30000)}
	if err := save(path, initial); err != nil {
		t.Fatal(err)
	}
	replacement := testConfig()
	replacement.Network.ShareBandwidth = 20
	replacement.Apps = []App{{AppName: "game", Protocol: "udp", SrcPort: 7777, PeerNode: "fedcba9876543210", DstPort: 7777, DstHost: "localhost", Enabled: 1}}
	if err := store.Replace(replacement); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Network.ShareBandwidth != 20 || len(loaded.Apps) != 2 || loaded.Apps[1].AppName != "_opl2_pair_fedcba9876543210" {
		t.Fatalf("replace mismatch: %#v, %v", loaded, err)
	}
	replacement.Apps = append(replacement.Apps, PairingApp("fedcba9876543210", 25673, 30001))
	if err := store.Replace(replacement); !errors.Is(err, ErrConfig) {
		t.Fatalf("UI supplied internal app accepted: %v", err)
	}
}

func TestReplaceConfigCreatesMissingFile(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "bin", "config.json"))
	config := testConfig()
	if err := store.Replace(config); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Network != config.Network || loaded.Apps == nil {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func testConfig() Config {
	return Config{Network: Network{
		Token: Uint64(11602319472897248650), Node: "0123456789abcdef", User: "gldoffice",
		ShareBandwidth: 10, ServerHost: "api.openp2p.cn", ServerPort: 27183,
	}, Apps: []App{}, LogLevel: 1}
}
