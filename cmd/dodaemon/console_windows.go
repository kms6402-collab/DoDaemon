//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// showErrorBox displays a native message box — the release build has no
// console at all (-H=windowsgui, see scripts/build.ps1) and no CLI, so
// this is the only way a fatal startup error is ever visible to the user.
func showErrorBox(title, message string) {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	msgPtr, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return
	}
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	const mbIconError = 0x00000010
	proc.Call(0, uintptr(unsafe.Pointer(msgPtr)), uintptr(unsafe.Pointer(titlePtr)), mbIconError)
}
