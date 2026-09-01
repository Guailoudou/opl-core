package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Guailoudou/opl-core/internal/device"
	"github.com/Guailoudou/opl-core/internal/discovery"
	"github.com/Guailoudou/opl-core/internal/invite"
	"github.com/Guailoudou/opl-core/internal/lease"
	"github.com/Guailoudou/opl-core/internal/openp2p"
	openp2pengine "github.com/Guailoudou/opl-core/internal/openp2pengine"
	"github.com/Guailoudou/opl-core/internal/pairing"
	"github.com/Guailoudou/opl-core/internal/room"
	"github.com/Guailoudou/opl-core/internal/secret"
	"github.com/Guailoudou/opl-core/internal/wg"
)

type Options struct {
	DataDir       string
	HostUID       string
	OpenP2PConfig string
}

var (
	ErrOpenP2PNotReady = errors.New("OpenP2P is not ready")
	ErrWireGuardFailed = errors.New("WireGuard failed")
	ErrWireGuardUpdate = errors.New("WireGuard peer update failed")
	ErrNetworkActive   = errors.New("network is active")
)

type Runtime struct {
	mu              sync.Mutex
	rooms           *room.Service
	manager         *wg.Manager
	relay           *discovery.HostRelay
	store           *openp2p.Store
	openP2PConfig   string
	dataDir         string
	client          *wg.Manager
	clientControl   *pairing.Session
	clientCancel    context.CancelFunc
	joinCancel      context.CancelFunc
	joinError       string
	clientDevices   []pairing.Device
	clientIP        netip.Addr
	clientSession   bool
	statsAt         time.Time
	ordinaryRunning bool
	processState    atomic.Value
	lifecycleState  atomic.Value
	networkMode     atomic.Value
}

type JoinOptions struct {
	Invite         string
	Name           string
	FixedDeviceKey bool
}

type JoinResult struct {
	HostUID       string `json:"hostUid"`
	AssignedIP    string `json:"assignedIP"`
	PrefixLength  byte   `json:"prefixLength"`
	WireGuardPort uint16 `json:"wireGuardPort"`
	RelayPort     uint16 `json:"discoveryRelayPort"`
}

type State struct {
	LifecycleState string                    `json:"state"`
	Room           room.Snapshot             `json:"room"`
	Joined         bool                      `json:"joined"`
	JoinState      string                    `json:"joinState"`
	JoinError      string                    `json:"joinError,omitempty"`
	Tunnels        []openp2p.App             `json:"tunnels"`
	OpenP2PRunning bool                      `json:"openP2PRunning"`
	OpenP2PState   string                    `json:"openP2PState"`
	TunnelStates   []openp2pengine.AppStatus `json:"tunnelStates"`
	Devices        []pairing.Device          `json:"devices"`
	VirtualIP      string                    `json:"virtualIP,omitempty"`
	Discovery      discovery.RelayStats      `json:"discovery"`
}

func New(options Options) (*Runtime, error) {
	if options.DataDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		options.DataDir = filepath.Join(base, "OPL", "core")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	directory := filepath.Dir(executable)
	if options.OpenP2PConfig == "" {
		options.OpenP2PConfig = filepath.Join(options.DataDir, "config.json")
	}
	store := openp2p.NewStore(options.OpenP2PConfig)
	if err := verifyReleaseComponents(directory); err != nil {
		return nil, err
	}
	if _, err := store.Load(); errors.Is(err, os.ErrNotExist) {
		uid := make([]byte, 8)
		if _, err := rand.Read(uid); err != nil {
			return nil, err
		}
		if err := store.Replace(defaultConfig(hex.EncodeToString(uid))); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("load OpenP2P config: %w", err)
	}
	if err := store.ClearInternal(); err != nil {
		return nil, fmt.Errorf("clear stale OpenP2P session: %w", err)
	}
	if options.HostUID == "" {
		if config, err := store.Load(); err == nil {
			options.HostUID = config.Network.Node
		}
	}
	runtime := &Runtime{store: store, dataDir: options.DataDir, openP2PConfig: options.OpenP2PConfig}
	runtime.processState.Store("stopped")
	runtime.lifecycleState.Store("idle")
	runtime.networkMode.Store("idle")
	rooms, err := room.New(room.Options{
		DataDir:      options.DataDir,
		HostUID:      options.HostUID,
		StartNetwork: runtime.startNetwork,
		StopNetwork:  runtime.stopNetwork,
		ApplyPeer:    runtime.applyPeer,
		RemovePeer:   runtime.removePeer,
	})
	if err != nil {
		return nil, err
	}
	runtime.rooms = rooms
	return runtime, nil
}

