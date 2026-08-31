// Package sftp adds SFTP (SSH File Transfer Protocol) access on top of the
// same FTP account list, permissions and IP allowlist internal/ftp already
// manages — it's presented in both UIs as "SFTP support for FTP", not a
// fifth independent protocol server, so there is deliberately no separate
// user store here: internal/auth.Store (built from config.FTPConfig.Users)
// is reused as-is.
package sftp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/ssh"

	"github.com/kms6402/dodaemon/internal/auth"
	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
	"github.com/kms6402/dodaemon/internal/security"
)

// Server is an SFTP-over-SSH listener. One instance owns the listening
// socket; each accepted TCP connection gets its own SSH handshake and,
// once authenticated, its own sftp.RequestServer scoped to that user's
// home directory.
type Server struct {
	cfg     config.FTPConfig
	dataDir string
	acl     *security.ACL
	bus     *eventbus.Bus
	log     *slog.Logger

	mu       sync.Mutex
	listener net.Listener
	wg       sync.WaitGroup

	shuttingDown atomic.Bool
}

// New builds a Server. dataDir is where the generated host key is
// persisted (<dataDir>/sftp_host_ed25519), so its fingerprint stays
// stable across restarts instead of changing (and alarming every client)
// every time the process starts. acl may be nil, meaning "allow everyone"
// — SFTP reuses FTP's own IP allowlist field, so the caller passes the
// same *security.ACL it built from cfg.IPAllowlist for the FTP server.
func New(cfg config.FTPConfig, dataDir string, acl *security.ACL, bus *eventbus.Bus, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, dataDir: dataDir, acl: acl, bus: bus, log: log.With("server", "sftp")}
}

func (s *Server) Name() string { return "sftp" }

func (s *Server) Start(ctx context.Context) error {
	signer, err := s.loadOrCreateHostKey()
	if err != nil {
		return fmt.Errorf("sftp: host key: %w", err)
	}

	store := auth.NewStore(toAuthUsers(s.cfg.Users))

	sshCfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			u, err := store.Authenticate(conn.User(), string(password))
			if err != nil {
				s.publish(eventbus.KindAuth, conn.RemoteAddr().String(), fmt.Sprintf("SFTP 인증 실패: %s", conn.User()), nil)
				return nil, err
			}
			s.publish(eventbus.KindAuth, conn.RemoteAddr().String(), fmt.Sprintf("SFTP 인증 성공: %s", conn.User()), nil)
			return &ssh.Permissions{Extensions: map[string]string{"username": u.Username}}, nil
		},
	}
	sshCfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", s.cfg.SFTPListen)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	s.log.Info("sftp server listening", "addr", s.cfg.SFTPListen)

	go func() {
		<-ctx.Done()
		s.shuttingDown.Store(true)
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || s.shuttingDown.Load() {
				break
			}
			continue
		}

		if s.acl != nil {
			host, _, splitErr := net.SplitHostPort(conn.RemoteAddr().String())
			if splitErr != nil {
				host = conn.RemoteAddr().String()
			}
			if ip := net.ParseIP(host); ip != nil && !s.acl.Allowed(ip) {
				s.publish(eventbus.KindError, conn.RemoteAddr().String(), "SFTP 연결 거부 (허용되지 않은 IP)", nil)
				conn.Close()
				continue
			}
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn, sshCfg, store)
		}()
	}

	s.wg.Wait()
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.shuttingDown.Store(true)
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
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
	s.bus.Publish(eventbus.Event{Source: "sftp", Kind: kind, RemoteAddr: remote, Message: msg, Fields: fields})
}

func toAuthUsers(users []config.FTPUser) []auth.User {
	out := make([]auth.User, len(users))
	for i, u := range users {
		out[i] = auth.User{Username: u.Username, PasswordHash: u.PasswordHash, HomeDir: u.HomeDir, Permissions: u.Permissions}
	}
	return out
}

// loadOrCreateHostKey reads the persisted Ed25519 host key from
// <dataDir>/sftp_host_ed25519, generating and saving a new one on first
// use. File permissions are set to owner-only where the OS supports it;
// on Windows this is advisory only (see docs/PLAN.md's threat model note
// about NTFS ACLs being out of scope for a first pass).
func (s *Server) loadOrCreateHostKey() (ssh.Signer, error) {
	path := filepath.Join(s.dataDir, "sftp_host_ed25519")

	if data, err := os.ReadFile(path); err == nil {
		signer, err := ssh.ParsePrivateKey(data)
		if err == nil {
			return signer, nil
		}
		s.log.Warn("existing sftp host key unreadable, regenerating", "path", path, "error", err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	if err := os.MkdirAll(s.dataDir, 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, err
	}

	return ssh.NewSignerFromKey(priv)
}
