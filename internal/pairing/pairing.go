package pairing

import (
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"unicode/utf8"
)

const (
	ProtocolVersion byte = 1
	MaxNameBytes         = 64
	MaxFrameBytes        = 16384

	FrameServerChallenge byte = 1
	FrameClientRequest   byte = 2
	FramePairResult      byte = 3
	FramePairAck         byte = 4
	FrameStatsReport     byte = 5
	FrameDeviceSnapshot  byte = 6

	StatusOK             byte = 0
	StatusAuthFailed     byte = 1
	StatusJoinPaused     byte = 2
	StatusRoomFull       byte = 3
	StatusInternal       byte = 4
	StatusMemberDisabled byte = 5
)

var ErrProtocol = errors.New("invalid pairing protocol message")

var (
	requestDomain = []byte("OPL2-PAIR-REQUEST")
	resultDomain  = []byte("OPL2-PAIR-RESULT")
	ackDomain     = []byte("OPL2-PAIR-ACK")
	pskDomain     = []byte("OPL2-WG-PSK")
)

type ServerChallenge struct {
	Version       byte
	ServerNonce   [32]byte
	HostPublicKey [32]byte
}

type RoomKey [12]byte

func NewServerChallenge(hostPublicKey [32]byte, random io.Reader) (ServerChallenge, error) {
	if isZero(hostPublicKey[:]) {
		return ServerChallenge{}, ErrProtocol
	}
	if random == nil {
		random = cryptorand.Reader
	}
	v := ServerChallenge{Version: ProtocolVersion, HostPublicKey: hostPublicKey}
	_, err := io.ReadFull(random, v.ServerNonce[:])
	return v, err
}

func (v ServerChallenge) MarshalBinary() []byte {
	b := make([]byte, 65)
	b[0] = v.Version
	copy(b[1:33], v.ServerNonce[:])
	copy(b[33:], v.HostPublicKey[:])
	return b
}

func ParseServerChallenge(b []byte) (ServerChallenge, error) {
	if len(b) != 65 || b[0] != ProtocolVersion || isZero(b[33:]) {
		return ServerChallenge{}, ErrProtocol
	}
	v := ServerChallenge{Version: b[0]}
	copy(v.ServerNonce[:], b[1:33])
	copy(v.HostPublicKey[:], b[33:])
	return v, nil
}

type ClientRequest struct {
	ClientNonce     [32]byte
	ClientPublicKey [32]byte
	Name            string
	MAC             [32]byte
}

func NewClientRequest(challenge ServerChallenge, clientPublicKey [32]byte, name string, roomKey RoomKey, random io.Reader) (ClientRequest, error) {
	return newClientRequest(challenge, clientPublicKey, name, roomKey[:], random)
}

func newClientRequest(challenge ServerChallenge, clientPublicKey [32]byte, name string, authKey []byte, random io.Reader) (ClientRequest, error) {
	if !validName(name) || !validAuthKey(authKey) || isZero(clientPublicKey[:]) || challenge.Version != ProtocolVersion || isZero(challenge.HostPublicKey[:]) {
		return ClientRequest{}, ErrProtocol
	}
	if random == nil {
		random = cryptorand.Reader
	}
	v := ClientRequest{ClientPublicKey: clientPublicKey, Name: name}
	if _, err := io.ReadFull(random, v.ClientNonce[:]); err != nil {
		return ClientRequest{}, err
	}
	v.MAC = requestMAC(challenge, v, authKey)
	return v, nil
}

func (v ClientRequest) MarshalBinary() ([]byte, error) {
	if !validName(v.Name) || isZero(v.ClientPublicKey[:]) {
		return nil, ErrProtocol
	}
	name := []byte(v.Name)
	b := make([]byte, 97+len(name))
	copy(b[:32], v.ClientNonce[:])
	copy(b[32:64], v.ClientPublicKey[:])
	b[64] = byte(len(name))
	copy(b[65:65+len(name)], name)
	copy(b[65+len(name):], v.MAC[:])
	return b, nil
}

func ParseClientRequest(b []byte) (ClientRequest, error) {
	if len(b) < 97 {
		return ClientRequest{}, ErrProtocol
	}
	nameLength := int(b[64])
	if nameLength > MaxNameBytes || len(b) != 97+nameLength {
		return ClientRequest{}, ErrProtocol
	}
	v := ClientRequest{Name: string(b[65 : 65+nameLength])}
	copy(v.ClientNonce[:], b[:32])
	copy(v.ClientPublicKey[:], b[32:64])
	copy(v.MAC[:], b[65+nameLength:])
	if !validName(v.Name) || isZero(v.ClientPublicKey[:]) {
		return ClientRequest{}, ErrProtocol
	}
	return v, nil
}

