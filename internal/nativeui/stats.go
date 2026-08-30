//go:build windows

package nativeui

import (
	"sync"
	"time"

	"github.com/kms6402/dodaemon/internal/eventbus"
)

// svcStats mirrors the per-source running counters kept by the web
// dashboard's static/app.js (`stats[source]`) so both UIs show the same
// numbers for a given service: total connections seen and errors seen
// this session, plus the timestamp of the last event.
type svcStats struct {
	total        int
	errors       int
	lastActivity time.Time
}

// statsTracker keeps svcStats per event source ("ftp", "tftp", "syslog",
// "webui"), updated from the same event stream that feeds the log and
// active-session views.
type statsTracker struct {
	mu    sync.Mutex
	bySrc map[string]*svcStats
}

func newStatsTracker() *statsTracker {
	return &statsTracker{bySrc: make(map[string]*svcStats)}
}

func (t *statsTracker) record(ev eventbus.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.bySrc[ev.Source]
	if s == nil {
		s = &svcStats{}
		t.bySrc[ev.Source] = s
	}
	s.lastActivity = ev.Time
	if ev.Kind == eventbus.KindConnect {
		s.total++
	}
	if ev.Kind == eventbus.KindError {
		s.errors++
	}
}

func (t *statsTracker) snapshot(source string) svcStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.bySrc[source]; s != nil {
		return *s
	}
	return svcStats{}
}
