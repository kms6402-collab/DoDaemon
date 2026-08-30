//go:build !windows

package autostart

func Available() bool                            { return false }
func IsEnabled() bool                            { return false }
func Enable(exePath string) error                { return nil }
func Disable() error                             { return nil }
func SetEnabled(want bool, exePath string) error { return nil }
