//go:build !windows

package main

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func startDaemon(address string) (string, error) {
	logPath, err := daemonLogPath()
	if err != nil {
		return "", err
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	command := exec.Command(executable, "--daemon", "--listen", address)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return logPath, command.Start()
}

func openBrowser(url string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	return exec.Command(command, url).Start()
}
