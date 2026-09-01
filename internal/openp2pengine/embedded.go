package openp2p

import (
	"errors"
	"math/rand"
	"path/filepath"
	"sync"
	"time"

	openp2pconfig "github.com/Guailoudou/opl-core/internal/openp2p"
)

var (
	GNetwork    *P2PNetwork
	configPath  = "config.json"
	moduleMu    sync.Mutex
	moduleRun   bool
	moduleID    uint64
	moduleState func(string)
)

type Options struct {
	ConfigPath string
	DataDir    string
	OnState    func(string)
}

type AppStatus struct {
	Protocol  string `json:"protocol"`
	SrcPort   int    `json:"srcPort"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

func AppStatuses() []AppStatus {
	networkMu.Lock()
	defer networkMu.Unlock()
	if GNetwork == nil {
		return []AppStatus{}
	}
	statuses := []AppStatus{}
	GNetwork.apps.Range(func(_, value any) bool {
		app := value.(*p2pApp)
		if app.config.SrcPort != 0 {
			statuses = append(statuses, appStatus(app))
		}
		return true
	})
	return statuses
}

// AddApp updates the running engine without interrupting existing tunnels.
func AddApp(app openp2pconfig.App) error {
	moduleMu.Lock()
	defer moduleMu.Unlock()
	if !moduleRun {
		return errors.New("OpenP2P is not running")
	}
	config := AppConfig{
		AppName: app.AppName, Protocol: app.Protocol, UnderlayProtocol: app.UnderlayProtocol,
		PunchPriority: app.PunchPriority, Whitelist: app.Whitelist, SrcPort: app.SrcPort,
		PeerNode: app.PeerNode, DstPort: app.DstPort, DstHost: app.DstHost,
		PeerUser: app.PeerUser, RelayNode: app.RelayNode, ForceRelay: app.ForceRelay, Enabled: app.Enabled,
	}
	gConf.add(config, true)
	// Start internal data-plane apps immediately. Waiting for autorunApp (1s)
	// leaves the WireGuard socket without a tunnel during the first handshake.
	networkMu.Lock()
	network := GNetwork
	networkMu.Unlock()
	if network != nil && network.online.Load() && network.findApp(&config) == nil {
		if err := network.AddApp(config); err != nil {
			return err
		}
	}
	return nil
}

func appStatus(app *p2pApp) AppStatus {
	status := AppStatus{Protocol: app.config.Protocol, SrcPort: app.config.SrcPort}
	if value := app.statusError.Load(); value != nil {
		status.Error, _ = value.(string)
	}
	for i := 0; i < app.tunnelNum; i++ {
		if tunnel := app.Tunnel(i); tunnel != nil && tunnel.isRuning() {
			status.Connected = true
			status.Error = ""
			break
		}
	}
	return status
}

func Start(options Options) error {
	moduleMu.Lock()
	defer moduleMu.Unlock()
	if moduleRun {
		return nil
	}
	if options.ConfigPath == "" {
		return errors.New("OpenP2P config path is empty")
	}
	abs, err := filepath.Abs(options.ConfigPath)
	if err != nil {
		return err
	}
	configPath = abs
	gConf = Config{}
	gConf.LogLevel = int(LvINFO)
	gConf.MaxLogSize = 1024 * 1024
	gConf.Network.ShareBandwidth = 10
	gConf.Network.ServerHost = "api.openp2p.cn"
	gConf.Network.ServerPort = WsPort
	gConf.sdwan.TunnelNum = 2
	if err := gConf.load(); err != nil {
		return err
	}
	if gConf.Network.Token == 0 || gConf.Network.Node == "" {
		return errors.New("OpenP2P token or node is missing")
	}
	if gConf.Network.ServerHost == "" {
		gConf.Network.ServerHost = "api.openp2p.cn"
	}
	if gConf.Network.ServerPort == 0 {
		gConf.Network.ServerPort = WsPort
	}
	if gConf.Network.ShareBandwidth == 0 {
		gConf.Network.ShareBandwidth = 10
	}
	if gConf.MaxLogSize == 0 {
		gConf.MaxLogSize = 1024 * 1024
	}
	gConf.setNode(gConf.Network.Node)
	if gConf.Network.PublicIPPort == 0 {
		gConf.Network.PublicIPPort = int(gConf.nodeID()%8192 + 1025)
	}
	gConf.Network.natDetectPort1 = NATDetectPort1
	gConf.Network.natDetectPort2 = NATDetectPort2
	if options.DataDir == "" {
		options.DataDir = filepath.Dir(abs)
	}
	gLog = NewLogger(options.DataDir, ProductName, LogLevel(gConf.LogLevel), int64(gConf.MaxLogSize), LogFile)
	if gLog == nil {
		return errors.New("OpenP2P logger initialization failed")
	}
	setFirewall()
	if err := setRLimit(); err != nil {
		gLog.i("setRLimit error: %s", err)
	}
	rand.Seed(time.Now().UnixNano())
	moduleRun = true
	moduleID++
	id := moduleID
	moduleState = options.OnState
	stateLocked("starting")
	go runEmbedded(id)
	return nil
}

func runEmbedded(id uint64) {
	P2PNetworkInstance()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := ""
	for {
		moduleMu.Lock()
		active := moduleRun && moduleID == id
		moduleMu.Unlock()
		if !active {
			return
		}
		networkMu.Lock()
		network := GNetwork
		networkMu.Unlock()
		state := "starting"
		if network != nil && network.online.Load() {
			state = "online"
		} else if network != nil && time.Since(network.initTime) > ClientAPITimeout*3 {
			state = "faulted"
		}
		if state != last {
			stateFor(id, state)
			last = state
		}
		<-ticker.C
	}
}

func Stop() error {
	moduleMu.Lock()
	if !moduleRun {
		moduleMu.Unlock()
		return nil
	}
	moduleRun = false
	moduleID++
	moduleMu.Unlock()

	networkMu.Lock()
	if GNetwork != nil {
		GNetwork.shutdown()
		GNetwork = nil
	}
	if v4l != nil {
		v4l.stop()
		v4l = nil
	}
	networkMu.Unlock()
	if gLog != nil {
		gLog.close()
	}
	stateValue("stopped")
	return nil
}

func stateLocked(value string) {
	if moduleState != nil {
		moduleState(value)
	}
}

func stateValue(value string) {
	moduleMu.Lock()
	callback := moduleState
	moduleMu.Unlock()
	if callback != nil {
		callback(value)
	}
}

func stateFor(id uint64, value string) {
	moduleMu.Lock()
	if !moduleRun || moduleID != id {
		moduleMu.Unlock()
		return
	}
	callback := moduleState
	moduleMu.Unlock()
	if callback != nil {
		callback(value)
	}
}

type p2pSDWAN struct {
	sysRoute sync.Map
	tunErr   string
	tun      interface{ Stop() }
}

type sdwanNode struct {
	id   uint64
	name string
}

func handleSDWAN(uint16, []byte) {}
func cleanTempFiles()            {}
