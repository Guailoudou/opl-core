package discovery

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

const (
	memberPacketsPerSecond = 128
	memberBytesPerSecond   = 256 * 1024
	roomPacketsPerSecond   = 2048
)

type RelayStats struct {
	Accepted  uint64 `json:"accepted"`
	Forwarded uint64 `json:"forwarded"`
	Dropped   uint64 `json:"dropped"`
}

type HostRelay struct {
	connection *net.UDPConn
	core       *relayCore
	local      <-chan Frame
	done       chan struct{}
	once       sync.Once
	workers    sync.WaitGroup
}

func StartHostRelay(port uint16, local <-chan Frame, inject func([]byte) error, members func() []netip.Addr) (*HostRelay, error) {
	if port == 0 || local == nil || inject == nil || members == nil {
		return nil, ErrFrame
	}
	address := netip.AddrPortFrom(netip.MustParseAddr("10.0.23.1"), port)
	connection, err := listenHostRelay(address)
	if err != nil {
		return nil, err
	}
	relay := &HostRelay{connection: connection, local: local, done: make(chan struct{})}
	relay.core = newRelayCore(inject, members, func(ip netip.Addr, payload []byte) error {
		_, err := connection.WriteToUDPAddrPort(payload, netip.AddrPortFrom(ip, port))
		return err
	})
	relay.workers.Add(2)
	go relay.readRemote()
	go relay.readLocal()
	return relay, nil
}

func listenHostRelay(address netip.AddrPort) (*net.UDPConn, error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		connection, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(address))
		if err == nil {
			return connection, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *HostRelay) Stats() RelayStats { return r.core.stats() }

func (r *HostRelay) Close() error {
	var err error
	r.once.Do(func() {
		close(r.done)
		err = r.connection.Close()
	})
	r.workers.Wait()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (r *HostRelay) readRemote() {
	defer r.workers.Done()
	buffer := make([]byte, headerBytes+MaxPacketBytes)
	for {
		count, source, err := r.connection.ReadFromUDPAddrPort(buffer)
		if err != nil {
			return
		}
		frame, err := Decode(buffer[:count])
		if err != nil || r.core.member(frame, source.Addr(), time.Now()) != nil {
			r.core.dropped.Add(1)
		}
	}
}

func (r *HostRelay) readLocal() {
	defer r.workers.Done()
	for {
		select {
		case frame, ok := <-r.local:
			if !ok {
				return
			}
			if r.core.host(frame, time.Now()) != nil {
				r.core.dropped.Add(1)
			}
		case <-r.done:
			return
		}
	}
}

type relayCore struct {
	inject    func([]byte) error
	members   func() []netip.Addr
	send      func(netip.Addr, []byte) error
	replay    *ReplayWindow
	limitMu   sync.Mutex
	memberLim map[netip.Addr]*trafficBucket
	roomLimit trafficBucket
	accepted  atomic.Uint64
	forwarded atomic.Uint64
	dropped   atomic.Uint64
}

func newRelayCore(inject func([]byte) error, members func() []netip.Addr, send func(netip.Addr, []byte) error) *relayCore {
	now := time.Now()
	return &relayCore{
		inject: inject, members: members, send: send,
		replay: NewReplayWindow(1024, 5*time.Second), memberLim: make(map[netip.Addr]*trafficBucket),
		roomLimit: trafficBucket{packets: roomPacketsPerSecond, bytes: 1 << 30, last: now},
	}
}

func (r *relayCore) member(frame Frame, source netip.Addr, now time.Time) error {
	if frame.Flags != 0 || frame.SenderIP != source || !containsIP(r.members(), source) || r.replay.Accept(source, frame.Sequence, now) != nil || !r.allow(source, len(frame.Packet), now) {
		return ErrFrame
	}
	payload, err := Encode(frame)
	if err != nil {
		return err
	}
	if err := r.inject(frame.Packet); err != nil {
		return err
	}
	r.accepted.Add(1)
	r.fanout(frame.SenderIP, payload)
	return nil
}

func (r *relayCore) host(frame Frame, now time.Time) error {
	if frame.Flags != FlagHostOrigin || frame.SenderIP != netip.MustParseAddr("10.0.23.1") || !r.allow(frame.SenderIP, len(frame.Packet), now) {
		return ErrFrame
	}
	payload, err := Encode(frame)
	if err != nil {
		return err
	}
	r.accepted.Add(1)
	r.fanout(netip.Addr{}, payload)
	return nil
}

func (r *relayCore) fanout(sender netip.Addr, payload []byte) {
	for _, member := range r.members() {
		if member == sender || !validSender(member) || member == netip.MustParseAddr("10.0.23.1") {
			continue
		}
		if r.send(member, payload) == nil {
			r.forwarded.Add(1)
		} else {
			r.dropped.Add(1)
		}
	}
}

func (r *relayCore) allow(sender netip.Addr, size int, now time.Time) bool {
	r.limitMu.Lock()
	defer r.limitMu.Unlock()
	member := r.memberLim[sender]
	if member == nil {
		member = &trafficBucket{packets: memberPacketsPerSecond, bytes: memberBytesPerSecond, last: now}
		r.memberLim[sender] = member
	}
	return member.take(memberPacketsPerSecond, memberBytesPerSecond, size, now) && r.roomLimit.take(roomPacketsPerSecond, 1<<30, size, now)
}

func (r *relayCore) stats() RelayStats {
	return RelayStats{Accepted: r.accepted.Load(), Forwarded: r.forwarded.Load(), Dropped: r.dropped.Load()}
}

type trafficBucket struct {
	packets float64
	bytes   float64
	last    time.Time
}

func (b *trafficBucket) take(packetRate, byteRate, size int, now time.Time) bool {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	b.packets = minFloat(float64(packetRate), b.packets+elapsed*float64(packetRate))
	b.bytes = minFloat(float64(byteRate), b.bytes+elapsed*float64(byteRate))
	b.last = now
	if b.packets < 1 || b.bytes < float64(size) {
		return false
	}
	b.packets--
	b.bytes -= float64(size)
	return true
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func containsIP(values []netip.Addr, target netip.Addr) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
