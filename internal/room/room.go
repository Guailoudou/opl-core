package room

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Guailoudou/opl-core/internal/device"
	"github.com/Guailoudou/opl-core/internal/invite"
	"github.com/Guailoudou/opl-core/internal/lease"
	"github.com/Guailoudou/opl-core/internal/pairing"
	"github.com/Guailoudou/opl-core/internal/secret"
	"github.com/Guailoudou/opl-core/internal/state"
)

const (
	DefaultPairPort      = 25673
	DefaultWireGuardPort = 25674
	DefaultRelayPort     = 25675
	unackedLeaseTTL      = 60 * time.Second
)

var (
	ErrRunning = errors.New("room is already running")
	ErrStopped = errors.New("room is not running")
	ErrMember  = errors.New("member not found")
	ErrInput   = errors.New("invalid room input")
)

type Options struct {
	DataDir            string
	HostUID            string
	StartNetwork       func(NetworkConfig) error
	StopNetwork        func() error
	ApplyPeer          func(lease.Lease, [32]byte) error
	RemovePeer         func([32]byte) error
	WireGuardPort      uint16
	DiscoveryRelayPort uint16
}

type NetworkConfig struct {
	PrivateKey [32]byte
	PublicKey  [32]byte
	LocalIP    netip.Addr
	ListenPort uint16
	RelayPort  uint16
}

type CreateOptions struct {
	HostUID       string
	PairPort      uint16
	WireGuardPort uint16
	FixedRoomKey  bool
	FixedHostKey  bool
}

