//go:build !windows

package main

func startTray(open, exit func()) func() { return func() {} }
