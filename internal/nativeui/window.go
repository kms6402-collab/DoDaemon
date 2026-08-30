//go:build windows

// Package nativeui is DoDaemon's native Win32 window (via lxn/walk, a
// pure-Go/syscall toolkit — no cgo, keeping the single-binary/no-C-compiler
// build story intact). It mirrors the web dashboard's sidebar + KPI +
// active-sessions + dark event-log layout and light visual style exactly
// (internal/webui/templates/dashboard.html, internal/webui/static/style.css)
// as the primary local UI when the exe is launched with no arguments; the
// embedded web server (internal/webui) still runs alongside it for
// remote/API access if enabled in config.
package nativeui

import (
	"fmt"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"

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

	configPath string

	nav      map[string]*navWidgets
	selected string
	level    string // "all" | "info" | "error"

	titleLbl, metaLbl, statusBadge          *walk.Label
	kpiActive, kpiTotal, kpiErrors, kpiLast *walk.Label
	filterAll, filterInfo, filterErr        *walk.PushButton
	autoscroll                              *walk.CheckBox
	logTableView                            *walk.TableView

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
		nav:         make(map[string]*navWidgets),
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
	navChildren = append(navChildren,
		Composite{
			Layout: HBox{MarginsZero: true, Spacing: 6},
			Children: []Widget{
				Label{Text: "●", TextColor: colorOK, Font: Font{PointSize: 9}},
				Label{Text: "DoDaeMon", Font: Font{PointSize: 11, Bold: true}},
			},
		},
		Label{Text: "TFTP · FTP · SYSLOG 서버 콘솔", Font: Font{PointSize: 8}, TextColor: colorFaint},
		Composite{MinSize: Size{Height: 10}},
		Label{Text: "서비스", Font: Font{PointSize: 8, Bold: true}, TextColor: colorFaint},
	)
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
			},
		})
	}
	navChildren = append(navChildren,
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
		MinSize:    Size{Width: 960, Height: 620},
		Size:       Size{Width: 1200, Height: 780},
		Layout:     HBox{MarginsZero: true, SpacingZero: true},
		Children: []Widget{
			Composite{
				Background: SolidColorBrush{Color: colorSidebarBG},
				MinSize:    Size{Width: 220},
				MaxSize:    Size{Width: 220},
				Layout:     VBox{Margins: Margins{Left: 12, Top: 16, Right: 12, Bottom: 12}, Spacing: 4},
				Children:   navChildren,
			},
			Composite{
				StretchFactor: 1,
				Background:    SolidColorBrush{Color: colorPageBG},
				Layout:        VBox{Margins: Margins{Left: 24, Top: 20, Right: 28, Bottom: 20}, Spacing: 12},
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
						},
					},
					Composite{
						Layout: HBox{Spacing: 12},
						Children: []Widget{
							kpiCard(&w.kpiActive, "활성 연결"),
							kpiCard(&w.kpiTotal, "총 연결 (세션 중)"),
							kpiCard(&w.kpiErrors, "오류"),
							kpiCard(&w.kpiLast, "마지막 활동"),
						},
					},
					Label{Text: "활성 세션", Font: titleFont},
					Composite{
						Border:     true,
						Background: SolidColorBrush{Color: colorCardBG},
						Layout:     VBox{MarginsZero: true},
						Children: []Widget{
							TableView{
								Model:               w.activeModel,
								LastColumnStretched: true,
								MinSize:             Size{Height: 120},
								MaxSize:             Size{Height: 120},
								Columns: []TableViewColumn{
									{Title: "클라이언트", Width: 140},
									{Title: "방향/종류", Width: 90},
									{Title: "파일/내용", Width: 220},
									{Title: "시작 시각"},
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
	}.Create()
	if err != nil {
		return nil, fmt.Errorf("nativeui: create window: %w", err)
	}

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

	w.setLevelFilter("all")
	w.refreshNav()
	w.refreshDetail()

	return w, nil
}

func kpiCard(assign **walk.Label, label string) Composite {
	return Composite{
		Border:        true,
		Background:    SolidColorBrush{Color: colorCardBG},
		StretchFactor: 1,
		Layout:        VBox{Margins: Margins{Left: 14, Top: 10, Right: 14, Bottom: 10}, SpacingZero: true},
		Children: []Widget{
			Label{AssignTo: assign, Text: "0", Font: Font{PointSize: 18, Bold: true}},
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

func (w *Window) defaultSelection() string {
	cfg := w.cfg.Load()
	for _, ne := range navOrder {
		if enabled, _, _, _ := serviceMeta(cfg, ne.key); enabled {
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
// service, mirroring internal/webui/static/app.js's handleEvent.
func (w *Window) consumeEvents(ch <-chan eventbus.Event) {
	for ev := range ch {
		ev := ev
		w.tracker.apply(ev)
		w.stats.record(ev)

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
		enabled, protocol, _, _ := serviceMeta(cfg, ne.key)

		dotColor := colorFaint
		if enabled {
			dotColor = colorOK
		}
		nw.dot.SetTextColor(dotColor)

		bg := colorSidebarBG
		nameColor := walk.RGB(0x14, 0x16, 0x1a) // --text
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
	enabled, _, _, meta := serviceMeta(cfg, w.selected)

	w.titleLbl.SetText(ne.name)
	w.metaLbl.SetText(meta)
	if enabled {
		w.statusBadge.SetText("● 실행 중")
		w.statusBadge.SetTextColor(colorOK)
	} else {
		w.statusBadge.SetText("● 중지됨")
		w.statusBadge.SetTextColor(colorFaint)
	}

	w.rebuildLog()
}

func (w *Window) refreshKPIs() {
	st := w.stats.snapshot(w.selected)
	w.kpiActive.SetText(fmt.Sprintf("%d", len(w.tracker.snapshotFor(w.selected))))
	w.kpiTotal.SetText(fmt.Sprintf("%d", st.total))
	w.kpiErrors.SetText(fmt.Sprintf("%d", st.errors))
	if st.lastActivity.IsZero() {
		w.kpiLast.SetText("-")
	} else {
		w.kpiLast.SetText(st.lastActivity.Local().Format("15:04:05"))
	}
}

func (w *Window) openDataDir() {
	dir := w.cfg.Load().Server.DataDir
	exec.Command("cmd", "/c", "start", "", dir).Start()
}

// serviceMeta computes the same display fields as internal/webui/server.go's
// buildServices, kept in sync by hand since nativeui (windows-only) and
// webui (cross-platform) can't share an unexported type across packages.
func serviceMeta(cfg *config.Config, key string) (enabled bool, protocol, listen, meta string) {
	switch key {
	case "tftp":
		_, port := splitAddr(cfg.TFTP.Listen)
		writeState := "다운로드만"
		if cfg.TFTP.AllowWrite {
			writeState = "업로드 허용"
		}
		return cfg.TFTP.Enabled, "UDP " + port, cfg.TFTP.Listen,
			fmt.Sprintf("udp/%s · 루트 %s · %s", port, cfg.TFTP.RootDir, writeState)

	case "ftp":
		_, port := splitAddr(cfg.FTP.Listen)
		return cfg.FTP.Enabled, "TCP " + port, cfg.FTP.Listen,
			fmt.Sprintf("tcp/%s · 패시브 %d-%d · 계정 %d개", port, cfg.FTP.PassivePortRange[0], cfg.FTP.PassivePortRange[1], len(cfg.FTP.Users))

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
			fmt.Sprintf("%s · 보관 %d일", detail, cfg.Syslog.Rotate.MaxAgeDay)

	case "webui":
		_, port := splitAddr(cfg.WebUI.Listen)
		return cfg.WebUI.Enabled, "HTTP " + port, cfg.WebUI.Listen,
			fmt.Sprintf("http://%s · 관리자 %s", cfg.WebUI.Listen, cfg.WebUI.Auth.Username)

	default:
		return false, "", "", ""
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
