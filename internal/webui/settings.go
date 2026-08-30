package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/kms6402/dodaemon/internal/auth"
	"github.com/kms6402/dodaemon/internal/autostart"
	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/security"
)

// ConfigPath is set once at startup (by cmd/dodaemon) so the settings API
// knows which file to rewrite — the same file config.Watcher is already
// watching, so a save here hot-reloads exactly like an external edit or
// the native GUI's own settings dialog (docs/PLAN.md §4.4).
var ConfigPath string

// The FTP permission codes below ("elradfmwMT") are what internal/auth
// stores and checks byte-by-byte; both UIs present them as a checkbox list
// instead of a raw string (see static/settings.js's PERM_CODES, which must
// stay in sync with this set) and convert with permsToString/stringToPerms.

func permsToString(codes []string) string { return strings.Join(codes, "") }

func stringToPerms(s string) []string {
	out := make([]string, 0, len(s))
	for _, c := range s {
		out = append(out, string(c))
	}
	return out
}

type ftpUserDTO struct {
	Username    string   `json:"username"`
	NewPassword string   `json:"new_password,omitempty"` // blank on GET; blank on POST = keep existing hash
	HomeDir     string   `json:"home_dir"`
	Permissions []string `json:"permissions"`
}

// settingsDTO is the wire shape for GET/POST /api/settings. It never
// carries a password hash — only ever a plaintext NewPassword field that's
// blank unless the caller wants to change it — so a GET response is safe
// to hand to the browser. Hostname is intentionally absent: it's pinned to
// config.FixedHostname and not user-editable.
type settingsDTO struct {
	DataDir string `json:"data_dir"`

	FTPEnabled          bool         `json:"ftp_enabled"`
	FTPListen           string       `json:"ftp_listen"`
	FTPPassiveLo        int          `json:"ftp_passive_lo"`
	FTPPassiveHi        int          `json:"ftp_passive_hi"`
	FTPMaxConnections   int          `json:"ftp_max_connections"`
	FTPAllowAnonymous   bool         `json:"ftp_allow_anonymous"`
	FTPAnonymousHomeDir string       `json:"ftp_anonymous_home_dir"`
	FTPTLSEnabled       bool         `json:"ftp_tls_enabled"`
	FTPTLSCert          string       `json:"ftp_tls_cert"`
	FTPTLSKey           string       `json:"ftp_tls_key"`
	FTPUsers            []ftpUserDTO `json:"ftp_users"`
	FTPIPAllowlist      []string     `json:"ftp_ip_allowlist"`

	TFTPEnabled    bool   `json:"tftp_enabled"`
	TFTPListen     string `json:"tftp_listen"`
	TFTPRootDir    string `json:"tftp_root_dir"`
	TFTPAllowRead  bool   `json:"tftp_allow_read"`
	TFTPAllowWrite bool   `json:"tftp_allow_write"`
	TFTPMaxBlksize int    `json:"tftp_max_blksize"`
	TFTPTimeoutSec int    `json:"tftp_timeout_sec"`
	TFTPMaxRetries int    `json:"tftp_max_retries"`

	SyslogEnabled     bool     `json:"syslog_enabled"`
	SyslogUDPListen   string   `json:"syslog_udp_listen"`
	SyslogTCPListen   string   `json:"syslog_tcp_listen"`
	SyslogLogDir      string   `json:"syslog_log_dir"`
	SyslogMaxSizeMB   int      `json:"syslog_max_size_mb"`
	SyslogMaxAgeDay   int      `json:"syslog_max_age_day"`
	SyslogCompress    bool     `json:"syslog_compress"`
	SyslogTLSEnabled  bool     `json:"syslog_tls_enabled"`
	SyslogTLSCert     string   `json:"syslog_tls_cert"`
	SyslogTLSKey      string   `json:"syslog_tls_key"`
	SyslogIPAllowlist []string `json:"syslog_ip_allowlist"`

	WebEnabled     bool     `json:"web_enabled"`
	WebListen      string   `json:"web_listen"`
	WebUsername    string   `json:"web_username"`
	WebNewPassword string   `json:"web_new_password,omitempty"`
	WebIPAllowlist []string `json:"web_ip_allowlist"`

	AutostartAvailable bool `json:"autostart_available"`
	AutostartEnabled   bool `json:"autostart_enabled"`
}

