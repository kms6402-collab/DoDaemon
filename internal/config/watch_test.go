package config

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher_ReloadsOnFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dodaemon.yaml")

	cfg := Default()
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	changes := make(chan *Config, 4)
	w := NewWatcher(path, slog.New(slog.NewTextHandler(discardWriter{}, nil)), func(c *Config) {
		changes <- c
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- w.Run(ctx) }()

	// Give the watcher a moment to actually register before we write —
	// this mirrors the same gap that matters in production.
	time.Sleep(150 * time.Millisecond)

	updated := Default()
	updated.TFTP.Enabled = true
	if err := Save(path, updated); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-changes:
		if !got.TFTP.Enabled {
			t.Error("reloaded config did not reflect the on-disk change")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not fire onChange after the file was rewritten")
	}
}

func TestWatcher_IgnoresUnrelatedFilesInSameDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dodaemon.yaml")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}

	changes := make(chan *Config, 4)
	w := NewWatcher(path, slog.New(slog.NewTextHandler(discardWriter{}, nil)), func(c *Config) {
		changes <- c
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("noise"), 0o640); err != nil {
		t.Fatal(err)
	}

	select {
	case <-changes:
		t.Fatal("watcher fired onChange for an unrelated file in the same directory")
	case <-time.After(600 * time.Millisecond):
		// expected: no reload
	}
}

func TestWatcher_InvalidReloadKeepsPreviousConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dodaemon.yaml")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}

	changes := make(chan *Config, 4)
	w := NewWatcher(path, slog.New(slog.NewTextHandler(discardWriter{}, nil)), func(c *Config) {
		changes <- c
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(path, []byte("not: valid: yaml: [["), 0o640); err != nil {
		t.Fatal(err)
	}

	select {
	case <-changes:
		t.Fatal("watcher must not call onChange for a config that fails validation/parsing")
	case <-time.After(600 * time.Millisecond):
		// expected: reload silently skipped, previous config kept by the caller
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
