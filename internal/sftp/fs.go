package sftp

import (
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"

	"github.com/kms6402/dodaemon/internal/auth"
	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
	"github.com/kms6402/dodaemon/internal/security"
)

// jailedFS is one SFTP session's view of the filesystem: every path is
// confined to the authenticated user's HomeDir via security.SafeJoin (the
// same helper internal/ftp/permfs.go uses) and every operation is gated
// by the same "elradfmwMT" permission characters FTP already checks —
// SFTP is deliberately just another door into the identical account/
// permission model, not a separate one.
type jailedFS struct {
	srv    *Server
	remote string
	user   auth.User
}

func (s *Server) newHandlers(remote string, u config.FTPUser) sftp.Handlers {
	fs := &jailedFS{
		srv:    s,
		remote: remote,
		user:   auth.User{Username: u.Username, PasswordHash: u.PasswordHash, HomeDir: u.HomeDir, Permissions: u.Permissions},
	}
	return sftp.Handlers{FileGet: fs, FilePut: fs, FileCmd: fs, FileList: fs}
}

func (fs *jailedFS) resolve(p string) (string, error) {
	return security.SafeJoin(fs.user.HomeDir, p)
}

func (fs *jailedFS) require(perm byte) error {
	if !fs.user.Allows(perm) {
		return os.ErrPermission
	}
	return nil
}

func (fs *jailedFS) publish(msg string, fields map[string]any) {
	fs.srv.publish(eventbus.KindTransfer, fs.remote, msg, fields)
}

// Fileread implements sftp.FileReader (SFTP Get).
func (fs *jailedFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	if err := fs.require(auth.PermRetrieve); err != nil {
		return nil, err
	}
	real, err := fs.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	var total int64
	if fi, err := f.Stat(); err == nil {
		total = fi.Size()
	}
	fs.publish(fmt.Sprintf("SFTP GET 시작: %s", r.Filepath), map[string]any{"file": r.Filepath})
	return &trackedReaderAt{File: f, fs: fs, name: r.Filepath, total: total, direction: "GET"}, nil
}

// Filewrite implements sftp.FileWriter (SFTP Put).
func (fs *jailedFS) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	flags := r.Pflags()
	perm := byte(auth.PermStore)
	if flags.Append {
		perm = auth.PermAppend
	}
	if err := fs.require(perm); err != nil {
		return nil, err
	}
	real, err := fs.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}

	osFlags := os.O_WRONLY | os.O_CREATE
	switch {
	case flags.Append:
		osFlags |= os.O_APPEND
	case flags.Trunc:
		osFlags |= os.O_TRUNC
	}
	if flags.Excl {
		osFlags |= os.O_EXCL
	}
	f, err := os.OpenFile(real, osFlags, 0o644)
	if err != nil {
		return nil, err
	}
	fs.publish(fmt.Sprintf("SFTP PUT 시작: %s", r.Filepath), map[string]any{"file": r.Filepath})
	return &trackedReaderAt{File: f, fs: fs, name: r.Filepath, direction: "PUT"}, nil
}

// Filecmd implements sftp.FileCmder (Setstat/Rename/Rmdir/Mkdir/Remove;
// Link/Symlink are deliberately unsupported — a symlink planted by an SFTP
// client is exactly the kind of jailbreak security.SafeJoin exists to
// prevent, so allowing SFTP to create one at all is safest to just refuse).
func (fs *jailedFS) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		if err := fs.require(auth.PermChmod); err != nil {
			return err
		}
		real, err := fs.resolve(r.Filepath)
		if err != nil {
			return err
		}
		if r.AttrFlags().Size {
			return os.Truncate(real, int64(r.Attributes().Size))
		}
		return nil

	case "Rename":
		if err := fs.require(auth.PermRename); err != nil {
			return err
		}
		oldReal, err := fs.resolve(r.Filepath)
		if err != nil {
			return err
		}
		newReal, err := fs.resolve(r.Target)
		if err != nil {
			return err
		}
		return os.Rename(oldReal, newReal)

	case "Rmdir", "Remove":
		if err := fs.require(auth.PermDelete); err != nil {
			return err
		}
		real, err := fs.resolve(r.Filepath)
		if err != nil {
			return err
		}
		return os.Remove(real)

	case "Mkdir":
		if err := fs.require(auth.PermMakeDir); err != nil {
			return err
		}
		real, err := fs.resolve(r.Filepath)
		if err != nil {
			return err
		}
		return os.Mkdir(real, 0o755)

	default:
		return fmt.Errorf("sftp: unsupported operation %q", r.Method)
	}
}

// Filelist implements sftp.FileLister (List/Stat).
func (fs *jailedFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	if err := fs.require(auth.PermList); err != nil {
		return nil, err
	}
	real, err := fs.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}

	switch r.Method {
	case "List":
		entries, err := os.ReadDir(real)
		if err != nil {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(entries))
		for _, e := range entries {
			if fi, err := e.Info(); err == nil {
				infos = append(infos, fi)
			}
		}
		return listerAt(infos), nil

	case "Stat":
		fi, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		return listerAt([]os.FileInfo{fi}), nil

	default:
		return nil, fmt.Errorf("sftp: unsupported list operation %q", r.Method)
	}
}

// listerAt implements sftp.ListerAt over a plain slice — the standard
// pattern from pkg/sftp's own reference handler (request-example.go).
type listerAt []os.FileInfo

func (l listerAt) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(dst, l[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

// trackedReaderAt wraps an *os.File as the WriterAtReaderAt pkg/sftp wants,
// publishing throttled transfer-progress events the same way
// internal/ftp/permfs.go's trackedFile and internal/tftp/session.go's
// publishProgress do, so both dashboards' 진행 중인 전송 table shows real
// SFTP progress too — not a separate, unfinished code path.
type trackedReaderAt struct {
	*os.File
	fs        *jailedFS
	name      string
	total     int64 // 0 = unknown (uploads: SFTP doesn't declare size up front)
	direction string

	done         int64
	lastProgress atomic.Value // time.Time
	closed       atomic.Bool
}

const progressInterval = 200 * time.Millisecond

func (t *trackedReaderAt) touch(n int) {
	if n <= 0 {
		return
	}
	atomic.AddInt64(&t.done, int64(n))

	now := time.Now()
	last, _ := t.lastProgress.Load().(time.Time)
	if now.Sub(last) < progressInterval {
		return
	}
	t.lastProgress.Store(now)
	t.fs.publish(fmt.Sprintf("SFTP %s 진행: %s", t.direction, t.name), map[string]any{
		"file": t.name, "bytes_done": atomic.LoadInt64(&t.done), "bytes_total": t.total, "progress": true,
	})
}

func (t *trackedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := t.File.ReadAt(p, off)
	t.touch(n)
	return n, err
}

func (t *trackedReaderAt) WriteAt(p []byte, off int64) (int, error) {
	n, err := t.File.WriteAt(p, off)
	t.touch(n)
	return n, err
}

func (t *trackedReaderAt) Close() error {
	err := t.File.Close()
	if t.closed.CompareAndSwap(false, true) {
		t.fs.publish(fmt.Sprintf("SFTP %s 완료: %s", t.direction, t.name), map[string]any{"file": t.name})
	}
	return err
}
