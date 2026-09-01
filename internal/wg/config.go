package wg

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

var ErrConfig = errors.New("invalid WireGuard configuration")

type Peer struct {
	PublicKey [32]byte
	PSK       [32]byte
	IP        netip.Addr
	Endpoint  netip.AddrPort
}

type PeerStat struct {
	PublicKey     [32]byte
	LastHandshake time.Time
	RxBytes       uint64
	TxBytes       uint64
}

func deviceConfig(privateKey [32]byte, listenPort uint16) (string, error) {
	if isZero(privateKey[:]) {
		return "", ErrConfig
	}
	return fmt.Sprintf("private_key=%s\nlisten_port=%d\n", hex.EncodeToString(privateKey[:]), listenPort), nil
}

func peerConfig(peer Peer, client bool) (string, error) {
	if isZero(peer.PublicKey[:]) || isZero(peer.PSK[:]) || !validIP(peer.IP, client) {
		return "", ErrConfig
	}
	var value strings.Builder
	fmt.Fprintf(&value, "public_key=%s\npreshared_key=%s\nreplace_allowed_ips=true\n", hex.EncodeToString(peer.PublicKey[:]), hex.EncodeToString(peer.PSK[:]))
	if client {
		value.WriteString("allowed_ip=10.0.23.0/24\n")
		if !peer.Endpoint.IsValid() || peer.Endpoint.Port() == 0 {
			return "", ErrConfig
		}
		fmt.Fprintf(&value, "endpoint=%s\n", peer.Endpoint)
		value.WriteString("persistent_keepalive_interval=25\n")
	} else {
		fmt.Fprintf(&value, "allowed_ip=%s/32\n", peer.IP)
	}
	return value.String(), nil
}

func removePeerConfig(publicKey [32]byte) (string, error) {
	if isZero(publicKey[:]) {
		return "", ErrConfig
	}
	return fmt.Sprintf("public_key=%s\nremove=true\n", hex.EncodeToString(publicKey[:])), nil
}

func validIP(ip netip.Addr, client bool) bool {
	if !ip.Is4() {
		return false
	}
	b := ip.As4()
	if client {
		return b == [4]byte{10, 0, 23, 1}
	}
	return b[0] == 10 && b[1] == 0 && b[2] == 23 && b[3] >= 2 && b[3] <= 254
}

func isZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}
