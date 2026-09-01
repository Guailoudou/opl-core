//go:build darwin

package wg

import (
	"fmt"
	"net/netip"
	"os/exec"

	"golang.zx2c4.com/wireguard/tun"
)

func createTUN() (tun.Device, string, error) {
	device, err := tun.CreateTUN("utun", 1420)
	if err != nil {
		return nil, "", err
	}
	name, err := device.Name()
	if err != nil {
		device.Close()
		return nil, "", err
	}
	return device, name, nil
}

func run(command string, args ...string) error {
	if output, err := exec.Command(command, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %w: %s", command, args, err, output)
	}
	return nil
}

func configureInterface(name string, address netip.Addr, _ bool) error {
	if err := run("ifconfig", name, "inet", address.String(), "10.0.23.1", "netmask", "255.255.255.0", "mtu", "1420", "up"); err != nil {
		return err
	}
	if err := run("route", "-n", "add", "-net", "224.0.0.0/4", "-interface", name); err != nil {
		return err
	}
	return run("route", "-n", "add", "-host", "255.255.255.255", "-interface", name)
}

func cleanupInterface(name string, _ netip.Addr) error {
	_ = exec.Command("route", "-n", "delete", "-net", "224.0.0.0/4", "-interface", name).Run()
	_ = exec.Command("route", "-n", "delete", "-host", "255.255.255.255", "-interface", name).Run()
	return nil
}
