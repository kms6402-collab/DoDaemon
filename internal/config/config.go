// Package config loads, validates and hot-reloads DoDaemon's YAML
// configuration file. See docs/PLAN.md §6 for the schema this mirrors.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FixedHostname is the server name shown throughout the UI. It's not
// user-configurable (the user asked for it pinned), so Load/Default always
// force this value rather than trusting whatever is in the YAML file.
const FixedHostname = "DoDaeMon"

type Config struct {
	Server ServerConfig `yaml:"server"`
	FTP    FTPConfig    `yaml:"ftp"`
	TFTP   TFTPConfig   `yaml:"tftp"`
	Syslog SyslogConfig `yaml:"syslog"`
	WebUI  WebUIConfig  `yaml:"webui"`
}

type ServerConfig struct {
	Hostname string `yaml:"hostname"`
	DataDir  string `yaml:"data_dir"`
}

type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// FTPUser's Permissions is still stored as the compact 3CDaemon-style
// character string internal/auth already checks byte-by-byte ("elradfmwMT")
// — only the UI layer (native dialog / web settings page) presents it as a
// checkbox list and converts to/from this string, so the storage format
// and internal/auth stay untouched.
type FTPUser struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
	HomeDir      string `yaml:"home_dir"`
	Permissions  string `yaml:"permissions"`
}

type FTPConfig struct {
	Enabled          bool      `yaml:"enabled"`
	Listen           string    `yaml:"listen"`
	PassivePortRange [2]int    `yaml:"passive_port_range"`
	TLS              TLSConfig `yaml:"tls"`
	AllowAnonymous   bool      `yaml:"allow_anonymous"`
	AnonymousHomeDir string    `yaml:"anonymous_home_dir"`
	Users            []FTPUser `yaml:"users"`
	MaxConnections   int       `yaml:"max_connections"`
	IPAllowlist      []string  `yaml:"ip_allowlist"`

	// SFTP shares the FTP account list/permissions/IP allowlist above — it's
	// presented as a sub-feature of "FTP" in both UIs, not a separate
	// service, rather than a fully independent 5th protocol server.
	SFTPEnabled bool   `yaml:"sftp_enabled"`
	SFTPListen  string `yaml:"sftp_listen"`
}

type TFTPConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Listen     string `yaml:"listen"`
	RootDir    string `yaml:"root_dir"`
	AllowRead  bool   `yaml:"allow_read"`  // RRQ / downloads
	AllowWrite bool   `yaml:"allow_write"` // WRQ / uploads
	MaxBlksize int    `yaml:"max_blksize"`
	TimeoutSec int    `yaml:"timeout_sec"` // ACK wait before retransmit
	MaxRetries int    `yaml:"max_retries"` // retransmit attempts before aborting a block
}

type RotateConfig struct {
	MaxSizeMB int  `yaml:"max_size_mb"`
	MaxAgeDay int  `yaml:"max_age_days"`
	Compress  bool `yaml:"compress"`
}

type SyslogConfig struct {
	Enabled     bool         `yaml:"enabled"`
	UDPListen   string       `yaml:"udp_listen"`
	TCPListen   string       `yaml:"tcp_listen"`
	TLS         TLSConfig    `yaml:"tls"`
	LogDir      string       `yaml:"log_dir"`
	Rotate      RotateConfig `yaml:"rotate"`
	IPAllowlist []string     `yaml:"ip_allowlist"`
}

type WebUIAuth struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
}

type WebUIConfig struct {
	Enabled     bool      `yaml:"enabled"`
	Listen      string    `yaml:"listen"`
	Auth        WebUIAuth `yaml:"auth"`
	IPAllowlist []string  `yaml:"ip_allowlist"`
}

// Default returns the safe-by-default configuration used when no config
// file exists yet (see docs/PLAN.md §8.6: every server opt-in, no
// unauthenticated write paths, WebUI bound to localhost only).
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Hostname: FixedHostname,
			DataDir:  "./data",
		},
		FTP: FTPConfig{
			Enabled:          false,
			Listen:           "0.0.0.0:21",
			PassivePortRange: [2]int{50000, 50100},
			AllowAnonymous:   false,
			MaxConnections:   100,
			SFTPEnabled:      false,
			// 22 is the conventional SSH port, but a real OpenSSH server is
			// often already running there (including on Windows Server) —
			// defaulting elsewhere avoids a bind conflict out of the box.
			SFTPListen: "0.0.0.0:2222",
		},
		TFTP: TFTPConfig{
			Enabled:    false,
			Listen:     "0.0.0.0:69",
			RootDir:    "./data/tftp",
			AllowRead:  true,
			AllowWrite: false,
			MaxBlksize: 65464,
			TimeoutSec: 3,
			MaxRetries: 5,
		},
		Syslog: SyslogConfig{
			Enabled:   false,
			UDPListen: "0.0.0.0:514",
			LogDir:    "./data/syslog",
			Rotate:    RotateConfig{MaxSizeMB: 100, MaxAgeDay: 30, Compress: true},
		},
		WebUI: WebUIConfig{
			Enabled: true,
			Listen:  "127.0.0.1:8080",
		},
	}
}

// Load reads and parses the YAML file at path, filling in defaults for any
// field that was left unset, then validates the result.
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.Server.Hostname = FixedHostname
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: invalid %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes cfg to path as YAML, creating parent directories as needed.
func Save(path string, cfg *Config) error {
	cfg.Server.Hostname = FixedHostname
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o640)
}

