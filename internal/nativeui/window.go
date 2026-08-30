//go:build windows

// Package nativeui is DoDaemon's native Win32 window (via lxn/walk, a
// pure-Go/syscall toolkit — no cgo, keeping the single-binary/no-C-compiler
// build story intact). It mirrors the web dashboard's title bar + sidebar +
// KPI + active-transfers + dark event-log + footer status bar layout and
// light visual style exactly (internal/webui/templates/dashboard.html,
// internal/webui/static/style.css) as the primary local UI when the exe is
// launched with no arguments; the embedded web server (internal/webui)
// still runs alongside it for remote/API access if enabled in config.
package nativeui

import (
	"fmt"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
	"github.com/kms6402/dodaemon/internal/icons"
)

var (
	// Light page/card palette — matches internal/webui/static/style.css's
	// :root tokens exactly, so the native window and the browser dashboard
	// read as the same design.
	colorPageBG    = walk.RGB(0xee, 0xf0, 0xf3) // --bg
	colorSidebarBG = walk.RGB(0xf5, 0xf6, 0xf8) // --bg-elevated
	colorCardBG    = walk.RGB(0xff, 0xff, 0xff) // --bg-card
	colorAccent    = walk.RGB(0x2f, 0x5f, 0xed) // --accent
	colorAccentDim = walk.RGB(0xe8, 0xed, 0xfd) // --accent-dim
	colorOK        = walk.RGB(0x1f, 0x9d, 0x55) // --ok
	colorDim       = walk.RGB(0x6b, 0x72, 0x80) // --text-dim
	colorFaint     = walk.RGB(0x9a, 0xa0, 0xab) // --text-faint
	colorText      = walk.RGB(0x14, 0x16, 0x1a) // --text
	colorStopBtn   = walk.RGB(0x33, 0x41, 0x5c) // --stop-btn

	// Dark terminal palette, used only by the event log panel — matches
	// style.css's --term-* tokens (the one deliberately dark surface).
	colorTermBG   = walk.RGB(0x10, 0x14, 0x1f)
	colorTermText = walk.RGB(0xe6, 0xe9, 0xef)
	colorTermDim  = walk.RGB(0x7d, 0x83, 0x98)
	colorTermInfo = walk.RGB(0x5a, 0xa9, 0xe6)
	colorTermErr  = walk.RGB(0xf2, 0x55, 0x5a)
)

// navEntry is the fixed service ordering shown in the sidebar — same order
// and Korean names as internal/webui/server.go's buildServices, so the two
// UIs present identically.
type navEntry struct{ key, name string }

var navOrder = []navEntry{
	{"tftp", "TFTP 서버"},
	{"ftp", "FTP 서버"},
	{"syslog", "Syslog 서버"},
	{"webui", "Web"},
}

// navWidgets groups the live pieces of one sidebar service row. dot is a
// Label rendering a single "●" glyph rather than a fixed-size Composite —
// an empty Composite's MinSize isn't reliably honored by walk's HBox
// layout (it can collapse to zero width), whereas a Label always sizes
// itself from its text content.
type navWidgets struct {
	container *walk.Composite
	dot       *walk.Label
	nameLbl   *walk.Label
	protoLbl  *walk.Label
	rateLbl   *walk.Label
}