func defaultConfig(uid string) openp2p.Config {
	return openp2p.Config{Network: openp2p.Network{
		Token: 11602319472897248650, Node: uid, User: "gldoffice", ShareBandwidth: 10,
		ServerHost: "api.openp2p.cn", ServerPort: 27183,
	}, Apps: []openp2p.App{}, LogLevel: 1}
}

func (r *Runtime) Rooms() *room.Service { return r.rooms }

func (r *Runtime) Logs() (string, error) {
	var output strings.Builder
	for _, item := range []struct{ name, path string }{
		{"Core", filepath.Join(r.dataDir, "core.log")},
		{"OpenP2P", filepath.Join(r.dataDir, "log", "openp2p.log")},
	} {
		data, err := tailFile(item.path, 256*1024)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&output, "===== %s =====\n%s\n", item.name, data)
	}
	return output.String(), nil
}

func tailFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, 0); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func (r *Runtime) State() (State, error) {
	r.refreshHostStats()
	r.mu.Lock()
	joined := r.clientControl != nil
	joinError := r.joinError
	joinState := "idle"
	if r.clientSession {
		if joined {
			joinState = "connected"
		} else if joinError != "" {
			joinState = "retrying"
		} else {
			joinState = "connecting"
		}
	} else if joinError != "" {
		joinState = "failed"
	}
	ordinaryRunning := r.ordinaryRunning
	relay := r.relay
	devices := make([]pairing.Device, len(r.clientDevices))
	copy(devices, r.clientDevices)
	virtualIP := ""
	if r.clientIP.IsValid() {
		virtualIP = r.clientIP.String()
	}
	r.mu.Unlock()
	var relayStats discovery.RelayStats
	if relay != nil {
		relayStats = relay.Stats()
	}
	tunnels, err := r.store.UserApps()
	if errors.Is(err, os.ErrNotExist) {
		tunnels, err = []openp2p.App{}, nil
	}
	processState, _ := r.processState.Load().(string)
	lifecycle, _ := r.lifecycleState.Load().(string)
	return State{LifecycleState: lifecycle, Room: r.rooms.Snapshot(), Joined: joined, JoinState: joinState, JoinError: joinError, Tunnels: tunnels, OpenP2PRunning: ordinaryRunning, OpenP2PState: processState, TunnelStates: openp2pengine.AppStatuses(), Devices: devices, VirtualIP: virtualIP, Discovery: relayStats}, err
}

func (r *Runtime) GetConfig() (openp2p.Config, error) { return r.store.PublicConfig() }

func (r *Runtime) ReplaceConfig(config openp2p.Config) error {
	roomRunning := r.rooms.Snapshot().Running
	r.mu.Lock()
	active := roomRunning || r.clientSession
	running := r.ordinaryRunning
	r.mu.Unlock()
	if active {
		current, err := r.store.Load()
		if err != nil {
			return err
		}
		if current.Network != config.Network {
			return ErrNetworkActive
		}
	}
	if err := r.store.Replace(config); err != nil {
		return err
	}
	if running || active {
		return r.restartOpenP2P()
	}
	return nil
}

