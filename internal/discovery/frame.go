package discovery

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"sync"
	"time"
)

const (
	Version        byte = 1
	FlagHostOrigin byte = 1
	MaxPacketBytes      = 1200
	headerBytes         = 20
)

var (
	magic        = [4]byte{'O', 'D', 'R', '2'}
	ErrFrame     = errors.New("invalid discovery relay frame")
	ErrDuplicate = errors.New("duplicate discovery relay frame")
)

type Frame struct {
	Flags    byte
	SenderIP netip.Addr
	Sequence uint64
	Packet   []byte
}

func Encode(v Frame) ([]byte, error) {
	if v.Flags&^FlagHostOrigin != 0 || !validPacket(v.SenderIP, v.Packet) {
		return nil, ErrFrame
	}
	sender := v.SenderIP.As4()
	b := make([]byte, headerBytes+len(v.Packet))
	copy(b[:4], magic[:])
	b[4] = Version
	b[5] = v.Flags
	copy(b[6:10], sender[:])
	binary.BigEndian.PutUint64(b[10:18], v.Sequence)
	binary.BigEndian.PutUint16(b[18:20], uint16(len(v.Packet)))
	copy(b[20:], v.Packet)
	return b, nil
}

func Decode(b []byte) (Frame, error) {
	if len(b) < headerBytes || [4]byte{b[0], b[1], b[2], b[3]} != magic || b[4] != Version || b[5]&^FlagHostOrigin != 0 {
		return Frame{}, ErrFrame
	}
	packetLength := int(binary.BigEndian.Uint16(b[18:20]))
	if packetLength == 0 || packetLength > MaxPacketBytes || len(b) != headerBytes+packetLength {
		return Frame{}, ErrFrame
	}
	v := Frame{
		Flags:    b[5],
		SenderIP: netip.AddrFrom4([4]byte{b[6], b[7], b[8], b[9]}),
		Sequence: binary.BigEndian.Uint64(b[10:18]),
		Packet:   append([]byte(nil), b[20:]...),
	}
	if !validPacket(v.SenderIP, v.Packet) {
		return Frame{}, ErrFrame
	}
	return v, nil
}

func validPacket(sender netip.Addr, packet []byte) bool {
	if !validSender(sender) || len(packet) < 28 || len(packet) > MaxPacketBytes || packet[0]>>4 != 4 {
		return false
	}
	headerLength := int(packet[0]&0x0f) * 4
	if headerLength < 20 || headerLength+8 > len(packet) || int(binary.BigEndian.Uint16(packet[2:4])) != len(packet) || packet[9] != 17 {
		return false
	}
	// Reject fragmented discovery packets; the OPL interface MTU keeps supported packets whole.
	if binary.BigEndian.Uint16(packet[6:8])&0x3fff != 0 {
		return false
	}
	source := netip.AddrFrom4([4]byte{packet[12], packet[13], packet[14], packet[15]})
	destination := netip.AddrFrom4([4]byte{packet[16], packet[17], packet[18], packet[19]})
	if source != sender || !validDestination(destination) {
		return false
	}
	udpLength := int(binary.BigEndian.Uint16(packet[headerLength+4 : headerLength+6]))
	return udpLength == len(packet)-headerLength
}

func validSender(ip netip.Addr) bool {
	if !ip.Is4() {
		return false
	}
	v := ip.As4()
	return v[0] == 10 && v[1] == 0 && v[2] == 23 && v[3] >= 1 && v[3] <= 254
}

func validDestination(ip netip.Addr) bool {
	if !ip.Is4() {
		return false
	}
	v := ip.As4()
	return v == [4]byte{255, 255, 255, 255} || v == [4]byte{10, 0, 23, 255} || v[0] >= 224 && v[0] <= 239
}

type replayKey struct {
	Sender   [4]byte
	Sequence uint64
}

type ReplayWindow struct {
	mu      sync.Mutex
	entries map[replayKey]time.Time
	max     int
	ttl     time.Duration
}

func NewReplayWindow(max int, ttl time.Duration) *ReplayWindow {
	if max < 1 {
		max = 1024
	}
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &ReplayWindow{entries: make(map[replayKey]time.Time), max: max, ttl: ttl}
}

func (w *ReplayWindow) Accept(sender netip.Addr, sequence uint64, now time.Time) error {
	if !validSender(sender) {
		return ErrFrame
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := now.Add(-w.ttl)
	for key, seen := range w.entries {
		if seen.Before(cutoff) {
			delete(w.entries, key)
		}
	}
	key := replayKey{Sender: sender.As4(), Sequence: sequence}
	if _, exists := w.entries[key]; exists {
		return ErrDuplicate
	}
	if len(w.entries) >= w.max {
		// ponytail: O(n) eviction is bounded at 1024 entries; use a heap only if profiling says this matters.
		var oldestKey replayKey
		var oldest time.Time
		for candidate, seen := range w.entries {
			if oldest.IsZero() || seen.Before(oldest) {
				oldestKey, oldest = candidate, seen
			}
		}
		delete(w.entries, oldestKey)
	}
	w.entries[key] = now
	return nil
}