// Window is the native main window. Create it, optionally call
// UpdateConfig as reloads happen, then call Run (blocks until closed).
type Window struct {
	mw   *walk.MainWindow
	icon *walk.Icon
	cfg  atomic.Pointer[config.Config]

	tracker     *activeTracker
	activeModel *activeTableModel
	logModel    *eventTableModel
	stats       *statsTracker

	evMu      sync.Mutex
	rawEvents []eventbus.Event // ring buffer, newest first, all sources
	rxTotal   int64            // bytes received by the server (uploads), session-cumulative
	txTotal   int64            // bytes sent by the server (downloads), session-cumulative

	configPath string
	serverAddr string

	nav      map[string]*navWidgets
	selected string
	level    string // "all" | "info" | "error"

	titleLbl, metaLbl, statusBadge         *walk.Label
	kpiActive, kpiCompleted, kpiThroughput *walk.Label
	kpiErrors                              *walk.Label
	btnRestart, btnStop                    *walk.PushButton
	filterAll, filterInfo, filterErr       *walk.PushButton
	autoscroll                             *walk.CheckBox
	logTableView                           *walk.TableView
	clockLbl                               *walk.Label
	fbStatusLbl, fbRxLbl, fbTxLbl          *walk.Label
	dirSection                             *walk.Composite
	dirPathLbl                             *walk.Label
	permSection                            *walk.Composite
	permRadios                             map[string]*walk.RadioButton

	unsubscribe func()
}

const maxRawEvents = 1000