func toDTO(cfg *config.Config) settingsDTO {
	users := make([]ftpUserDTO, len(cfg.FTP.Users))
	for i, u := range cfg.FTP.Users {
		users[i] = ftpUserDTO{Username: u.Username, HomeDir: u.HomeDir, Permissions: stringToPerms(u.Permissions)}
	}
	return settingsDTO{
		DataDir: cfg.Server.DataDir,

		FTPEnabled:          cfg.FTP.Enabled,
		FTPListen:           cfg.FTP.Listen,
		FTPPassiveLo:        cfg.FTP.PassivePortRange[0],
		FTPPassiveHi:        cfg.FTP.PassivePortRange[1],
		FTPMaxConnections:   cfg.FTP.MaxConnections,
		FTPAllowAnonymous:   cfg.FTP.AllowAnonymous,
		FTPAnonymousHomeDir: cfg.FTP.AnonymousHomeDir,
		FTPTLSEnabled:       cfg.FTP.TLS.Enabled,
		FTPTLSCert:          cfg.FTP.TLS.CertFile,
		FTPTLSKey:           cfg.FTP.TLS.KeyFile,
		FTPUsers:            users,
		FTPIPAllowlist:      cfg.FTP.IPAllowlist,

		TFTPEnabled:    cfg.TFTP.Enabled,
		TFTPListen:     cfg.TFTP.Listen,
		TFTPRootDir:    cfg.TFTP.RootDir,
		TFTPAllowRead:  cfg.TFTP.AllowRead,
		TFTPAllowWrite: cfg.TFTP.AllowWrite,
		TFTPMaxBlksize: cfg.TFTP.MaxBlksize,
		TFTPTimeoutSec: cfg.TFTP.TimeoutSec,
		TFTPMaxRetries: cfg.TFTP.MaxRetries,

		SyslogEnabled:     cfg.Syslog.Enabled,
		SyslogUDPListen:   cfg.Syslog.UDPListen,
		SyslogTCPListen:   cfg.Syslog.TCPListen,
		SyslogLogDir:      cfg.Syslog.LogDir,
		SyslogMaxSizeMB:   cfg.Syslog.Rotate.MaxSizeMB,
		SyslogMaxAgeDay:   cfg.Syslog.Rotate.MaxAgeDay,
		SyslogCompress:    cfg.Syslog.Rotate.Compress,
		SyslogTLSEnabled:  cfg.Syslog.TLS.Enabled,
		SyslogTLSCert:     cfg.Syslog.TLS.CertFile,
		SyslogTLSKey:      cfg.Syslog.TLS.KeyFile,
		SyslogIPAllowlist: cfg.Syslog.IPAllowlist,

		WebEnabled:     cfg.WebUI.Enabled,
		WebListen:      cfg.WebUI.Listen,
		WebUsername:    cfg.WebUI.Auth.Username,
		WebIPAllowlist: cfg.WebUI.IPAllowlist,

		AutostartAvailable: autostart.Available(),
		AutostartEnabled:   autostart.IsEnabled(),
	}
}

