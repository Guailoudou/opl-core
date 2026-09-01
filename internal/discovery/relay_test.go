package discovery

import (
	"net/netip"
	"testing"
	"time"
)

func TestRelayValidatesInjectsAndExcludesSender(t *testing.T) {
	sender := netip.MustParseAddr("10.0.23.2")
	receiver := netip.MustParseAddr("10.0.23.3")
	packet := udpPacket(sender, netip.MustParseAddr("255.255.255.255"), []byte("discover"))
	var injected [][]byte
	var destinations []netip.Addr
	core := newRelayCore(
		func(packet []byte) error { injected = append(injected, append([]byte(nil), packet...)); return nil },
		func() []netip.Addr { return []netip.Addr{sender, receiver} },
		func(ip netip.Addr, _ []byte) error { destinations = append(destinations, ip); return nil },
	)
	now := time.Unix(100, 0)
	frame := Frame{SenderIP: sender, Sequence: 1, Packet: packet}
	if err := core.member(frame, sender, now); err != nil {
		t.Fatal(err)
	}
	if len(injected) != 1 || len(destinations) != 1 || destinations[0] != receiver {
		t.Fatalf("invalid relay result: inject=%d destinations=%v", len(injected), destinations)
	}
	if err := core.member(frame, sender, now); err == nil {
		t.Fatal("duplicate relay accepted")
	}
	if err := core.member(Frame{SenderIP: sender, Sequence: 2, Packet: packet}, receiver, now); err == nil {
		t.Fatal("spoofed sender accepted")
	}
}

func TestRelayHostFansOutAndRateLimits(t *testing.T) {
	host := netip.MustParseAddr("10.0.23.1")
	members := []netip.Addr{netip.MustParseAddr("10.0.23.2"), netip.MustParseAddr("10.0.23.3")}
	packet := udpPacket(host, netip.MustParseAddr("239.1.2.3"), []byte("discover"))
	forwarded := 0
	core := newRelayCore(func([]byte) error { return nil }, func() []netip.Addr { return members }, func(netip.Addr, []byte) error { forwarded++; return nil })
	now := time.Unix(100, 0)
	for sequence := uint64(1); sequence <= memberPacketsPerSecond; sequence++ {
		if err := core.host(Frame{Flags: FlagHostOrigin, SenderIP: host, Sequence: sequence, Packet: packet}, now); err != nil {
			t.Fatalf("packet %d rejected: %v", sequence, err)
		}
	}
	if err := core.host(Frame{Flags: FlagHostOrigin, SenderIP: host, Sequence: 999, Packet: packet}, now); err == nil {
		t.Fatal("rate limit did not reject excess packet")
	}
	if forwarded != memberPacketsPerSecond*len(members) {
		t.Fatalf("unexpected forwards: %d", forwarded)
	}
	stats := core.stats()
	if stats.Accepted != memberPacketsPerSecond || stats.Forwarded != uint64(forwarded) {
		t.Fatalf("unexpected relay stats: %#v", stats)
	}
}