// New builds and shows the window. onClose is invoked (once) when the user
// closes it, by the X button or the 종료 button alike — the caller uses it
// to trigger the app's own graceful shutdown.
func New(configPath string, initialCfg *config.Config, bus *eventbus.Bus, onClose func()) (*Window, error) {
	w := &Window{
		tracker:     newActiveTracker(),
		activeModel: &activeTableModel{},
		logModel:    newEventTableModel(),
		stats:       newStatsTracker(),
		configPath:  configPath,
		serverAddr:  localOutboundAddr(),
		nav:         make(map[string]*navWidgets),
		permRadios:  make(map[string]*walk.RadioButton),
		level:       "all",
	}
	w.cfg.Store(initialCfg)
	w.selected = w.defaultSelection()

	icon, err := walk.NewIconFromImage(icons.Draw(256))
	if err != nil {
		return nil, fmt.Errorf("nativeui: build icon: %w", err)
	}
	w.icon = icon

	baseFont := Font{PointSize: 9}
	titleFont := Font{PointSize: 9, Bold: true}

	navChildren := make([]Widget, 0, len(navOrder)+4)
	navChildren = append(navChildren, Label{Text: "서비스", Font: Font{PointSize: 8, Bold: true}, TextColor: colorFaint})
	for _, ne := range navOrder {
		ne := ne
		nw := &navWidgets{}
		w.nav[ne.key] = nw
		navChildren = append(navChildren, Composite{
			AssignTo:   &nw.container,
			Background: SolidColorBrush{Color: colorSidebarBG},
			Layout:     HBox{Margins: Margins{Left: 8, Top: 7, Right: 8, Bottom: 7}, Spacing: 6},
			Children: []Widget{
				Label{AssignTo: &nw.dot, Text: "●", TextColor: colorFaint, Font: Font{PointSize: 9}},
				Composite{
					Layout: VBox{MarginsZero: true, SpacingZero: true},
					Children: []Widget{
						Label{AssignTo: &nw.nameLbl, Text: ne.name, Font: Font{PointSize: 9, Bold: true}},
						Label{AssignTo: &nw.protoLbl, Text: "-", Font: Font{PointSize: 8}, TextColor: colorFaint},
					},
				},
				HSpacer{},
				Label{AssignTo: &nw.rateLbl, Text: "—", Font: Font{PointSize: 8}, TextColor: colorDim},
			},
		})
	}

	var rwRadio, roRadio, woRadio *walk.RadioButton
	permWidgets := []Widget{
		RadioButton{AssignTo: &rwRadio, Text: "읽기·쓰기 허용", OnClicked: func() { w.setTftpPermMode("rw") }},
		RadioButton{AssignTo: &roRadio, Text: "읽기 전용", OnClicked: func() { w.setTftpPermMode("ro") }},
		RadioButton{AssignTo: &woRadio, Text: "쓰기 전용", OnClicked: func() { w.setTftpPermMode("wo") }},
	}

	navChildren = append(navChildren,
		Composite{MinSize: Size{Height: 10}},
		Composite{
			AssignTo: &w.dirSection,
			Layout:   VBox{MarginsZero: true, Spacing: 4},
			Children: []Widget{
				Label{Text: "디렉터리", Font: Font{PointSize: 8, Bold: true}, TextColor: colorFaint},
				Label{AssignTo: &w.dirPathLbl, Text: "-", Font: Font{PointSize: 8}, TextColor: colorText},
				Composite{
					Layout: HBox{MarginsZero: true, Spacing: 6},
					Children: []Widget{
						PushButton{Text: "변경", OnClicked: w.onDirChange},
						PushButton{Text: "열기", OnClicked: w.onDirOpen},
					},
				},
			},
		},
		Composite{
			AssignTo: &w.permSection,
			Layout:   VBox{MarginsZero: true, Spacing: 2},
			Children: append([]Widget{Label{Text: "권한", Font: Font{PointSize: 8, Bold: true}, TextColor: colorFaint}}, permWidgets...),
		},
		VSpacer{},
		PushButton{Text: "⚙ 설정", OnClicked: w.openSettings},
		PushButton{Text: "데이터 폴더 열기", OnClicked: w.openDataDir},
		PushButton{Text: "종료", OnClicked: func() { w.mw.Close() }},
	)

	err = MainWindow{
		AssignTo:   &w.mw,
		Title:      "DoDaemon",
		Icon:       icon,
		Font:       baseFont,
		Background: SolidColorBrush{Color: colorPageBG},
		MinSize:    Size{Width: 980, Height: 640},
		Size:       Size{Width: 1200, Height: 780},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		Children: []Widget{
			Composite{
				Background: SolidColorBrush{Color: colorSidebarBG},
				MaxSize:    Size{Height: 34},
				Layout:     HBox{Margins: Margins{Left: 14, Top: 6, Right: 14, Bottom: 6}, Spacing: 8},
				Children: []Widget{
					Label{Text: "□", Font: Font{PointSize: 8}, TextColor: colorFaint},
					Label{Text: "□", Font: Font{PointSize: 8}, TextColor: colorFaint},
					Label{Text: "□", Font: Font{PointSize: 8}, TextColor: colorFaint},
					Label{Text: "DoDaeMon", Font: Font{PointSize: 10, Bold: true}},
					Label{Text: "TFTP · FTP · SYSLOG · WEB 서버 콘솔", Font: Font{PointSize: 8}, TextColor: colorFaint},
					HSpacer{},
					Label{AssignTo: &w.clockLbl, Text: "-", Font: Font{PointSize: 8}, TextColor: colorDim},
				},
			},
			Composite{
				StretchFactor: 1,
				Layout:        HBox{MarginsZero: true, SpacingZero: true},
				Children: []Widget{
					Composite{
						Background: SolidColorBrush{Color: colorSidebarBG},
						MinSize:    Size{Width: 230},
						MaxSize:    Size{Width: 230},
						Layout:     VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 4},
						Children:   navChildren,
					},
					Composite{
						StretchFactor: 1,
						Background:    SolidColorBrush{Color: colorPageBG},
						Layout:        VBox{Margins: Margins{Left: 24, Top: 16, Right: 28, Bottom: 16}, Spacing: 10},
						Children: []Widget{
							Label{Text: "패킷 서비스", Font: Font{PointSize: 8, Bold: true}, TextColor: colorAccent},
							Composite{
								Layout: HBox{MarginsZero: true},
								Children: []Widget{
									Composite{
										Layout: VBox{MarginsZero: true, SpacingZero: true},
										Children: []Widget{
											Label{AssignTo: &w.titleLbl, Text: "-", Font: Font{PointSize: 16, Bold: true}},
											Label{AssignTo: &w.metaLbl, Text: "-", Font: Font{PointSize: 9}, TextColor: colorDim},
										},
									},
									HSpacer{},
									Label{AssignTo: &w.statusBadge, Text: "확인 중", Font: Font{PointSize: 8, Bold: true}, TextColor: colorDim},
									PushButton{AssignTo: &w.btnRestart, Text: "재시작", OnClicked: w.onRestart},
									PushButton{AssignTo: &w.btnStop, Text: "정지", Background: SolidColorBrush{Color: colorStopBtn}, OnClicked: w.onStop},
								},
							},
							Composite{
								Layout: HBox{MarginsZero: true},
								Children: []Widget{
									kpiTile(&w.kpiActive, "활성 전송"),
									kpiTile(&w.kpiCompleted, "완료"),
									kpiTile(&w.kpiThroughput, "처리량 (MB/s)"),
									kpiTile(&w.kpiErrors, "오류"),
								},
							},
							Composite{
								Border:     true,
								Background: SolidColorBrush{Color: colorCardBG},
								Layout:     VBox{MarginsZero: true},
								Children: []Widget{
									Label{Text: "진행 중인 전송", Font: titleFont, Background: SolidColorBrush{Color: colorCardBG}},
									TableView{
										Model:               w.activeModel,
										LastColumnStretched: true,
										MinSize:             Size{Height: 130},
										MaxSize:             Size{Height: 130},
										Columns: []TableViewColumn{
											{Title: "파일", Width: 200},
											{Title: "클라이언트", Width: 110},
											{Title: "방향", Width: 60},
											{Title: "진행률", Width: 70},
											{Title: "속도", Width: 90},
											{Title: "남은 시간"},
										},
									},
								},
							},
							Composite{
								Border:        true,
								Background:    SolidColorBrush{Color: colorTermBG},
								Layout:        VBox{MarginsZero: true},
								StretchFactor: 1,
								Children: []Widget{
									Composite{
										Background: SolidColorBrush{Color: colorTermBG},
										Layout:     HBox{Margins: Margins{Left: 12, Top: 8, Right: 12, Bottom: 8}, Spacing: 6},
										Children: []Widget{
											Label{Text: "이벤트 로그", Font: titleFont, TextColor: colorTermText},
											PushButton{AssignTo: &w.filterAll, Text: "전체", OnClicked: func() { w.setLevelFilter("all") }},
											PushButton{AssignTo: &w.filterInfo, Text: "INFO", OnClicked: func() { w.setLevelFilter("info") }},
											PushButton{AssignTo: &w.filterErr, Text: "ERROR", OnClicked: func() { w.setLevelFilter("error") }},
											HSpacer{},
											CheckBox{AssignTo: &w.autoscroll, Text: "자동 스크롤", Checked: true},
										},
									},
									TableView{
										AssignTo:            &w.logTableView,
										Model:               w.logModel,
										LastColumnStretched: true,
										StretchFactor:       1,
										Columns: []TableViewColumn{
											{Title: "시간", Width: 80},
											{Title: "소스", Width: 70},
											{Title: "종류", Width: 90},
											{Title: "메시지"},
										},
									},
								},
							},
						},
					},
				},
			},
			Composite{
				Background: SolidColorBrush{Color: colorSidebarBG},
				MaxSize:    Size{Height: 26},
				Layout:     HBox{Margins: Margins{Left: 14, Top: 4, Right: 14, Bottom: 4}, Spacing: 12},
				Children: []Widget{
					Label{AssignTo: &w.fbStatusLbl, Text: "-", Font: Font{PointSize: 8}, TextColor: colorFaint},
					HSpacer{},
					Label{AssignTo: &w.fbRxLbl, Text: "RX 0 B", Font: Font{PointSize: 8}, TextColor: colorFaint},
					Label{AssignTo: &w.fbTxLbl, Text: "TX 0 B", Font: Font{PointSize: 8}, TextColor: colorFaint},
				},
			},
		},
	}.Create()
	if err != nil {
		return nil, fmt.Errorf("nativeui: create window: %w", err)
	}

	w.permRadios["rw"], w.permRadios["ro"], w.permRadios["wo"] = rwRadio, roRadio, woRadio

	enableDarkTitleBar(w.mw.Handle())

	closed := false
	w.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if !closed && onClose != nil {
			closed = true
			onClose()
		}
	})

	for key, nw := range w.nav {
		key := key
		nw.container.MouseDown().Attach(func(x, y int, button walk.MouseButton) { w.selectService(key) })
		nw.nameLbl.MouseDown().Attach(func(x, y int, button walk.MouseButton) { w.selectService(key) })
		nw.protoLbl.MouseDown().Attach(func(x, y int, button walk.MouseButton) { w.selectService(key) })
	}

	ch, unsub := bus.Subscribe(256)
	w.unsubscribe = unsub
	go w.consumeEvents(ch)
	go w.tickClock()

	w.setLevelFilter("all")
	w.refreshNav()
	w.refreshDetail()

	return w, nil
}