func (r *Runtime) StartOpenP2P() error {
	roomRunning := r.rooms.Snapshot().Running
	r.mu.Lock()
	if roomRunning || r.clientSession || r.ordinaryRunning {
		r.mu.Unlock()
		return ErrNetworkActive
	}
	r.ordinaryRunning = true
	r.mu.Unlock()
	r.networkMode.Store("ordinary")
	r.setLifecycle("starting_openp2p")
	if err := r.startOpenP2PEngine(); err != nil {
		r.mu.Lock()
		r.ordinaryRunning = false
		r.mu.Unlock()
		r.networkMode.Store("idle")
		r.setLifecycle("faulted")
		return fmt.Errorf("%w: %v", ErrOpenP2PNotReady, err)
	}
	return nil
}

func (r *Runtime) StopOpenP2P() error {
	roomRunning := r.rooms.Snapshot().Running
	r.mu.Lock()
	if roomRunning || r.clientSession {
		r.mu.Unlock()
		return ErrNetworkActive
	}
	if !r.ordinaryRunning {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	r.setLifecycle("stopping")
	err := openp2pengine.Stop()
	if err != nil {
		r.setLifecycle("faulted")
	} else {
		r.mu.Lock()
		r.ordinaryRunning = false
		r.mu.Unlock()
		r.networkMode.Store("idle")
		r.setLifecycle("idle")
	}
	return err
}

func (r *Runtime) ListTunnels() ([]openp2p.App, error) { return r.store.UserApps() }

func (r *Runtime) ReplaceTunnels(apps []openp2p.App) error {
	if err := r.store.ReplaceUserApps(apps); err != nil {
		return err
	}
	if err := openp2pengine.Stop(); err != nil {
		return err
	}
	r.mu.Lock()
	running := r.ordinaryRunning || r.clientSession
	r.mu.Unlock()
	if r.rooms.Snapshot().Running || running {
		return r.startOpenP2PEngine()
	}
	return nil
}

func (r *Runtime) Close() error {
	r.setLifecycle("stopping")
	leaveErr := r.Leave()
	roomErr := r.rooms.Stop()
	configErr := r.store.ClearInternal()
	if errors.Is(configErr, os.ErrNotExist) {
		configErr = nil
	}
	processErr := openp2pengine.Stop()
	err := errors.Join(leaveErr, roomErr, configErr, processErr)
	r.networkMode.Store("idle")
	if err != nil {
		r.setLifecycle("faulted")
	} else {
		r.setLifecycle("idle")
	}
	return err
}

func (r *Runtime) Join(ctx context.Context, options JoinOptions) (JoinResult, error) {
	if r.rooms.Snapshot().Running {
		return JoinResult{}, errors.New("host room is running")
	}
	r.mu.Lock()
	if r.clientSession || r.ordinaryRunning {
		r.mu.Unlock()
		return JoinResult{}, ErrNetworkActive
	}
	joinContext, cancelJoin := context.WithCancel(ctx)
	r.clientSession, r.joinCancel, r.joinError = true, cancelJoin, ""
	r.mu.Unlock()
	defer cancelJoin()
	r.networkMode.Store("client")
	r.setLifecycle("joining")
	succeeded := false
	defer func() {
		if !succeeded {
			_ = r.Leave()
		}
	}()
	invitation, err := invite.Decode(options.Invite)
	if err != nil {
		return JoinResult{}, err
	}
	identity, err := device.LoadOrCreate(filepath.Join(r.dataDir, "client-"+invitation.HostUID+".secret"), options.FixedDeviceKey)
	if err != nil {
		return JoinResult{}, err
	}
	membershipPath := filepath.Join(r.dataDir, "client-"+invitation.HostUID+".membership.secret")
	memberPSK, hasMembership, err := loadMembershipKey(membershipPath, options.FixedDeviceKey)
	if err != nil {
		return JoinResult{}, err
	}
	pairPort, err := freePort("tcp4")
	if err != nil {
		return JoinResult{}, err
	}
	wireGuardPort, err := freePort("udp4")
	if err != nil {
		return JoinResult{}, err
	}
	pairingApp := openp2p.PairingApp(invitation.HostUID, invitation.PairPort, pairPort)
	if err := r.store.SetInternal([]openp2p.App{pairingApp}); err != nil {
		return JoinResult{}, fmt.Errorf("%w: %v", ErrOpenP2PNotReady, err)
	}
	if err := r.restartOpenP2P(); err != nil {
		_ = r.store.ClearInternal()
		return JoinResult{}, fmt.Errorf("%w: %v", ErrOpenP2PNotReady, err)
	}
	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(pairPort))
	fmt.Fprintln(os.Stderr, "join: waiting for pairing tunnel")
	var paired pairing.PairResult
	var control *pairing.Session
	configured := false
	usingMembership := hasMembership
	configure := func(result pairing.PairResult) error {
		fmt.Fprintln(os.Stderr, "join: pairing accepted, creating virtual adapter")
		wireGuardApp := openp2p.WireGuardApp(invitation.HostUID, result.WireGuardPort, wireGuardPort)
		if configErr := r.store.SetInternal([]openp2p.App{pairingApp, wireGuardApp}); configErr != nil {
			return configErr
		}
		if configErr := openp2pengine.AddApp(wireGuardApp); configErr != nil {
			return fmt.Errorf("%w: %v", ErrOpenP2PNotReady, configErr)
		}
		manager, startErr := wg.Start(wg.StartOptions{
			PrivateKey: identity.Private, LocalIP: result.AssignedIP, RelayPort: result.DiscoveryRelayPort,
		})
		if startErr != nil {
			return fmt.Errorf("%w: %v", ErrWireGuardFailed, startErr)
		}
		if !usingMembership {
			memberPSK = pairing.DerivePSK(pairing.RoomKey(invitation.RoomKey), result.HostPublicKey, identity.Public)
		}
		peerErr := manager.SetPeer(wg.Peer{
			PublicKey: result.HostPublicKey, PSK: memberPSK, IP: netip.MustParseAddr("10.0.23.1"),
			Endpoint: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), wireGuardPort),
		}, true)
		if peerErr != nil {
			_ = manager.Close()
			return fmt.Errorf("%w: %v", ErrWireGuardUpdate, peerErr)
		}
		r.mu.Lock()
		if !r.clientSession {
			r.mu.Unlock()
			_ = manager.Close()
			return context.Canceled
		}
		r.client = manager
		r.clientIP = result.AssignedIP
		r.mu.Unlock()
		fmt.Fprintln(os.Stderr, "join: virtual adapter ready")
		configured = true
		return nil
	}
	for {
		if usingMembership {
			paired, control, err = pairing.ReconnectWithConfiguredSession(joinContext, address, memberPSK, identity.Public, options.Name, configure)
			if errors.Is(err, pairing.ErrAuthFailed) {
				usingMembership = false
				continue
			}
		} else {
			paired, control, err = pairing.PairWithSession(joinContext, address, pairing.RoomKey(invitation.RoomKey), identity.Public, options.Name, configure)
		}
		if configured || terminalPairError(err) || joinContext.Err() != nil {
			break
		}
		r.mu.Lock()
		r.joinError = err.Error()
		r.mu.Unlock()
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-timer.C:
		case <-joinContext.Done():
			timer.Stop()
			err = joinContext.Err()
		}
		if err == joinContext.Err() {
			break
		}
	}
	if err != nil {
		r.mu.Lock()
		r.joinError = err.Error()
		r.mu.Unlock()
		return JoinResult{}, err
	}
	if options.FixedDeviceKey && !usingMembership {
		if err := secret.Save(membershipPath, memberPSK[:]); err != nil {
			return JoinResult{}, err
		}
	}
	config, err := r.store.Load()
	if err != nil {
		return JoinResult{}, err
	}
	fmt.Fprintln(os.Stderr, "join: control session ready")
	controlContext, cancelControl := context.WithCancel(context.Background())
	r.mu.Lock()
	if !r.clientSession {
		r.mu.Unlock()
		cancelControl()
		_ = control.Close()
		return JoinResult{}, context.Canceled
	}
	r.clientControl, r.clientCancel = control, cancelControl
	r.joinCancel, r.joinError = nil, ""
	r.mu.Unlock()
	go r.syncDevices(controlContext, address, memberPSK, identity.Public, options.Name, config.Network.Node, control)
	succeeded = true
	r.setLifecycle("online")
	return JoinResult{
		HostUID: invitation.HostUID, AssignedIP: paired.AssignedIP.String(), PrefixLength: paired.PrefixLength,
		WireGuardPort: paired.WireGuardPort, RelayPort: paired.DiscoveryRelayPort,
	}, nil
}

