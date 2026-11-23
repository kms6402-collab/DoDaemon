package ftp

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kms6402/dodaemon/internal/auth"
	"github.com/kms6402/dodaemon/internal/eventbus"
)

// Start, progress and complete events of one transfer must all carry the
// same "file" field: the UIs key active-transfer rows on it, and a start
// event without it used to show every FTP transfer twice.
func TestTransferEventsCarryFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	bus := eventbus.New()
	ch, unsub := bus.Subscribe(64)
	defer unsub()

	fs := newPermissionFs(root, auth.User{Username: "u", Permissions: "elr"}, bus, "10.0.0.1:5000")
	f, err := fs.OpenFile("/a.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, f)
	f.Close()

	timeout := time.After(time.Second)
	var n int
	for n < 2 {
		select {
		case ev := <-ch:
			if ev.Fields["progress"] == true {
				continue
			}
			if ev.Fields["file"] != "/a.bin" {
				t.Fatalf("event %q has file=%v", ev.Message, ev.Fields["file"])
			}
			n++
		case <-timeout:
			t.Fatalf("got %d start/complete events, want 2", n)
		}
	}
}
