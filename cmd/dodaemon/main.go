// Command dodaemon is DoDaemon: a single-file native Windows GUI app
// bundling the FTP/TFTP/Syslog servers and an embedded web management UI.
// Running the exe — however it's launched — always opens the native window
// (internal/nativeui); there is no separate CLI (docs/PLAN.md §4.6). It
// chdirs to its own directory on startup so config/data always live next
// to the exe regardless of how it was launched.
package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
	"github.com/kms6402/dodaemon/internal/ftp"
	"github.com/kms6402/dodaemon/internal/nativeui"
	"github.com/kms6402/dodaemon/internal/security"
	"github.com/kms6402/dodaemon/internal/server"
	"github.com/kms6402/dodaemon/internal/syslogsrv"
	"github.com/kms6402/dodaemon/internal/tftp"
	"github.com/kms6402/dodaemon/internal/webui"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const (
	shutdownTimeout = 15 * time.Second
	configPath      = "configs/dodaemon.yaml" // relative to the exe's own directory, see run()
)

func main() {
	if err := run(); err != nil {
		// There is no console (the exe is built with -H=windowsgui, see
		// scripts/build.ps1) and no CLI to print to, so a message box is
		// the only way a startup failure is ever visible.
		showErrorBox("DoDaemon 시작 실패", err.Error())
		os.Exit(1)
	}
}

func run() error {
	if err := chdirToExeDir(); err != nil {
		return err
	}
	firstRun, err := ensureConfigExists(configPath)
	if err != nil {
		return err
	}
	return runNativeGUI(configPath, firstRun)
}

// chdirToExeDir makes every relative path in config.Default() (data_dir,
// tftp.root_dir, syslog.log_dir, ...) and configPath above resolve next to
// the exe itself, no matter what the caller's working directory was —
// double-click, a shortcut without an explicit "start in", Task Scheduler,
// all end up the same.
func chdirToExeDir() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	return os.Chdir(filepath.Dir(exePath))
}

// ensureConfigExists auto-creates the safe-by-default config (every server
// disabled except the WebUI, see config.Default) the first time DoDaemon is
// launched with no setup — there's no CLI "config init" step anymore, so
// this has to just work on first run. From then on Load reads back exactly
// what was last saved (by either UI), which is what makes settings persist
// across restarts. The returned bool tells the caller whether this really
// was a first run, so it knows whether to show first-run guidance.
func ensureConfigExists(path string) (firstRun bool, err error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return true, config.Save(path, config.Default())
}

// runNativeGUI shows the native window and runs the server supervisor in
// the background for as long as the window is open.
func runNativeGUI(absConfigPath string, firstRun bool) error {
	cfg, err := config.Load(absConfigPath)
	if err != nil {
		return err
	}

	bus := eventbus.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	win, err := nativeui.New(absConfigPath, cfg, bus, cancel)
	if err != nil {
		return err
	}
	if firstRun {
		// docs/tftp_daemon_console_spec.pdf §5's firewall-exception
		// guidance: TFTP/FTP/Syslog listen for inbound connections, so
		// Windows Firewall will prompt the first time one of them actually
		// binds — this just tells the operator that prompt is expected and
		// safe to allow, rather than auto-registering a rule (which would
		// need elevation we don't have and shouldn't silently request).
		win.ShowFirstRunNotice()
	}

	run := newRunFunc(absConfigPath, bus, win.UpdateConfig)
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	win.Run() // blocks until the window is closed
	cancel()

	select {
	case <-done:
	case <-time.After(shutdownTimeout + 2*time.Second):
	}
	return nil
}

