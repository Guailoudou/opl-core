package pairing

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxConcurrent    = 16
	idleTimeout      = 10 * time.Second
	totalTimeout     = 30 * time.Second
	configureTimeout = 2 * time.Minute
	maxDevices       = 253
)

var (
	ErrAuthFailed     = errors.New("pairing authentication failed")
	ErrJoinPaused     = errors.New("room is not accepting members")
	ErrMemberDisabled = errors.New("member is disabled")
)

type Assignment struct {
	IP       netip.Addr
	Revision uint64
	Created  bool
	Token    uint64
}

type Device struct {
	LatencyMS int64  `json:"latencyMs"`
	UID       string `json:"uid"`
	VirtualIP string `json:"virtualIP"`
	RxBytes   uint64 `json:"rxBytes"`
	TxBytes   uint64 `json:"txBytes"`
}

type StatsReport struct {
	TimestampMS int64
	UID         string
	RxBytes     uint64
	TxBytes     uint64
}

type ServerConfig struct {
	RoomKey             RoomKey
	HostPublicKey       [32]byte
	WireGuardPort       uint16
	DiscoveryRelayPort  uint16
	JoinEnabled         func() bool
	BlockedUID          func(string) bool
	KnownMember         func([32]byte) bool
	MemberAuthKey       func([32]byte) ([32]byte, bool)
	Assign              func([32]byte, string, RoomKey, bool) (Assignment, error)
	PairingFailed       func([32]byte, Assignment)
	PairingAcknowledged func([32]byte, Assignment)
	ReportStats         func([32]byte, Device)
	HostDevice          Device
}

type Server struct {
	listener     net.Listener
	config       ServerConfig
	once         sync.Once
	workers      sync.WaitGroup
	limit        chan struct{}
	rate         rateLimiter
	connections  map[net.Conn]struct{}
	devices      map[[32]byte]Device
	memberUIDs   map[[32]byte]string
	deviceOwners map[[32]byte]net.Conn
	roomKey      RoomKey
	keyMu        sync.RWMutex
	writeMu      sync.Mutex
	mu           sync.Mutex
}

func Listen(address string, config ServerConfig) (*Server, error) {
	if isZero(config.RoomKey[:]) || isZero(config.HostPublicKey[:]) || config.WireGuardPort == 0 || config.DiscoveryRelayPort == 0 || config.JoinEnabled == nil || config.Assign == nil {
		return nil, ErrProtocol
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return nil, err
	}
	addr, parseErr := netip.ParseAddrPort(listener.Addr().String())
	if parseErr != nil || !addr.Addr().IsLoopback() {
		listener.Close()
		return nil, ErrProtocol
	}
	s := &Server{
		listener: listener, config: config, limit: make(chan struct{}, maxConcurrent),
		rate: rateLimiter{tokens: 10, last: time.Now()}, connections: make(map[net.Conn]struct{}), devices: make(map[[32]byte]Device), memberUIDs: make(map[[32]byte]string), deviceOwners: make(map[[32]byte]net.Conn), roomKey: config.RoomKey,
	}
	s.workers.Add(1)
	go s.serve()
	return s, nil
}

func (s *Server) Addr() net.Addr { return s.listener.Addr() }

func (s *Server) SetRoomKey(roomKey RoomKey) {
	s.keyMu.Lock()
	s.roomKey = roomKey
	s.keyMu.Unlock()
}

func (s *Server) RemoveMember(key [32]byte) {
	s.mu.Lock()
	connection := s.deviceOwners[key]
	delete(s.devices, key)
	delete(s.memberUIDs, key)
	delete(s.deviceOwners, key)
	s.mu.Unlock()
	if connection == nil {
		return
	}
	// ponytail: one global control-write lock; split per connection if status traffic becomes high.
	s.writeMu.Lock()
	_ = WriteFrame(connection, FramePairResult, []byte{StatusMemberDisabled})
	s.writeMu.Unlock()
}

func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		err = s.listener.Close()
		s.mu.Lock()
		for connection := range s.connections {
			_ = connection.Close()
		}
		s.mu.Unlock()
	})
	s.workers.Wait()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) serve() {
	defer s.workers.Done()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		if !s.rate.allow(time.Now()) {
			connection.Close()
			continue
		}
		select {
		case s.limit <- struct{}{}:
			s.workers.Add(1)
			go s.handle(connection)
		default:
			connection.Close()
		}
	}
}

