package server

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// fakeServer is a minimal Server whose Start/Shutdown behavior is scripted
// per test via startFn, and which records how many times each was called.
type fakeServer struct {
	name       string
	starts     atomic.Int32
	shutdowns  atomic.Int32
	startFn    func(ctx context.Context) error
	shutdownFn func(ctx context.Context) error
}

func (f *fakeServer) Name() string { return f.name }

func (f *fakeServer) Start(ctx context.Context) error {
	f.starts.Add(1)
	if f.startFn != nil {
		return f.startFn(ctx)
	}
	<-ctx.Done()
	return nil
}

func (f *fakeServer) Shutdown(ctx context.Context) error {
	f.shutdowns.Add(1)
	if f.shutdownFn != nil {
		return f.shutdownFn(ctx)
	}
	return nil
}

// blockUntilDone is the default/typical Start behavior: block until the
// context is cancelled, then return nil (mirrors every real server's
// self-draining contract).
func blockUntilDone(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestSupervisor_StartAndGracefulShutdown(t *testing.T) {
	sup := New(testLogger())
	f := &fakeServer{name: "tftp", startFn: blockUntilDone}
	sup.Add(f)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := sup.Run(ctx, time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if f.starts.Load() != 1 {
		t.Errorf("starts = %d, want 1", f.starts.Load())
	}
	if f.shutdowns.Load() != 1 {
		t.Errorf("shutdowns = %d, want 1", f.shutdowns.Load())
	}
}

func TestSupervisor_CrashIsRestarted(t *testing.T) {
	sup := New(testLogger())

	var callCount atomic.Int32
	f := &fakeServer{
		name: "syslog",
		startFn: func(ctx context.Context) error {
			n := callCount.Add(1)
			if n == 1 {
				return errors.New("boom")
			}
			<-ctx.Done()
			return nil
		},
	}
	sup.Add(f)

	// The retry backoff starts at 1s, so give it enough headroom to crash
	// once and come back up.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	if err := sup.Run(ctx, time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := f.starts.Load(); got < 2 {
		t.Errorf("starts = %d, want at least 2 (initial crash + restart)", got)
	}
}

func TestSupervisor_Replace(t *testing.T) {
	sup := New(testLogger())
	oldSrv := &fakeServer{name: "ftp", startFn: blockUntilDone}
	sup.Add(oldSrv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		sup.Run(ctx, time.Second)
		close(runDone)
	}()

	// Give the initial Start a moment to actually begin running before we
	// replace it.
	waitFor(t, func() bool { return oldSrv.starts.Load() == 1 })

	newSrv := &fakeServer{name: "ftp", startFn: blockUntilDone}
	sup.Replace(newSrv)

	waitFor(t, func() bool { return newSrv.starts.Load() == 1 })
	if oldSrv.shutdowns.Load() != 1 {
		t.Errorf("old server shutdowns = %d, want 1 (Replace must gracefully stop it)", oldSrv.shutdowns.Load())
	}

	cancel()
	<-runDone

	if newSrv.shutdowns.Load() != 1 {
		t.Errorf("new server shutdowns = %d, want 1 (final process shutdown must still reach the replacement)", newSrv.shutdowns.Load())
	}
	if oldSrv.starts.Load() != 1 {
		t.Errorf("old server starts = %d, want 1 (must not be restarted after being replaced)", oldSrv.starts.Load())
	}
}

func TestSupervisor_Remove(t *testing.T) {
	sup := New(testLogger())
	f := &fakeServer{name: "webui", startFn: blockUntilDone}
	sup.Add(f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		sup.Run(ctx, time.Second)
		close(runDone)
	}()

	waitFor(t, func() bool { return f.starts.Load() == 1 })

	sup.Remove("webui")
	waitFor(t, func() bool { return f.shutdowns.Load() == 1 })

	cancel()
	<-runDone

	// Removed servers must not be touched again by the final shutdown pass.
	if f.shutdowns.Load() != 1 {
		t.Errorf("shutdowns = %d, want exactly 1", f.shutdowns.Load())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
