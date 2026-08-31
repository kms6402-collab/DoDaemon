//go:build windows

package nativeui

import (
	_ "embed"
	"unsafe"

	"github.com/lxn/win"
)

// JetBrains Mono (SIL Open Font License 1.1) — embedded directly in the
// binary and registered as a private, process-only font via
// AddFontMemResourceEx, so every Font{Family: fontFamily} in this package
// resolves correctly even on a machine that never installed the font.
//
//go:embed fonts/JetBrainsMono-Regular.ttf
var jetBrainsMonoRegular []byte

//go:embed fonts/JetBrainsMono-Bold.ttf
var jetBrainsMonoBold []byte

// fontFamily is the name every Font{} literal in this package uses.
const fontFamily = "JetBrains Mono"

// loadEmbeddedFonts registers the embedded JetBrains Mono weights with
// GDI for the lifetime of this process only (no system-wide install, no
// registry changes, nothing left behind on exit) — must run once before
// building the first window, since walk resolves Font{Family: ...} to an
// actual GDI font at widget-creation time.
func loadEmbeddedFonts() {
	addFontFromMemory(jetBrainsMonoRegular)
	addFontFromMemory(jetBrainsMonoBold)
}

func addFontFromMemory(data []byte) {
	if len(data) == 0 {
		return
	}
	var numFonts uint32
	win.AddFontMemResourceEx(uintptr(unsafe.Pointer(&data[0])), uint32(len(data)), nil, &numFonts)
}
