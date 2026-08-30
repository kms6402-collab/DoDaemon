//go:build windows

package nativeui

import (
	"fmt"
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
	detail     string // filename
	startedAt  time.Time

	bytesDone  int64
	bytesTotal int64   // 0 = unknown (e.g. an upload with no declared size)
	speed      float64 // bytes/sec, derived between consecutive progress events

	prevBytes int64
	prevTime  time.Time
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
	isProgress, _ := ev.Fields["progress"].(bool)

	switch {
	case ev.Kind == eventbus.KindConnect:
		t.rows[connKey] = activeRow{source: ev.Source, remoteAddr: ev.RemoteAddr, kind: "연결", startedAt: ev.Time}
	case ev.Kind == eventbus.KindDisconnect:
		delete(t.rows, connKey)
	case ev.Kind == eventbus.KindTransfer && isProgress:
		row, ok := t.rows[key]
		if !ok {
			row = activeRow{source: ev.Source, remoteAddr: ev.RemoteAddr, kind: "전송", direction: "GET", detail: file, startedAt: ev.Time}
		}
		done := fieldInt64(ev.Fields["bytes_done"])
		total := fieldInt64(ev.Fields["bytes_total"])
		if !row.prevTime.IsZero() {
			if dt := ev.Time.Sub(row.prevTime).Seconds(); dt > 0 {
				if delta := done - row.prevBytes; delta > 0 {
					row.speed = float64(delta) / dt
				}
			}
		}
		row.bytesDone, row.bytesTotal = done, total
		row.prevBytes, row.prevTime = done, ev.Time
		t.rows[key] = row
	case ev.Kind == eventbus.KindTransfer && startPattern.MatchString(ev.Message):
		direction := "GET"
		if m := dirPattern.FindString(ev.Message); m == "PUT" || m == "업로드" {
			direction = "PUT"
		}
		t.rows[key] = activeRow{source: ev.Source, remoteAddr: ev.RemoteAddr, kind: "전송", direction: direction, detail: file, startedAt: ev.Time, prevTime: ev.Time}
	case endPattern.MatchString(ev.Message) || ev.Kind == eventbus.KindError:
		delete(t.rows, key)
	}
}

// fieldInt64 reads an eventbus.Event Fields value as int64. Values arrive
// as the Go type the publisher actually set (int64 for byte counters, see
// internal/tftp/session.go and internal/ftp/permfs.go) since nativeui
// subscribes to the in-process bus directly — no JSON round-trip like the
// web UI's SSE path, so no float64 conversion is needed, but the type
// switch stays defensive in case that ever changes.
func fieldInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
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

// get returns the single row for key (source|remoteAddr|file), used by
// consumeEvents to look up a transfer's direction when tallying RX/TX
// totals from a progress event.
func (t *activeTracker) get(key string) (activeRow, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.rows[key]
	return r, ok
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

// throughputFor sums the live speed of every active transfer for source —
// the native equivalent of app.js's throughputFor, used for the sidebar
// rate readout and the KPI strip's 처리량 tile.
func (t *activeTracker) throughputFor(source string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sum float64
	for _, r := range t.rows {
		if r.source == source && r.kind == "전송" {
			sum += r.speed
		}
	}
	return sum
}

// activeTableModel is the walk.TableModel backing the "진행 중인 전송"
// TableView. Its 6 columns (파일/클라이언트/방향/진행률/속도/남은 시간) match
// the web dashboard's active-sessions table exactly.
type activeTableModel struct {
	walk.TableModelBase
	rows []activeRow
}

func (m *activeTableModel) RowCount() int { return len(m.rows) }

func (m *activeTableModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	r := m.rows[row]
	switch col {
	case 0:
		if r.detail != "" {
			return r.detail
		}
		return "-"
	case 1:
		return r.remoteAddr
	case 2:
		return r.direction
	case 3:
		return formatProgress(r)
	case 4:
		return formatSpeed(r.speed)
	case 5:
		return formatRemaining(r)
	default:
		return ""
	}
}

func formatProgress(r activeRow) string {
	if r.bytesTotal <= 0 {
		return formatBytes(r.bytesDone)
	}
	pct := int(float64(r.bytesDone) / float64(r.bytesTotal) * 100)
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("%d%%", pct)
}

func formatSpeed(bps float64) string {
	if bps <= 0 {
		return "-"
	}
	return formatBytes(int64(bps)) + "/s"
}

func formatRemaining(r activeRow) string {
	if r.bytesTotal <= 0 || r.speed <= 0 {
		return "-"
	}
	remain := float64(r.bytesTotal-r.bytesDone) / r.speed
	if remain < 0 {
		remain = 0
	}
	m, s := int(remain)/60, int(remain)%60
	return fmt.Sprintf("%02d:%02d", m, s)
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB"}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), units[exp])
}

func (m *activeTableModel) setRows(rows []activeRow) {
	m.rows = rows
	m.PublishRowsReset()
}