func (r *Runtime) Leave() error {
	r.mu.Lock()
	client := r.client
	control, cancelControl := r.clientControl, r.clientCancel
	cancelJoin := r.joinCancel
	r.client = nil
	r.clientControl, r.clientCancel, r.joinCancel, r.clientDevices, r.clientIP = nil, nil, nil, nil, netip.Addr{}
	active := r.clientSession
	r.clientSession = false
	r.mu.Unlock()
	if !active {
		return nil
	}
	r.setLifecycle("stopping")
	var deviceErr error
	if cancelJoin != nil {
		cancelJoin()
	}
	if cancelControl != nil {
		cancelControl()
	}
	if control != nil {
		_ = control.Close()
	}
	if client != nil {
		deviceErr = client.Close()
	}
	configErr := r.store.ClearInternal()
	if errors.Is(configErr, os.ErrNotExist) {
		configErr = nil
	}
	_, listErr := r.store.UserApps()
	if errors.Is(listErr, os.ErrNotExist) {
		listErr = nil
	}
	var processErr error
	r.mu.Lock()
	ordinaryRunning := r.ordinaryRunning
	r.mu.Unlock()
	if listErr == nil && ordinaryRunning {
		processErr = r.restartOpenP2P()
	} else {
		processErr = openp2pengine.Stop()
	}
	err := errors.Join(deviceErr, configErr, listErr, processErr)
	r.networkMode.Store("idle")
	if err != nil {
		r.setLifecycle("faulted")
	} else {
		r.setLifecycle("idle")
	}
	return err
}

