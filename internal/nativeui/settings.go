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
// of the web UI's /api/browse modal (internal/webui/browse.go).
func browseFolder(owner walk.Form, edit *walk.LineEdit) {
	dlg := walk.FileDialog{Title: "폴더 찾기", InitialDirPath: edit.Text()}
	if ok, err := dlg.ShowBrowseFolder(owner); err == nil && ok {
		edit.SetText(dlg.FilePath)
	}
}

// pathField builds a folder LineEdit paired with a "폴더 찾기" button.
// owner is a pointer to the dialog's own *walk.Dialog variable — at the
// time this is called (building the declarative tree) that variable is
// still nil, since Dialog.AssignTo only populates it once Run() actually
// creates the window; capturing **walk.Dialog defers the dereference to
// click time, by which point it's set.
func pathField(edit **walk.LineEdit, initial string, owner **walk.Dialog) Widget {
	return Composite{
		Layout: HBox{MarginsZero: true},
		Children: []Widget{
			LineEdit{AssignTo: edit, Text: initial},
			PushButton{Text: "폴더 찾기", OnClicked: func() { browseFolder(*owner, *edit) }},
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
	var saveBtn, cancelBtn *walk.PushButton

	var dataDirEdit *walk.LineEdit
	var autostartEnabled *walk.CheckBox

	var ftpEnabled, ftpAllowAnon, ftpTLSEnabled *walk.CheckBox
	var ftpListen, ftpAnonHome, ftpTLSCert, ftpTLSKey *walk.LineEdit
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

	fieldFont := Font{Family: "Segoe UI", PointSize: 9}
	hintFont := Font{Family: "Segoe UI", PointSize: 8}

	dialog := Dialog{
		AssignTo:      &dlg,
		Title:         "DoDaemon 설정",
		Icon:          w.icon,
		Font:          fieldFont,
		MinSize:       Size{Width: 720, Height: 620},
		Layout:        VBox{},
		DefaultButton: &saveBtn,
		CancelButton:  &cancelBtn,
		Children: []Widget{
			TabWidget{
				Pages: []TabPage{
					{
						Title:  "일반",
						Layout: VBox{},
						Children: []Widget{
							Label{Text: "데이터 폴더"},
							pathField(&dataDirEdit, cfg.Server.DataDir, &dlg),
							CheckBox{
								AssignTo: &autostartEnabled, Text: "Windows 시작 시 자동 실행",
								Checked: autostart.IsEnabled(), Enabled: autostart.Available(),
							},
							VSpacer{},
						},
					},
					{
						Title:  "FTP",
						Layout: VBox{},
						Children: []Widget{
							ScrollView{
								Layout: VBox{MarginsZero: true},
								Children: []Widget{
									CheckBox{AssignTo: &ftpEnabled, Text: "FTP 서버 사용", Checked: cfg.FTP.Enabled},
									Label{Text: "리슨 주소 (host:port)"},
									LineEdit{AssignTo: &ftpListen, Text: cfg.FTP.Listen},
									Label{Text: "패시브 포트 범위"},
									Composite{
										Layout: HBox{MarginsZero: true},
										Children: []Widget{
											NumberEdit{AssignTo: &ftpPassiveLo, Value: float64(cfg.FTP.PassivePortRange[0]), MinValue: 1, MaxValue: 65535, Decimals: 0},
											Label{Text: "~"},
											NumberEdit{AssignTo: &ftpPassiveHi, Value: float64(cfg.FTP.PassivePortRange[1]), MinValue: 1, MaxValue: 65535, Decimals: 0},
										},
									},
									Label{Text: "최대 동시 연결 수"},
									NumberEdit{AssignTo: &ftpMaxConn, Value: float64(cfg.FTP.MaxConnections), MinValue: 1, MaxValue: 10000, Decimals: 0},
									CheckBox{AssignTo: &ftpAllowAnon, Text: "익명 접속 허용", Checked: cfg.FTP.AllowAnonymous},
									Label{Text: "익명 홈 디렉터리"},
									pathField(&ftpAnonHome, cfg.FTP.AnonymousHomeDir, &dlg),
									CheckBox{AssignTo: &ftpTLSEnabled, Text: "TLS(FTPS) 사용", Checked: cfg.FTP.TLS.Enabled},
									Label{Text: "TLS 인증서 파일"},
									LineEdit{AssignTo: &ftpTLSCert, Text: cfg.FTP.TLS.CertFile},
									Label{Text: "TLS 키 파일"},
									LineEdit{AssignTo: &ftpTLSKey, Text: cfg.FTP.TLS.KeyFile},
									Label{Text: "접속 허용 IP 대역 (화이트리스트, 한 줄에 하나, 비워두면 전체 허용)", Font: Font{Family: "Segoe UI", PointSize: 9, Bold: true}},
									TextEdit{AssignTo: &ftpAllowlist, Text: joinAllowlist(cfg.FTP.IPAllowlist), MinSize: Size{Height: 50}, VScroll: true},
									Label{Text: "FTP 계정", Font: Font{Family: "Segoe UI", PointSize: 9, Bold: true}},
									ListBox{AssignTo: &ftpUserList, Model: userDisplayItems(), MinSize: Size{Height: 110}},
									Composite{
										Layout: HBox{MarginsZero: true},
										Children: []Widget{
											PushButton{Text: "추가", OnClicked: func() {
												u, ok := editFTPUserDialog(dlg, nil)
												if ok {
													users = append(users, u)
													refreshUserList()
												}
											}},
											PushButton{Text: "편집", OnClicked: func() {
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
											}},
											PushButton{Text: "삭제", OnClicked: func() {
												idx := ftpUserList.CurrentIndex()
												if idx < 0 {
													walk.MsgBox(dlg, "안내", "삭제할 계정을 선택하세요.", walk.MsgBoxIconInformation)
													return
												}
												users = append(users[:idx], users[idx+1:]...)
												refreshUserList()
											}},
											HSpacer{},
										},
									},
								},
							},
						},
					},
					{
						Title:  "TFTP",
						Layout: VBox{},
						Children: []Widget{
							CheckBox{AssignTo: &tftpEnabled, Text: "TFTP 서버 사용", Checked: cfg.TFTP.Enabled},
							Label{Text: "리슨 주소 (host:port)"},
							LineEdit{AssignTo: &tftpListen, Text: cfg.TFTP.Listen},
							Label{Text: "루트 폴더"},
							pathField(&tftpRootDir, cfg.TFTP.RootDir, &dlg),
							Label{Text: "권한", Font: Font{Family: "Segoe UI", PointSize: 9, Bold: true}},
							RadioButton{AssignTo: &tftpPermRW, Text: "읽기·쓰기 허용"},
							RadioButton{AssignTo: &tftpPermRO, Text: "읽기 전용"},
							RadioButton{AssignTo: &tftpPermWO, Text: "쓰기 전용"},
							Label{Text: "최대 블록 크기 (8~65464)"},
							NumberEdit{AssignTo: &tftpMaxBlksize, Value: float64(cfg.TFTP.MaxBlksize), MinValue: 8, MaxValue: 65464, Decimals: 0},
							Label{Text: "ACK 응답 대기 시간 (초, 1~255)"},
							NumberEdit{AssignTo: &tftpTimeoutSec, Value: float64(cfg.TFTP.TimeoutSec), MinValue: 1, MaxValue: 255, Decimals: 0},
							Label{Text: "재전송 최대 횟수 (1~20)"},
							NumberEdit{AssignTo: &tftpMaxRetries, Value: float64(cfg.TFTP.MaxRetries), MinValue: 1, MaxValue: 20, Decimals: 0},
							VSpacer{},
						},
					},
					{
						Title:  "Syslog",
						Layout: VBox{},
						Children: []Widget{
							ScrollView{
								Layout: VBox{MarginsZero: true},
								Children: []Widget{
									CheckBox{AssignTo: &syslogEnabled, Text: "Syslog 서버 사용", Checked: cfg.Syslog.Enabled},
									Label{Text: "UDP 리슨 주소"},
									LineEdit{AssignTo: &syslogUDP, Text: cfg.Syslog.UDPListen},
									Label{Text: "TCP 리슨 주소 (비워두면 비활성)"},
									LineEdit{AssignTo: &syslogTCP, Text: cfg.Syslog.TCPListen},
									Label{Text: "로그 폴더"},
									pathField(&syslogLogDir, cfg.Syslog.LogDir, &dlg),
									Label{Text: "로그 파일 최대 크기 (MB)"},
									NumberEdit{AssignTo: &syslogMaxSize, Value: float64(cfg.Syslog.Rotate.MaxSizeMB), MinValue: 1, MaxValue: 100000, Decimals: 0},
									Label{Text: "로그 보관 기간 (일)"},
									NumberEdit{AssignTo: &syslogMaxAge, Value: float64(cfg.Syslog.Rotate.MaxAgeDay), MinValue: 0, MaxValue: 3650, Decimals: 0},
									CheckBox{AssignTo: &syslogCompress, Text: "오래된 로그 압축", Checked: cfg.Syslog.Rotate.Compress},
									CheckBox{AssignTo: &syslogTLSEnabled, Text: "TLS 사용 (TCP)", Checked: cfg.Syslog.TLS.Enabled},
									Label{Text: "TLS 인증서 파일"},
									LineEdit{AssignTo: &syslogTLSCert, Text: cfg.Syslog.TLS.CertFile},
									Label{Text: "TLS 키 파일"},
									LineEdit{AssignTo: &syslogTLSKey, Text: cfg.Syslog.TLS.KeyFile},
									Label{Text: "접속 허용 IP 대역 (화이트리스트, 한 줄에 하나, 비워두면 전체 허용)", Font: Font{Family: "Segoe UI", PointSize: 9, Bold: true}},
									TextEdit{AssignTo: &syslogAllowlist, Text: joinAllowlist(cfg.Syslog.IPAllowlist), MinSize: Size{Height: 50}, VScroll: true},
								},
							},
						},
					},
					{
						Title:  "Web",
						Layout: VBox{},
						Children: []Widget{
							CheckBox{AssignTo: &webEnabled, Text: "Web 사용", Checked: cfg.WebUI.Enabled},
							Label{Text: "리슨 주소 (host:port)"},
							LineEdit{AssignTo: &webListen, Text: cfg.WebUI.Listen},
							Label{Text: "관리자 계정", Font: Font{Family: "Segoe UI", PointSize: 9, Bold: true}},
							Label{Text: "사용자 이름"},
							LineEdit{AssignTo: &webUsername, Text: cfg.WebUI.Auth.Username},
							Label{Text: "비밀번호 (변경하려면 입력, 비워두면 유지)"},
							LineEdit{AssignTo: &webPassword, PasswordMode: true},
							Label{Text: "접속 허용 IP 대역 (화이트리스트, 한 줄에 하나, 비워두면 전체 허용)", Font: Font{Family: "Segoe UI", PointSize: 9, Bold: true}},
							TextEdit{AssignTo: &webAllowlist, Text: joinAllowlist(cfg.WebUI.IPAllowlist), MinSize: Size{Height: 50}, VScroll: true},
							VSpacer{},
						},
					},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{Text: "저장하면 실행 중인 서비스에 바로 적용됩니다.", Font: hintFont, TextColor: colorDim},
					HSpacer{},
					PushButton{AssignTo: &saveBtn, Text: "저장", OnClicked: func() {
						newCfg := cfg

						newCfg.Server.Hostname = config.FixedHostname
						newCfg.Server.DataDir = dataDirEdit.Text()

						newCfg.FTP.Enabled = ftpEnabled.Checked()
						newCfg.FTP.Listen = ftpListen.Text()
						newCfg.FTP.PassivePortRange = [2]int{int(ftpPassiveLo.Value()), int(ftpPassiveHi.Value())}
						newCfg.FTP.MaxConnections = int(ftpMaxConn.Value())
						newCfg.FTP.AllowAnonymous = ftpAllowAnon.Checked()
						newCfg.FTP.AnonymousHomeDir = ftpAnonHome.Text()
						newCfg.FTP.TLS.Enabled = ftpTLSEnabled.Checked()
						newCfg.FTP.TLS.CertFile = ftpTLSCert.Text()
						newCfg.FTP.TLS.KeyFile = ftpTLSKey.Text()
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
					}},
					PushButton{AssignTo: &cancelBtn, Text: "취소", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}

	if err := dialog.Create(w.mw); err != nil {
		walk.MsgBox(w.mw, "오류", err.Error(), walk.MsgBoxIconError)
		return
	}
	// RadioButton has no declarative Checked field (unlike CheckBox), so the
	// initial TFTP 권한 selection is set imperatively here, once the real
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

	var dlg *walk.Dialog
	var okBtn, cancelBtn *walk.PushButton
	var usernameEdit, passwordEdit, homeDirEdit *walk.LineEdit
	permChecks := make([]*walk.CheckBox, len(permCodes))

	checkboxWidgets := make([]Widget, len(permCodes))
	for i, p := range permCodes {
		i, p := i, p
		checkboxWidgets[i] = CheckBox{
			AssignTo: &permChecks[i],
			Text:     p.code + " · " + p.label,
			Checked:  strings.Contains(result.Permissions, p.code),
		}
	}
	setAllPerms := func(codes string) {
		for i, p := range permCodes {
			permChecks[i].SetChecked(strings.Contains(codes, p.code))
		}
	}

	resultCode, err := Dialog{
		AssignTo:      &dlg,
		Title:         title,
		MinSize:       Size{Width: 460, Height: 420},
		Layout:        VBox{},
		DefaultButton: &okBtn,
		CancelButton:  &cancelBtn,
		Children: []Widget{
			Label{Text: "사용자 이름"},
			LineEdit{AssignTo: &usernameEdit, Text: result.Username},
			Label{Text: pwHint},
			LineEdit{AssignTo: &passwordEdit, PasswordMode: true},
			Label{Text: "홈 디렉터리"},
			pathField(&homeDirEdit, result.HomeDir, &dlg),
			Label{Text: "권한"},
			Composite{Layout: Grid{Columns: 2}, Children: checkboxWidgets},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: "전체 권한", OnClicked: func() { setAllPerms(permsFull) }},
					PushButton{Text: "읽기 전용", OnClicked: func() { setAllPerms(permsReadOnly) }},
					HSpacer{},
				},
			},
			VSpacer{},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &okBtn, Text: "확인", OnClicked: func() {
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
					}},
					PushButton{AssignTo: &cancelBtn, Text: "취소", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(owner)

	if err != nil || resultCode != walk.DlgCmdOK {
		return config.FTPUser{}, false
	}
	return result, true
}
