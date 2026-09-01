//go:build windows

package main

import (
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	trayMessage      = 0x0401
	wmDestroy        = 0x0002
	wmClose          = 0x0010
	wmNull           = 0x0000
	wmRButtonUp      = 0x0205
	wmLButtonDblClk  = 0x0203
	nimAdd           = 0x00000000
	nimDelete        = 0x00000002
	nifMessage       = 0x00000001
	nifIcon          = 0x00000002
	nifTip           = 0x00000004
	mfString         = 0x00000000
	mfSeparator      = 0x00000800
	tpmRightButton   = 0x0002
	tpmBottomAlign   = 0x0020
	tpmReturnCommand = 0x0100
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	shell32               = windows.NewLazySystemDLL("shell32.dll")
	kernel32Tray          = windows.NewLazySystemDLL("kernel32.dll")
	registerClassExW      = user32.NewProc("RegisterClassExW")
	createWindowExW       = user32.NewProc("CreateWindowExW")
	defWindowProcW        = user32.NewProc("DefWindowProcW")
	loadIconW             = user32.NewProc("LoadIconW")
	createPopupMenu       = user32.NewProc("CreatePopupMenu")
	appendMenuW           = user32.NewProc("AppendMenuW")
	trackPopupMenu        = user32.NewProc("TrackPopupMenu")
	destroyMenu           = user32.NewProc("DestroyMenu")
	getCursorPos          = user32.NewProc("GetCursorPos")
	setForegroundWindow   = user32.NewProc("SetForegroundWindow")
	postMessageW          = user32.NewProc("PostMessageW")
	getMessageW           = user32.NewProc("GetMessageW")
	translateMessage      = user32.NewProc("TranslateMessage")
	dispatchMessageW      = user32.NewProc("DispatchMessageW")
	postQuitMessage       = user32.NewProc("PostQuitMessage")
	getModuleHandleW      = kernel32Tray.NewProc("GetModuleHandleW")
	shellNotifyIconW      = shell32.NewProc("Shell_NotifyIconW")
	trayWindowProcPointer = syscall.NewCallback(trayWindowProc)
	trayOpen, trayExit    func()
	trayWindow            uintptr
)

type trayPoint struct{ x, y int32 }
type trayMessageData struct {
	hwnd           uintptr
	message        uint32
	wParam, lParam uintptr
	time           uint32
	point          trayPoint
	private        uint32
}
type trayWindowClass struct {
	size, style             uint32
	windowProc              uintptr
	classExtra, windowExtra int32
	instance, icon, cursor  uintptr
	background              uintptr
	menuName, className     *uint16
	smallIcon               uintptr
}
type trayIconData struct {
	size                uint32
	hwnd                uintptr
	id, flags, callback uint32
	icon                uintptr
	tip                 [128]uint16
	state, stateMask    uint32
	info                [256]uint16
	timeoutOrVersion    uint32
	infoTitle           [64]uint16
	infoFlags           uint32
	guid                [16]byte
	balloonIcon         uintptr
}

func startTray(open, exit func()) func() {
	ready := make(chan struct{})
	trayOpen, trayExit = open, exit
	go trayLoop(ready)
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
	}
	return func() {
		if trayWindow != 0 {
			postMessageW.Call(trayWindow, wmClose, 0, 0)
		}
	}
}

func trayLoop(ready chan<- struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	instance, _, _ := getModuleHandleW.Call(0)
	className, _ := windows.UTF16PtrFromString("OPLCoreTrayWindow")
	class := trayWindowClass{size: uint32(unsafe.Sizeof(trayWindowClass{})), windowProc: trayWindowProcPointer, instance: instance, className: className}
	if result, _, _ := registerClassExW.Call(uintptr(unsafe.Pointer(&class))); result == 0 {
		close(ready)
		return
	}
	trayWindow, _, _ = createWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if trayWindow == 0 {
		close(ready)
		return
	}
	icon, _, _ := loadIconW.Call(0, 32512)
	notify := trayIconData{size: uint32(unsafe.Sizeof(trayIconData{})), hwnd: trayWindow, id: 1, flags: nifMessage | nifIcon | nifTip, callback: trayMessage, icon: icon}
	tip, _ := windows.UTF16FromString("OPL Core")
	copy(notify.tip[:], tip)
	shellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&notify)))
	close(ready)
	var message trayMessageData
	for {
		result, _, _ := getMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) <= 0 {
			break
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&message)))
		dispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func trayWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case trayMessage:
		switch uint32(lParam) {
		case wmLButtonDblClk:
			if trayOpen != nil {
				go trayOpen()
			}
		case wmRButtonUp:
			showTrayMenu(hwnd)
		}
		return 0
	case wmDestroy:
		notify := trayIconData{size: uint32(unsafe.Sizeof(trayIconData{})), hwnd: hwnd, id: 1}
		shellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&notify)))
		trayWindow = 0
		postQuitMessage.Call(0)
		return 0
	}
	result, _, _ := defWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		return
	}
	defer destroyMenu.Call(menu)
	openText, _ := windows.UTF16PtrFromString("打开 Web 管理")
	exitText, _ := windows.UTF16PtrFromString("退出 Core")
	appendMenuW.Call(menu, mfString, 1, uintptr(unsafe.Pointer(openText)))
	appendMenuW.Call(menu, mfSeparator, 0, 0)
	appendMenuW.Call(menu, mfString, 2, uintptr(unsafe.Pointer(exitText)))
	var point trayPoint
	getCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	setForegroundWindow.Call(hwnd)
	command, _, _ := trackPopupMenu.Call(menu, tpmRightButton|tpmBottomAlign|tpmReturnCommand, uintptr(point.x), uintptr(point.y), 0, hwnd, 0)
	postMessageW.Call(hwnd, wmNull, 0, 0)
	if command == 1 && trayOpen != nil {
		go trayOpen()
	}
	if command == 2 && trayExit != nil {
		go trayExit()
		postMessageW.Call(hwnd, wmClose, 0, 0)
	}
}
