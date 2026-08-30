package syslogsrv

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kms6402/dodaemon/internal/config"
)

// Store appends parsed messages as JSON Lines to a size-rotated,
// age-expired log file under dir (docs/PLAN.md §2.3, §9's rotate config).
// JSON encoding (rather than raw text) is itself the log-injection defense
// described in §8.5: encoding/json escapes control characters and quotes,
// so a hostile message body cannot forge extra log lines.
type Store struct {
	dir      string
	maxSize  int64
	maxAge   time.Duration
	compress bool

	mu   sync.Mutex
	file *os.File
	size int64
}

func NewStore(cfg config.SyslogConfig) (*Store, error) {
	if err := os.MkdirAll(cfg.LogDir, 0o750); err != nil {
		return nil, fmt.Errorf("syslogsrv: create log dir: %w", err)
	}
	s := &Store{
		dir:      cfg.LogDir,
		maxSize:  int64(cfg.Rotate.MaxSizeMB) * 1024 * 1024,
		maxAge:   time.Duration(cfg.Rotate.MaxAgeDay) * 24 * time.Hour,
		compress: cfg.Rotate.Compress,
	}
	if s.maxSize <= 0 {
		s.maxSize = 100 * 1024 * 1024
	}
	if err := s.openCurrent(); err != nil {
		return nil, err
	}
	go s.cleanupLoop()
	return s, nil
}

func (s *Store) currentPath() string {
	return filepath.Join(s.dir, "syslog-current.log")
}

func (s *Store) openCurrent() error {
	f, err := os.OpenFile(s.currentPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	s.file = f
	s.size = fi.Size()
	return nil
}

func (s *Store) Write(m Message) error {
	line, err := json.Marshal(struct {
		Time       time.Time `json:"time"`
		Facility   string    `json:"facility"`
		Severity   string    `json:"severity"`
		Hostname   string    `json:"hostname"`
		AppName    string    `json:"app_name"`
		ProcID     string    `json:"proc_id"`
		MsgID      string    `json:"msg_id"`
		Text       string    `json:"message"`
		Format     string    `json:"format"`
		RemoteAddr string    `json:"remote_addr"`
	}{m.Time, m.FacilityName(), m.SeverityName(), m.Hostname, m.AppName, m.ProcID, m.MsgID, m.Text, m.Format, m.RemoteAddr})
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.size+int64(len(line)) > s.maxSize {
		if err := s.rotateLocked(); err != nil {
			return err
		}
	}
	n, err := s.file.Write(line)
	s.size += int64(n)
	return err
}

func (s *Store) rotateLocked() error {
	if err := s.file.Close(); err != nil {
		return err
	}
	rotated := filepath.Join(s.dir, "syslog-"+time.Now().Format("20060102-150405")+".log")
	if err := os.Rename(s.currentPath(), rotated); err != nil {
		return err
	}
	if s.compress {
		go compressFile(rotated)
	}
	return s.openCurrent()
}

func compressFile(path string) {
	in, err := os.Open(path)
	if err != nil {
		return
	}
	defer in.Close()

	out, err := os.Create(path + ".gz")
	if err != nil {
		return
	}
	gw := gzip.NewWriter(out)
	if _, err := io.Copy(gw, in); err == nil {
		if gw.Close() == nil {
			out.Close()
			os.Remove(path)
			return
		}
	}
	gw.Close()
	out.Close()
	os.Remove(path + ".gz")
}

func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	s.cleanupOnce()
	for range ticker.C {
		s.cleanupOnce()
	}
}

func (s *Store) cleanupOnce() {
	if s.maxAge <= 0 {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-s.maxAge)
	for _, e := range entries {
		if e.IsDir() || e.Name() == "syslog-current.log" || !strings.HasPrefix(e.Name(), "syslog-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(filepath.Join(s.dir, e.Name()))
	}
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Close()
}

// ListFiles returns rotated log file names, newest first (used by the web UI).
func (s *Store) ListFiles() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}
