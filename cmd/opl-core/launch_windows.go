//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
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
	if !windows.GetCurrentProcessToken().IsElevated() {
		verb, _ := windows.UTF16PtrFromString("runas")
		file, _ := windows.UTF16PtrFromString(executable)
		args, _ := windows.UTF16PtrFromString("--daemon --listen=" + address)
		return logPath, windows.ShellExecute(0, verb, file, args, nil, 0)
	}
	command := exec.Command(executable, "--daemon", "--listen", address)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000008, HideWindow: true}
	return logPath, command.Start()
}

func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
