package tftp

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"

	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
)

// Server is a TFTP listener; one instance owns the main :69 socket and
// spawns a dedicated per-client socket for each RRQ/WRQ session.
//
// Deliberately no IP allowlist here (docs/PLAN.md §8.2): TFTP has no
// authentication of its own, so gating it by source IP would be the only
// access control it has, and the user asked for TFTP to stay open rather
// than gain that extra configuration surface — path confinement
// (internal/security.SafeJoin) and the read/write toggles below are TFTP's
// actual safety net.
type Server struct {
	cfg config.TFTPConfig
	log *slog.Logger
	bus *eventbus.Bus

	mu     sync.Mutex
	conn   *net.UDPConn
	wg     sync.WaitGroup
	active map[string]struct{} // remote "ip:port" of sessions in progress
}

func New(cfg config.TFTPConfig, bus *eventbus.Bus, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, bus: bus, log: log.With("server", "tftp")}
}

func (s *Server) Name() string { return "tftp" }

func (s *Server) Start(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", s.cfg.Listen)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	s.log.Info("tftp server listening", "addr", s.cfg.Listen, "root", s.cfg.RootDir, "allow_write", s.cfg.AllowWrite)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 65507)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			// Shutdown closes the socket before the supervisor cancels ctx;
			// without the ErrClosed check this loop spun at 100% CPU on the
			// dead socket for as long as in-flight sessions took to finish.
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			continue
		}

		req, err := parseRequest(buf[:n])
		if err != nil {
			s.log.Warn("malformed request", "remote", remote.String(), "error", err)
			continue
		}

		// A client that hasn't heard back yet (e.g. our OACK is still in
		// flight) retransmits its RRQ/WRQ from the same port. Starting a
		// second session for it would send the file twice in parallel — or,
		// for WRQ, have two sessions truncate and write the same file — and
		// show the transfer twice in the UI. The in-progress session already
		// owns that client, so the repeat is dropped.
		key := remote.String()
		s.mu.Lock()
		if s.active == nil {
			s.active = make(map[string]struct{})
		}
		if _, busy := s.active[key]; busy {
			s.mu.Unlock()
			continue
		}
		s.active[key] = struct{}{}
		s.mu.Unlock()

		reqCopy := *req
		remoteCopy := *remote
		s.wg.Add(1)
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.active, key)
				s.mu.Unlock()
				s.wg.Done()
			}()
			s.handleSession(ctx, &reqCopy, &remoteCopy)
		}()
	}

	s.wg.Wait()
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

func (s *Server) publish(kind eventbus.Kind, remote, msg string, fields map[string]any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(eventbus.Event{Source: "tftp", Kind: kind, RemoteAddr: remote, Message: msg, Fields: fields})
}
