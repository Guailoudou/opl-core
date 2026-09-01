package wg

import (
	"net/netip"
	"strings"
	"testing"
)

func TestUAPIConfigurations(t *testing.T) {
	privateKey, publicKey, psk := key(1), key(2), key(3)
	device, err := deviceConfig(privateKey, 25674)
	if err != nil || !strings.Contains(device, "listen_port=25674") || strings.Contains(device, "OPL2") {
		t.Fatalf("invalid device config: %q, %v", device, err)
	}
	hostPeer, err := peerConfig(Peer{PublicKey: publicKey, PSK: psk, IP: netip.MustParseAddr("10.0.23.2")}, false)
	if err != nil || !strings.Contains(hostPeer, "allowed_ip=10.0.23.2/32") || strings.Contains(hostPeer, "persistent_keepalive") || strings.Contains(hostPeer, "replace_peers") {
		t.Fatalf("invalid host peer: %q, %v", hostPeer, err)
	}
	clientPeer, err := peerConfig(Peer{
		PublicKey: publicKey, PSK: psk, IP: netip.MustParseAddr("10.0.23.1"),
		Endpoint: netip.MustParseAddrPort("127.0.0.1:30000"),
	}, true)
	if err != nil || !strings.Contains(clientPeer, "allowed_ip=10.0.23.0/24") || !strings.Contains(clientPeer, "endpoint=127.0.0.1:30000") || !strings.Contains(clientPeer, "persistent_keepalive_interval=25") || strings.Contains(clientPeer, "replace_peers") {
		t.Fatalf("invalid client peer: %q, %v", clientPeer, err)
	}
	remove, err := removePeerConfig(publicKey)
	if err != nil || !strings.Contains(remove, "remove=true") {
		t.Fatalf("invalid removal: %q, %v", remove, err)
	}
}

func key(value byte) [32]byte {
	var result [32]byte
	result[0] = value
	return result
}
