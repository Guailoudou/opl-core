//go:build linux && !android

package wg

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"

	"golang.zx2c4.com/wireguard/tun"
)

const interfaceName = "opl"

func createTUN() (tun.Device, string, error) {
	device, err := tun.CreateTUN(interfaceName, 1420)
	return device, interfaceName, err
}

func run(command string, args ...string) error {
	if output, err := exec.Command(command, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %w: %s", command, args, err, output)
	}
	return nil
}

func configureInterface(name string, address netip.Addr, host bool) error {
	if err := run("ip", "link", "set", "dev", name, "mtu", "1420", "up"); err != nil {
		return err
	}
	if err := run("ip", "addr", "replace", address.String()+"/24", "dev", name); err != nil {
		return err
	}
	if host {
		_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
	}
	if err := run("ip", "route", "replace", "224.0.0.0/4", "dev", name, "metric", "5"); err != nil {
		return err
	}
	return run("ip", "route", "replace", "255.255.255.255/32", "dev", name, "metric", "5")
}

func cleanupInterface(name string, _ netip.Addr) error {
	_ = exec.Command("ip", "route", "del", "224.0.0.0/4", "dev", name).Run()
	_ = exec.Command("ip", "route", "del", "255.255.255.255/32", "dev", name).Run()
	return nil
}