func (s *Server) handle(connection net.Conn) {
	s.mu.Lock()
	s.connections[connection] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.connections, connection)
		s.mu.Unlock()
		_ = connection.Close()
		s.workers.Done()
	}()
	key, assignment, ok := s.pair(connection)
	<-s.limit
	if !ok {
		return
	}
	s.control(connection, key, assignment.IP)
}

func (s *Server) pair(connection net.Conn) ([32]byte, Assignment, bool) {
	s.keyMu.RLock()
	roomKey := s.roomKey
	s.keyMu.RUnlock()
	deadline := time.Now().Add(totalTimeout)
	_ = connection.SetDeadline(deadline)
	challenge, err := NewServerChallenge(s.config.HostPublicKey, nil)
	if err != nil || WriteFrame(connection, FrameServerChallenge, challenge.MarshalBinary()) != nil {
		return [32]byte{}, Assignment{}, false
	}
	if !setIdleDeadline(connection, deadline) {
		return [32]byte{}, Assignment{}, false
	}
	frameType, payload, err := ReadFrame(connection)
	if err != nil || frameType != FrameClientRequest {
		return [32]byte{}, Assignment{}, false
	}
	request, err := ParseClientRequest(payload)
	authKey := roomKey[:]
	existingAuth := false
	if err == nil && !verifyClientRequest(challenge, request, authKey) && s.config.MemberAuthKey != nil {
		if memberKey, ok := s.config.MemberAuthKey(request.ClientPublicKey); ok && verifyClientRequest(challenge, request, memberKey[:]) {
			authKey = memberKey[:]
			existingAuth = true
		}
	}
	if err != nil || !verifyClientRequest(challenge, request, authKey) {
		_ = WriteFrame(connection, FramePairResult, []byte{StatusAuthFailed})
		return [32]byte{}, Assignment{}, false
	}
	if request.UID != "" && s.config.BlockedUID != nil && s.config.BlockedUID(request.UID) {
		_ = WriteFrame(connection, FramePairResult, []byte{StatusMemberDisabled})
		return [32]byte{}, Assignment{}, false
	}
	if !s.config.JoinEnabled() && (s.config.KnownMember == nil || !s.config.KnownMember(request.ClientPublicKey)) {
		_ = WriteFrame(connection, FramePairResult, []byte{StatusJoinPaused})
		return [32]byte{}, Assignment{}, false
	}
	assignment, err := s.config.Assign(request.ClientPublicKey, request.Name, roomKey, existingAuth)
	if err != nil {
		status := StatusInternal
		if errors.Is(err, ErrRoomFull) {
			status = StatusRoomFull
		} else if errors.Is(err, ErrMemberDisabled) {
			status = StatusMemberDisabled
		}
		_ = WriteFrame(connection, FramePairResult, []byte{status})
		return [32]byte{}, Assignment{}, false
	}
	result, err := newPairResult(challenge, request, assignment.IP, 24, s.config.WireGuardPort, s.config.DiscoveryRelayPort, assignment.Revision, authKey)
	if err != nil {
		s.failed(request.ClientPublicKey, assignment)
		return [32]byte{}, Assignment{}, false
	}
	resultBytes, _ := result.MarshalBinary()
	if WriteFrame(connection, FramePairResult, resultBytes) != nil || connection.SetDeadline(time.Now().Add(configureTimeout)) != nil {
		s.failed(request.ClientPublicKey, assignment)
		return [32]byte{}, Assignment{}, false
	}
	frameType, payload, err = ReadFrame(connection)
	if err != nil || frameType != FramePairAck || len(payload) != 32 {
		s.failed(request.ClientPublicKey, assignment)
		return [32]byte{}, Assignment{}, false
	}
	var ack [32]byte
	copy(ack[:], payload)
	if !verifyPairAck(authKey, assignment.Revision, ack) {
		s.failed(request.ClientPublicKey, assignment)
		return [32]byte{}, Assignment{}, false
	}
	if s.config.PairingAcknowledged != nil {
		s.config.PairingAcknowledged(request.ClientPublicKey, assignment)
	}
	s.mu.Lock()
	s.memberUIDs[request.ClientPublicKey] = request.UID
	s.mu.Unlock()
	return request.ClientPublicKey, assignment, true
}

