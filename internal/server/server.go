// Package server defines the common lifecycle contract shared by the FTP,
// TFTP, Syslog and WebUI servers, plus a supervisor that starts/stops them
// together, isolates crashes so one server going down doesn't take down the
// rest, and lets a running server be swapped out for a freshly configured
// instance without restarting the whole process (docs/PLAN.md §4.3/§4.4).
package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Server is implemented by each protocol server (FTP/TFTP/Syslog/WebUI).
type Server interface {
	// Name returns a short identifier used in logs ("ftp", "tftp", "syslog").
	Name() string
	// Start blocks until ctx is cancelled or an unrecoverable error occurs.
	// Implementations must stop listening and return once ctx is done —
	// callers other than Supervisor rely on this alone for cleanup, so
	// Start must not depend on Shutdown also being called.
	Start(ctx context.Context) error
	// Shutdown performs a graceful stop, waiting for in-flight work to
	// finish until ctx's deadline is reached. It must be safe to call even
	// after (or concurrently with) ctx passed to Start being cancelled.
	Shutdown(ctx context.Context) error
}

const restartShutdownTimeout = 10 * time.Second

// Supervisor runs a set of Servers concurrently, restarting any that exit
// with an error using exponential backoff, and lets a caller (typically a
// config.Watcher callback) hot-swap a named server for a new instance built
// from reloaded configuration.
type Supervisor struct {
	log *slog.Logger

	mu        sync.Mutex
	entries   map[string]*entry
	order     []string // preserves Add order for readability, not used for correctness
	parentCtx context.Context
	running   bool
}

type entry struct {
	mu          sync.Mutex
	srv         Server
	cancel      context.CancelFunc // cancels the in-flight attempt's context
	attemptDone chan struct{}      // closed when the in-flight Start() call returns
}

// New creates an empty Supervisor.
func New(log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{log: log, entries: make(map[string]*entry)}
}

// Add registers a server under its Name(). If the Supervisor is already
// running (Run has been called), the server is started immediately;
// otherwise it's queued and started when Run is called. Adding a second
// server under a name already in use panics — use Replace for that.
func (s *Supervisor) Add(srv Server) {
	name := srv.Name()

	s.mu.Lock()
	if _, exists := s.entries[name]; exists {
		s.mu.Unlock()
		panic(fmt.Sprintf("server: %q already registered; use Replace to swap it", name))
	}
	e := &entry{srv: srv}
	s.entries[name] = e
	s.order = append(s.order, name)
	running := s.running
	parentCtx := s.parentCtx
	s.mu.Unlock()

	if running {
		go s.runEntry(parentCtx, name, e)
	}
}

// Replace atomically stops the currently running server registered under
// srv.Name() (gracefully, then forcibly cancelling its context) and starts
// srv in its place — used for config changes that require rebinding a
// listener (address, TLS material) rather than the live in-place updates
// some servers support for lighter changes (ACL, users). If no server is
// registered under that name yet, Replace behaves like Add.
func (s *Supervisor) Replace(srv Server) {
	name := srv.Name()

	s.mu.Lock()
	e, exists := s.entries[name]
	if !exists {
		s.mu.Unlock()
		s.Add(srv)
		return
	}
	running := s.running
	s.mu.Unlock()

	if !running {
		// Not started yet: just swap the pending server, nothing to stop.
		e.mu.Lock()
		e.srv = srv
		e.mu.Unlock()
		return
	}

	// Swap e.srv to the new instance BEFORE tearing down the old one: the
	// entry's runEntry goroutine re-reads e.srv (under e.mu) the moment the
	// old Start() call returns, so the write here must happen-before that
	// cancellation is even requested, or runEntry could race ahead and
	// restart the OLD server instead of picking up srv.
	e.mu.Lock()
	oldSrv := e.srv
	cancel := e.cancel
	done := e.attemptDone
	e.srv = srv
	e.mu.Unlock()

	if cancel == nil {
		return // old attempt hadn't started yet; runEntry will start srv directly
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), restartShutdownTimeout)
	defer cancelShutdown()
	if err := oldSrv.Shutdown(shutdownCtx); err != nil {
		s.log.Error("graceful shutdown failed during replace, forcing stop", "server", name, "error", err)
	}
	cancel()
	if done != nil {
		<-done
	}
}

