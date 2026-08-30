//go:build windows

package nativeui

import (
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

// enableDarkTitleBar switches the window's title bar to the dark variant
// Windows 10 (20H1+) and 11 use for modern apps, via
// DwmSetWindowAttribute(DWMWA_USE_IMMERSIVE_DARK_MODE). It's a best-effort
// cosmetic touch: on older Windows builds the call simply fails and is
// ignored, leaving the default light title bar.
func enableDarkTitleBar(hwnd win.HWND) {
	const dwmwaUseImmersiveDarkMode = 20
	proc := windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	if proc.Find() != nil {
		return
	}
	value := int32(1)
	proc.Call(
		uintptr(hwnd),
		uintptr(dwmwaUseImmersiveDarkMode),
		uintptr(unsafe.Pointer(&value)),
		unsafe.Sizeof(value),
	)
}