func (s *Server) control(connection net.Conn, key [32]byte, ip netip.Addr) {
	defer func() {
		s.mu.Lock()
		if s.deviceOwners[key] == connection {
			delete(s.devices, key)
			delete(s.deviceOwners, key)
		}
		s.mu.Unlock()
	}()
	for {
		_ = connection.SetDeadline(time.Now().Add(4 * idleTimeout))
		frameType, payload, err := ReadFrame(connection)
		if err != nil || frameType != FrameStatsReport {
			return
		}
		report, err := parseStatsReport(payload)
		if err != nil {
			return
		}
		if s.config.KnownMember != nil && !s.config.KnownMember(key) {
			s.writeMu.Lock()
			_ = WriteFrame(connection, FramePairResult, []byte{StatusMemberDisabled})
			s.writeMu.Unlock()
			return
		}
		s.mu.Lock()
		expectedUID := s.memberUIDs[key]
		s.mu.Unlock()
		if expectedUID != "" {
			report.UID = expectedUID
		}
		latency := time.Now().UnixMilli() - report.TimestampMS
		if report.TimestampMS <= 0 || latency < 0 {
			latency = 0
		}
		device := Device{UID: report.UID, VirtualIP: ip.String(), RxBytes: report.RxBytes, TxBytes: report.TxBytes, LatencyMS: latency}
		s.mu.Lock()
		s.devices[key] = device
		s.deviceOwners[key] = connection
		devices := make([]Device, 0, len(s.devices)+1)
		if s.config.HostDevice.VirtualIP != "" {
			devices = append(devices, s.config.HostDevice)
		}
		for _, item := range s.devices {
			devices = append(devices, item)
		}
		s.mu.Unlock()
		sort.Slice(devices, func(i, j int) bool {
			return netip.MustParseAddr(devices[i].VirtualIP).Compare(netip.MustParseAddr(devices[j].VirtualIP)) < 0
		})
		if s.config.ReportStats != nil {
			s.config.ReportStats(key, device)
		}
		s.writeMu.Lock()
		err = WriteFrame(connection, FrameDeviceSnapshot, marshalDevices(devices))
		s.writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

func (s *Server) failed(key [32]byte, assignment Assignment) {
	if s.config.PairingFailed != nil {
		s.config.PairingFailed(key, assignment)
	}
}

type Session struct {
	connection net.Conn
	mu         sync.Mutex
	once       sync.Once
}

func (s *Session) Close() error {
	var err error
	s.once.Do(func() { err = s.connection.Close() })
	return err
}

func (s *Session) Sync(ctx context.Context, report StatsReport) ([]Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := time.Now()
	report.TimestampMS = started.UnixMilli()
	payload, err := marshalStatsReport(report)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline := time.Now().Add(idleTimeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = s.connection.SetDeadline(deadline)
	if err := WriteFrame(s.connection, FrameStatsReport, payload); err != nil {
		return nil, err
	}
	frameType, payload, err := ReadFrame(s.connection)
	if err != nil {
		return nil, protocolOrIO(err)
	}
	if frameType == FramePairResult && len(payload) == 1 {
		return nil, statusError(payload[0])
	}
	if frameType != FrameDeviceSnapshot {
		return nil, ErrProtocol
	}
	devices, err := parseDevices(payload)
	for index := range devices {
		if devices[index].VirtualIP == "10.0.23.1" {
			devices[index].LatencyMS = time.Since(started).Milliseconds()
		}
	}
	return devices, err
}

func Pair(ctx context.Context, address string, roomKey RoomKey, clientPublicKey [32]byte, name string) (PairResult, error) {
	return PairWithConfigure(ctx, address, roomKey, clientPublicKey, name, nil)
}

func PairWithConfigure(ctx context.Context, address string, roomKey RoomKey, clientPublicKey [32]byte, name string, configure func(PairResult) error) (PairResult, error) {
	return pairWithConfigure(ctx, address, roomKey[:], clientPublicKey, name, configure)
}

func ReconnectWithConfigure(ctx context.Context, address string, memberPSK [32]byte, clientPublicKey [32]byte, name string, configure func(PairResult) error) (PairResult, error) {
	return pairWithConfigure(ctx, address, memberPSK[:], clientPublicKey, name, configure)
}

func pairWithConfigure(ctx context.Context, address string, authKey []byte, clientPublicKey [32]byte, name string, configure func(PairResult) error) (PairResult, error) {
	result, session, err := pairWithSession(ctx, address, authKey, clientPublicKey, "", name, configure)
	if session != nil {
		_ = session.Close()
	}
	return result, err
}

func PairWithSession(ctx context.Context, address string, roomKey RoomKey, clientPublicKey [32]byte, name string, configure func(PairResult) error) (PairResult, *Session, error) {
	return pairWithSession(ctx, address, roomKey[:], clientPublicKey, "", name, configure)
}

func ReconnectWithSession(ctx context.Context, address string, memberPSK [32]byte, clientPublicKey [32]byte, name string) (PairResult, *Session, error) {
	return pairWithSession(ctx, address, memberPSK[:], clientPublicKey, "", name, nil)
}

func ReconnectWithConfiguredSession(ctx context.Context, address string, memberPSK [32]byte, clientPublicKey [32]byte, name string, configure func(PairResult) error) (PairResult, *Session, error) {
	return pairWithSession(ctx, address, memberPSK[:], clientPublicKey, "", name, configure)
}

func PairWithUIDSession(ctx context.Context, address string, roomKey RoomKey, clientPublicKey [32]byte, uid, name string, configure func(PairResult) error) (PairResult, *Session, error) {
	return pairWithSession(ctx, address, roomKey[:], clientPublicKey, uid, name, configure)
}

func ReconnectWithUIDSession(ctx context.Context, address string, memberPSK [32]byte, clientPublicKey [32]byte, uid, name string, configure func(PairResult) error) (PairResult, *Session, error) {
	return pairWithSession(ctx, address, memberPSK[:], clientPublicKey, uid, name, configure)
}

func pairWithSession(ctx context.Context, address string, authKey []byte, clientPublicKey [32]byte, uid, name string, configure func(PairResult) error) (PairResult, *Session, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return PairResult{}, nil, err
	}
	fail := func(err error) (PairResult, *Session, error) {
		_ = connection.Close()
		return PairResult{}, nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(totalTimeout))
	}
	frameType, payload, err := ReadFrame(connection)
	if err != nil || frameType != FrameServerChallenge {
		return fail(protocolOrIO(err))
	}
	challenge, err := ParseServerChallenge(payload)
	if err != nil {
		return fail(err)
	}
	request, err := newClientRequest(challenge, clientPublicKey, uid, name, authKey, nil)
	if err != nil {
		return fail(err)
	}
	requestBytes, _ := request.MarshalBinary()
	if err := WriteFrame(connection, FrameClientRequest, requestBytes); err != nil {
		return fail(err)
	}
	frameType, payload, err = ReadFrame(connection)
	if err != nil || frameType != FramePairResult {
		return fail(protocolOrIO(err))
	}
	if len(payload) == 1 && ValidFailureStatus(payload[0]) {
		return fail(statusError(payload[0]))
	}
	result, err := ParsePairResult(payload)
	if err != nil || !verifyPairResult(challenge, request, result, authKey) {
		return fail(ErrAuthFailed)
	}
	if configure != nil {
		if err := connection.SetDeadline(time.Now().Add(configureTimeout)); err != nil {
			return fail(err)
		}
		if err := configure(result); err != nil {
			return fail(err)
		}
	}
	ack := pairAck(authKey, result.LeaseRevision)
	if err := WriteFrame(connection, FramePairAck, ack[:]); err != nil {
		return fail(err)
	}
	_ = connection.SetDeadline(time.Time{})
	return result, &Session{connection: connection}, nil
}

func marshalStatsReport(report StatsReport) ([]byte, error) {
	if !validUID(report.UID) {
		return nil, ErrProtocol
	}
	uid := []byte(report.UID)
	payload := make([]byte, 25+len(uid))
	payload[0] = byte(len(uid))
	copy(payload[1:], uid)
	binary.BigEndian.PutUint64(payload[1+len(uid):], report.RxBytes)
	binary.BigEndian.PutUint64(payload[9+len(uid):], report.TxBytes)
	binary.BigEndian.PutUint64(payload[17+len(uid):], uint64(report.TimestampMS))
	return payload, nil
}

func parseStatsReport(payload []byte) (StatsReport, error) {
	if len(payload) < 26 {
		return StatsReport{}, ErrProtocol
	}
	uidLength := int(payload[0])
	if len(payload) != 25+uidLength {
		return StatsReport{}, ErrProtocol
	}
	report := StatsReport{UID: string(payload[1 : 1+uidLength]), RxBytes: binary.BigEndian.Uint64(payload[1+uidLength:]), TxBytes: binary.BigEndian.Uint64(payload[9+uidLength:]), TimestampMS: int64(binary.BigEndian.Uint64(payload[17+uidLength:]))}
	if !validUID(report.UID) {
		return StatsReport{}, ErrProtocol
	}
	return report, nil
}

func marshalDevices(devices []Device) []byte {
	payload := make([]byte, 2)
	binary.BigEndian.PutUint16(payload, uint16(len(devices)))
	for _, device := range devices {
		uid, ip := []byte(device.UID), netip.MustParseAddr(device.VirtualIP).As4()
		entry := make([]byte, 29+len(uid))
		entry[0] = byte(len(uid))
		copy(entry[1:], uid)
		copy(entry[1+len(uid):], ip[:])
		binary.BigEndian.PutUint64(entry[5+len(uid):], device.RxBytes)
		binary.BigEndian.PutUint64(entry[13+len(uid):], device.TxBytes)
		binary.BigEndian.PutUint64(entry[21+len(uid):], uint64(device.LatencyMS))
		payload = append(payload, entry...)
	}
	return payload
}

func parseDevices(payload []byte) ([]Device, error) {
	if len(payload) < 2 {
		return nil, ErrProtocol
	}
	count, offset := int(binary.BigEndian.Uint16(payload)), 2
	if count > maxDevices {
		return nil, ErrProtocol
	}
	devices := make([]Device, 0, count)
	for i := 0; i < count; i++ {
		if offset >= len(payload) {
			return nil, ErrProtocol
		}
		uidLength := int(payload[offset])
		if offset+29+uidLength > len(payload) {
			return nil, ErrProtocol
		}
		uid := string(payload[offset+1 : offset+1+uidLength])
		if !validUID(uid) {
			return nil, ErrProtocol
		}
		ip := netip.AddrFrom4([4]byte(payload[offset+1+uidLength : offset+5+uidLength]))
		devices = append(devices, Device{UID: uid, VirtualIP: ip.String(), RxBytes: binary.BigEndian.Uint64(payload[offset+5+uidLength:]), TxBytes: binary.BigEndian.Uint64(payload[offset+13+uidLength:]), LatencyMS: int64(binary.BigEndian.Uint64(payload[offset+21+uidLength:]))})
		offset += 29 + uidLength
	}
	if offset != len(payload) {
		return nil, ErrProtocol
	}
	return devices, nil
}

func validUID(value string) bool {
	if value != strings.TrimSpace(value) || !utf8.ValidString(value) || len(value) < 8 || len(value) > 31 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

var ErrRoomFull = errors.New("room is full")

func statusError(status byte) error {
	switch status {
	case StatusAuthFailed:
		return ErrAuthFailed
	case StatusJoinPaused:
		return ErrJoinPaused
	case StatusRoomFull:
		return ErrRoomFull
	case StatusMemberDisabled:
		return ErrMemberDisabled
	default:
		return ErrProtocol
	}
}

func protocolOrIO(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return ErrProtocol
	}
	return err
}

func setIdleDeadline(connection net.Conn, total time.Time) bool {
	idle := time.Now().Add(idleTimeout)
	if idle.After(total) {
		idle = total
	}
	return connection.SetDeadline(idle) == nil
}

type rateLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (r *rateLimiter) allow(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens += now.Sub(r.last).Seconds() * 5
	if r.tokens > 10 {
		r.tokens = 10
	}
	r.last = now
	if r.tokens < 1 {
		return false
	}
	r.tokens--
	return true
}
