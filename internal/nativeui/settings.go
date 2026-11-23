//go:build windows

package nativeui

import (
	"fmt"
	"os"
	"strings"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/kms6402/dodaemon/internal/auth"
	"github.com/kms6402/dodaemon/internal/autostart"
	"github.com/kms6402/dodaemon/internal/config"
)

// permCodes are the FTP/TFTP-style permission letters internal/auth stores
// and checks byte-by-byte; both UIs present them as a checkbox list instead
// of a raw string (see internal/webui/static/settings.js's PERM_CODES,
// which must stay in sync with this set).
var permCodes = []struct{ code, label string }{
	{"e", "디렉터리 이동"},
	{"l", "목록 조회"},
	{"r", "다운로드"},
	{"a", "이어받기"},
	{"d", "삭제"},
	{"f", "이름변경"},
	{"m", "폴더생성"},
	{"w", "업로드"},
	{"M", "권한변경"},
	{"T", "시간변경"},
}

const permsFull = "elradfmwMT"
const permsReadOnly = "lr"

// browseFolder opens the native "폴더 찾기" dialog seeded at edit's current
// text and writes the chosen path back into it — the native-GUI equivalent
// of the web UI's /api/browse modal (internal/webui/browse.go). Uses our
// own browseForFolder (browsefolder_windows.go) rather than lxn/walk's
// FileDialog.ShowBrowseFolder, which locks navigation to whatever drive
// the seed path is on — see that file's doc comment.
func browseFolder(owner walk.Form, edit *walk.LineEdit) {
	if path, ok := browseForFolder(owner.Handle(), "폴더 찾기", edit.Text()); ok {
		edit.SetText(path)
	}
}

// browseFile opens a standard native "파일 찾기" open dialog (unlike
// folder browsing, walk.FileDialog.ShowOpen uses GetOpenFileName's own
// InitialDir handling, which doesn't have ShowBrowseFolder's drive-lock
// bug — see browsefolder_windows.go's doc comment — so no custom wrapper
// is needed here) seeded at edit's current file, and writes the chosen
// path back into it. Used for the TLS cert/key fields.
func browseFile(owner walk.Form, edit *walk.LineEdit, filter string) {
	// GetOpenFileName (without OFN_NOCHANGEDIR, which walk doesn't set)
	// changes the process working directory to wherever the user browsed.
	// Every relative path in the config (./data/tftp, ./logs, ...) and the
	// SFTP host key resolve against the cwd, so without restoring it
	// DoDaemon would silently start creating its folders next to the
	// picked certificate instead of next to the exe.
	if cwd, err := os.Getwd(); err == nil {
		defer os.Chdir(cwd)
	}
	dlg := &walk.FileDialog{Title: "파일 찾기", Filter: filter, FilePath: edit.Text()}
	if ok, err := dlg.ShowOpen(owner); err == nil && ok {
		edit.SetText(dlg.FilePath)
	}
}

// addBtnFunc matches the signature of the addBtn closure each dialog
// builds locally (see openSettings/editFTPUserDialog): it creates one
// styledButton, defers its click-wiring until after the dialog exists,
// and returns the widget to place in the declarative tree.
type addBtnFunc func(text string, bg walk.Color, onClick func()) Widget

// newAddBtn returns an addBtn closure plus the slice it appends deferred
// wiring calls to — callers must invoke every entry in that slice once
// their dialog's Create() has returned real widgets to attach clicks to.
func newAddBtn() (addBtnFunc, *[]func()) {
	deferred := make([]func(), 0, 8)
	addBtn := func(text string, bg walk.Color, onClick func()) Widget {
		var box *walk.Composite
		var lbl *walk.Label
		widget := styledButton(&box, &lbl, text, bg)
		deferred = append(deferred, func() { wireButtonClick(box, lbl, onClick) })
		return widget
	}
	return addBtn, &deferred
}

