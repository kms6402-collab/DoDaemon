//go:build windows

// Package autostart manages DoDaemon's entry in the current user's Windows
// "Run at login" registry key, so it can start automatically on a regular
// PC or a Windows Server without any separate installer step — this is the
// same mechanism most consumer Windows apps (and Task Manager's Startup
// Apps list) use, requiring no admin rights, unlike registering a true
// Windows Service (which cmd/dodaemon intentionally doesn't do anymore —
// docs/PLAN.md's GUI-only direction).
package autostart

import "golang.org/x/sys/windows/registry"

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName  = "DoDaemon"
)

// Available reports whether this platform supports autostart at all (the
// UI uses this to hide the option outside Windows).
func Available() bool { return true }

// IsEnabled reports whether DoDaemon is currently registered to start at
// login.
func IsEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(valueName)
	return err == nil
}

// Enable registers exePath to run automatically when the current user logs
// in.
func Enable(exePath string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(valueName, `"`+exePath+`"`)
}

// Disable removes the login-start registration, if any.
func Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(valueName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// SetEnabled is a convenience wrapper switching on want.
func SetEnabled(want bool, exePath string) error {
	if want {
		return Enable(exePath)
	}
	return Disable()
}
