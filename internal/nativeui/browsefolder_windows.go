//go:build windows

package nativeui

import (
	"syscall"
	"unsafe"

	"github.com/lxn/win"
)

// Win32's SHBrowseForFolder message/flag constants that lxn/win doesn't
// expose itself.
const (
	bffmInitialized     = 1
	bffmSelChanged      = 2
	bffmSetSelectionW   = win.WM_USER + 103
	bffmEnableOK        = win.WM_USER + 101
	bifNewDialogStyle   = 0x00000040
	bifReturnOnlyFSDirs = 0x00000001
)

// browseInitialPath is read once by browseFolderCallback on BFFM_INITIALIZED
// and is only ever set immediately before, and cleared immediately after,
// a single browseForFolder call — there's no concurrent use since dialogs
// are always modal to the one native window this app has.
var browseInitialPath *uint16

// browseFolderCallback pre-navigates the dialog to browseInitialPath once
// it opens, and mirrors Explorer's own OK-button-disable-on-non-folder
// behavior on selection change (the same thing lxn/win's own
// FileDialog.ShowBrowseFolder does).
func browseFolderCallback(hwnd win.HWND, msg uint32, lp, lpData uintptr) uintptr {
	switch msg {
	case bffmInitialized:
		if browseInitialPath != nil {
			win.SendMessage(hwnd, bffmSetSelectionW, 1, uintptr(unsafe.Pointer(browseInitialPath)))
		}
	case bffmSelChanged:
		var path [win.MAX_PATH]uint16
		var enabled uintptr
		if win.SHGetPathFromIDList(lp, &path[0]) {
			enabled = 1
		}
		win.SendMessage(hwnd, bffmEnableOK, 0, enabled)
	}
	return 0
}

var browseFolderCallbackPtr = syscall.NewCallback(browseFolderCallback)

// browseForFolder shows an unrestricted "폴더 찾기" dialog — every drive
// stays reachable — that still opens pre-navigated to startPath.
//
// lxn/walk's own FileDialog.ShowBrowseFolder can't do both at once: it
// feeds InitialDirPath into BROWSEINFO.PidlRoot, which Windows treats as a
// hard boundary you cannot browse above (SHBrowseForFolder's documented
// behavior — pidlRoot fixes the root of the whole tree). Passing our
// current TFTP/FTP/Syslog directory there silently trapped the picker on
// whichever drive that directory happened to be on, e.g. always D:\ if
// the app itself lives on D:\ — this is the fix: PidlRoot is deliberately
// left zero (Desktop/이 PC, unrestricted) and BFFM_SETSELECTION is used
// instead to start the view near startPath.
func browseForFolder(owner win.HWND, title, startPath string) (string, bool) {
	if hr := win.OleInitialize(); hr != win.S_OK && hr != win.S_FALSE {
		return "", false
	}
	defer win.OleUninitialize()

	if startPath != "" {
		if u16, err := syscall.UTF16PtrFromString(startPath); err == nil {
			browseInitialPath = u16
		}
	}
	defer func() { browseInitialPath = nil }()

	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return "", false
	}

	bi := win.BROWSEINFO{
		HwndOwner: owner,
		LpszTitle: titlePtr,
		UlFlags:   bifNewDialogStyle | bifReturnOnlyFSDirs,
		Lpfn:      browseFolderCallbackPtr,
	}

	pidl := win.SHBrowseForFolder(&bi)
	if pidl == 0 {
		return "", false
	}
	defer win.CoTaskMemFree(pidl)

	var buf [win.MAX_PATH]uint16
	if !win.SHGetPathFromIDList(pidl, &buf[0]) {
		return "", false
	}
	return syscall.UTF16ToString(buf[:]), true
}