func kpiTile(assign **walk.Label, label string) Composite {
	return Composite{
		StretchFactor: 1,
		Layout:        VBox{MarginsZero: true, SpacingZero: true},
		Children: []Widget{
			Label{AssignTo: assign, Text: "0", Font: Font{PointSize: 16, Bold: true}},
			Label{Text: label, Font: Font{PointSize: 8}, TextColor: colorDim},
		},
	}
}

// Run blocks until the window is closed and returns its exit code.
func (w *Window) Run() int {
	return w.mw.Run()
}

// Close programmatically closes the window (used by the app's own shutdown
// path if it needs to tear the UI down for a reason other than the user
// clicking 종료/X, e.g. a fatal startup error elsewhere).
func (w *Window) Close() {
	w.mw.Synchronize(func() { w.mw.Close() })
}

// UpdateConfig applies a hot-reloaded configuration to the sidebar and
// detail panel.
func (w *Window) UpdateConfig(cfg *config.Config) {
	w.cfg.Store(cfg)
	w.mw.Synchronize(func() {
		w.refreshNav()
		w.refreshDetail()
	})
}

func (w *Window) tickClock() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now().Format("15:04:05")
		w.mw.Synchronize(func() {
			if w.clockLbl != nil {
				w.clockLbl.SetText(w.serverAddr + "  |  " + now)
			}
		})
	}
}