// pathField builds a folder LineEdit paired with a "폴더 찾기" button.
// owner is a pointer to the dialog's own *walk.Dialog variable — at the
// time this is called (building the declarative tree) that variable is
// still nil, since Dialog.AssignTo only populates it once Run() actually
// creates the window; capturing **walk.Dialog defers the dereference to
// click time, by which point it's set.
func pathField(edit **walk.LineEdit, initial string, owner **walk.Dialog, addBtn addBtnFunc) Widget {
	return Composite{
		Layout: HBox{MarginsZero: true},
		Children: []Widget{
			LineEdit{AssignTo: edit, Text: initial, Font: Font{Family: fontFamily, PointSize: 9}},
			addBtn("폴더 찾기", colorBtnNeutral, func() { browseFolder(*owner, *edit) }),
		},
	}
}

// certKeyField mirrors pathField for a TLS certificate/key file instead of
// a directory — same deferred-click plumbing, but backed by browseFile.
func certKeyField(edit **walk.LineEdit, initial string, owner **walk.Dialog, addBtn addBtnFunc) Widget {
	// Win32's OPENFILENAME filter is NUL-separated description/pattern
	// pairs (walk.FileDialog.show copies this string as-is, with no '|'
	// translation), not the '|'-delimited shorthand some other toolkits
	// accept.
	const filter = "인증서/키 파일 (*.crt;*.cer;*.pem;*.key)\x00*.crt;*.cer;*.pem;*.key\x00모든 파일 (*.*)\x00*.*\x00"
	return Composite{
		Layout: HBox{MarginsZero: true},
		Children: []Widget{
			LineEdit{AssignTo: edit, Text: initial, Font: Font{Family: fontFamily, PointSize: 9}},
			addBtn("찾기", colorBtnNeutral, func() { browseFile(*owner, *edit, filter) }),
		},
	}
}

func joinAllowlist(entries []string) string { return strings.Join(entries, "\r\n") }

