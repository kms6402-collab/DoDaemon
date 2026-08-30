package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
	"github.com/kms6402/dodaemon/internal/security"
)

const recentEventsCap = 200

// Server is the embedded management dashboard (docs/PLAN.md §4.6/§7).
type Server struct {
	cfg atomic.Pointer[config.Config]
	acl atomic.Pointer[security.ACL]
	bus *eventbus.Bus
	log *slog.Logger

	sessions *sessionStore

	mu     sync.Mutex
	recent []eventbus.Event

	httpSrv *http.Server
}

func New(cfg *config.Config, acl *security.ACL, bus *eventbus.Bus, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{bus: bus, log: log.With("server", "webui"), sessions: newSessionStore()}
	s.cfg.Store(cfg)
	s.acl.Store(acl)
	return s
}

func (s *Server) Name() string { return "webui" }

// UpdateConfig applies a hot-reloaded configuration (status tiles and auth
// credentials only — the listen address change requires a restart) and
// tells any open dashboard tab to refresh its service metadata by
// publishing a KindConfig event on the bus (see static/app.js).
func (s *Server) UpdateConfig(cfg *config.Config, acl *security.ACL) {
	s.cfg.Store(cfg)
	s.acl.Store(acl)
	if s.bus != nil {
		s.bus.Publish(eventbus.Event{Source: "config", Kind: eventbus.KindConfig, Message: "설정이 변경되었습니다"})
	}
}

// aclMiddleware rejects requests from addresses outside webui.ip_allowlist
// before they reach any route, including login — an empty allowlist (the
// default) permits everything (docs/PLAN.md §8.6 asks the operator to
// opt into narrowing this explicitly).
func (s *Server) aclMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acl := s.acl.Load()
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip != nil && acl != nil && !acl.Allowed(ip) {
			http.Error(w, "접근이 거부되었습니다.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Start(ctx context.Context) error {
	cfg := s.cfg.Load()

	unsub := s.subscribeEvents()
	defer unsub()

	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/", s.requireAuth(s.handleDashboard))
	mux.HandleFunc("/settings", s.requireAuth(s.handleSettingsPage))
	mux.HandleFunc("/api/events", s.requireAuth(s.handleEventsSSE))
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatusJSON))
	mux.HandleFunc("/api/services", s.requireAuth(s.handleServicesJSON))
	mux.HandleFunc("/api/settings", s.requireAuth(s.handleSettingsAPI))
	mux.HandleFunc("/api/browse", s.requireAuth(s.handleBrowseAPI))

	ln, err := net.Listen("tcp", cfg.WebUI.Listen)
	if err != nil {
		return err
	}
	s.httpSrv = &http.Server{Handler: s.aclMiddleware(mux), ReadHeaderTimeout: 10 * time.Second}
	s.log.Info("web UI listening", "addr", cfg.WebUI.Listen)

	errCh := make(chan error, 1)
	go func() { errCh <- s.httpSrv.Serve(ln) }()

	select {
	case <-ctx.Done():
		// Serve() only returns once the listener is actually closed; without
		// this, Start would return while the HTTP server keeps accepting
		// connections in its background goroutine (it does not self-drain
		// on ctx alone, unlike the FTP/TFTP/Syslog servers).
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

// subscribeEvents keeps a capped ring buffer of recent events (for the
// dashboard's first render) and republishes nothing further — SSE clients
// subscribe to the bus directly in handleEventsSSE.
func (s *Server) subscribeEvents() func() {
	ch, unsub := s.bus.Subscribe(128)
	go func() {
		for ev := range ch {
			s.mu.Lock()
			s.recent = append(s.recent, ev)
			if len(s.recent) > recentEventsCap {
				s.recent = s.recent[len(s.recent)-recentEventsCap:]
			}
			s.mu.Unlock()
		}
	}()
	return unsub
}

// serviceInfo describes one of the four services for the sidebar/detail
// panel — shared between the dashboard's initial server-render and the
// /api/services JSON endpoint the client re-fetches after a config change.
type serviceInfo struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Protocol string `json:"protocol"`
	Listen   string `json:"listen"`
	Meta     string `json:"meta"`
}