// localOutboundAddr returns this machine's LAN IP (best-effort, no packets
// actually sent — UDP "connect" just resolves routing), for the title
// bar's address readout; falls back to loopback if nothing routable exists.
func localOutboundAddr() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return "127.0.0.1"
}

func (w *Window) defaultSelection() string {
	cfg := w.cfg.Load()
	for _, ne := range navOrder {
		if enabled, _, _, _, _ := serviceMeta(cfg, ne.key); enabled {
			return ne.key
		}
	}
	return navOrder[0].key
}

func (w *Window) selectService(key string) {
	if key == w.selected {
		return
	}
	w.selected = key
	w.refreshNav()
	w.refreshDetail()
}

func (w *Window) setLevelFilter(level string) {
	w.level = level
	activeBrush, _ := walk.NewSolidColorBrush(colorAccentDim)
	idleBrush, _ := walk.NewSolidColorBrush(colorCardBG)
	for _, btn := range []*walk.PushButton{w.filterAll, w.filterInfo, w.filterErr} {
		btn.SetBackground(idleBrush)
	}
	switch level {
	case "info":
		w.filterInfo.SetBackground(activeBrush)
	case "error":
		w.filterErr.SetBackground(activeBrush)
	default:
		w.filterAll.SetBackground(activeBrush)
	}
	w.rebuildLog()
}