func splitAllowlist(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// cancelProxyButton is an invisible real PushButton whose only job is
// keeping Esc-to-cancel working. Windows' modal dialog navigation
// (IsDialogMessage, in walk's own message loop) intercepts Escape before
// it ever reaches a WM_KEYDOWN handler and turns it into WM_COMMAND
// IDCANCEL — which walk's Dialog only acts on if a real *walk.PushButton
// is registered via CancelButton (declarative.Dialog.CancelButton's type
// requires it). Every button the user can actually see is a styled
// Composite+Label instead (see styledButton's doc comment for why plain
// PushButton is avoided everywhere else), so this one stays Visible:false
// and never appears. There's no Enter-to-save equivalent: some tabs hold
// multi-line TextEdit allowlists where Enter must stay a plain newline.
func cancelProxyButton(box **walk.PushButton, onCancel func()) Widget {
	return PushButton{AssignTo: box, Visible: false, OnClicked: onCancel}
}

// openSettings shows the modal settings dialog, seeded from the currently
// loaded config. On 저장 it validates and writes the config file; the
// config.Watcher already running in the supervisor (see cmd/dodaemon's
// newRunFunc) picks up that filesystem write like it would any external
// edit and hot-reloads exactly the servers that need it — this dialog
// never talks to the supervisor directly, it only edits the file.
func (w *Window) openSettings() {
	cfg := *w.cfg.Load()
	users := append([]config.FTPUser(nil), cfg.FTP.Users...)

	var dlg *walk.Dialog
	var cancelProxy *walk.PushButton
	addBtn, deferred := newAddBtn()

	var autostartEnabled *walk.CheckBox

	var ftpEnabled, ftpAllowAnon, ftpTLSEnabled, sftpEnabled *walk.CheckBox
	var ftpListen, ftpAnonHome, ftpTLSCert, ftpTLSKey, sftpListen *walk.LineEdit
	var ftpPassiveLo, ftpPassiveHi, ftpMaxConn *walk.NumberEdit
	var ftpUserList *walk.ListBox
	var ftpAllowlist *walk.TextEdit

	var tftpEnabled *walk.CheckBox
	var tftpPermRW, tftpPermRO, tftpPermWO *walk.RadioButton
	var tftpListen, tftpRootDir *walk.LineEdit
	var tftpMaxBlksize, tftpTimeoutSec, tftpMaxRetries *walk.NumberEdit

	var syslogEnabled, syslogCompress, syslogTLSEnabled *walk.CheckBox
	var syslogUDP, syslogTCP, syslogLogDir, syslogTLSCert, syslogTLSKey *walk.LineEdit
	var syslogMaxSize, syslogMaxAge *walk.NumberEdit
	var syslogAllowlist *walk.TextEdit

	var webEnabled *walk.CheckBox
	var webListen, webUsername, webPassword *walk.LineEdit
	var webAllowlist *walk.TextEdit

	userDisplayItems := func() []string {
		items := make([]string, len(users))
		for i, u := range users {
			items[i] = fmt.Sprintf("%s  (%s)", u.Username, u.HomeDir)
		}
		return items
	}
	refreshUserList := func() {
		ftpUserList.SetModel(userDisplayItems())
	}

	fieldFont := Font{Family: fontFamily, PointSize: 9}
	hintFont := Font{Family: fontFamily, PointSize: 8}
	sectionFont := Font{Family: fontFamily, PointSize: 9, Bold: true}

	dialog := Dialog{
		AssignTo: &dlg,
		Title:    "DoDaemon 설정",
		Icon:     w.icon,
		Font:     fieldFont,
		// Wide enough that every tab's two-column rows (passive port
		// range, TLS cert/key + their 찾기 buttons, etc.) lay out on one
		// line without wrapping or clipping, and tall enough that most
		// tabs fit without the ScrollView below ever needing to scroll on
		// a normal 1080p+ display — every tab is wrapped in one anyway so
		// nothing is ever unreachable on a smaller screen.
		MinSize:      Size{Width: 820, Height: 660},
		Layout:       VBox{},
		CancelButton: &cancelProxy,
		Children: []Widget{
			cancelProxyButton(&cancelProxy, func() { dlg.Cancel() }),
			TabWidget{
				Font: fieldFont, // TabWidget's tab strip is a second native
				// control that only takes the Font that's set directly on
				// it (see styledButton's neighbor, applyFontRecursive-less
				// approach used throughout this file); without this the
				// tab labels render in Windows' hardcoded default font
				// even though every field below them uses JetBrains Mono.
				Pages: []TabPage{
					{
						Title:  "FTP",
						Font:   fieldFont,
						Layout: VBox{},
						Children: []Widget{
							ScrollView{
								Layout: VBox{MarginsZero: true},
								Children: []Widget{
									CheckBox{AssignTo: &ftpEnabled, Text: "FTP 서버 사용", Font: fieldFont, Checked: cfg.FTP.Enabled},
									Label{Text: "리슨 주소 (host:port)", Font: fieldFont},
									LineEdit{AssignTo: &ftpListen, Text: cfg.FTP.Listen, Font: fieldFont},
									Label{Text: "패시브 포트 범위", Font: fieldFont},
									Composite{
										Layout: HBox{MarginsZero: true},
										Children: []Widget{
											NumberEdit{AssignTo: &ftpPassiveLo, Value: float64(cfg.FTP.PassivePortRange[0]), MinValue: 1, MaxValue: 65535, Decimals: 0, Font: fieldFont},
											Label{Text: "~", Font: fieldFont},
											NumberEdit{AssignTo: &ftpPassiveHi, Value: float64(cfg.FTP.PassivePortRange[1]), MinValue: 1, MaxValue: 65535, Decimals: 0, Font: fieldFont},
										},
									},
									Label{Text: "최대 동시 연결 수", Font: fieldFont},
									NumberEdit{AssignTo: &ftpMaxConn, Value: float64(cfg.FTP.MaxConnections), MinValue: 1, MaxValue: 10000, Decimals: 0, Font: fieldFont},
									CheckBox{AssignTo: &ftpAllowAnon, Text: "익명 접속 허용", Font: fieldFont, Checked: cfg.FTP.AllowAnonymous},
									Label{Text: "익명 홈 디렉터리", Font: fieldFont},
									pathField(&ftpAnonHome, cfg.FTP.AnonymousHomeDir, &dlg, addBtn),
									CheckBox{AssignTo: &ftpTLSEnabled, Text: "TLS(FTPS) 사용", Font: fieldFont, Checked: cfg.FTP.TLS.Enabled},
									Composite{
										Layout: Grid{Columns: 2},
										Children: []Widget{
											Label{Text: "TLS 인증서 파일", Font: fieldFont},
											Label{Text: "TLS 키 파일", Font: fieldFont},
											certKeyField(&ftpTLSCert, cfg.FTP.TLS.CertFile, &dlg, addBtn),
											certKeyField(&ftpTLSKey, cfg.FTP.TLS.KeyFile, &dlg, addBtn),
										},
									},
									CheckBox{AssignTo: &sftpEnabled, Text: "SFTP 사용 (FTP 계정·권한·IP 허용목록 공유)", Font: fieldFont, Checked: cfg.FTP.SFTPEnabled},
									Label{Text: "SFTP 리슨 주소 (host:port)", Font: fieldFont},
									LineEdit{AssignTo: &sftpListen, Text: cfg.FTP.SFTPListen, Font: fieldFont},
									Label{Text: "접속 허용 IP 대역 (화이트리스트, 한 줄에 하나, 비워두면 전체 허용)", Font: sectionFont},
									TextEdit{AssignTo: &ftpAllowlist, Text: joinAllowlist(cfg.FTP.IPAllowlist), MinSize: Size{Height: 50}, VScroll: true, Font: fieldFont},
									Label{Text: "FTP 계정", Font: sectionFont},
									ListBox{AssignTo: &ftpUserList, Model: userDisplayItems(), MinSize: Size{Height: 110}, Font: fieldFont},
									Composite{
										Layout: HBox{MarginsZero: true},
										Children: []Widget{
											addBtn("추가", colorAccent, func() {
												u, ok := editFTPUserDialog(dlg, nil)
												if ok {
													users = append(users, u)
													refreshUserList()
												}
											}),
											addBtn("편집", colorBtnNeutral, func() {
												idx := ftpUserList.CurrentIndex()
												if idx < 0 {
													walk.MsgBox(dlg, "안내", "편집할 계정을 선택하세요.", walk.MsgBoxIconInformation)
													return
												}
												u, ok := editFTPUserDialog(dlg, &users[idx])
												if ok {
													users[idx] = u
													refreshUserList()
												}
											}),
											addBtn("삭제", colorBtnDanger, func() {
												idx := ftpUserList.CurrentIndex()
												if idx < 0 {
													walk.MsgBox(dlg, "안내", "삭제할 계정을 선택하세요.", walk.MsgBoxIconInformation)
													return
												}
												users = append(users[:idx], users[idx+1:]...)
												refreshUserList()
											}),
											HSpacer{},
										},
									},
								},
							},
						},
					},
					{
						Title:  "TFTP",
						Font:   fieldFont,
						Layout: VBox{},
						Children: []Widget{
							ScrollView{
								Layout: VBox{MarginsZero: true},
								Children: []Widget{
									CheckBox{AssignTo: &tftpEnabled, Text: "TFTP 서버 사용", Font: fieldFont, Checked: cfg.TFTP.Enabled},
									Label{Text: "리슨 주소 (host:port)", Font: fieldFont},
									LineEdit{AssignTo: &tftpListen, Text: cfg.TFTP.Listen, Font: fieldFont},
									Label{Text: "루트 폴더", Font: fieldFont},
									pathField(&tftpRootDir, cfg.TFTP.RootDir, &dlg, addBtn),
									Label{Text: "권한", Font: sectionFont},
									RadioButton{AssignTo: &tftpPermRW, Text: "읽기·쓰기 허용", Font: fieldFont},
									RadioButton{AssignTo: &tftpPermRO, Text: "읽기 전용", Font: fieldFont},
									RadioButton{AssignTo: &tftpPermWO, Text: "쓰기 전용", Font: fieldFont},
									Label{Text: "최대 블록 크기 (8~65464)", Font: fieldFont},
									NumberEdit{AssignTo: &tftpMaxBlksize, Value: float64(cfg.TFTP.MaxBlksize), MinValue: 8, MaxValue: 65464, Decimals: 0, Font: fieldFont},
									Label{Text: "ACK 응답 대기 시간 (초, 1~255)", Font: fieldFont},
									NumberEdit{AssignTo: &tftpTimeoutSec, Value: float64(cfg.TFTP.TimeoutSec), MinValue: 1, MaxValue: 255, Decimals: 0, Font: fieldFont},
									Label{Text: "재전송 최대 횟수 (1~20)", Font: fieldFont},
									NumberEdit{AssignTo: &tftpMaxRetries, Value: float64(cfg.TFTP.MaxRetries), MinValue: 1, MaxValue: 20, Decimals: 0, Font: fieldFont},
									VSpacer{},
								},
							},
						},
					},
					{
						Title:  "Syslog",
						Font:   fieldFont,
						Layout: VBox{},
						Children: []Widget{
							ScrollView{
								Layout: VBox{MarginsZero: true},
								Children: []Widget{
									CheckBox{AssignTo: &syslogEnabled, Text: "Syslog 서버 사용", Font: fieldFont, Checked: cfg.Syslog.Enabled},
									Label{Text: "UDP 리슨 주소", Font: fieldFont},
									LineEdit{AssignTo: &syslogUDP, Text: cfg.Syslog.UDPListen, Font: fieldFont},
									Label{Text: "TCP 리슨 주소 (비워두면 비활성)", Font: fieldFont},
									LineEdit{AssignTo: &syslogTCP, Text: cfg.Syslog.TCPListen, Font: fieldFont},
									Label{Text: "로그 폴더", Font: fieldFont},
									pathField(&syslogLogDir, cfg.Syslog.LogDir, &dlg, addBtn),
									Composite{
										Layout: Grid{Columns: 2},
										Children: []Widget{
											Label{Text: "로그 파일 최대 크기 (MB)", Font: fieldFont},
											Label{Text: "로그 보관 기간 (일)", Font: fieldFont},
											// 0 isn't a valid value for either — config.Validate
											// now floors a saved 0/blank back to the same
											// 100 MB / 30 day defaults Default() uses, so the
											// field's own MinValue just keeps the user from
											// typing a number that would immediately get
											// silently corrected on the next load.
											NumberEdit{AssignTo: &syslogMaxSize, Value: float64(cfg.Syslog.Rotate.MaxSizeMB), MinValue: 1, MaxValue: 100000, Decimals: 0, Font: fieldFont},
											NumberEdit{AssignTo: &syslogMaxAge, Value: float64(cfg.Syslog.Rotate.MaxAgeDay), MinValue: 1, MaxValue: 3650, Decimals: 0, Font: fieldFont},
										},
									},
									CheckBox{AssignTo: &syslogCompress, Text: "오래된 로그 압축", Font: fieldFont, Checked: cfg.Syslog.Rotate.Compress},
									CheckBox{AssignTo: &syslogTLSEnabled, Text: "TLS 사용 (TCP)", Font: fieldFont, Checked: cfg.Syslog.TLS.Enabled},
									Composite{
										Layout: Grid{Columns: 2},
										Children: []Widget{
											Label{Text: "TLS 인증서 파일", Font: fieldFont},
											Label{Text: "TLS 키 파일", Font: fieldFont},
											certKeyField(&syslogTLSCert, cfg.Syslog.TLS.CertFile, &dlg, addBtn),
											certKeyField(&syslogTLSKey, cfg.Syslog.TLS.KeyFile, &dlg, addBtn),
										},
									},
									Label{Text: "접속 허용 IP 대역 (화이트리스트, 한 줄에 하나, 비워두면 전체 허용)", Font: sectionFont},
									TextEdit{AssignTo: &syslogAllowlist, Text: joinAllowlist(cfg.Syslog.IPAllowlist), MinSize: Size{Height: 50}, VScroll: true, Font: fieldFont},
								},
							},
						},
					},
					{
						Title:  "Web",
						Font:   fieldFont,
						Layout: VBox{},
						Children: []Widget{
							ScrollView{
								Layout: VBox{MarginsZero: true},
								Children: []Widget{
									CheckBox{AssignTo: &webEnabled, Text: "Web 사용", Font: fieldFont, Checked: cfg.WebUI.Enabled},
									Label{Text: "리슨 주소 (host:port)", Font: fieldFont},
									LineEdit{AssignTo: &webListen, Text: cfg.WebUI.Listen, Font: fieldFont},
									Label{Text: "관리자 계정", Font: sectionFont},
									Label{Text: "사용자 이름", Font: fieldFont},
									LineEdit{AssignTo: &webUsername, Text: cfg.WebUI.Auth.Username, Font: fieldFont},
									Label{Text: "비밀번호 (변경하려면 입력, 비워두면 유지)", Font: fieldFont},
									LineEdit{AssignTo: &webPassword, PasswordMode: true, Font: fieldFont},
									Label{Text: "접속 허용 IP 대역 (화이트리스트, 한 줄에 하나, 비워두면 전체 허용)", Font: sectionFont},
									TextEdit{AssignTo: &webAllowlist, Text: joinAllowlist(cfg.WebUI.IPAllowlist), MinSize: Size{Height: 50}, VScroll: true, Font: fieldFont},
									VSpacer{},
								},
							},
						},
					},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					// The former 일반 tab held only this checkbox plus a
					// "데이터 폴더" path that nothing user-facing needed (it
					// is now fixed to <exe 폴더>\data), so the tab is gone
					// and the checkbox lives here in the footer instead.
					CheckBox{
						AssignTo: &autostartEnabled, Text: "Windows 시작 시 자동 실행", Font: fieldFont,
						Checked: autostart.IsEnabled(), Enabled: autostart.Available(),
					},
					HSpacer{Size: 12},
					Label{Text: "저장하면 실행 중인 서비스에 바로 적용됩니다.", Font: hintFont, TextColor: colorDim},
					HSpacer{},
					addBtn("저장", colorAccent, func() {
						newCfg := cfg

						newCfg.Server.Hostname = config.FixedHostname
						newCfg.Server.DataDir = config.FixedDataDir

						newCfg.FTP.Enabled = ftpEnabled.Checked()
						newCfg.FTP.Listen = ftpListen.Text()
						newCfg.FTP.PassivePortRange = [2]int{int(ftpPassiveLo.Value()), int(ftpPassiveHi.Value())}
						newCfg.FTP.MaxConnections = int(ftpMaxConn.Value())
						newCfg.FTP.AllowAnonymous = ftpAllowAnon.Checked()
						newCfg.FTP.AnonymousHomeDir = ftpAnonHome.Text()
						newCfg.FTP.TLS.Enabled = ftpTLSEnabled.Checked()
						newCfg.FTP.TLS.CertFile = ftpTLSCert.Text()
						newCfg.FTP.TLS.KeyFile = ftpTLSKey.Text()
						newCfg.FTP.SFTPEnabled = sftpEnabled.Checked()
						newCfg.FTP.SFTPListen = sftpListen.Text()
						newCfg.FTP.Users = users
						newCfg.FTP.IPAllowlist = splitAllowlist(ftpAllowlist.Text())

						newCfg.TFTP.Enabled = tftpEnabled.Checked()
						newCfg.TFTP.Listen = tftpListen.Text()
						newCfg.TFTP.RootDir = tftpRootDir.Text()
						newCfg.TFTP.AllowRead = tftpPermRW.Checked() || tftpPermRO.Checked()
						newCfg.TFTP.AllowWrite = tftpPermRW.Checked() || tftpPermWO.Checked()
						newCfg.TFTP.MaxBlksize = int(tftpMaxBlksize.Value())
						newCfg.TFTP.TimeoutSec = int(tftpTimeoutSec.Value())
						newCfg.TFTP.MaxRetries = int(tftpMaxRetries.Value())

						newCfg.Syslog.Enabled = syslogEnabled.Checked()
						newCfg.Syslog.UDPListen = syslogUDP.Text()
						newCfg.Syslog.TCPListen = syslogTCP.Text()
						newCfg.Syslog.LogDir = syslogLogDir.Text()
						newCfg.Syslog.Rotate.MaxSizeMB = int(syslogMaxSize.Value())
						newCfg.Syslog.Rotate.MaxAgeDay = int(syslogMaxAge.Value())
						newCfg.Syslog.Rotate.Compress = syslogCompress.Checked()
						newCfg.Syslog.TLS.Enabled = syslogTLSEnabled.Checked()
						newCfg.Syslog.TLS.CertFile = syslogTLSCert.Text()
						newCfg.Syslog.TLS.KeyFile = syslogTLSKey.Text()
						newCfg.Syslog.IPAllowlist = splitAllowlist(syslogAllowlist.Text())

						newCfg.WebUI.Enabled = webEnabled.Checked()
						newCfg.WebUI.Listen = webListen.Text()
						newCfg.WebUI.Auth.Username = webUsername.Text()
						newCfg.WebUI.IPAllowlist = splitAllowlist(webAllowlist.Text())
						if pw := webPassword.Text(); pw != "" {
							hash, herr := auth.HashPassword(pw)
							if herr != nil {
								walk.MsgBox(dlg, "오류", herr.Error(), walk.MsgBoxIconError)
								return
							}
							newCfg.WebUI.Auth.PasswordHash = hash
						}

						if err := newCfg.Validate(); err != nil {
							walk.MsgBox(dlg, "설정 오류", err.Error(), walk.MsgBoxIconError)
							return
						}
						if err := config.Save(w.configPath, &newCfg); err != nil {
							walk.MsgBox(dlg, "저장 실패", err.Error(), walk.MsgBoxIconError)
							return
						}
						if autostart.Available() {
							if exePath, err := os.Executable(); err == nil {
								_ = autostart.SetEnabled(autostartEnabled.Checked(), exePath)
							}
						}
						dlg.Accept()
					}),
					addBtn("취소", colorBtnNeutral, func() { dlg.Cancel() }),
				},
			},
		},
	}

	if err := dialog.Create(w.mw); err != nil {
		walk.MsgBox(w.mw, "오류", err.Error(), walk.MsgBoxIconError)
		return
	}
	for _, wire := range *deferred {
		wire()
	}
	// RadioButton has no declarative Checked field (unlike CheckBox), so the
	// initial TFTP 권한 선택 is set imperatively here, once the real
	// *walk.RadioButton values exist post-Create.
	switch {
	case cfg.TFTP.AllowRead && cfg.TFTP.AllowWrite:
		tftpPermRW.SetChecked(true)
	case cfg.TFTP.AllowWrite:
		tftpPermWO.SetChecked(true)
	default:
		tftpPermRO.SetChecked(true)
	}
	dlg.Run()
}

// editFTPUserDialog shows a small modal for adding or editing one FTP
// account. existing == nil means "add"; otherwise its fields seed the
// form. Returns the (possibly new) user and whether the user confirmed.
// Leaving the password field blank when editing keeps the existing hash.
func editFTPUserDialog(owner walk.Form, existing *config.FTPUser) (config.FTPUser, bool) {
	result := config.FTPUser{Permissions: permsFull}
	isNew := existing == nil
	if !isNew {
		result = *existing
	}

	title := "FTP 계정 추가"
	pwHint := "비밀번호"
	if !isNew {
		title = "FTP 계정 편집"
		pwHint = "비밀번호 (변경하려면 입력, 비워두면 유지)"
	}

	fieldFont := Font{Family: fontFamily, PointSize: 9}

	var dlg *walk.Dialog
	var cancelProxy *walk.PushButton
	addBtn, deferred := newAddBtn()
	var usernameEdit, passwordEdit, homeDirEdit *walk.LineEdit
	permChecks := make([]*walk.CheckBox, len(permCodes))

	checkboxWidgets := make([]Widget, len(permCodes))
	for i, p := range permCodes {
		i, p := i, p
		checkboxWidgets[i] = CheckBox{
			AssignTo: &permChecks[i],
			Text:     p.code + " · " + p.label,
			Font:     fieldFont,
			Checked:  strings.Contains(result.Permissions, p.code),
		}
	}
	setAllPerms := func(codes string) {
		for i, p := range permCodes {
			permChecks[i].SetChecked(strings.Contains(codes, p.code))
		}
	}

	dialog := Dialog{
		AssignTo:     &dlg,
		Title:        title,
		Font:         fieldFont,
		MinSize:      Size{Width: 460, Height: 420},
		Layout:       VBox{},
		CancelButton: &cancelProxy,
		Children: []Widget{
			cancelProxyButton(&cancelProxy, func() { dlg.Cancel() }),
			Label{Text: "사용자 이름", Font: fieldFont},
			LineEdit{AssignTo: &usernameEdit, Text: result.Username, Font: fieldFont},
			Label{Text: pwHint, Font: fieldFont},
			LineEdit{AssignTo: &passwordEdit, PasswordMode: true, Font: fieldFont},
			Label{Text: "홈 디렉터리", Font: fieldFont},
			pathField(&homeDirEdit, result.HomeDir, &dlg, addBtn),
			Label{Text: "권한", Font: fieldFont},
			Composite{Layout: Grid{Columns: 2}, Children: checkboxWidgets},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					addBtn("전체 권한", colorBtnNeutral, func() { setAllPerms(permsFull) }),
					addBtn("읽기 전용", colorBtnNeutral, func() { setAllPerms(permsReadOnly) }),
					HSpacer{},
				},
			},
			VSpacer{},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					addBtn("확인", colorAccent, func() {
						if usernameEdit.Text() == "" {
							walk.MsgBox(dlg, "오류", "사용자 이름을 입력하세요.", walk.MsgBoxIconError)
							return
						}
						if homeDirEdit.Text() == "" {
							walk.MsgBox(dlg, "오류", "홈 디렉터리를 입력하세요.", walk.MsgBoxIconError)
							return
						}
						if isNew && passwordEdit.Text() == "" {
							walk.MsgBox(dlg, "오류", "새 계정은 비밀번호를 입력해야 합니다.", walk.MsgBoxIconError)
							return
						}
						var perms strings.Builder
						for i, p := range permCodes {
							if permChecks[i].Checked() {
								perms.WriteString(p.code)
							}
						}
						if perms.Len() == 0 {
							walk.MsgBox(dlg, "오류", "최소 하나 이상의 권한을 선택하세요.", walk.MsgBoxIconError)
							return
						}
						if pw := passwordEdit.Text(); pw != "" {
							hash, herr := auth.HashPassword(pw)
							if herr != nil {
								walk.MsgBox(dlg, "오류", herr.Error(), walk.MsgBoxIconError)
								return
							}
							result.PasswordHash = hash
						}
						result.Username = usernameEdit.Text()
						result.HomeDir = homeDirEdit.Text()
						result.Permissions = perms.String()
						dlg.Accept()
					}),
					addBtn("취소", colorBtnNeutral, func() { dlg.Cancel() }),
				},
			},
		},
	}

	if err := dialog.Create(owner); err != nil {
		return config.FTPUser{}, false
	}
	for _, wire := range *deferred {
		wire()
	}

	resultCode := dlg.Run()
	if resultCode != walk.DlgCmdOK {
		return config.FTPUser{}, false
	}
	return result, true
}
