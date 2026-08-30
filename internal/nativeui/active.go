//go:build windows

package nativeui

import (
	"regexp"
	"sync"
	"time"

	"github.com/lxn/walk"

	"github.com/kms6402/dodaemon/internal/eventbus"
)

// activeRow is one in-flight connection/transfer, derived purely from
// connect/disconnect/transfer event pairs already on the bus — see
// activeTracker.apply for the matching rules (mirrored in
// internal/webui/static/app.js so both UIs agree on what "active" means).
type activeRow struct {
	source     string
	remoteAddr string
	kind       string // "연결" | "전송"
	direction  string // "GET" | "PUT" | ""
	detail     string
	startedAt  time.Time
}

var (
	startPattern = regexp.MustCompile(`started|시작`)
	endPattern   = regexp.MustCompile(`complete|완료|실패`)
	dirPattern   = regexp.MustCompile(`GET|PUT|다운로드|업로드`)
)

// activeTracker keeps the current set of active connections/transfers,
// derived from the same event stream the log already shows.
type activeTracker struct {
	mu   sync.Mutex
	rows map[string]activeRow // key: source|remoteAddr|file
}

func newActiveTracker() *activeTracker {
	return &activeTracker{rows: make(map[string]activeRow)}
}

func (t *activeTracker) apply(ev eventbus.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()

	file, _ := ev.Fields["file"].(string)
	key := ev.Source + "|" + ev.RemoteAddr + "|" + file
	connKey := ev.Source + "|" + ev.RemoteAddr + "|"

	switch {
	case ev.Kind == eventbus.KindConnect:
		t.rows[connKey] = activeRow{source: ev.Source, remoteAddr: ev.RemoteAddr, kind: "연결", startedAt: ev.Time}
	case ev.Kind == eventbus.KindDisconnect:
		delete(t.rows, connKey)
	case ev.Kind == eventbus.KindTransfer && startPattern.MatchString(ev.Message):
		direction := "GET"
		if m := dirPattern.FindString(ev.Message); m == "PUT" || m == "업로드" {
			direction = "PUT"
		}
		t.rows[key] = activeRow{source: ev.Source, remoteAddr: ev.RemoteAddr, kind: "전송", direction: direction, detail: ev.Message, startedAt: ev.Time}
	case endPattern.MatchString(ev.Message) || ev.Kind == eventbus.KindError:
		delete(t.rows, key)
	}
}

func (t *activeTracker) snapshotAll() []activeRow {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]activeRow, 0, len(t.rows))
	for _, r := range t.rows {
		out = append(out, r)
	}
	return out
}

// snapshotFor returns only the rows for one service, matching how the web
// dashboard's active-sessions table is always scoped to whichever service
// is currently selected in the sidebar.
func (t *activeTracker) snapshotFor(source string) []activeRow {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]activeRow, 0, len(t.rows))
	for _, r := range t.rows {
		if r.source == source {
			out = append(out, r)
		}
	}
	return out
}

// activeTableModel is the walk.TableModel backing the "활성 세션" TableView.
type activeTableModel struct {
	walk.TableModelBase
	rows []activeRow
}

func (m *activeTableModel) RowCount() int { return len(m.rows) }

// Value's 4 columns (클라이언트/방향·종류/파일·내용/시작 시각) match the web
// dashboard's active-sessions table — no separate 서비스 column, since rows
// are always pre-filtered to the sidebar's currently-selected service.
func (m *activeTableModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	r := m.rows[row]
	switch col {
	case 0:
		return r.remoteAddr
	case 1:
		if r.direction != "" {
			return r.direction + " " + r.kind
		}
		return r.kind
	case 2:
		if r.detail != "" {
			return r.detail
		}
		return "-"
	case 3:
		return r.startedAt.Local().Format("15:04:05")
	default:
		return ""
	}
}

func (m *activeTableModel) setRows(rows []activeRow) {
	m.rows = rows
	m.PublishRowsReset()
}
