package ftp

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/spf13/afero"

	"github.com/kms6402/dodaemon/internal/auth"
	"github.com/kms6402/dodaemon/internal/eventbus"
	"github.com/kms6402/dodaemon/internal/security"
)

// progressInterval throttles how often "진행" events are published during a
// transfer, mirroring internal/tftp/session.go's publishProgress so both
// protocols' active-session UI updates at the same cadence.
const progressInterval = 200 * time.Millisecond

// permissionFs wraps an afero.Fs rooted at a user's home directory and
// enforces the user's 3CDaemon-style permission characters on every
// operation, in addition to (not instead of) the base-path confinement
// already provided by afero.NewBasePathFs — the library's own driver is
// not trusted alone, per docs/PLAN.md §5.2/§8.1. It also publishes
// transfer start/complete events (docs/PLAN.md §7's active-session table)
// since ftpserverlib itself never tells the driver about individual
// STOR/RETR operations.
type permissionFs struct {
	inner      afero.Fs
	root       string
	user       auth.User
	bus        *eventbus.Bus
	remoteAddr string
}

func newPermissionFs(root string, user auth.User, bus *eventbus.Bus, remoteAddr string) afero.Fs {
	return &permissionFs{
		inner: afero.NewBasePathFs(afero.NewOsFs(), root), root: root, user: user,
		bus: bus, remoteAddr: remoteAddr,
	}
}

func (f *permissionFs) publish(kind eventbus.Kind, msg string) {
	if f.bus == nil {
		return
	}
	f.bus.Publish(eventbus.Event{Source: "ftp", Kind: kind, RemoteAddr: f.remoteAddr, Message: msg})
}

// track wraps file for progress reporting. total is the known size in
// bytes for a GET (the caller already Stat'd it) or 0 for a PUT, where
// ftpserverlib never tells the driver how many bytes the client intends to
// send — subscribers show a byte counter instead of a percentage when 0.
func (f *permissionFs) track(file afero.File, err error, direction, name string, total int64) (afero.File, error) {
	if err != nil {
		return file, err
	}
	f.publish(eventbus.KindTransfer, fmt.Sprintf("FTP %s 시작: %s", direction, name))
	tf := &trackedFile{File: file, fs: f, direction: direction, name: name, total: total}
	tf.lastProgress.Store(time.Now())
	return tf, nil
}

// trackedFile publishes a "complete" event exactly once when the FTP
// client closes the data connection, however that happens (clean finish,
// ABOR, or a dropped connection — ftpserverlib always calls Close()), and
// throttled "progress" events as bytes flow through Read/Write so the
// active-session UI (web and native) can show live transfer progress.
type trackedFile struct {
	afero.File
	fs        *permissionFs
	direction string
	name      string
	total     int64
	done      int64
	closed    bool

	lastProgress atomic.Value // time.Time
}

func (t *trackedFile) publishProgress() {
	now := time.Now()
	last := t.lastProgress.Load().(time.Time)
	if now.Sub(last) < progressInterval {
		return
	}
	t.lastProgress.Store(now)
	done := atomic.LoadInt64(&t.done)
	t.fs.bus.Publish(eventbus.Event{
		Source: "ftp", Kind: eventbus.KindTransfer, RemoteAddr: t.fs.remoteAddr,
		Message: fmt.Sprintf("FTP %s 진행: %s", t.direction, t.name),
		Fields:  map[string]any{"file": t.name, "bytes_done": done, "bytes_total": t.total, "progress": true},
	})
}

func (t *trackedFile) Read(p []byte) (int, error) {
	n, err := t.File.Read(p)
	if n > 0 {
		atomic.AddInt64(&t.done, int64(n))
		if t.fs.bus != nil {
			t.publishProgress()
		}
	}
	return n, err
}

func (t *trackedFile) Write(p []byte) (int, error) {
	n, err := t.File.Write(p)
	if n > 0 {
		atomic.AddInt64(&t.done, int64(n))
		if t.fs.bus != nil {
			t.publishProgress()
		}
	}
	return n, err
}

func (t *trackedFile) Close() error {
	err := t.File.Close()
	if !t.closed {
		t.closed = true
		t.fs.publish(eventbus.KindTransfer, fmt.Sprintf("FTP %s 완료: %s", t.direction, t.name))
	}
	return err
}

func (f *permissionFs) checkPath(name string) error {
	_, err := security.SafeJoin(f.root, name)
	return err
}

func (f *permissionFs) require(perm byte) error {
	if !f.user.Allows(perm) {
		return os.ErrPermission
	}
	return nil
}

func (f *permissionFs) Name() string { return "dodaemon-permission-fs" }

