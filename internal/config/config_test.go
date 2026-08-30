package config

import (
	"path/filepath"
	"testing"
)

func TestDefault_AllServersDisabledExceptWebUI(t *testing.T) {
	cfg := Default()
	if cfg.FTP.Enabled || cfg.TFTP.Enabled || cfg.Syslog.Enabled {
		t.Error("FTP/TFTP/Syslog must be opt-in (disabled by default) per PLAN.md §8.6")
	}
	if cfg.FTP.AllowAnonymous {
		t.Error("anonymous FTP must default to disabled")
	}
	if cfg.TFTP.AllowWrite {
		t.Error("TFTP uploads (WRQ) must default to disabled")
	}
	if cfg.WebUI.Listen != "127.0.0.1:8080" {
		t.Errorf("WebUI must default to localhost-only bind, got %q", cfg.WebUI.Listen)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("default config must validate cleanly (only webui enabled): %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dodaemon.yaml")

	cfg := Default()
	cfg.FTP.Enabled = true
	cfg.FTP.Users = []FTPUser{{Username: "admin", PasswordHash: "$2a$10$x", HomeDir: dir, Permissions: "elradfmwMT"}}

	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.FTP.Enabled || len(loaded.FTP.Users) != 1 || loaded.FTP.Users[0].Username != "admin" {
		t.Errorf("round trip lost data: %+v", loaded.FTP)
	}
}

func TestValidate_FTP(t *testing.T) {
	base := func() *Config {
		c := Default()
		c.FTP.Enabled = true
		c.FTP.Users = []FTPUser{{Username: "a", HomeDir: "x"}}
		return c
	}

	if err := base().Validate(); err != nil {
		t.Errorf("valid FTP config rejected: %v", err)
	}

	badListen := base()
	badListen.FTP.Listen = "not-a-listen-addr"
	if err := badListen.Validate(); err == nil {
		t.Error("expected error for invalid ftp.listen")
	}

	badRange := base()
	badRange.FTP.PassivePortRange = [2]int{50100, 50000} // lo > hi
	if err := badRange.Validate(); err == nil {
		t.Error("expected error for inverted passive_port_range")
	}

	noUsername := base()
	noUsername.FTP.Users = []FTPUser{{Username: "", HomeDir: "x"}}
	if err := noUsername.Validate(); err == nil {
		t.Error("expected error for empty username")
	}

	tlsNoFiles := base()
	tlsNoFiles.FTP.TLS.Enabled = true
	if err := tlsNoFiles.Validate(); err == nil {
		t.Error("expected error for tls.enabled without cert/key files")
	}
}

func TestValidate_TFTP(t *testing.T) {
	base := func() *Config { c := Default(); c.TFTP.Enabled = true; return c }

	if err := base().Validate(); err != nil {
		t.Errorf("valid TFTP config rejected: %v", err)
	}

	badBlksize := base()
	badBlksize.TFTP.MaxBlksize = 4 // below RFC 2348 minimum of 8
	if err := badBlksize.Validate(); err == nil {
		t.Error("expected error for out-of-range max_blksize")
	}

	noRoot := base()
	noRoot.TFTP.RootDir = ""
	if err := noRoot.Validate(); err == nil {
		t.Error("expected error for empty root_dir")
	}
}

func TestValidate_Syslog(t *testing.T) {
	base := func() *Config { c := Default(); c.Syslog.Enabled = true; return c }

	if err := base().Validate(); err != nil {
		t.Errorf("valid syslog config (UDP only) rejected: %v", err)
	}

	noListeners := base()
	noListeners.Syslog.UDPListen = ""
	noListeners.Syslog.TCPListen = ""
	if err := noListeners.Validate(); err == nil {
		t.Error("expected error when neither udp_listen nor tcp_listen is set")
	}
}

func TestValidate_SecurityACLEntries(t *testing.T) {
	cfg := Default()
	cfg.FTP.Enabled = true
	cfg.FTP.IPAllowlist = []string{"10.0.0.0/8", "192.168.1.1"}
	cfg.Syslog.IPAllowlist = []string{"10.0.0.0/8"}
	cfg.WebUI.IPAllowlist = []string{"192.168.1.1"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid ACL entries rejected: %v", err)
	}

	cfg.FTP.IPAllowlist = []string{"not-an-ip-or-cidr"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid ACL entry")
	}
}