func VerifyClientRequest(challenge ServerChallenge, request ClientRequest, roomKey RoomKey) bool {
	return verifyClientRequest(challenge, request, roomKey[:])
}

func verifyClientRequest(challenge ServerChallenge, request ClientRequest, authKey []byte) bool {
	if challenge.Version != ProtocolVersion || isZero(challenge.HostPublicKey[:]) || isZero(request.ClientPublicKey[:]) || !validAuthKey(authKey) || !validName(request.Name) {
		return false
	}
	expected := requestMAC(challenge, request, authKey)
	return hmac.Equal(expected[:], request.MAC[:])
}

func requestMAC(challenge ServerChallenge, request ClientRequest, authKey []byte) [32]byte {
	h := hmac.New(sha256.New, authKey)
	h.Write(requestDomain)
	h.Write([]byte{challenge.Version})
	h.Write(challenge.ServerNonce[:])
	h.Write(request.ClientNonce[:])
	h.Write(challenge.HostPublicKey[:])
	h.Write(request.ClientPublicKey[:])
	h.Write([]byte{byte(len([]byte(request.Name)))})
	h.Write([]byte(request.Name))
	return sum32(h.Sum(nil))
}

type PairResult struct {
	Status             byte
	AssignedIP         netip.Addr
	PrefixLength       byte
	WireGuardPort      uint16
	DiscoveryRelayPort uint16
	HostPublicKey      [32]byte
	LeaseRevision      uint64
	MAC                [32]byte
}

func NewPairResult(challenge ServerChallenge, request ClientRequest, assignedIP netip.Addr, prefix byte, wireGuardPort, relayPort uint16, revision uint64, roomKey RoomKey) (PairResult, error) {
	return newPairResult(challenge, request, assignedIP, prefix, wireGuardPort, relayPort, revision, roomKey[:])
}

func newPairResult(challenge ServerChallenge, request ClientRequest, assignedIP netip.Addr, prefix byte, wireGuardPort, relayPort uint16, revision uint64, authKey []byte) (PairResult, error) {
	if !assignedIP.Is4() || prefix > 32 || wireGuardPort == 0 || relayPort == 0 || !validAuthKey(authKey) {
		return PairResult{}, ErrProtocol
	}
	v := PairResult{
		Status:             StatusOK,
		AssignedIP:         assignedIP,
		PrefixLength:       prefix,
		WireGuardPort:      wireGuardPort,
		DiscoveryRelayPort: relayPort,
		HostPublicKey:      challenge.HostPublicKey,
		LeaseRevision:      revision,
	}
	v.MAC = resultMAC(challenge, request, v, authKey)
	return v, nil
}

func (v PairResult) MarshalBinary() ([]byte, error) {
	if !v.AssignedIP.Is4() || v.PrefixLength > 32 || v.WireGuardPort == 0 || v.DiscoveryRelayPort == 0 {
		return nil, ErrProtocol
	}
	b := make([]byte, 82)
	b[0] = v.Status
	ip := v.AssignedIP.As4()
	copy(b[1:5], ip[:])
	b[5] = v.PrefixLength
	binary.BigEndian.PutUint16(b[6:8], v.WireGuardPort)
	binary.BigEndian.PutUint16(b[8:10], v.DiscoveryRelayPort)
	copy(b[10:42], v.HostPublicKey[:])
	binary.BigEndian.PutUint64(b[42:50], v.LeaseRevision)
	copy(b[50:], v.MAC[:])
	return b, nil
}

func ParsePairResult(b []byte) (PairResult, error) {
	if len(b) != 82 || b[0] != StatusOK || b[5] > 32 {
		return PairResult{}, ErrProtocol
	}
	v := PairResult{
		Status:             b[0],
		AssignedIP:         netip.AddrFrom4([4]byte{b[1], b[2], b[3], b[4]}),
		PrefixLength:       b[5],
		WireGuardPort:      binary.BigEndian.Uint16(b[6:8]),
		DiscoveryRelayPort: binary.BigEndian.Uint16(b[8:10]),
		LeaseRevision:      binary.BigEndian.Uint64(b[42:50]),
	}
	if v.WireGuardPort == 0 || v.DiscoveryRelayPort == 0 {
		return PairResult{}, ErrProtocol
	}
	copy(v.HostPublicKey[:], b[10:42])
	copy(v.MAC[:], b[50:])
	return v, nil
}

