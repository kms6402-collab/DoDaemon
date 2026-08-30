//go:build !windows

package webui

func listDrives() []string { return []string{"/"} }
