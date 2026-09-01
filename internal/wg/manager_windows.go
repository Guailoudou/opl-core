//go:build windows

package wg

import (
	"fmt"
	"net/netip"
	"os/exec"

	"golang.zx2c4.com/wireguard/tun"
)

const interfaceName = "OPL"

func createTUN() (tun.Device, string, error) {
	device, err := tun.CreateTUN(interfaceName, 1420)
	return device, interfaceName, err
}

func configureInterface(name string, address netip.Addr, host bool) error {
	args := []string{"interface", "ipv4", "set", "address", "name=" + name, "source=static", "address=" + address.String(), "mask=255.255.255.0", "gateway=none", "store=active"}
	if output, err := exec.Command("netsh", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("netsh %v: %w: %s", args, err, output)
	}
	if host {
		args = []string{"interface", "ipv4", "set", "interface", "interface=" + name, "forwarding=enabled", "store=active"}
		if output, err := exec.Command("netsh", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("netsh %v: %w: %s", args, err, output)
		}
	}
	for _, prefix := range []string{"224.0.0.0/4", "255.255.255.255/32"} {
		if err := ensureRoute(prefix, name); err != nil {
			return err
		}
	}
	return nil
}

func ensureRoute(prefix, name string) error {
	base := []string{"route", prefix, name, "metric=5", "store=active"}
	if _, err := exec.Command("netsh", append([]string{"interface", "ipv4", "set"}, base...)...).CombinedOutput(); err == nil {
		return nil
	}
	args := append([]string{"interface", "ipv4", "add"}, base...)
	if output, err := exec.Command("netsh", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("netsh %v: %w: %s", args, err, output)
	}
	return nil
}

func cleanupInterface(name string, address netip.Addr) error {
	var result error
	if address == netip.MustParseAddr("10.0.23.1") {
		args := []string{"interface", "ipv4", "set", "interface", "interface=" + name, "forwarding=disabled", "store=active"}
		if output, err := exec.Command("netsh", args...).CombinedOutput(); err != nil {
			result = fmt.Errorf("netsh %v: %w: %s", args, err, output)
		}
	}
	for _, args := range [][]string{{"interface", "ipv4", "delete", "route", "224.0.0.0/4", name, "store=active"}, {"interface", "ipv4", "delete", "route", "255.255.255.255/32", name, "store=active"}} {
		if output, err := exec.Command("netsh", args...).CombinedOutput(); err != nil && result == nil {
			result = fmt.Errorf("netsh %v: %w: %s", args, err, output)
		}
	}
	return result
}