func (r *Runtime) syncDevices(ctx context.Context, address string, memberPSK [32]byte, publicKey [32]byte, name, uid string, session *pairing.Session) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		var rx, tx uint64
		r.mu.Lock()
		client := r.client
		r.mu.Unlock()
		if client != nil {
			if stats, err := client.PeerStats(); err == nil {
				for _, stat := range stats {
					rx += stat.RxBytes
					tx += stat.TxBytes
				}
			}
		}
		devices, err := session.Sync(ctx, pairing.StatsReport{UID: uid, RxBytes: rx, TxBytes: tx})
		if err != nil {
			r.mu.Lock()
			if r.clientControl == session {
				r.clientControl = nil
			}
			r.joinError = err.Error()
			r.mu.Unlock()
			_ = session.Close()
			if ctx.Err() != nil {
				return
			}
			r.setLifecycle("joining")
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			_, next, pairErr := pairing.ReconnectWithSession(ctx, address, memberPSK, publicKey, name)
			if pairErr != nil {
				r.mu.Lock()
				r.joinError = pairErr.Error()
				r.mu.Unlock()
				if terminalPairError(pairErr) {
					_ = r.Leave()
					return
				}
				continue
			}
			session = next
			r.mu.Lock()
			if !r.clientSession {
				r.mu.Unlock()
				_ = session.Close()
				return
			}
			r.clientControl = session
			r.joinError = ""
			r.mu.Unlock()
			r.setLifecycle("online")
			continue
		}
		r.mu.Lock()
		if !r.clientSession {
			r.mu.Unlock()
			return
		}
		r.clientDevices = devices
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) restartOpenP2P() error {
	if err := openp2pengine.Stop(); err != nil {
		return err
	}
	return r.startOpenP2PEngine()
}

