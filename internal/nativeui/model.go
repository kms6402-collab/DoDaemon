//go:build windows

package nativeui

import (
	"regexp"
	"sync"

	"github.com/lxn/walk"

	"github.com/kms6402/dodaemon/internal/eventbus"
)

const maxEventRows = 500

var warnPattern = regexp.MustCompile(`(?i)warn|재시도|retransmit|timeout|타임아웃`)

// severityOf classifies an event into the same four buckets the web
// dashboard's static/app.js uses (info/transfer/warn/error), so both UIs
// filter and color the log identically.
func severityOf(ev eventbus.Event) string {
	switch {
	case ev.Kind == eventbus.KindError:
		return "error"
	case warnPattern.MatchString(ev.Message):
		return "warn"
	case ev.Kind == eventbus.KindTransfer:
		return "transfer"
	default:
		return "info"
	}
}

// eventRow is a display-ready copy of an eventbus.Event (walk's TableView
// re-reads Value() often, so we pre-format once on push rather than on
// every paint).
type eventRow struct {
	time     string
	source   string
	kind     string
	message  string
	severity string
}

// eventTableModel feeds the live event TableView. All mutation must happen
// on the UI goroutine (callers push through Window.mw.Synchronize), so no
// extra locking is needed for the walk-facing methods, but push is kept
// safe to call from any goroutine via an internal mutex as a defensive
// backstop.
type eventTableModel struct {
	walk.TableModelBase

	mu   sync.Mutex
	rows []eventRow
}

func newEventTableModel() *eventTableModel {
	return &eventTableModel{}
}

func (m *eventTableModel) RowCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (m *eventTableModel) Value(row, col int) interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	r := m.rows[row]
	switch col {
	case 0:
		return r.time
	case 1:
		return r.source
	case 2:
		return r.kind
	case 3:
		return r.message
	default:
		return ""
	}
}

// push prepends ev (newest first, matching the web dashboard) and caps the
// row count, then tells the TableView to redraw.
func (m *eventTableModel) push(ev eventbus.Event) {
	m.mu.Lock()
	m.rows = append([]eventRow{toEventRow(ev)}, m.rows...)
	if len(m.rows) > maxEventRows {
		m.rows = m.rows[:maxEventRows]
	}
	m.mu.Unlock()

	m.PublishRowsReset()
}

func toEventRow(ev eventbus.Event) eventRow {
	return eventRow{
		time:     ev.Time.Local().Format("15:04:05"),
		source:   ev.Source,
		kind:     string(ev.Kind),
		message:  formatMessage(ev),
		severity: severityOf(ev),
	}
}

// replace swaps the whole row set at once (used when the selected service
// or level filter changes, requiring a full re-derive from the window's raw
// event buffer rather than an incremental prepend).
func (m *eventTableModel) replace(events []eventbus.Event) {
	rows := make([]eventRow, len(events))
	for i, ev := range events {
		rows[i] = toEventRow(ev)
	}
	m.mu.Lock()
	m.rows = rows
	m.mu.Unlock()
	m.PublishRowsReset()
}

// severityColor maps a severity bucket to its console color — the exact
// palette from docs/tftp_daemon_console_spec.pdf §4 Task 1.
func severityColor(sev string) walk.Color {
	switch sev {
	case "error":
		return colorTermErr
	case "warn":
		return colorTermWarn
	case "transfer":
		return colorTermOK
	default:
		return colorTermInfo
	}
}

// StyleCell renders the event log as a dark terminal panel — the one
// deliberately dark surface in an otherwise light UI (matching the web
// dashboard's .log-viewer, see internal/webui/static/style.css) — with
// each row's severity picked out in its own color, independent of the
// light theme the rest of the native window uses.
func (m *eventTableModel) StyleCell(style *walk.CellStyle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := style.Row()
	if row < 0 || row >= len(m.rows) {
		return
	}
	r := m.rows[row]
	sevColor := severityColor(r.severity)

	style.BackgroundColor = colorTermBG
	style.TextColor = colorTermText
	switch style.Col() {
	case 1: // source
		style.TextColor = colorTermInfo
	case 2: // kind
		style.TextColor = sevColor
	case 3: // message
		if r.severity == "warn" || r.severity == "error" {
			style.TextColor = sevColor
		}
	default:
		style.TextColor = colorTermDim
	}
}

func formatMessage(ev eventbus.Event) string {
	if ev.RemoteAddr == "" {
		return ev.Message
	}
	return ev.Message + " (" + ev.RemoteAddr + ")"
}