type Member struct {
	UID           string `json:"uid,omitempty"`
	PublicKey     string `json:"publicKey"`
	IP            string `json:"ip"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	State         string `json:"state"`
	LeaseRevision uint64 `json:"leaseRevision"`
	LastHandshake int64  `json:"lastHandshake,omitempty"`
	RxBytes       uint64 `json:"rxBytes,omitempty"`
	TxBytes       uint64 `json:"txBytes,omitempty"`
}

type MemberStats struct {
	LastHandshake time.Time
	RxBytes       uint64
	TxBytes       uint64
}

type Snapshot struct {
	Running       bool     `json:"running"`
	FixedRoomKey  bool     `json:"fixedRoomKey"`
	FixedHostKey  bool     `json:"fixedHostKey"`
	JoinEnabled   bool     `json:"joinEnabled"`
	HostUID       string   `json:"hostUid,omitempty"`
	PairPort      uint16   `json:"pairPort,omitempty"`
	WireGuardPort uint16   `json:"wireGuardPort,omitempty"`
	RelayPort     uint16   `json:"discoveryRelayPort,omitempty"`
	HostIP        string   `json:"hostIP,omitempty"`
	Members       []Member `json:"members"`
}

type Service struct {
	mu             sync.Mutex
	dataDir        string
	defaultHostUID string
	startNetwork   func(NetworkConfig) error
	stopNetwork    func() error
	applyPeer      func(lease.Lease, [32]byte) error
	removePeer     func([32]byte) error
	wgPort         uint16
	relayPort      uint16
	running        bool
	fixedRoom      bool
	fixedHost      bool
	joinEnabled    bool
	hostUID        string
	pairPort       uint16
	roomID         string
	roomKey        pairing.RoomKey
	hostPrivate    [32]byte
	hostPublic     [32]byte
	allocator      *lease.Allocator
	memberStates   map[[32]byte]string
	memberPSKs     map[[32]byte][32]byte
	blocked        map[[32]byte]bool
	runtimeStats   map[[32]byte]MemberStats
	reportedStats  map[[32]byte]MemberStats
	memberUIDs     map[[32]byte]string
	pending        map[[32]byte]pendingPair
	pairAttempt    uint64
	timers         map[[32]byte]*time.Timer
	server         *pairing.Server
}

type pendingPair struct {
	token       uint64
	previous    lease.Lease
	existed     bool
	previousPSK [32]byte
	hadPSK      bool
	state       string
}

type roomSecretRecord struct {
	Version    int               `json:"version"`
	RoomKey    string            `json:"roomKey"`
	MemberPSKs map[string]string `json:"memberPsks"`
}

func New(options Options) (*Service, error) {
	if options.DataDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		options.DataDir = filepath.Join(base, "OPL", "core")
	}
	if options.WireGuardPort == 0 {
		options.WireGuardPort = DefaultWireGuardPort
	}
	if options.DiscoveryRelayPort == 0 {
		options.DiscoveryRelayPort = DefaultRelayPort
	}
	return &Service{
		dataDir:        options.DataDir,
		defaultHostUID: options.HostUID,
		startNetwork:   options.StartNetwork,
		stopNetwork:    options.StopNetwork,
		applyPeer:      options.ApplyPeer,
		removePeer:     options.RemovePeer,
		wgPort:         options.WireGuardPort,
		relayPort:      options.DiscoveryRelayPort,
		memberStates:   make(map[[32]byte]string),
		memberPSKs:     make(map[[32]byte][32]byte),
		blocked:        make(map[[32]byte]bool),
		runtimeStats:   make(map[[32]byte]MemberStats),
		reportedStats:  make(map[[32]byte]MemberStats),
		memberUIDs:     make(map[[32]byte]string),
		pending:        make(map[[32]byte]pendingPair),
		timers:         make(map[[32]byte]*time.Timer),
	}, nil
}

func (s *Service) Create(options CreateOptions) (Snapshot, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return Snapshot{}, ErrRunning
	}
	if options.PairPort == 0 {
		port, err := availablePort("tcp4")
		if err != nil {
			s.mu.Unlock()
			return Snapshot{}, err
		}
		options.PairPort = port
	}
	if options.WireGuardPort == 0 {
		port, err := availablePort("udp4")
		if err != nil {
			s.mu.Unlock()
			return Snapshot{}, err
		}
		options.WireGuardPort = port
	}
	if options.HostUID == "" {
		options.HostUID = s.defaultHostUID
	}
	if _, err := invite.Encode(invite.Invite{HostUID: options.HostUID, PairPort: options.PairPort, RoomKey: [12]byte{1}}); err != nil {
		s.mu.Unlock()
		return Snapshot{}, ErrInput
	}
	if options.FixedRoomKey && !options.FixedHostKey {
		s.mu.Unlock()
		return Snapshot{}, ErrInput
	}
	roomID, roomKey, err := newRoomIdentity()
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	hostKey, err := device.LoadOrCreate(s.hostSecretPath(), options.FixedHostKey)
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	allocator, _ := lease.New(nil)
	s.fixedRoom = options.FixedRoomKey
	s.fixedHost = options.FixedHostKey
	s.joinEnabled = true
	s.hostUID = options.HostUID
	s.pairPort = options.PairPort
	s.wgPort = options.WireGuardPort
	s.roomID = roomID
	s.roomKey = roomKey
	s.hostPrivate = hostKey.Private
	s.hostPublic = hostKey.Public
	s.allocator = allocator
	s.memberStates = make(map[[32]byte]string)
	s.memberPSKs = make(map[[32]byte][32]byte)
	s.blocked = make(map[[32]byte]bool)
	s.runtimeStats = make(map[[32]byte]MemberStats)
	s.reportedStats = make(map[[32]byte]MemberStats)
	s.memberUIDs = make(map[[32]byte]string)
	s.pending = make(map[[32]byte]pendingPair)
	if s.startNetwork != nil {
		if err := s.startNetwork(s.networkConfigLocked()); err != nil {
			s.clearLocked()
			s.mu.Unlock()
			return Snapshot{}, err
		}
	}
	if err := s.listenLocked(); err != nil {
		s.clearLocked()
		s.mu.Unlock()
		if s.stopNetwork != nil {
			_ = s.stopNetwork()
		}
		return Snapshot{}, err
	}
	if s.fixedRoom {
		if err := s.persistLocked(); err != nil {
			server := s.server
			s.clearLocked()
			s.mu.Unlock()
			_ = server.Close()
			if s.stopNetwork != nil {
				_ = s.stopNetwork()
			}
			return Snapshot{}, err
		}
	}
	s.running = true
	snapshot := s.snapshotLocked()
	s.mu.Unlock()
	return snapshot, nil
}

func (s *Service) StartOrCreate(options CreateOptions) (Snapshot, error) {
	if _, err := os.Stat(s.statePath()); err == nil {
		return s.Start()
	} else if !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	return s.Create(options)
}

func availablePort(network string) (uint16, error) {
	if network == "tcp4" {
		listener, err := net.Listen(network, "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		defer listener.Close()
		return uint16(listener.Addr().(*net.TCPAddr).Port), nil
	}
	packet, err := net.ListenPacket(network, "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer packet.Close()
	return uint16(packet.LocalAddr().(*net.UDPAddr).Port), nil
}

func (s *Service) Start() (Snapshot, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return Snapshot{}, ErrRunning
	}
	persisted, err := state.Load(s.statePath())
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, fmt.Errorf("load fixed room: %w", err)
	}
	roomSecret, err := secret.Load(s.roomSecretPath())
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, fmt.Errorf("load fixed room secret: %w", err)
	}
	roomKey, memberPSKs, err := parseRoomSecret(roomSecret)
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	hostKey, err := device.LoadOrCreate(s.hostSecretPath(), true)
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	privateKey, publicKey := hostKey.Private, hostKey.Public
	leasing, err := leasesFromState(persisted.Members)
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	allocator, err := lease.New(leasing)
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	s.fixedRoom, s.fixedHost, s.joinEnabled, s.hostUID = true, true, persisted.JoinEnabled, persisted.HostUID
	s.pairPort, s.wgPort, s.relayPort = persisted.PairPort, persisted.WireGuardPort, persisted.RelayPort
	s.roomID, s.roomKey = persisted.RoomID, roomKey
	s.hostPrivate, s.hostPublic, s.allocator = privateKey, publicKey, allocator
	s.memberStates = make(map[[32]byte]string, len(leasing))
	s.memberPSKs = memberPSKs
	s.blocked = blockedFromState(persisted.BlockedMembers)
	s.runtimeStats = make(map[[32]byte]MemberStats)
	s.reportedStats = make(map[[32]byte]MemberStats)
	s.memberUIDs = make(map[[32]byte]string)
	s.pending = make(map[[32]byte]pendingPair)
	for _, item := range leasing {
		s.memberStates[item.PublicKey] = "offline"
	}
	if s.startNetwork != nil {
		if err := s.startNetwork(s.networkConfigLocked()); err != nil {
			s.clearLocked()
			s.mu.Unlock()
			return Snapshot{}, err
		}
	}
	for _, item := range leasing {
		if item.Enabled && s.applyPeer != nil {
			psk, ok := s.memberPSKs[item.PublicKey]
			if !ok {
				psk = pairing.DerivePSK(roomKey, publicKey, item.PublicKey)
				s.memberPSKs[item.PublicKey] = psk
			}
			if err := s.applyPeer(item, psk); err != nil {
				s.clearLocked()
				s.mu.Unlock()
				if s.stopNetwork != nil {
					_ = s.stopNetwork()
				}
				return Snapshot{}, err
			}
		}
	}
	if err := s.persistRoomSecretLocked(); err != nil {
		s.clearLocked()
		s.mu.Unlock()
		if s.stopNetwork != nil {
			_ = s.stopNetwork()
		}
		return Snapshot{}, err
	}
	if err := s.listenLocked(); err != nil {
		s.clearLocked()
		s.mu.Unlock()
		if s.stopNetwork != nil {
			_ = s.stopNetwork()
		}
		return Snapshot{}, err
	}
	s.running = true
	snapshot := s.snapshotLocked()
	s.mu.Unlock()
	return snapshot, nil
}

func (s *Service) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	server := s.server
	s.server = nil
	s.running = false
	for key, timer := range s.timers {
		timer.Stop()
		delete(s.timers, key)
	}
	if !s.fixedRoom {
		s.clearLocked()
	}
	s.mu.Unlock()
	listenerErr := server.Close()
	if s.stopNetwork != nil {
		return errors.Join(listenerErr, s.stopNetwork())
	}
	return listenerErr
}

func (s *Service) SetJoinEnabled(enabled bool) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return Snapshot{}, ErrStopped
	}
	previous := s.joinEnabled
	s.joinEnabled = enabled
	if s.fixedRoom {
		if err := s.persistLocked(); err != nil {
			s.joinEnabled = previous
			return Snapshot{}, err
		}
	}
	return s.snapshotLocked(), nil
}

func (s *Service) GetInvite() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return "", ErrStopped
	}
	return invite.Encode(invite.Invite{FixedRoomKey: s.fixedRoom, HostUID: s.hostUID, PairPort: s.pairPort, RoomKey: [12]byte(s.roomKey)})
}

func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Service) UpdateMemberStats(stats map[[32]byte]MemberStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allocator == nil {
		return
	}
	for _, item := range s.allocator.Snapshot() {
		stat, ok := stats[item.PublicKey]
		if !ok {
			continue
		}
		if !stat.LastHandshake.IsZero() && time.Since(stat.LastHandshake) <= 3*time.Minute {
			s.memberStates[item.PublicKey] = "online"
		} else {
			s.memberStates[item.PublicKey] = "configured"
		}
		s.runtimeStats[item.PublicKey] = stat
	}
}

func (s *Service) RemoveMember(encodedKey string) (Snapshot, error) {
	key, err := decodePublicKey(encodedKey)
	if err != nil {
		return Snapshot{}, ErrInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return Snapshot{}, ErrStopped
	}
	previous, ok := findLease(s.allocator.Snapshot(), key)
	if !ok {
		return Snapshot{}, ErrMember
	}
	previousPSK, hadPSK := s.memberPSKs[key]
	previousState := s.memberStates[key]
	previousBlocked := s.blocked[key]
	s.blocked[key] = true
	s.allocator.Remove(key)
	if timer := s.timers[key]; timer != nil {
		timer.Stop()
		delete(s.timers, key)
	}
	delete(s.memberStates, key)
	delete(s.memberPSKs, key)
	delete(s.runtimeStats, key)
	delete(s.reportedStats, key)
	delete(s.memberUIDs, key)
	delete(s.pending, key)
	if s.removePeer != nil {
		if err := s.removePeer(key); err != nil {
			if !previousBlocked {
				delete(s.blocked, key)
			}
			_ = s.allocator.Restore(previous)
			s.memberStates[key] = previousState
			if hadPSK {
				s.memberPSKs[key] = previousPSK
			}
			return Snapshot{}, err
		}
	}
	if s.fixedRoom {
		if err := s.persistLocked(); err != nil {
			if !previousBlocked {
				delete(s.blocked, key)
			}
			_ = s.allocator.Restore(previous)
			s.memberStates[key] = previousState
			if hadPSK {
				s.memberPSKs[key] = previousPSK
			}
			if s.applyPeer != nil && hadPSK {
				_ = s.applyPeer(previous, previousPSK)
			}
			return Snapshot{}, err
		}
	}
	return s.snapshotLocked(), nil
}

func (s *Service) RenameMember(encodedKey, name string) (Snapshot, error) {
	key, err := decodePublicKey(encodedKey)
	if err != nil || !validName(name) {
		return Snapshot{}, ErrInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return Snapshot{}, ErrStopped
	}
	previous := s.allocator.Snapshot()
	if _, exists := findLease(previous, key); !exists {
		return Snapshot{}, ErrMember
	}
	if _, _, err := s.allocator.Assign(key, name); err != nil {
		return Snapshot{}, err
	}
	if s.fixedRoom {
		if err := s.persistLocked(); err != nil {
			s.allocator, _ = lease.New(previous)
			return Snapshot{}, err
		}
	}
	return s.snapshotLocked(), nil
}

func (s *Service) RotateKey() (string, error) {
	var next pairing.RoomKey
	if _, err := cryptorand.Read(next[:]); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return "", ErrStopped
	}
	code, err := invite.Encode(invite.Invite{FixedRoomKey: s.fixedRoom, HostUID: s.hostUID, PairPort: s.pairPort, RoomKey: [12]byte(next)})
	if err != nil {
		return "", err
	}
	previous := s.roomKey
	s.roomKey = next
	if s.fixedRoom {
		if err := s.persistRoomSecretLocked(); err != nil {
			s.roomKey = previous
			return "", err
		}
	}
	s.server.SetRoomKey(next)
	return code, nil
}

func (s *Service) listenLocked() error {
	server, err := pairing.Listen(fmt.Sprintf("127.0.0.1:%d", s.pairPort), pairing.ServerConfig{
		RoomKey:            s.roomKey,
		HostPublicKey:      s.hostPublic,
		WireGuardPort:      s.wgPort,
		DiscoveryRelayPort: s.relayPort,
		JoinEnabled: func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.running && s.joinEnabled
		},
		KnownMember:         s.knownMember,
		MemberAuthKey:       s.memberAuthKey,
		Assign:              s.assign,
		PairingFailed:       s.pairingFailed,
		PairingAcknowledged: s.pairingAcknowledged,
		ReportStats:         s.reportStats,
	})
	if err != nil {
		return err
	}
	s.server = server
	return nil
}

func (s *Service) assign(key [32]byte, name string, roomKey pairing.RoomKey, existingAuth bool) (pairing.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return pairing.Assignment{}, pairing.ErrJoinPaused
	}
	if s.blocked[key] {
		return pairing.Assignment{}, pairing.ErrMemberDisabled
	}
	previous, existed := findLease(s.allocator.Snapshot(), key)
	if !s.joinEnabled && !existed {
		return pairing.Assignment{}, pairing.ErrJoinPaused
	}
	if _, busy := s.pending[key]; busy {
		return pairing.Assignment{}, errors.New("member is already pairing")
	}
	previousPSK, hadPSK := s.memberPSKs[key]
	if existingAuth && (!existed || !previous.Enabled || !hadPSK) {
		return pairing.Assignment{}, pairing.ErrMemberDisabled
	}
	previousState := s.memberStates[key]
	item, created, err := s.allocator.Assign(key, name)
	if errors.Is(err, lease.ErrFull) {
		return pairing.Assignment{}, pairing.ErrRoomFull
	}
	if err != nil {
		return pairing.Assignment{}, err
	}
	s.pairAttempt++
	pending := pendingPair{token: s.pairAttempt, previous: previous, existed: existed, previousPSK: previousPSK, hadPSK: hadPSK, state: previousState}
	s.pending[key] = pending
	psk := previousPSK
	if !existingAuth {
		psk = pairing.DerivePSK(roomKey, s.hostPublic, key)
	}
	s.memberPSKs[key] = psk
	if s.applyPeer != nil && !existingAuth {
		if err := s.applyPeer(item, psk); err != nil {
			s.rollbackPairLocked(key, pending)
			return pairing.Assignment{}, err
		}
	}
	s.memberStates[key] = "pairing"
	if s.fixedRoom {
		if err := s.persistLocked(); err != nil {
			s.rollbackPairLocked(key, pending)
			return pairing.Assignment{}, err
		}
	}
	return pairing.Assignment{IP: item.IP, Revision: item.Revision, Created: created, Token: pending.token}, nil
}

func (s *Service) knownMember(key [32]byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.allocator == nil || s.blocked[key] {
		return false
	}
	item, ok := findLease(s.allocator.Snapshot(), key)
	return ok && item.Enabled
}

func (s *Service) memberAuthKey(key [32]byte) ([32]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.allocator == nil || s.blocked[key] {
		return [32]byte{}, false
	}
	item, memberExists := findLease(s.allocator.Snapshot(), key)
	value, keyExists := s.memberPSKs[key]
	return value, memberExists && item.Enabled && keyExists
}

func (s *Service) pairingFailed(key [32]byte, assignment pairing.Assignment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.pending[key]
	if !ok || pending.token != assignment.Token {
		return
	}
	delete(s.pending, key)
	if !assignment.Created {
		s.rollbackPairLocked(key, pending)
		if s.fixedRoom {
			_ = s.persistLocked()
		}
		return
	}
	if existing := s.timers[key]; existing != nil {
		existing.Stop()
	}
	s.timers[key] = time.AfterFunc(unackedLeaseTTL, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.timers, key)
		if !s.running || s.allocator == nil {
			return
		}
		item, ok := findLease(s.allocator.Snapshot(), key)
		if !ok || item.Revision != assignment.Revision || s.memberStates[key] == "configured" {
			return
		}
		if !s.allocator.RemoveIfRevision(key, assignment.Revision) {
			return
		}
		delete(s.memberStates, key)
		delete(s.memberPSKs, key)
		if s.removePeer != nil {
			_ = s.removePeer(key)
		}
		if s.fixedRoom {
			_ = s.persistLocked()
		}
	})
}

func (s *Service) pairingAcknowledged(key [32]byte, assignment pairing.Assignment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.allocator == nil {
		return
	}
	if pending, ok := s.pending[key]; !ok || pending.token != assignment.Token {
		return
	}
	delete(s.pending, key)
	if timer := s.timers[key]; timer != nil {
		timer.Stop()
		delete(s.timers, key)
	}
	s.memberStates[key] = "configured"
}

func (s *Service) reportStats(key [32]byte, device pairing.Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allocator == nil {
		return
	}
	if item, ok := findLease(s.allocator.Snapshot(), key); !ok || item.IP.String() != device.VirtualIP {
		return
	}
	s.memberUIDs[key] = device.UID
	s.reportedStats[key] = MemberStats{RxBytes: device.RxBytes, TxBytes: device.TxBytes}
}

func (s *Service) rollbackPairLocked(key [32]byte, pending pendingPair) {
	delete(s.pending, key)
	if pending.existed {
		_ = s.allocator.Restore(pending.previous)
	} else {
		s.allocator.Remove(key)
	}
	if pending.hadPSK {
		s.memberPSKs[key] = pending.previousPSK
	} else {
		delete(s.memberPSKs, key)
	}
	if pending.state == "" {
		delete(s.memberStates, key)
	} else {
		s.memberStates[key] = pending.state
	}
	if s.applyPeer != nil && pending.existed && pending.hadPSK {
		_ = s.applyPeer(pending.previous, pending.previousPSK)
	} else if s.removePeer != nil {
		_ = s.removePeer(key)
	}
}

func (s *Service) persistLocked() error {
	previousSecret, previousErr := secret.Load(s.roomSecretPath())
	if err := s.persistRoomSecretLocked(); err != nil {
		return err
	}
	err := state.Save(s.statePath(), state.Room{
		SchemaVersion:  state.SchemaVersion,
		RoomID:         s.roomID,
		HostUID:        s.hostUID,
		PairPort:       s.pairPort,
		WireGuardPort:  s.wgPort,
		RelayPort:      s.relayPort,
		JoinEnabled:    s.joinEnabled,
		Members:        stateFromLeases(s.allocator.Snapshot()),
		BlockedMembers: blockedForState(s.blocked),
	})
	if err == nil {
		return nil
	}
	var rollbackErr error
	if previousErr == nil {
		rollbackErr = secret.Save(s.roomSecretPath(), previousSecret)
	} else if errors.Is(previousErr, os.ErrNotExist) {
		rollbackErr = os.Remove(s.roomSecretPath())
		if errors.Is(rollbackErr, os.ErrNotExist) {
			rollbackErr = nil
		}
	}
	return errors.Join(err, rollbackErr)
}

func (s *Service) persistRoomSecretLocked() error {
	record := roomSecretRecord{Version: 1, RoomKey: base64.StdEncoding.EncodeToString(s.roomKey[:]), MemberPSKs: make(map[string]string, len(s.memberPSKs))}
	for key, psk := range s.memberPSKs {
		record.MemberPSKs[base64.StdEncoding.EncodeToString(key[:])] = base64.StdEncoding.EncodeToString(psk[:])
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return secret.Save(s.roomSecretPath(), data)
}

func (s *Service) snapshotLocked() Snapshot {
	result := Snapshot{Running: s.running, FixedRoomKey: s.fixedRoom, FixedHostKey: s.fixedHost, JoinEnabled: s.joinEnabled, Members: []Member{}}
	if s.running || s.fixedRoom {
		result.HostUID, result.PairPort = s.hostUID, s.pairPort
		result.WireGuardPort, result.RelayPort, result.HostIP = s.wgPort, s.relayPort, "10.0.23.1"
	}
	if s.allocator == nil {
		return result
	}
	for _, item := range s.allocator.Snapshot() {
		stat := s.runtimeStats[item.PublicKey]
		traffic, ok := s.reportedStats[item.PublicKey]
		if !ok {
			traffic = stat
		}
		var lastHandshake int64
		if !stat.LastHandshake.IsZero() {
			lastHandshake = stat.LastHandshake.Unix()
		}
		result.Members = append(result.Members, Member{
			UID:       s.memberUIDs[item.PublicKey],
			PublicKey: base64.StdEncoding.EncodeToString(item.PublicKey[:]),
			IP:        item.IP.String(), Name: item.Name, Enabled: item.Enabled,
			State: s.memberStates[item.PublicKey], LeaseRevision: item.Revision,
			LastHandshake: lastHandshake, RxBytes: traffic.RxBytes, TxBytes: traffic.TxBytes,
		})
	}
	return result
}

func (s *Service) clearLocked() {
	s.running, s.fixedRoom, s.fixedHost, s.joinEnabled = false, false, false, false
	s.hostUID, s.roomID, s.pairPort = "", "", 0
	s.roomKey, s.hostPrivate, s.hostPublic = pairing.RoomKey{}, [32]byte{}, [32]byte{}
	s.allocator, s.server = nil, nil
	s.memberStates = make(map[[32]byte]string)
	s.memberPSKs = make(map[[32]byte][32]byte)
	s.blocked = make(map[[32]byte]bool)
	s.runtimeStats = make(map[[32]byte]MemberStats)
	s.reportedStats = make(map[[32]byte]MemberStats)
	s.memberUIDs = make(map[[32]byte]string)
	s.pending = make(map[[32]byte]pendingPair)
}

func (s *Service) networkConfigLocked() NetworkConfig {
	return NetworkConfig{
		PrivateKey: s.hostPrivate,
		PublicKey:  s.hostPublic,
		LocalIP:    netip.MustParseAddr("10.0.23.1"),
		ListenPort: s.wgPort,
		RelayPort:  s.relayPort,
	}
}

func (s *Service) statePath() string      { return filepath.Join(s.dataDir, "room.json") }
func (s *Service) roomSecretPath() string { return filepath.Join(s.dataDir, "room.secret") }
func (s *Service) hostSecretPath() string { return filepath.Join(s.dataDir, "host.secret") }

func newRoomIdentity() (string, pairing.RoomKey, error) {
	var id [16]byte
	var key pairing.RoomKey
	if _, err := cryptorand.Read(id[:]); err != nil {
		return "", key, err
	}
	if _, err := cryptorand.Read(key[:]); err != nil {
		return "", key, err
	}
	return hex.EncodeToString(id[:]), key, nil
}

func parseRoomSecret(data []byte) (pairing.RoomKey, map[[32]byte][32]byte, error) {
	var record roomSecretRecord
	if json.Unmarshal(data, &record) != nil || record.Version != 1 {
		return pairing.RoomKey{}, nil, ErrInput
	}
	decodedRoomKey, err := base64.StdEncoding.DecodeString(record.RoomKey)
	if err != nil || len(decodedRoomKey) != 12 {
		return pairing.RoomKey{}, nil, ErrInput
	}
	var roomKey pairing.RoomKey
	copy(roomKey[:], decodedRoomKey)
	memberPSKs := make(map[[32]byte][32]byte, len(record.MemberPSKs))
	for encodedKey, encodedPSK := range record.MemberPSKs {
		key, keyErr := decodePublicKey(encodedKey)
		pskBytes, pskErr := base64.StdEncoding.DecodeString(encodedPSK)
		if keyErr != nil || pskErr != nil || len(pskBytes) != 32 {
			return pairing.RoomKey{}, nil, ErrInput
		}
		var psk [32]byte
		copy(psk[:], pskBytes)
		memberPSKs[key] = psk
	}
	return roomKey, memberPSKs, nil
}

func stateFromLeases(items []lease.Lease) []state.Member {
	result := make([]state.Member, 0, len(items))
	for _, item := range items {
		result = append(result, state.Member{
			PublicKey: base64.StdEncoding.EncodeToString(item.PublicKey[:]), IP: item.IP.String(),
			Name: item.Name, Enabled: item.Enabled, LeaseRevision: item.Revision,
		})
	}
	return result
}

func blockedForState(blocked map[[32]byte]bool) []string {
	result := make([]string, 0, len(blocked))
	for key, value := range blocked {
		if value {
			result = append(result, base64.StdEncoding.EncodeToString(key[:]))
		}
	}
	sort.Strings(result)
	return result
}

func blockedFromState(items []string) map[[32]byte]bool {
	result := make(map[[32]byte]bool, len(items))
	for _, encoded := range items {
		key, _ := decodePublicKey(encoded)
		result[key] = true
	}
	return result
}

func leasesFromState(items []state.Member) ([]lease.Lease, error) {
	result := make([]lease.Lease, 0, len(items))
	for _, item := range items {
		key, err := decodePublicKey(item.PublicKey)
		ip, ipErr := netip.ParseAddr(item.IP)
		if err != nil || ipErr != nil {
			return nil, ErrInput
		}
		result = append(result, lease.Lease{PublicKey: key, IP: ip, Name: item.Name, Enabled: item.Enabled, Revision: item.LeaseRevision})
	}
	return result, nil
}

func decodePublicKey(value string) ([32]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, ErrInput
	}
	var key [32]byte
	copy(key[:], decoded)
	return key, nil
}

func validName(name string) bool {
	if !utf8.ValidString(name) || len([]byte(name)) > pairing.MaxNameBytes {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func findLease(items []lease.Lease, key [32]byte) (lease.Lease, bool) {
	for _, item := range items {
		if item.PublicKey == key {
			return item, true
		}
	}
	return lease.Lease{}, false
}