func freePort(network string) (uint16, error) {
	if network == "tcp4" {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := uint16(listener.Addr().(*net.TCPAddr).Port)
		listener.Close()
		return port, nil
	}
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return 0, err
	}
	port := uint16(connection.LocalAddr().(*net.UDPAddr).Port)
	connection.Close()
	return port, nil
}

func terminalPairError(err error) bool {
	return err == nil || errors.Is(err, pairing.ErrAuthFailed) || errors.Is(err, pairing.ErrJoinPaused) || errors.Is(err, pairing.ErrRoomFull) || errors.Is(err, pairing.ErrMemberDisabled)
}

func loadMembershipKey(path string, fixed bool) ([32]byte, bool, error) {
	if !fixed {
		return [32]byte{}, false, nil
	}
	data, err := secret.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return [32]byte{}, false, nil
	}
	if err != nil {
		return [32]byte{}, false, err
	}
	if len(data) != 32 {
		return [32]byte{}, false, fmt.Errorf("%w: invalid membership secret", secret.ErrStore)
	}
	var value [32]byte
	copy(value[:], data)
	return value, true, nil
}

func (r *Runtime) startNetwork(config room.NetworkConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.manager != nil {
		return errors.New("WireGuard is already running")
	}
	if r.clientSession {
		return errors.New("client room is already running")
	}
	if r.ordinaryRunning {
		return ErrNetworkActive
	}
	r.networkMode.Store("host")
	r.setLifecycle("starting_openp2p")
	if err := r.store.ClearInternal(); err != nil {
		r.networkMode.Store("idle")
		r.setLifecycle("faulted")
		return fmt.Errorf("%w: %v", ErrOpenP2PNotReady, err)
	}
	if err := r.startOpenP2PEngine(); err != nil {
		r.networkMode.Store("idle")
		r.setLifecycle("faulted")
		return fmt.Errorf("%w: %v", ErrOpenP2PNotReady, err)
	}
	r.setLifecycle("starting_wireguard")
	frames := make(chan discovery.Frame, 256)
	manager, err := wg.Start(wg.StartOptions{
		PrivateKey: config.PrivateKey,
		LocalIP:    config.LocalIP,
		ListenPort: config.ListenPort,
		RelayPort:  config.RelayPort,
		Host:       true,
		OnDiscovery: func(frame discovery.Frame) {
			select {
			case frames <- frame:
			default:
			}
		},
		Logf: func(format string, arguments ...any) {
			fmt.Fprintf(os.Stderr, "wireguard: "+format+"\n", arguments...)
		},
	})
	if err != nil {
		_ = openp2pengine.Stop()
		r.networkMode.Store("idle")
		r.setLifecycle("faulted")
		return fmt.Errorf("%w: %v", ErrWireGuardFailed, err)
	}
	relay, err := discovery.StartHostRelay(config.RelayPort, frames, manager.InjectDiscovery, r.memberIPs)
	if err != nil {
		_ = manager.Close()
		_ = openp2pengine.Stop()
		r.networkMode.Store("idle")
		r.setLifecycle("faulted")
		return fmt.Errorf("%w: %v", ErrWireGuardFailed, err)
	}
	r.manager, r.relay = manager, relay
	r.setLifecycle("listening")
	return nil
}

