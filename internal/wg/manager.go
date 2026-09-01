package wg

import (
	"encoding/hex"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Guailoudou/opl-core/internal/discovery"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
)

type StartOptions struct {
	PrivateKey  [32]byte
	LocalIP     netip.Addr
	ListenPort  uint16
	RelayPort   uint16
	Host        bool
	OnDiscovery func(discovery.Frame)
	Logf        func(string, ...any)
}

type Manager struct {
	mu        sync.Mutex
	device    *device.Device
	discovery *discovery.DiscoveryTun
	name      string
	localIP   netip.Addr
	closed    bool
}

func Start(options StartOptions) (*Manager, error) {
	if !options.LocalIP.Is4() || options.RelayPort == 0 || isZero(options.PrivateKey[:]) || (options.Host && options.ListenPort == 0) {
		return nil, ErrConfig
	}
	role := discovery.RoleClient
	if options.Host {
		if options.LocalIP != netip.MustParseAddr("10.0.23.1") || options.OnDiscovery == nil {
			return nil, ErrConfig
		}
		role = discovery.RoleHost
	}
	base, name, err := createTUN()
	if err != nil {
		return nil, err
	}
	wrapped, err := discovery.Wrap(base, role, options.LocalIP, options.RelayPort, options.OnDiscovery)
	if err != nil {
		base.Close()
		return nil, err
	}
	logger := &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf}
	if options.Logf != nil {
		logger.Errorf = options.Logf
	}
	engine := device.NewDevice(wrapped, conn.NewDefaultBind(), logger)
	configuration, _ := deviceConfig(options.PrivateKey, options.ListenPort)
	if err := engine.IpcSet(configuration); err != nil {
		engine.Close()
		return nil, err
	}
	if err := engine.Up(); err != nil {
		engine.Close()
		return nil, err
	}
	if err := configureInterface(name, options.LocalIP, options.Host); err != nil {
		engine.Close()
		return nil, err
	}
	return &Manager{device: engine, discovery: wrapped, name: name, localIP: options.LocalIP}, nil
}

func (m *Manager) SetPeer(peer Peer, client bool) error {
	configuration, err := peerConfig(peer, client)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("WireGuard is closed")
	}
	return m.device.IpcSet(configuration)
}

func (m *Manager) RemovePeer(publicKey [32]byte) error {
	configuration, err := removePeerConfig(publicKey)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("WireGuard is closed")
	}
	return m.device.IpcSet(configuration)
}

func (m *Manager) InjectDiscovery(packet []byte) error { return m.discovery.InjectInbound(packet) }

func (m *Manager) PeerStats() ([]PeerStat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("WireGuard is closed")
	}
	value, err := m.device.IpcGet()
	if err != nil {
		return nil, err
	}
	return parsePeerStats(value)
}

func parsePeerStats(value string) ([]PeerStat, error) {
	var result []PeerStat
	var current *PeerStat
	var seconds, nanos int64
	flush := func() {
		if current != nil {
			current.LastHandshake = time.Unix(seconds, nanos)
			result = append(result, *current)
		}
		current, seconds, nanos = nil, 0, 0
	}
	for _, line := range strings.Split(value, "\n") {
		key, text, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if key == "public_key" {
			flush()
			decoded, err := hex.DecodeString(text)
			if err != nil || len(decoded) != 32 {
				return nil, ErrConfig
			}
			current = &PeerStat{}
			copy(current.PublicKey[:], decoded)
			continue
		}
		if current == nil {
			continue
		}
		var err error
		switch key {
		case "last_handshake_time_sec":
			seconds, err = strconv.ParseInt(text, 10, 64)
		case "last_handshake_time_nsec":
			nanos, err = strconv.ParseInt(text, 10, 64)
		case "rx_bytes":
			current.RxBytes, err = strconv.ParseUint(text, 10, 64)
		case "tx_bytes":
			current.TxBytes, err = strconv.ParseUint(text, 10, 64)
		}
		if err != nil {
			return nil, ErrConfig
		}
	}
	flush()
	return result, nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	engine, name, localIP := m.device, m.name, m.localIP
	m.mu.Unlock()
	cleanupErr := cleanupInterface(name, localIP)
	engine.Close()
	return cleanupErr
}