// newRunFunc builds the supervisor entry point: it loads config, wires up
// every enabled protocol server plus the web UI, and watches the config
// file for hot-reloadable changes until ctx is cancelled. onReload, if
// non-nil, is called with every config the supervisor picks up — the
// initial load and every successful hot-reload — so the native window's
// status cards stay current.
//
// Each service (except TFTP, which has no per-user auth to protect and
// intentionally skips this) gets its own IP allowlist rather than one
// shared list, so e.g. FTP can be opened to a wider range than the web
// admin panel.
func newRunFunc(absConfigPath string, bus *eventbus.Bus, onReload func(*config.Config)) func(context.Context) error {
	return func(ctx context.Context) error {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

		cfg, err := config.Load(absConfigPath)
		if err != nil {
			logger.Error("failed to load config", "path", absConfigPath, "error", err)
			return err
		}
		if onReload != nil {
			onReload(cfg)
		}
		webui.ConfigPath = absConfigPath
		webui.Version = version

		ftpACL, err := security.NewACL(cfg.FTP.IPAllowlist, nil)
		if err != nil {
			return err
		}
		syslogACL, err := security.NewACL(cfg.Syslog.IPAllowlist, nil)
		if err != nil {
			return err
		}
		webACL, err := security.NewACL(cfg.WebUI.IPAllowlist, nil)
		if err != nil {
			return err
		}

		sup := server.New(logger)

		var ftpSrv *ftp.Server
		if cfg.FTP.Enabled {
			ftpSrv, err = ftp.New(cfg.FTP, ftpACL, bus, logger)
			if err != nil {
				return err
			}
			sup.Add(ftpSrv)
		}

		var tftpSrv *tftp.Server
		if cfg.TFTP.Enabled {
			tftpSrv = tftp.New(cfg.TFTP, bus, logger)
			sup.Add(tftpSrv)
		}

		var syslogSrv *syslogsrv.Server
		if cfg.Syslog.Enabled {
			syslogSrv, err = syslogsrv.New(cfg.Syslog, syslogACL, bus, logger)
			if err != nil {
				return err
			}
			sup.Add(syslogSrv)
		}

		var webSrv *webui.Server
		if cfg.WebUI.Enabled {
			webSrv = webui.New(cfg, webACL, bus, logger)
			sup.Add(webSrv)
		}

		liveCfg := cfg
		watcher := config.NewWatcher(absConfigPath, logger, func(newCfg *config.Config) {
			oldCfg := liveCfg

			newFtpACL, err := security.NewACL(newCfg.FTP.IPAllowlist, nil)
			if err != nil {
				logger.Error("hot reload: invalid ftp.ip_allowlist, keeping previous config", "error", err)
				return
			}
			newSyslogACL, err := security.NewACL(newCfg.Syslog.IPAllowlist, nil)
			if err != nil {
				logger.Error("hot reload: invalid syslog.ip_allowlist, keeping previous config", "error", err)
				return
			}
			newWebACL, err := security.NewACL(newCfg.WebUI.IPAllowlist, nil)
			if err != nil {
				logger.Error("hot reload: invalid webui.ip_allowlist, keeping previous config", "error", err)
				return
			}

			// FTP: a listener/TLS/ACL change needs a fresh instance bound to
			// the new address; user/permission changes apply to the
			// already-running instance without dropping connections.
			switch {
			case !newCfg.FTP.Enabled && ftpSrv != nil:
				sup.Remove("ftp")
				ftpSrv = nil
			case newCfg.FTP.Enabled && (ftpSrv == nil || oldCfg.FTP.Listen != newCfg.FTP.Listen ||
				oldCfg.FTP.PassivePortRange != newCfg.FTP.PassivePortRange || oldCfg.FTP.TLS != newCfg.FTP.TLS ||
				!reflect.DeepEqual(oldCfg.FTP.IPAllowlist, newCfg.FTP.IPAllowlist)):
				newFtpSrv, err := ftp.New(newCfg.FTP, newFtpACL, bus, logger)
				if err != nil {
					logger.Error("hot reload: failed to build new FTP server, keeping previous", "error", err)
					break
				}
				sup.Replace(newFtpSrv)
				ftpSrv = newFtpSrv
			case newCfg.FTP.Enabled && ftpSrv != nil:
				ftpSrv.UpdateConfig(newCfg.FTP, newFtpACL)
			}

			// TFTP has no partial live-update path today: any change to an
			// already-enabled server gets a fresh instance. No ACL here by
			// design (docs/PLAN.md §8.2 already treats TFTP as unauthenticated).
			switch {
			case !newCfg.TFTP.Enabled && tftpSrv != nil:
				sup.Remove("tftp")
				tftpSrv = nil
			case newCfg.TFTP.Enabled && (tftpSrv == nil || oldCfg.TFTP != newCfg.TFTP):
				newTftpSrv := tftp.New(newCfg.TFTP, bus, logger)
				sup.Replace(newTftpSrv)
				tftpSrv = newTftpSrv
			}

			// Syslog: same story — rotation/log_dir/listener/ACL changes all
			// need a fresh Store, so just rebuild on any change.
			switch {
			case !newCfg.Syslog.Enabled && syslogSrv != nil:
				sup.Remove("syslog")
				syslogSrv = nil
			case newCfg.Syslog.Enabled && (syslogSrv == nil || !reflect.DeepEqual(oldCfg.Syslog, newCfg.Syslog)):
				newSyslogSrv, err := syslogsrv.New(newCfg.Syslog, newSyslogACL, bus, logger)
				if err != nil {
					logger.Error("hot reload: failed to build new syslog server, keeping previous", "error", err)
					break
				}
				sup.Replace(newSyslogSrv)
				syslogSrv = newSyslogSrv
			}

			// WebUI: the bind address needs a fresh listener; auth
			// credentials, ACL and status-tile data apply live.
			switch {
			case !newCfg.WebUI.Enabled && webSrv != nil:
				sup.Remove("webui")
				webSrv = nil
			case newCfg.WebUI.Enabled && (webSrv == nil || oldCfg.WebUI.Listen != newCfg.WebUI.Listen):
				newWebSrv := webui.New(newCfg, newWebACL, bus, logger)
				sup.Replace(newWebSrv)
				webSrv = newWebSrv
			case newCfg.WebUI.Enabled && webSrv != nil:
				webSrv.UpdateConfig(newCfg, newWebACL)
			}

			liveCfg = newCfg
			if onReload != nil {
				onReload(newCfg)
			}
			logger.Info("config hot-reloaded")
		})
		go func() {
			if err := watcher.Run(ctx); err != nil {
				logger.Error("config watcher stopped", "error", err)
			}
		}()

		logger.Info("dodaemon starting", "version", version, "config", absConfigPath)
		return sup.Run(ctx, shutdownTimeout)
	}
}