// consumeEvents is the single reader of the event bus for the native UI —
// it updates the active-session tracker and per-service stats for every
// event (regardless of which service is selected) but only touches the
// visible TableViews when the event belongs to the currently selected
// service, mirroring internal/webui/static/app.js's handleEvent. It also
// accumulates session RX/TX totals from progress-event byte deltas, same
// idea as app.js's rxTotal/txTotal.
func (w *Window) consumeEvents(ch <-chan eventbus.Event) {
	lastBytes := make(map[string]int64) // this goroutine is the sole writer/reader — no lock needed
	for ev := range ch {
		ev := ev
		w.tracker.apply(ev)
		w.stats.record(ev)

		if isProgress, _ := ev.Fields["progress"].(bool); isProgress {
			file, _ := ev.Fields["file"].(string)
			key := ev.Source + "|" + ev.RemoteAddr + "|" + file
			done := fieldInt64(ev.Fields["bytes_done"])
			if delta := done - lastBytes[key]; delta > 0 {
				if row, ok := w.tracker.get(key); ok && row.direction == "PUT" {
					atomic.AddInt64(&w.rxTotal, delta)
				} else {
					atomic.AddInt64(&w.txTotal, delta)
				}
			}
			lastBytes[key] = done
		}

		w.evMu.Lock()
		w.rawEvents = append([]eventbus.Event{ev}, w.rawEvents...)
		if len(w.rawEvents) > maxRawEvents {
			w.rawEvents = w.rawEvents[:maxRawEvents]
		}
		w.evMu.Unlock()

		w.mw.Synchronize(func() {
			if ev.Source == w.selected {
				w.activeModel.setRows(w.tracker.snapshotFor(w.selected))
				if w.passesLevel(ev) {
					w.logModel.push(ev)
					if w.autoscroll.Checked() {
						w.logTableView.EnsureItemVisible(0)
					}
				}
				w.refreshKPIs()
			}
			w.refreshSidebarRates()
			w.refreshFooter()
		})
	}
}

func (w *Window) passesLevel(ev eventbus.Event) bool {
	switch w.level {
	case "info":
		return ev.Kind != eventbus.KindError
	case "error":
		return ev.Kind == eventbus.KindError
	default:
		return true
	}
}

// rebuildLog re-derives the active-session and event-log views from scratch
// for the currently selected service — used on selection or filter change,
// where an incremental update isn't enough.
func (w *Window) rebuildLog() {
	if w.logModel == nil {
		return
	}
	w.evMu.Lock()
	filtered := make([]eventbus.Event, 0, len(w.rawEvents))
	for _, ev := range w.rawEvents {
		if ev.Source == w.selected && w.passesLevel(ev) {
			filtered = append(filtered, ev)
		}
	}
	w.evMu.Unlock()
	w.logModel.replace(filtered)
	w.activeModel.setRows(w.tracker.snapshotFor(w.selected))
	w.refreshKPIs()
}

func (w *Window) refreshNav() {
	cfg := w.cfg.Load()
	for _, ne := range navOrder {
		nw := w.nav[ne.key]
		enabled, protocol, _, _, _ := serviceMeta(cfg, ne.key)

		dotColor := colorFaint
		if enabled {
			dotColor = colorOK
		}
		nw.dot.SetTextColor(dotColor)

		bg := colorSidebarBG
		nameColor := colorText
		if ne.key == w.selected {
			bg = colorCardBG
			nameColor = colorAccent
		}
		if brush, err := walk.NewSolidColorBrush(bg); err == nil {
			nw.container.SetBackground(brush)
		}
		nw.nameLbl.SetTextColor(nameColor)
		nw.protoLbl.SetText(protocol)
	}
	w.refreshSidebarRates()
	w.refreshDirPanel()
}

func (w *Window) refreshSidebarRates() {
	for key, nw := range w.nav {
		bps := w.tracker.throughputFor(key)
		if bps > 0 {
			nw.rateLbl.SetText(formatBytes(int64(bps)) + "/s")
		} else {
			nw.rateLbl.SetText("—")
		}
	}
}

func (w *Window) refreshDirPanel() {
	cfg := w.cfg.Load()
	_, _, _, _, dir := serviceMeta(cfg, w.selected)

	if dir == "" {
		w.dirSection.SetVisible(false)
	} else {
		w.dirSection.SetVisible(true)
		w.dirPathLbl.SetText(dir)
	}

	if w.selected == "tftp" {
		w.permSection.SetVisible(true)
		mode := "ro"
		if cfg.TFTP.AllowRead && cfg.TFTP.AllowWrite {
			mode = "rw"
		} else if cfg.TFTP.AllowWrite {
			mode = "wo"
		}
		for value, rb := range w.permRadios {
			if rb != nil {
				rb.SetChecked(value == mode)
			}
		}
	} else {
		w.permSection.SetVisible(false)
	}
}