func VerifyPairResult(challenge ServerChallenge, request ClientRequest, result PairResult, roomKey RoomKey) bool {
	return verifyPairResult(challenge, request, result, roomKey[:])
}

func verifyPairResult(challenge ServerChallenge, request ClientRequest, result PairResult, authKey []byte) bool {
	if result.Status != StatusOK || !result.AssignedIP.Is4() || result.PrefixLength > 32 || result.WireGuardPort == 0 || result.DiscoveryRelayPort == 0 || result.HostPublicKey != challenge.HostPublicKey || !validAuthKey(authKey) {
		return false
	}
	expected := resultMAC(challenge, request, result, authKey)
	return hmac.Equal(expected[:], result.MAC[:])
}

func resultMAC(challenge ServerChallenge, request ClientRequest, result PairResult, authKey []byte) [32]byte {
	challengeBytes := challenge.MarshalBinary()
	requestBytes, _ := request.MarshalBinary()
	transcript := sha256.Sum256(append(challengeBytes, requestBytes...))
	resultBytes := resultWithoutMAC(result)
	h := hmac.New(sha256.New, authKey)
	h.Write(resultDomain)
	h.Write(transcript[:])
	h.Write(resultBytes)
	return sum32(h.Sum(nil))
}

func resultWithoutMAC(v PairResult) []byte {
	b := make([]byte, 50)
	b[0] = v.Status
	if v.AssignedIP.Is4() {
		ip := v.AssignedIP.As4()
		copy(b[1:5], ip[:])
	}
	b[5] = v.PrefixLength
	binary.BigEndian.PutUint16(b[6:8], v.WireGuardPort)
	binary.BigEndian.PutUint16(b[8:10], v.DiscoveryRelayPort)
	copy(b[10:42], v.HostPublicKey[:])
	binary.BigEndian.PutUint64(b[42:50], v.LeaseRevision)
	return b
}

func PairAck(roomKey RoomKey, revision uint64) [32]byte {
	return pairAck(roomKey[:], revision)
}

func pairAck(authKey []byte, revision uint64) [32]byte {
	h := hmac.New(sha256.New, authKey)
	h.Write(ackDomain)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], revision)
	h.Write(b[:])
	return sum32(h.Sum(nil))
}

func VerifyPairAck(roomKey RoomKey, revision uint64, mac [32]byte) bool {
	return verifyPairAck(roomKey[:], revision, mac)
}

func verifyPairAck(authKey []byte, revision uint64, mac [32]byte) bool {
	if !validAuthKey(authKey) {
		return false
	}
	expected := pairAck(authKey, revision)
	return hmac.Equal(expected[:], mac[:])
}

func DerivePSK(roomKey RoomKey, hostPublicKey, clientPublicKey [32]byte) [32]byte {
	h := hmac.New(sha256.New, roomKey[:])
	h.Write(pskDomain)
	h.Write(hostPublicKey[:])
	h.Write(clientPublicKey[:])
	return sum32(h.Sum(nil))
}

func WriteFrame(w io.Writer, frameType byte, payload []byte) error {
	length := 1 + len(payload)
	if !validFrameType(frameType) || length > MaxFrameBytes {
		return ErrProtocol
	}
	var header [5]byte
	binary.BigEndian.PutUint32(header[:4], uint32(length))
	header[4] = frameType
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

func ReadFrame(r io.Reader) (byte, []byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(size[:])
	if length == 0 || length > MaxFrameBytes {
		return 0, nil, ErrProtocol
	}
	b := make([]byte, length)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, nil, err
	}
	if !validFrameType(b[0]) {
		return 0, nil, ErrProtocol
	}
	return b[0], b[1:], nil
}

func validFrameType(value byte) bool {
	return value >= FrameServerChallenge && value <= FrameDeviceSnapshot
}

func ValidFailureStatus(value byte) bool {
	return value >= StatusAuthFailed && value <= StatusMemberDisabled
}

func validName(name string) bool {
	if !utf8.ValidString(name) || len([]byte(name)) > MaxNameBytes {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validAuthKey(value []byte) bool {
	return (len(value) == len(RoomKey{}) || len(value) == sha256.Size) && !isZero(value)
}

func isZero(value []byte) bool {
	var combined byte
	for _, b := range value {
		combined |= b
	}
	return combined == 0
}

func sum32(value []byte) [32]byte {
	var result [32]byte
	copy(result[:], value)
	return result
}

func writeAll(w io.Writer, value []byte) error {
	for len(value) > 0 {
		n, err := w.Write(value)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		value = value[n:]
	}
	return nil
}