// Remove stops and unregisters a server (e.g. a protocol disabled via a
// config hot-reload). It's a no-op if name isn't registered.
func (s *Supervisor) Remove(name string) {
	s.mu.Lock()
	e, exists := s.entries[name]
	if exists {
		delete(s.entries, name)
	}
	s.mu.Unlock()
	if !exists {
		return
	}
	s.stopCurrentAttempt(e, name)
}

// stopCurrentAttempt gracefully shuts down the entry's live server
// (respecting restartShutdownTimeout), then cancels its attempt context as
// a backstop, and waits for the in-flight Start() call to return.
func (s *Supervisor) stopCurrentAttempt(e *entry, name string) {
	e.mu.Lock()
	srv := e.srv
	cancel := e.cancel
	done := e.attemptDone
	e.mu.Unlock()

	if cancel == nil {
		return // never actually started an attempt yet
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), restartShutdownTimeout)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.log.Error("graceful shutdown failed, forcing stop", "server", name, "error", err)
	}
	cancel()

	if done != nil {
		<-done
	}
}

// Run starts every registered server and blocks until ctx is cancelled, at
// which point it gracefully shuts every currently registered server down.
func (s *Supervisor) Run(ctx context.Context, shutdownTimeout time.Duration) error {
	s.mu.Lock()
	s.parentCtx = ctx
	s.running = true
	entries := make(map[string]*entry, len(s.entries))
	for name, e := range s.entries {
		entries[name] = e
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for name, e := range entries {
		wg.Add(1)
		go func(name string, e *entry) {
			defer wg.Done()
			s.runEntry(ctx, name, e)
		}(name, e)
	}

	<-ctx.Done()
	s.log.Info("shutdown signal received, stopping servers", "timeout", shutdownTimeout)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	var shutdownWG sync.WaitGroup
	s.mu.Lock()
	current := make(map[string]*entry, len(s.entries))
	for name, e := range s.entries {
		current[name] = e
	}
	s.running = false
	s.mu.Unlock()

	for name, e := range current {
		e.mu.Lock()
		srv := e.srv
		e.mu.Unlock()
		shutdownWG.Add(1)
		go func(name string, srv Server) {
			defer shutdownWG.Done()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				s.log.Error("server shutdown error", "server", name, "error", err)
			}
		}(name, srv)
	}
	shutdownWG.Wait()
	wg.Wait()
	return nil
}

// runEntry drives one entry for the Supervisor's whole lifetime: it starts
// e.srv, and on return decides whether that was the final process shutdown
// (parentCtx done: return), a crash (Start returned a non-nil error while
// parentCtx is still live: back off and retry the same e.srv), or a
// deliberate restart (Start returned nil because only this attempt's own
// context was cancelled, by Replace/Remove: loop immediately onto whatever
// e.srv currently is, which Replace may have already swapped).
func (s *Supervisor) runEntry(parentCtx context.Context, name string, e *entry) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if parentCtx.Err() != nil {
			return
		}

		e.mu.Lock()
		srv := e.srv
		attemptCtx, cancel := context.WithCancel(parentCtx)
		done := make(chan struct{})
		e.cancel = cancel
		e.attemptDone = done
		e.mu.Unlock()

		s.log.Info("starting server", "server", name)
		err := srv.Start(attemptCtx)
		cancel()
		close(done)

		if parentCtx.Err() != nil {
			return
		}
		if err == nil {
			// Either a deliberate restart (attemptCtx was cancelled by
			// Replace/Remove) or the server stopped itself; either way,
			// loop right back — Remove will have deleted this entry from
			// the map so a removed server simply won't be re-Added, but
			// its own runEntry goroutine loops once more harmlessly before
			// noticing nothing references it; guard via a registration
			// check to exit cleanly instead.
			s.mu.Lock()
			_, stillRegistered := s.entries[name]
			s.mu.Unlock()
			if !stillRegistered {
				return
			}
			continue
		}

		s.log.Error("server crashed, restarting", "server", name, "error", err, "backoff", backoff)

		select {
		case <-time.After(backoff):
		case <-parentCtx.Done():
			return
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Err wraps an error with the server name for consistent log messages.
func Err(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", name, err)
}
