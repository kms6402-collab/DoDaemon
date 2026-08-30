// Package ftp implements the FTP server by wrapping github.com/fclairamb/ftpserverlib
// with our own auth, permission-checked filesystem, ACL and event publishing
// (docs/PLAN.md §2.1/§5.2).
package ftp

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"

	"github.com/kms6402/dodaemon/internal/auth"
	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
	"github.com/kms6402/dodaemon/internal/security"
)

const anonymousUser = "anonymous"

type driver struct {
	cfg   atomic.Pointer[config.FTPConfig]
	store atomic.Pointer[auth.Store]
	acl   atomic.Pointer[security.ACL]
	bus   *eventbus.Bus
	log   *slog.Logger

	tlsConfig atomic.Pointer[tls.Config]
}

func newDriver(cfg config.FTPConfig, acl *security.ACL, bus *eventbus.Bus, log *slog.Logger) (*driver, error) {
	d := &driver{bus: bus, log: log}
	d.updateConfig(cfg, acl)

	if cfg.TLS.Enabled {
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("ftp: load TLS keypair: %w", err)
		}
		d.tlsConfig.Store(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	return d, nil
}

// updateConfig atomically swaps the live config, ACL and derived user store
// so a config hot-reload never blocks an in-flight session.
func (d *driver) updateConfig(cfg config.FTPConfig, acl *security.ACL) {
	users := make([]auth.User, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		users = append(users, auth.User{
			Username:     u.Username,
			PasswordHash: u.PasswordHash,
			HomeDir:      u.HomeDir,
			Permissions:  u.Permissions,
		})
	}
	d.cfg.Store(&cfg)
	d.store.Store(auth.NewStore(users))
	d.acl.Store(acl)
}

func (d *driver) GetSettings() (*ftpserver.Settings, error) {
	cfg := *d.cfg.Load()
	settings := &ftpserver.Settings{
		ListenAddr: cfg.Listen,
		Banner:     "DoDaemon FTP",
		PassiveTransferPortRange: &ftpserver.PortRange{
			Start: cfg.PassivePortRange[0],
			End:   cfg.PassivePortRange[1],
		},
		DefaultTransferType: ftpserver.TransferTypeBinary,
	}
	if cfg.TLS.Enabled {
		settings.TLSRequired = ftpserver.ClearOrEncrypted
	}
	return settings, nil
}

func (d *driver) ClientConnected(cc ftpserver.ClientContext) (string, error) {
	ip := remoteIP(cc.RemoteAddr())
	if acl := d.acl.Load(); acl != nil && !acl.Allowed(ip) {
		d.publish(eventbus.KindError, cc.RemoteAddr().String(), "connection rejected by IP ACL", nil)
		return "", fmt.Errorf("access denied")
	}
	d.publish(eventbus.KindConnect, cc.RemoteAddr().String(), "client connected", nil)
	return "DoDaemon FTP ready", nil
}

func (d *driver) ClientDisconnected(cc ftpserver.ClientContext) {
	d.publish(eventbus.KindDisconnect, cc.RemoteAddr().String(), "client disconnected", nil)
}

func (d *driver) AuthUser(cc ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	cfg := *d.cfg.Load()
	remote := cc.RemoteAddr().String()

	if user == anonymousUser || user == "ftp" {
		if !cfg.AllowAnonymous {
			d.publish(eventbus.KindAuth, remote, "anonymous login rejected (disabled)", map[string]any{"user": user})
			return nil, fmt.Errorf("anonymous access disabled")
		}
		d.publish(eventbus.KindAuth, remote, "anonymous login accepted", nil)
		anon := auth.User{Username: anonymousUser, HomeDir: cfg.AnonymousHomeDir, Permissions: "lr"}
		return newPermissionFs(anon.HomeDir, anon, d.bus, remote), nil
	}

	u, err := d.store.Load().Authenticate(user, pass)
	if err != nil {
		d.publish(eventbus.KindAuth, remote, "login failed", map[string]any{"user": user})
		return nil, err
	}
	d.publish(eventbus.KindAuth, remote, "login succeeded", map[string]any{"user": user})
	return newPermissionFs(u.HomeDir, u, d.bus, remote), nil
}

func (d *driver) GetTLSConfig() (*tls.Config, error) {
	return d.tlsConfig.Load(), nil
}

func (d *driver) publish(kind eventbus.Kind, remote, msg string, fields map[string]any) {
	if d.bus == nil {
		return
	}
	d.bus.Publish(eventbus.Event{Source: "ftp", Kind: kind, RemoteAddr: remote, Message: msg, Fields: fields})
}

func remoteIP(addr net.Addr) net.IP {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.IP
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

var _ afero.Fs = (*permissionFs)(nil)
