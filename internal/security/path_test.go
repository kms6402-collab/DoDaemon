package security

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSafeJoin_NormalPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		in   string
		want string
	}{
		{"file.txt", filepath.Join(root, "file.txt")},
		{"sub/file.txt", filepath.Join(root, "sub", "file.txt")},
		{"/file.txt", filepath.Join(root, "file.txt")}, // leading slash treated as root-relative
		{"./file.txt", filepath.Join(root, "file.txt")},
	}
	for _, c := range cases {
		got, err := SafeJoin(root, c.in)
		if err != nil {
			t.Errorf("SafeJoin(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("SafeJoin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSafeJoin_TraversalBlocked(t *testing.T) {
	root := t.TempDir()

	cases := []string{
		"../etc/passwd",
		"../../etc/passwd",
		"sub/../../etc/passwd",
		"..\\..\\windows\\system32",
		"a/b/../../../c",
	}
	for _, in := range cases {
		if _, err := SafeJoin(root, in); err != ErrPathEscapesRoot {
			t.Errorf("SafeJoin(%q) = err %v, want ErrPathEscapesRoot", in, err)
		}
	}
}

func TestSafeJoin_SymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation typically requires elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o640); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := SafeJoin(root, "escape/secret.txt"); err != ErrPathEscapesRoot {
		t.Errorf("SafeJoin through symlink = err %v, want ErrPathEscapesRoot", err)
	}
}

func TestSafeJoin_NonExistentNestedPath(t *testing.T) {
	root := t.TempDir()
	// A WRQ/STOR target for a file that doesn't exist yet must still resolve
	// cleanly within root.
	got, err := SafeJoin(root, "new/deep/upload.bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(root, "new", "deep", "upload.bin")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
