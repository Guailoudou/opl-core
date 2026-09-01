package openp2p

import (
	"os"
	"path/filepath"
	"testing"

	openp2pconfig "github.com/Guailoudou/opl-core/internal/openp2p"
)

func TestStartRejectsMissingConfig(t *testing.T) {
	if err := Start(Options{ConfigPath: t.TempDir() + "/missing.json"}); err == nil {
		t.Fatal("Start accepted a missing configuration")
	}
}

func TestStartRejectsMalformedConfigWithoutPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"Network":{"Token":"not-a-number"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Start(Options{ConfigPath: path}); err == nil {
		t.Fatal("Start accepted malformed configuration")
	}
}

func TestLoadCoreConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"Network":{"Token":11602319472897248650,"Node":"0123456789abcdef","ServerHost":"api.openp2p.cn","ServerPort":443,"ShareBandwidth":10},"Apps":[]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	configPath = path
	var config Config
	if err := config.load(); err != nil || config.Network.Token != 11602319472897248650 {
		t.Fatalf("load failed: token=%d err=%v", config.Network.Token, err)
	}
}

func TestAppStatusTracksTunnelConnection(t *testing.T) {
	app := &p2pApp{config: AppConfig{Protocol: "tcp", SrcPort: 25565}}
	app.Init(2)
	if appStatus(app).Connected {
		t.Fatal("app without a tunnel was reported connected")
	}
	app.statusError.Store(ErrPeerOffline.Error())
	if status := appStatus(app); status.Error != ErrPeerOffline.Error() {
		t.Fatalf("connection error was not reported: %#v", status)
	}
	tunnel := &P2PTunnel{running: true, done: make(chan struct{})}
	app.SetTunnel(tunnel, 0)
	if !appStatus(app).Connected {
		t.Fatal("running tunnel was not reported connected")
	}
	if status := appStatus(app); status.Error != "" {
		t.Fatalf("connected tunnel retained an error: %#v", status)
	}
	tunnel.setRun(false)
	if appStatus(app).Connected {
		t.Fatal("stopped tunnel remained connected")
	}
}

func TestAddAppUpdatesRunningConfiguration(t *testing.T) {
	configPath = filepath.Join(t.TempDir(), "config.json")
	gConf = Config{Network: NetworkConfig{Token: 1}}
	moduleRun = true
	defer func() {
		moduleRun = false
		configPath = "config.json"
		gConf = Config{}
	}()

	app := openp2pconfig.App{AppName: "_opl2_wg_host", Protocol: "udp", SrcPort: 20001, PeerNode: "host", DstPort: 20002, DstHost: "127.0.0.1", Enabled: 1}
	if err := AddApp(app); err != nil {
		t.Fatal(err)
	}
	if len(gConf.Apps) != 1 || gConf.Apps[0].AppName != app.AppName || gConf.Apps[0].DstPort != app.DstPort {
		t.Fatalf("running configuration did not receive app: %#v", gConf.Apps)
	}
}
