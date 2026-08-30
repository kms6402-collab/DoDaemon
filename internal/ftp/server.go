package ftp

import (
	"context"
	"log/slog"

	ftpserver "github.com/fclairamb/ftpserverlib"

	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
	"github.com/kms6402/dodaemon/internal/security"
)

// Server adapts ftpserverlib.FtpServer to our internal/server.Server
// lifecycle contract.
type Server struct {
	drv *driver
	srv *ftpserver.FtpServer
	log *slog.Logger
}

func New(cfg config.FTPConfig, acl *security.ACL, bus *eventbus.Bus, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("server", "ftp")

	drv, err := newDriver(cfg, acl, bus, log)
	if err != nil {
		return nil, err
	}
	return &Server{drv: drv, srv: ftpserver.NewFtpServer(drv), log: log}, nil
}

func (s *Server) Name() string { return "ftp" }

// UpdateConfig applies a hot-reloaded configuration. The listen address and
// TLS material take effect only on the next restart of this server (which
// the supervisor performs when config.Watcher signals a change requiring
// it); user/permission/ACL changes apply immediately to new connections.
func (s *Server) UpdateConfig(cfg config.FTPConfig, acl *security.ACL) {
	s.drv.updateConfig(cfg, acl)
}

func (s *Server) Start(ctx context.Context) error {
	if err := s.srv.Listen(); err != nil {
		return err
	}
	s.log.Info("ftp server listening", "addr", s.srv.Addr())

	go func() {
		<-ctx.Done()
		_ = s.srv.Stop()
	}()

	return s.srv.Serve()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Stop()
}