// Validate checks structural invariants that YAML unmarshalling alone
// can't guarantee: valid listen addresses, sane port ranges, and that
// referenced directories are usable.
func (c *Config) Validate() error {
	if c.Server.DataDir == "" {
		return fmt.Errorf("server.data_dir must not be empty")
	}

	if c.FTP.Enabled {
		if err := validateListenAddr("ftp.listen", c.FTP.Listen); err != nil {
			return err
		}
		lo, hi := c.FTP.PassivePortRange[0], c.FTP.PassivePortRange[1]
		if lo <= 0 || hi <= 0 || lo > hi || hi > 65535 {
			return fmt.Errorf("ftp.passive_port_range %v is invalid", c.FTP.PassivePortRange)
		}
		if c.FTP.TLS.Enabled {
			if err := validateTLSFiles("ftp.tls", c.FTP.TLS); err != nil {
				return err
			}
		}
		for i, u := range c.FTP.Users {
			if u.Username == "" {
				return fmt.Errorf("ftp.users[%d].username must not be empty", i)
			}
			if u.HomeDir == "" {
				return fmt.Errorf("ftp.users[%d].home_dir must not be empty", i)
			}
		}
		if err := validateCIDRList("ftp.ip_allowlist", c.FTP.IPAllowlist); err != nil {
			return err
		}
		if c.FTP.SFTPEnabled {
			if err := validateListenAddr("ftp.sftp_listen", c.FTP.SFTPListen); err != nil {
				return err
			}
			if c.FTP.SFTPListen == c.FTP.Listen {
				return fmt.Errorf("ftp.sftp_listen must differ from ftp.listen")
			}
		}
	}

	if c.TFTP.Enabled {
		if err := validateListenAddr("tftp.listen", c.TFTP.Listen); err != nil {
			return err
		}
		if c.TFTP.RootDir == "" {
			return fmt.Errorf("tftp.root_dir must not be empty")
		}
		// The directory is created on demand rather than required to exist
		// up front: the moment a user enables TFTP, RootDir is almost
		// always still the untouched default (./data/tftp), which nothing
		// has created yet — rejecting that would make "enable TFTP" fail
		// on a fresh install. A path that genuinely can't be created (bad
		// drive, reserved name, no permission) still surfaces as an error.
		if fi, err := os.Stat(c.TFTP.RootDir); err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("tftp.root_dir %q exists but is not a directory", c.TFTP.RootDir)
			}
		} else if err := os.MkdirAll(c.TFTP.RootDir, 0o750); err != nil {
			return fmt.Errorf("tftp.root_dir %q is not usable: %w", c.TFTP.RootDir, err)
		}
		if c.TFTP.MaxBlksize < 8 || c.TFTP.MaxBlksize > 65464 {
			return fmt.Errorf("tftp.max_blksize %d out of range [8,65464]", c.TFTP.MaxBlksize)
		}
		if c.TFTP.TimeoutSec < 1 || c.TFTP.TimeoutSec > 255 {
			return fmt.Errorf("tftp.timeout_sec %d out of range [1,255]", c.TFTP.TimeoutSec)
		}
		if c.TFTP.MaxRetries < 1 || c.TFTP.MaxRetries > 20 {
			return fmt.Errorf("tftp.max_retries %d out of range [1,20]", c.TFTP.MaxRetries)
		}
	}

	if c.Syslog.Enabled {
		if c.Syslog.UDPListen == "" && c.Syslog.TCPListen == "" {
			return fmt.Errorf("syslog: at least one of udp_listen/tcp_listen must be set")
		}
		if c.Syslog.UDPListen != "" {
			if err := validateListenAddr("syslog.udp_listen", c.Syslog.UDPListen); err != nil {
				return err
			}
		}
		if c.Syslog.TCPListen != "" {
			if err := validateListenAddr("syslog.tcp_listen", c.Syslog.TCPListen); err != nil {
				return err
			}
			if c.Syslog.TLS.Enabled {
				if err := validateTLSFiles("syslog.tls", c.Syslog.TLS); err != nil {
					return err
				}
			}
		}
		if err := validateCIDRList("syslog.ip_allowlist", c.Syslog.IPAllowlist); err != nil {
			return err
		}
	}

	if c.WebUI.Enabled {
		if err := validateListenAddr("webui.listen", c.WebUI.Listen); err != nil {
			return err
		}
		if err := validateCIDRList("webui.ip_allowlist", c.WebUI.IPAllowlist); err != nil {
			return err
		}
	}

	return nil
}

func validateListenAddr(field, addr string) error {
	if addr == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid host:port: %w", field, addr, err)
	}
	if portStr == "" {
		return fmt.Errorf("%s %q is missing a port", field, addr)
	}
	return nil
}

func validateTLSFiles(field string, t TLSConfig) error {
	if t.CertFile == "" || t.KeyFile == "" {
		return fmt.Errorf("%s: cert_file and key_file are required when tls.enabled is true", field)
	}
	if _, err := os.Stat(t.CertFile); err != nil {
		return fmt.Errorf("%s.cert_file: %w", field, err)
	}
	if _, err := os.Stat(t.KeyFile); err != nil {
		return fmt.Errorf("%s.key_file: %w", field, err)
	}
	return nil
}

func validateCIDRList(field string, entries []string) error {
	for _, e := range entries {
		if _, _, err := net.ParseCIDR(e); err != nil && net.ParseIP(e) == nil {
			return fmt.Errorf("%s: invalid IP/CIDR entry %q", field, e)
		}
	}
	return nil
}
