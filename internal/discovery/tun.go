package discovery

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

type Role byte

const (
	RoleClient Role = iota
	RoleHost
)

type DiscoveryTun struct {
	tun.Device
	role        Role
	localIP     netip.Addr
	relayPort   uint16
	onDiscovery func(Frame)
	sequence    atomic.Uint64
	replay      *ReplayWindow
	writeMu     sync.Mutex
}

func Wrap(device tun.Device, role Role, localIP netip.Addr, relayPort uint16, onDiscovery func(Frame)) (*DiscoveryTun, error) {
	if device == nil || !validSender(localIP) || relayPort == 0 || (role == RoleHost && onDiscovery == nil) {
		return nil, ErrFrame
	}
	return &DiscoveryTun{Device: device, role: role, localIP: localIP, relayPort: relayPort, onDiscovery: onDiscovery, replay: NewReplayWindow(1024, 5*time.Second)}, nil
}

func (d *DiscoveryTun) Read(buffers [][]byte, sizes []int, offset int) (int, error) {
	for {
		count, err := d.Device.Read(buffers, sizes, offset)
		kept := 0
		for i := 0; i < count; i++ {
			packet := buffers[i][offset : offset+sizes[i]]
			frame := Frame{SenderIP: d.localIP, Sequence: d.sequence.Add(1), Packet: packet}
			if d.role == RoleHost {
				frame.Flags = FlagHostOrigin
			}
			if _, encodeErr := Encode(frame); encodeErr != nil {
				kept = keepPacket(buffers, sizes, offset, kept, packet)
				continue
			}
			if d.role == RoleHost {
				d.onDiscovery(copyFrame(frame))
				continue
			}
			payload, _ := Encode(frame)
			relayPacket := buildIPv4UDP(d.localIP, netip.MustParseAddr("10.0.23.1"), d.relayPort, d.relayPort, payload)
			kept = keepPacket(buffers, sizes, offset, kept, relayPacket)
		}
		if kept > 0 || err != nil {
			return kept, err
		}
	}
}

func (d *DiscoveryTun) Write(buffers [][]byte, offset int) (int, error) {
	if d.role == RoleHost {
		d.writeMu.Lock()
		defer d.writeMu.Unlock()
		return d.Device.Write(buffers, offset)
	}
	filtered := make([][]byte, 0, len(buffers))
	for _, buffer := range buffers {
		packet := buffer[offset:]
		payload, ok := relayPayload(packet, netip.MustParseAddr("10.0.23.1"), d.localIP, d.relayPort)
		if !ok {
			filtered = append(filtered, buffer)
			continue
		}
		frame, err := Decode(payload)
		if err != nil || d.replay.Accept(frame.SenderIP, frame.Sequence, time.Now()) != nil {
			continue
		}
		injected := make([]byte, offset+len(frame.Packet))
		copy(injected[offset:], frame.Packet)
		filtered = append(filtered, injected)
	}
	if len(filtered) == 0 {
		return len(buffers), nil
	}
	d.writeMu.Lock()
	_, err := d.Device.Write(filtered, offset)
	d.writeMu.Unlock()
	return len(buffers), err
}

func (d *DiscoveryTun) InjectInbound(packet []byte) error {
	sender, ok := packetSource(packet)
	if d.role != RoleHost || !ok || !validPacket(sender, packet) {
		return ErrFrame
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.Device.Write([][]byte{packet}, 0)
	return err
}

func keepPacket(buffers [][]byte, sizes []int, offset, index int, packet []byte) int {
	if offset+len(packet) > len(buffers[index]) {
		return index
	}
	copy(buffers[index][offset:], packet)
	sizes[index] = len(packet)
	return index + 1
}

func copyFrame(value Frame) Frame {
	value.Packet = append([]byte(nil), value.Packet...)
	return value
}

func buildIPv4UDP(source, destination netip.Addr, sourcePort, destinationPort uint16, payload []byte) []byte {
	packet := make([]byte, 28+len(payload))
	packet[0], packet[8], packet[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	src, dst := source.As4(), destination.As4()
	copy(packet[12:16], src[:])
	copy(packet[16:20], dst[:])
	binary.BigEndian.PutUint16(packet[10:12], ipv4Checksum(packet[:20]))
	binary.BigEndian.PutUint16(packet[20:22], sourcePort)
	binary.BigEndian.PutUint16(packet[22:24], destinationPort)
	binary.BigEndian.PutUint16(packet[24:26], uint16(8+len(payload)))
	copy(packet[28:], payload)
	return packet
}

func relayPayload(packet []byte, source, destination netip.Addr, port uint16) ([]byte, bool) {
	if len(packet) < 28 || packet[0]>>4 != 4 || packet[9] != 17 || binary.BigEndian.Uint16(packet[6:8])&0x3fff != 0 {
		return nil, false
	}
	headerLength := int(packet[0]&0x0f) * 4
	if headerLength < 20 || headerLength+8 > len(packet) || int(binary.BigEndian.Uint16(packet[2:4])) != len(packet) {
		return nil, false
	}
	src := netip.AddrFrom4([4]byte{packet[12], packet[13], packet[14], packet[15]})
	dst := netip.AddrFrom4([4]byte{packet[16], packet[17], packet[18], packet[19]})
	udp := packet[headerLength:]
	if src != source || dst != destination || binary.BigEndian.Uint16(udp[2:4]) != port || int(binary.BigEndian.Uint16(udp[4:6])) != len(udp) {
		return nil, false
	}
	return udp[8:], true
}

func ipv4Checksum(header []byte) uint16 {
	var sum uint32
	for i := 0; i < len(header); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[i : i+2]))
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

func packetSource(packet []byte) (netip.Addr, bool) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{packet[12], packet[13], packet[14], packet[15]}), true
}

var _ tun.Device = (*DiscoveryTun)(nil)