// applyDTO merges dto onto a copy of the current config, hashing any
// non-blank plaintext passwords along the way. It does not validate or
// save — callers do that so partial failures never touch disk.
func applyDTO(base *config.Config, dto settingsDTO) (config.Config, error) {
	newCfg := *base

	newCfg.Server.Hostname = config.FixedHostname
	newCfg.Server.DataDir = dto.DataDir

	newCfg.FTP.Enabled = dto.FTPEnabled
	newCfg.FTP.Listen = dto.FTPListen
	newCfg.FTP.PassivePortRange = [2]int{dto.FTPPassiveLo, dto.FTPPassiveHi}
	newCfg.FTP.MaxConnections = dto.FTPMaxConnections
	newCfg.FTP.AllowAnonymous = dto.FTPAllowAnonymous
	newCfg.FTP.AnonymousHomeDir = dto.FTPAnonymousHomeDir
	newCfg.FTP.TLS.Enabled = dto.FTPTLSEnabled
	newCfg.FTP.TLS.CertFile = dto.FTPTLSCert
	newCfg.FTP.TLS.KeyFile = dto.FTPTLSKey
	newCfg.FTP.IPAllowlist = cleanList(dto.FTPIPAllowlist)

	existingHash := make(map[string]string, len(base.FTP.Users))
	for _, u := range base.FTP.Users {
		existingHash[u.Username] = u.PasswordHash
	}
	users := make([]config.FTPUser, len(dto.FTPUsers))
	for i, u := range dto.FTPUsers {
		hash := existingHash[u.Username]
		if u.NewPassword != "" {
			h, err := auth.HashPassword(u.NewPassword)
			if err != nil {
				return config.Config{}, err
			}
			hash = h
		}
		if hash == "" {
			return config.Config{}, errNewAccountNeedsPassword(u.Username)
		}
		users[i] = config.FTPUser{Username: u.Username, PasswordHash: hash, HomeDir: u.HomeDir, Permissions: permsToString(u.Permissions)}
	}
	newCfg.FTP.Users = users

	newCfg.TFTP.Enabled = dto.TFTPEnabled
	newCfg.TFTP.Listen = dto.TFTPListen
	newCfg.TFTP.RootDir = dto.TFTPRootDir
	newCfg.TFTP.AllowRead = dto.TFTPAllowRead
	newCfg.TFTP.AllowWrite = dto.TFTPAllowWrite
	newCfg.TFTP.MaxBlksize = dto.TFTPMaxBlksize
	newCfg.TFTP.TimeoutSec = dto.TFTPTimeoutSec
	newCfg.TFTP.MaxRetries = dto.TFTPMaxRetries

	newCfg.Syslog.Enabled = dto.SyslogEnabled
	newCfg.Syslog.UDPListen = dto.SyslogUDPListen
	newCfg.Syslog.TCPListen = dto.SyslogTCPListen
	newCfg.Syslog.LogDir = dto.SyslogLogDir
	newCfg.Syslog.Rotate.MaxSizeMB = dto.SyslogMaxSizeMB
	newCfg.Syslog.Rotate.MaxAgeDay = dto.SyslogMaxAgeDay
	newCfg.Syslog.Rotate.Compress = dto.SyslogCompress
	newCfg.Syslog.TLS.Enabled = dto.SyslogTLSEnabled
	newCfg.Syslog.TLS.CertFile = dto.SyslogTLSCert
	newCfg.Syslog.TLS.KeyFile = dto.SyslogTLSKey
	newCfg.Syslog.IPAllowlist = cleanList(dto.SyslogIPAllowlist)

	newCfg.WebUI.Enabled = dto.WebEnabled
	newCfg.WebUI.Listen = dto.WebListen
	newCfg.WebUI.Auth.Username = dto.WebUsername
	newCfg.WebUI.Auth.PasswordHash = base.WebUI.Auth.PasswordHash
	newCfg.WebUI.IPAllowlist = cleanList(dto.WebIPAllowlist)
	if dto.WebNewPassword != "" {
		h, err := auth.HashPassword(dto.WebNewPassword)
		if err != nil {
			return config.Config{}, err
		}
		newCfg.WebUI.Auth.PasswordHash = h
	}

	return newCfg, nil
}

// cleanList drops blank entries (e.g. an empty last line from a textarea).
func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

type dtoError string

func (e dtoError) Error() string { return string(e) }

func errNewAccountNeedsPassword(username string) error {
	return dtoError("계정 '" + username + "'은 새 계정이므로 비밀번호를 입력해야 합니다.")
}

func (s *Server) handleSettingsAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toDTO(s.cfg.Load()))

	case http.MethodPost:
		var dto settingsDTO
		if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
			http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
			return
		}

		newCfg, err := applyDTO(s.cfg.Load(), dto)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := newCfg.Validate(); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if ConfigPath == "" {
			writeJSONError(w, http.StatusInternalServerError, "설정 파일 경로가 설정되지 않았습니다.")
			return
		}
		if err := config.Save(ConfigPath, &newCfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Update our own in-memory copy (config + ACL) immediately rather
		// than waiting for the config.Watcher to notice the write — that
		// happens after a debounce delay, and this same page's next GET
		// (right after the save completes) would otherwise read stale data
		// back. The watcher's own UpdateConfig call still follows shortly
		// after and is a harmless no-op repeat of the same data.
		s.cfg.Store(&newCfg)
		if newACL, err := security.NewACL(newCfg.WebUI.IPAllowlist, nil); err == nil {
			s.acl.Store(newACL)
		}

		if autostart.Available() {
			exePath, _ := os.Executable()
			_ = autostart.SetEnabled(dto.AutostartEnabled, exePath)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": message})
}
