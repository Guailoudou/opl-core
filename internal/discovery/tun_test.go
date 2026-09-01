package discovery

import (
	"net/netip"
	"os"
	"testing"

	"golang.zx2c4.com/wireguard/tun"
)

func TestDiscoveryTunClientWrapsAndUnwraps(t *testing.T) {
	base := newFakeTun()
	clientIP := netip.MustParseAddr("10.0.23.2")
	wrapped, err := Wrap(base, RoleClient, clientIP, 25675, nil)
	if err != nil {
		t.Fatal(err)
	}
	original := udpPacket(clientIP, netip.MustParseAddr("255.255.255.255"), []byte("discover"))
	base.reads <- original
	buffer := make([]byte, 2048)
	sizes := make([]int, 1)
	count, err := wrapped.Read([][]byte{buffer}, sizes, 0)
	if err != nil || count != 1 {
		t.Fatalf("read failed: %d, %v", count, err)
	}
	payload, ok := relayPayload(buffer[:sizes[0]], clientIP, netip.MustParseAddr("10.0.23.1"), 25675)
	frame, decodeErr := Decode(payload)
	if !ok || decodeErr != nil || string(frame.Packet) != string(original) {
		t.Fatal("discovery packet was not wrapped")
	}

	relay, _ := Encode(Frame{SenderIP: clientIP, Sequence: 9, Packet: original})
	incoming := buildIPv4UDP(netip.MustParseAddr("10.0.23.1"), clientIP, 25675, 25675, relay)
	if _, err := wrapped.Write([][]byte{incoming}, 0); err != nil {
		t.Fatal(err)
	}
	if got := <-base.writes; string(got) != string(original) {
		t.Fatal("relay packet was not unwrapped")
	}
}

func TestDiscoveryTunHostCapturesAndInjects(t *testing.T) {
	base := newFakeTun()
	hostIP := netip.MustParseAddr("10.0.23.1")
	captured := make(chan Frame, 1)
	wrapped, _ := Wrap(base, RoleHost, hostIP, 25675, func(frame Frame) { captured <- frame })
	discovery := udpPacket(hostIP, netip.MustParseAddr("239.1.2.3"), []byte("discover"))
	unicast := udpPacket(hostIP, netip.MustParseAddr("10.0.23.2"), []byte("normal"))
	base.reads <- discovery
	base.reads <- unicast
	buffer := make([]byte, 2048)
	sizes := make([]int, 1)
	count, err := wrapped.Read([][]byte{buffer}, sizes, 0)
	if err != nil || count != 1 || string(buffer[:sizes[0]]) != string(unicast) {
		t.Fatal("host did not suppress only discovery traffic")
	}
	if got := <-captured; got.Flags != FlagHostOrigin || string(got.Packet) != string(discovery) {
		t.Fatal("host capture mismatch")
	}
	if err := wrapped.InjectInbound(discovery); err != nil {
		t.Fatal(err)
	}
	if got := <-base.writes; string(got) != string(discovery) {
		t.Fatal("host injection mismatch")
	}
}

type fakeTun struct {
	reads  chan []byte
	writes chan []byte
	events chan tun.Event
}

func newFakeTun() *fakeTun {
	return &fakeTun{reads: make(chan []byte, 4), writes: make(chan []byte, 4), events: make(chan tun.Event)}
}

func (f *fakeTun) File() *os.File           { return nil }
func (f *fakeTun) MTU() (int, error)        { return 1420, nil }
func (f *fakeTun) Name() (string, error)    { return "fake", nil }
func (f *fakeTun) Events() <-chan tun.Event { return f.events }
func (f *fakeTun) BatchSize() int           { return 1 }
func (f *fakeTun) Close() error             { close(f.events); return nil }
func (f *fakeTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	packet, ok := <-f.reads
	if !ok {
		return 0, os.ErrClosed
	}
	copy(bufs[0][offset:], packet)
	sizes[0] = len(packet)
	return 1, nil
}
func (f *fakeTun) Write(bufs [][]byte, offset int) (int, error) {
	for _, packet := range bufs {
		f.writes <- append([]byte(nil), packet[offset:]...)
	}
	return len(bufs), nil
}

var _ tun.Device = (*fakeTun)(nil)