func (w *Window) refreshDetail() {
	cfg := w.cfg.Load()
	ne := navOrder[0]
	for _, e := range navOrder {
		if e.key == w.selected {
			ne = e
			break
		}
	}
	enabled, _, _, meta, _ := serviceMeta(cfg, w.selected)

	w.titleLbl.SetText(ne.name)
	w.metaLbl.SetText(meta)
	if enabled {
		w.statusBadge.SetText("실행 중")
		w.statusBadge.SetTextColor(colorOK)
	} else {
		w.statusBadge.SetText("중지됨")
		w.statusBadge.SetTextColor(colorFaint)
	}

	w.refreshDirPanel()
	w.rebuildLog()
	w.refreshFooter()
}

func (w *Window) refreshKPIs() {
	st := w.stats.snapshot(w.selected)
	w.kpiActive.SetText(fmt.Sprintf("%d", len(w.tracker.snapshotFor(w.selected))))
	w.kpiCompleted.SetText(fmt.Sprintf("%d", st.completed))
	w.kpiErrors.SetText(fmt.Sprintf("%d", st.errors))
	tp := w.tracker.throughputFor(w.selected) / (1024 * 1024)
	w.kpiThroughput.SetText(fmt.Sprintf("%.1f", tp))
}

func (w *Window) refreshFooter() {
	cfg := w.cfg.Load()
	enabled, _, _, _, _ := serviceMeta(cfg, w.selected)
	ne := navOrder[0]
	for _, e := range navOrder {
		if e.key == w.selected {
			ne = e
			break
		}
	}
	status := "중지됨"
	if enabled {
		status = "정상 · 리스너 응답 중"
	}
	w.fbStatusLbl.SetText(ne.name + " " + status)
	w.fbRxLbl.SetText("RX " + formatBytes(atomic.LoadInt64(&w.rxTotal)))
	w.fbTxLbl.SetText("TX " + formatBytes(atomic.LoadInt64(&w.txTotal)))
}

func (w *Window) openDataDir() {
	dir := w.cfg.Load().Server.DataDir
	exec.Command("cmd", "/c", "start", "", dir).Start()
}

func (w *Window) onDirOpen() {
	cfg := w.cfg.Load()
	_, _, _, _, dir := serviceMeta(cfg, w.selected)
	if dir == "" {
		return
	}
	exec.Command("cmd", "/c", "start", "", dir).Start()
}

func (w *Window) onDirChange() {
	cfg := w.cfg.Load()
	_, _, _, _, dir := serviceMeta(cfg, w.selected)
	dlg := walk.FileDialog{Title: "폴더 찾기", InitialDirPath: dir}
	ok, err := dlg.ShowBrowseFolder(w.mw)
	if err != nil || !ok || dlg.FilePath == "" {
		return
	}
	w.applyConfigChange(func(c *config.Config) {
		switch w.selected {
		case "tftp":
			c.TFTP.RootDir = dlg.FilePath
		case "ftp":
			c.FTP.AnonymousHomeDir = dlg.FilePath
		case "syslog":
			c.Syslog.LogDir = dlg.FilePath
		}
	})
}

func (w *Window) setTftpPermMode(mode string) {
	w.applyConfigChange(func(c *config.Config) {
		c.TFTP.AllowRead = mode == "rw" || mode == "ro"
		c.TFTP.AllowWrite = mode == "rw" || mode == "wo"
	})
}

func (w *Window) onStop() {
	w.setServiceEnabled(w.selected, false)
}

func (w *Window) onRestart() {
	w.setServiceEnabled(w.selected, false)
	time.AfterFunc(400*time.Millisecond, func() {
		w.mw.Synchronize(func() { w.setServiceEnabled(w.selected, true) })
	})
}