func (s *Server) buildServices() []serviceInfo {
	cfg := s.cfg.Load()

	_, tftpPort := splitAddr(cfg.TFTP.Listen)
	_, ftpPort := splitAddr(cfg.FTP.Listen)
	syslogAddr := firstNonEmpty(cfg.Syslog.UDPListen, cfg.Syslog.TCPListen)
	_, webPort := splitAddr(cfg.WebUI.Listen)

	writeState := "다운로드만"
	if cfg.TFTP.AllowWrite {
		writeState = "업로드 허용"
	}

	syslogProto := "UDP"
	syslogDetail := "udp " + cfg.Syslog.UDPListen
	if cfg.Syslog.UDPListen != "" && cfg.Syslog.TCPListen != "" {
		syslogProto = "UDP/TCP"
		syslogDetail = fmt.Sprintf("udp %s · tcp %s", cfg.Syslog.UDPListen, cfg.Syslog.TCPListen)
	} else if cfg.Syslog.TCPListen != "" {
		syslogProto = "TCP"
		syslogDetail = "tcp " + cfg.Syslog.TCPListen
	}

	return []serviceInfo{
		{
			Key: "tftp", Name: "TFTP 서버", Enabled: cfg.TFTP.Enabled,
			Protocol: "UDP " + tftpPort, Listen: cfg.TFTP.Listen,
			Meta: fmt.Sprintf("udp/%s · 루트 %s · %s", tftpPort, cfg.TFTP.RootDir, writeState),
		},
		{
			Key: "ftp", Name: "FTP 서버", Enabled: cfg.FTP.Enabled,
			Protocol: "TCP " + ftpPort, Listen: cfg.FTP.Listen,
			Meta: fmt.Sprintf("tcp/%s · 패시브 %d-%d · 계정 %d개", ftpPort, cfg.FTP.PassivePortRange[0], cfg.FTP.PassivePortRange[1], len(cfg.FTP.Users)),
		},
		{
			Key: "syslog", Name: "Syslog 서버", Enabled: cfg.Syslog.Enabled,
			Protocol: syslogProto, Listen: syslogAddr,
			Meta: fmt.Sprintf("%s · 보관 %d일", syslogDetail, cfg.Syslog.Rotate.MaxAgeDay),
		},
		{
			Key: "webui", Name: "Web", Enabled: cfg.WebUI.Enabled,
			Protocol: "HTTP " + webPort, Listen: cfg.WebUI.Listen,
			Meta: fmt.Sprintf("http://%s · 관리자 %s", cfg.WebUI.Listen, cfg.WebUI.Auth.Username),
		},
	}
}

func splitAddr(addr string) (host, port string) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, ""
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		h = "0.0.0.0"
	}
	return h, p
}

type dashboardData struct {
	Hostname     string
	Services     []serviceInfo
	RecentEvents []eventbus.Event
	BootstrapJS  template.JS
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()

	s.mu.Lock()
	recent := make([]eventbus.Event, len(s.recent))
	copy(recent, s.recent)
	s.mu.Unlock()
	// newest first
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}

	services := s.buildServices()
	// json.Marshal HTML-escapes '<', '>' and '&' by default, which is
	// exactly what makes this safe to splice into a <script> block below.
	bootstrap, err := json.Marshal(map[string]any{
		"hostname":      cfg.Server.Hostname,
		"services":      services,
		"recent_events": recent,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := dashboardData{
		Hostname:     cfg.Server.Hostname,
		Services:     services,
		RecentEvents: recent,
		BootstrapJS:  template.JS(bootstrap),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		templates.ExecuteTemplate(w, "login.html", map[string]string{})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cfg := s.cfg.Load()
	username := r.FormValue("username")
	password := r.FormValue("password")

	ok := username == cfg.WebUI.Auth.Username &&
		bcrypt.CompareHashAndPassword([]byte(cfg.WebUI.Auth.PasswordHash), []byte(password)) == nil

	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		templates.ExecuteTemplate(w, "login.html", map[string]string{"Error": "아이디 또는 비밀번호가 올바르지 않습니다."})
		return
	}

	token, err := s.sessions.create()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, r, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.revoke(c.Value)
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) handleStatusJSON(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ftp":    map[string]any{"Enabled": cfg.FTP.Enabled, "Listen": cfg.FTP.Listen},
		"tftp":   map[string]any{"Enabled": cfg.TFTP.Enabled, "Listen": cfg.TFTP.Listen},
		"syslog": map[string]any{"Enabled": cfg.Syslog.Enabled, "Listen": firstNonEmpty(cfg.Syslog.UDPListen, cfg.Syslog.TCPListen)},
	})
}

func (s *Server) handleServicesJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"hostname": s.cfg.Load().Server.Hostname,
		"services": s.buildServices(),
	})
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "settings.html", nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleEventsSSE streams live eventbus.Event values as
// "data: <json>\n\n" frames (text/event-stream), consumed by static/app.js.
func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.bus.Subscribe(64)
	defer unsub()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}
