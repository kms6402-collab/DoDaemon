package nativeui

import "errors"

// ErrUnsupported is returned by New on platforms other than Windows — the
// native window is built on lxn/walk, a Win32-only toolkit (docs/PLAN.md's
// Linux/macOS targets stay CLI/web-UI only for now). It's never returned
// on Windows, but the symbol must exist unconditionally so cross-platform
// callers (cmd/dodaemon) can reference it regardless of GOOS.
var ErrUnsupported = errors.New("nativeui: native window is only supported on Windows")