func (w *Window) setServiceEnabled(key string, enabled bool) {
	w.applyConfigChange(func(c *config.Config) {
		switch key {
		case "tftp":
			c.TFTP.Enabled = enabled
		case "ftp":
			c.FTP.Enabled = enabled
		case "syslog":
			c.Syslog.Enabled = enabled
		case "webui":
			c.WebUI.Enabled = enabled
		}
	})
}

// applyConfigChange loads the current config, applies mutate, validates and
// saves — the config.Watcher already running in the supervisor picks up the
// write and hot-reloads exactly like an external edit or the settings
// dialog's own save, so the dashboard's quick controls need no direct link
// to the supervisor.
func (w *Window) applyConfigChange(mutate func(*config.Config)) {
	newCfg := *w.cfg.Load()
	mutate(&newCfg)
	if err := newCfg.Validate(); err != nil {
		walk.MsgBox(w.mw, "오류", err.Error(), walk.MsgBoxIconError)
		return
	}
	if err := config.Save(w.configPath, &newCfg); err != nil {
		walk.MsgBox(w.mw, "저장 실패", err.Error(), walk.MsgBoxIconError)
	}
}

// serviceMeta computes the same display fields as internal/webui/server.go's
// buildServices, kept in sync by hand since nativeui (windows-only) and
// webui (cross-platform) can't share an unexported type across packages.
// dir is the root/home/log directory for the sidebar quick-editor, empty
// for webui.
func serviceMeta(cfg *config.Config, key string) (enabled bool, protocol, listen, meta, dir string) {
	switch key {
	case "tftp":
		_, port := splitAddr(cfg.TFTP.Listen)
		writeState := "다운로드만"
		if cfg.TFTP.AllowWrite {
			writeState = "업로드 허용"
		}
		return cfg.TFTP.Enabled, "UDP " + port, cfg.TFTP.Listen,
			fmt.Sprintf("udp/%s · 루트 %s · %s", port, cfg.TFTP.RootDir, writeState), cfg.TFTP.RootDir

	case "ftp":
		_, port := splitAddr(cfg.FTP.Listen)
		dir := cfg.FTP.AnonymousHomeDir
		if dir == "" && len(cfg.FTP.Users) > 0 {
			dir = cfg.FTP.Users[0].HomeDir
		}
		return cfg.FTP.Enabled, "TCP " + port, cfg.FTP.Listen,
			fmt.Sprintf("tcp/%s · 패시브 %d-%d · 계정 %d개", port, cfg.FTP.PassivePortRange[0], cfg.FTP.PassivePortRange[1], len(cfg.FTP.Users)), dir

	case "syslog":
		addr := cfg.Syslog.UDPListen
		if addr == "" {
			addr = cfg.Syslog.TCPListen
		}
		proto := "UDP"
		detail := "udp " + cfg.Syslog.UDPListen
		switch {
		case cfg.Syslog.UDPListen != "" && cfg.Syslog.TCPListen != "":
			proto = "UDP/TCP"
			detail = fmt.Sprintf("udp %s · tcp %s", cfg.Syslog.UDPListen, cfg.Syslog.TCPListen)
		case cfg.Syslog.TCPListen != "":
			proto = "TCP"
			detail = "tcp " + cfg.Syslog.TCPListen
		}
		return cfg.Syslog.Enabled, proto, addr,
			fmt.Sprintf("%s · 보관 %d일", detail, cfg.Syslog.Rotate.MaxAgeDay), cfg.Syslog.LogDir

	case "webui":
		_, port := splitAddr(cfg.WebUI.Listen)
		return cfg.WebUI.Enabled, "HTTP " + port, cfg.WebUI.Listen,
			fmt.Sprintf("http://%s · 관리자 %s", cfg.WebUI.Listen, cfg.WebUI.Auth.Username), ""

	default:
		return false, "", "", "", ""
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
