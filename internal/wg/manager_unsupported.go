//go:build !windows && !linux && !darwin

package wg

import (
	"errors"
	"net/netip"

	"golang.zx2c4.com/wireguard/tun"
)

var ErrUnsupported = errors.New("WireGuard platform backend requires a VPN TUN supplied by the host application")

func createTUN() (tun.Device, string, error)            { return nil, "", ErrUnsupported }
func configureInterface(string, netip.Addr, bool) error { return ErrUnsupported }
func cleanupInterface(string, netip.Addr) error         { return nil }