func (r *Runtime) stopNetwork() error {
	r.setLifecycle("stopping")
	r.mu.Lock()
	manager, relay := r.manager, r.relay
	r.manager, r.relay = nil, nil
	r.mu.Unlock()
	var relayErr, managerErr error
	if relay != nil {
		stats := relay.Stats()
		fmt.Fprintf(os.Stderr, "discovery relay: accepted=%d forwarded=%d dropped=%d\n", stats.Accepted, stats.Forwarded, stats.Dropped)
		relayErr = relay.Close()
	}
	if manager != nil {
		managerErr = manager.Close()
	}
	_, configErr := r.store.UserApps()
	var processErr error
	r.mu.Lock()
	ordinaryRunning := r.ordinaryRunning
	r.mu.Unlock()
	if configErr != nil || !ordinaryRunning {
		processErr = openp2pengine.Stop()
	}
	err := errors.Join(relayErr, managerErr, configErr, processErr)
	r.networkMode.Store("idle")
	if err != nil {
		r.setLifecycle("faulted")
	} else {
		r.setLifecycle("idle")
	}
	return err
}

func (r *Runtime) onProcessState(value string) {
	r.processState.Store(value)
	current, _ := r.lifecycleState.Load().(string)
	if current == "stopping" || current == "idle" {
		return
	}
	mode, _ := r.networkMode.Load().(string)
	switch value {
	case "online":
		switch mode {
		case "host":
			if current != "starting_openp2p" && current != "starting_wireguard" {
				r.setLifecycle("listening")
			}
		case "client":
			if current != "joining" {
				r.setLifecycle("online")
			}
		case "ordinary":
			r.setLifecycle("online")
		}
	case "faulted":
		r.setLifecycle("faulted")
	case "starting":
		if current != "joining" && current != "starting_wireguard" {
			r.setLifecycle("starting_openp2p")
		}
	}
}

func (r *Runtime) setLifecycle(value string) { r.lifecycleState.Store(value) }

func (r *Runtime) startOpenP2PEngine() error {
	return openp2pengine.Start(openp2pengine.Options{
		ConfigPath: r.openP2PConfig,
		DataDir:    filepath.Dir(r.openP2PConfig),
		OnState:    r.onProcessState,
	})
}

func (r *Runtime) applyPeer(item lease.Lease, psk [32]byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.manager == nil {
		return errors.New("WireGuard is not running")
	}
	if err := r.manager.SetPeer(wg.Peer{PublicKey: item.PublicKey, PSK: psk, IP: item.IP}, false); err != nil {
		return fmt.Errorf("%w: %v", ErrWireGuardUpdate, err)
	}
	return nil
}

func (r *Runtime) removePeer(publicKey [32]byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.manager == nil {
		return errors.New("WireGuard is not running")
	}
	if err := r.manager.RemovePeer(publicKey); err != nil {
		return fmt.Errorf("%w: %v", ErrWireGuardUpdate, err)
	}
	return nil
}

func (r *Runtime) memberIPs() []netip.Addr {
	r.refreshHostStats()
	snapshot := r.rooms.Snapshot()
	result := make([]netip.Addr, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		if !member.Enabled || (member.State != "configured" && member.State != "online") {
			continue
		}
		if ip, err := netip.ParseAddr(member.IP); err == nil {
			result = append(result, ip)
		}
	}
	return result
}

func (r *Runtime) refreshHostStats() {
	r.mu.Lock()
	if r.manager == nil || time.Since(r.statsAt) < time.Second {
		r.mu.Unlock()
		return
	}
	manager := r.manager
	r.statsAt = time.Now()
	r.mu.Unlock()
	stats, err := manager.PeerStats()
	if err != nil {
		return
	}
	values := make(map[[32]byte]room.MemberStats, len(stats))
	for _, stat := range stats {
		values[stat.PublicKey] = room.MemberStats{
			LastHandshake: stat.LastHandshake, RxBytes: stat.RxBytes, TxBytes: stat.TxBytes,
		}
	}
	r.rooms.UpdateMemberStats(values)
}
