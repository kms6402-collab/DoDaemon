package syslogsrv

import (
	"bufio"
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"sync"

	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
	"github.com/kms6402/dodaemon/internal/security"
)

// Server listens for syslog messages on UDP and/or TCP(+TLS), parses them
// (RFC 3164/5424, auto-detected — see parse.go) and fans each one out to
// the Store (disk) and the event bus (web UI live tail).
type Server struct {
	cfg config.SyslogConfig
	acl *security.ACL
	bus *eventbus.Bus
	log *slog.Logger

	store *Store

	mu       sync.Mutex
	udpConn  *net.UDPConn
	tcpLis   net.Listener
	tcpConns map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func New(cfg config.SyslogConfig, acl *security.ACL, bus *eventbus.Bus, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	store, err := NewStore(cfg)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, acl: acl, bus: bus, store: store,
		log:      log.With("server", "syslog"),
		tcpConns: make(map[net.Conn]struct{}),
	}, nil
}

func (s *Server) Name() string { return "syslog" }

func (s *Server) Start(ctx context.Context) error {
	if s.cfg.UDPListen != "" {
		if err := s.startUDP(ctx); err != nil {
			return err
		}
	}
	if s.cfg.TCPListen != "" {
		if err := s.startTCP(ctx); err != nil {
			return err
		}
	}

	<-ctx.Done()
	s.wg.Wait()
	return nil
}

func (s *Server) startUDP(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", s.cfg.UDPListen)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.udpConn = conn
	s.mu.Unlock()
	s.log.Info("syslog UDP listening", "addr", s.cfg.UDPListen)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		buf := make([]byte, 65507)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if s.acl != nil && !s.acl.Allowed(remote.IP) {
				continue
			}
			s.handleLine(string(buf[:n]), remote.String())
		}
	}()
	return nil
}

func (s *Server) startTCP(ctx context.Context) error {
	var lis net.Listener
	var err error

	if s.cfg.TLS.Enabled {
		cert, cErr := tls.LoadX509KeyPair(s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
		if cErr != nil {
			return cErr
		}
		lis, err = tls.Listen("tcp", s.cfg.TCPListen, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	} else {
		lis, err = net.Listen("tcp", s.cfg.TCPListen)
	}
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.tcpLis = lis
	s.mu.Unlock()
	s.log.Info("syslog TCP listening", "addr", s.cfg.TCPListen, "tls", s.cfg.TLS.Enabled)

	go func() {
		<-ctx.Done()
		lis.Close()
	}()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := lis.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			remoteAddr := conn.RemoteAddr()
			if tcpAddr, ok := remoteAddr.(*net.TCPAddr); ok && s.acl != nil && !s.acl.Allowed(tcpAddr.IP) {
				conn.Close()
				continue
			}
			s.mu.Lock()
			s.tcpConns[conn] = struct{}{}
			s.mu.Unlock()

			s.wg.Add(1)
			go s.handleTCPConn(ctx, conn)
		}
	}()
	return nil
}

func (s *Server) handleTCPConn(ctx context.Context, conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.tcpConns, conn)
		s.mu.Unlock()
	}()

	remote := conn.RemoteAddr().String()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		s.handleLine(scanner.Text(), remote)
	}
}

func (s *Server) handleLine(line, remoteAddr string) {
	if line == "" {
		return
	}
	msg, err := Parse(line, remoteAddr)
	if err != nil {
		s.log.Warn("failed to parse syslog message", "remote", remoteAddr, "error", err)
		return
	}
	if err := s.store.Write(msg); err != nil {
		s.log.Error("failed to persist syslog message", "error", err)
	}
	if s.bus != nil {
		s.bus.Publish(eventbus.Event{
			Source:     "syslog",
			Kind:       eventbus.KindTransfer,
			RemoteAddr: remoteAddr,
			Message:    msg.Text,
			Fields: map[string]any{
				"facility": msg.FacilityName(),
				"severity": msg.SeverityName(),
				"hostname": msg.Hostname,
				"app_name": msg.AppName,
				"format":   msg.Format,
			},
		})
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.udpConn != nil {
		s.udpConn.Close()
	}
	if s.tcpLis != nil {
		s.tcpLis.Close()
	}
	for c := range s.tcpConns {
		c.Close()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return s.store.Close()
}