func (f *permissionFs) Create(name string) (afero.File, error) {
	if err := f.checkPath(name); err != nil {
		return nil, err
	}
	if err := f.require(auth.PermStore); err != nil {
		return nil, err
	}
	file, err := f.inner.Create(name)
	return f.track(file, err, "PUT", name, 0) // upload size unknown until EOF
}

func (f *permissionFs) Mkdir(name string, perm os.FileMode) error {
	if err := f.checkPath(name); err != nil {
		return err
	}
	if err := f.require(auth.PermMakeDir); err != nil {
		return err
	}
	return f.inner.Mkdir(name, perm)
}

func (f *permissionFs) MkdirAll(path string, perm os.FileMode) error {
	if err := f.checkPath(path); err != nil {
		return err
	}
	if err := f.require(auth.PermMakeDir); err != nil {
		return err
	}
	return f.inner.MkdirAll(path, perm)
}

func (f *permissionFs) Open(name string) (afero.File, error) {
	if err := f.checkPath(name); err != nil {
		return nil, err
	}
	isDir := false
	var size int64
	if fi, err := f.inner.Stat(name); err == nil {
		isDir = fi.IsDir()
		size = fi.Size()
	}
	if isDir {
		if err := f.require(auth.PermList); err != nil {
			return nil, err
		}
		return f.inner.Open(name)
	}
	if err := f.require(auth.PermRetrieve); err != nil {
		return nil, err
	}
	file, err := f.inner.Open(name)
	return f.track(file, err, "GET", name, size)
}

func (f *permissionFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if err := f.checkPath(name); err != nil {
		return nil, err
	}
	direction := "GET"
	switch {
	case flag&(os.O_WRONLY|os.O_RDWR) != 0 && flag&os.O_APPEND != 0:
		if err := f.require(auth.PermAppend); err != nil {
			return nil, err
		}
		direction = "PUT"
	case flag&(os.O_WRONLY|os.O_RDWR) != 0:
		if err := f.require(auth.PermStore); err != nil {
			return nil, err
		}
		direction = "PUT"
	default:
		if err := f.require(auth.PermRetrieve); err != nil {
			return nil, err
		}
	}
	var size int64
	if direction == "GET" {
		if fi, err := f.inner.Stat(name); err == nil {
			size = fi.Size()
		}
	}
	file, err := f.inner.OpenFile(name, flag, perm)
	return f.track(file, err, direction, name, size)
}

func (f *permissionFs) Remove(name string) error {
	if err := f.checkPath(name); err != nil {
		return err
	}
	if err := f.require(auth.PermDelete); err != nil {
		return err
	}
	return f.inner.Remove(name)
}

// RemoveDir implements ftpserver.ClientDriverExtensionRemoveDir so RMD is
// distinguished from DELE at the protocol layer, even though both map to
// the same 'd' permission character.
func (f *permissionFs) RemoveDir(name string) error {
	return f.Remove(name)
}

func (f *permissionFs) RemoveAll(path string) error {
	if err := f.checkPath(path); err != nil {
		return err
	}
	if err := f.require(auth.PermDelete); err != nil {
		return err
	}
	return f.inner.RemoveAll(path)
}

func (f *permissionFs) Rename(oldname, newname string) error {
	if err := f.checkPath(oldname); err != nil {
		return err
	}
	if err := f.checkPath(newname); err != nil {
		return err
	}
	if err := f.require(auth.PermRename); err != nil {
		return err
	}
	return f.inner.Rename(oldname, newname)
}

func (f *permissionFs) Stat(name string) (os.FileInfo, error) {
	if err := f.checkPath(name); err != nil {
		return nil, err
	}
	return f.inner.Stat(name)
}

// ReadDir implements ftpserver.ClientDriverExtensionFileList.
func (f *permissionFs) ReadDir(name string) ([]os.FileInfo, error) {
	if err := f.checkPath(name); err != nil {
		return nil, err
	}
	if err := f.require(auth.PermList); err != nil {
		return nil, err
	}
	entries, err := afero.ReadDir(f.inner, name)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (f *permissionFs) Chmod(name string, mode os.FileMode) error {
	if err := f.checkPath(name); err != nil {
		return err
	}
	if err := f.require(auth.PermChmod); err != nil {
		return err
	}
	return f.inner.Chmod(name, mode)
}

func (f *permissionFs) Chtimes(name string, atime, mtime time.Time) error {
	if err := f.checkPath(name); err != nil {
		return err
	}
	if err := f.require(auth.PermModTime); err != nil {
		return err
	}
	return f.inner.Chtimes(name, atime, mtime)
}

func (f *permissionFs) Chown(name string, uid, gid int) error {
	return os.ErrPermission
}
