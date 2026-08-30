//go:build windows

package webui

import "golang.org/x/sys/windows"

// listDrives returns the letter of every mounted drive (e.g. "C:\\") for
// the folder-browse modal's quick-jump row.
func listDrives() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var drives []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 {
			drives = append(drives, string(rune('A'+i))+":\\")
		}
	}
	return drives
}
