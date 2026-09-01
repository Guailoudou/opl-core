package discovery

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestFrameRoundTripForDiscoveryDestinations(t *testing.T) {
	sender := netip.MustParseAddr("10.0.23.2")
	for _, destination := range []netip.Addr{
		netip.MustParseAddr("255.255.255.255"),
		netip.MustParseAddr("10.0.23.255"),
		netip.MustParseAddr("224.0.2.60"),
	} {
		original := Frame{SenderIP: sender, Sequence: 42, Packet: udpPacket(sender, destination, []byte("discover"))}
		encoded, err := Encode(original)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Flags != original.Flags || decoded.SenderIP != sender || decoded.Sequence != 42 || string(decoded.Packet) != string(original.Packet) {
			t.Fatalf("frame mismatch: %#v", decoded)
		}
	}
}

func TestFrameRejectsSpoofingUnicastAndOversize(t *testing.T) {
	sender := netip.MustParseAddr("10.0.23.2")
	other := netip.MustParseAddr("10.0.23.3")
	broadcast := netip.MustParseAddr("255.255.255.255")
	for _, frame := range []Frame{
		{SenderIP: sender, Packet: udpPacket(other, broadcast, nil)},
		{SenderIP: sender, Packet: udpPacket(sender, netip.MustParseAddr("10.0.23.1"), nil)},
		{SenderIP: sender, Packet: make([]byte, MaxPacketBytes+1)},
	} {
		if _, err := Encode(frame); !errors.Is(err, ErrFrame) {
			t.Fatalf("expected rejection, got %v", err)
		}
	}
}

func TestReplayWindow(t *testing.T) {
	w := NewReplayWindow(2, time.Second)
	sender := netip.MustParseAddr("10.0.23.2")
	now := time.Unix(100, 0)
	if err := w.Accept(sender, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := w.Accept(sender, 1, now); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected duplicate, got %v", err)
	}
	if err := w.Accept(sender, 1, now.Add(2*time.Second)); err != nil {
		t.Fatalf("expired sequence was not accepted: %v", err)
	}
}

func udpPacket(source, destination netip.Addr, payload []byte) []byte {
	src, dst := source.As4(), destination.As4()
	b := make([]byte, 28+len(payload))
	b[0] = 0x45
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	b[8] = 1
	b[9] = 17
	copy(b[12:16], src[:])
	copy(b[16:20], dst[:])
	binary.BigEndian.PutUint16(b[20:22], 4445)
	binary.BigEndian.PutUint16(b[22:24], 4445)
	binary.BigEndian.PutUint16(b[24:26], uint16(8+len(payload)))
	copy(b[28:], payload)
	return b
}
