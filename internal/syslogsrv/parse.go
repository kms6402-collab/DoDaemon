// Package syslogsrv receives syslog messages over UDP/TCP(+TLS) and parses
// both RFC 3164 (BSD) and RFC 5424 framing, auto-detected per message
// (docs/PLAN.md §2.3/§5.4). Listening is implemented directly on net/tls;
// parsing is hand-rolled to avoid pulling in a parser tuned for a single
// RFC when real-world devices mix both on the same port.
package syslogsrv

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Message is a normalized, parsed syslog entry regardless of source RFC.
type Message struct {
	Time           time.Time
	Facility       int
	Severity       int
	Hostname       string
	AppName        string
	ProcID         string
	MsgID          string
	StructuredData string
	Text           string
	Format         string // "rfc5424" | "rfc3164" | "unstructured"
	Raw            string
	RemoteAddr     string
}

var facilityNames = [...]string{
	"kern", "user", "mail", "daemon", "auth", "syslog", "lpr", "news",
	"uucp", "cron", "authpriv", "ftp", "ntp", "security", "console", "solaris-cron",
	"local0", "local1", "local2", "local3", "local4", "local5", "local6", "local7",
}

var severityNames = [...]string{
	"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug",
}

func (m Message) FacilityName() string {
	if m.Facility >= 0 && m.Facility < len(facilityNames) {
		return facilityNames[m.Facility]
	}
	return strconv.Itoa(m.Facility)
}

func (m Message) SeverityName() string {
	if m.Severity >= 0 && m.Severity < len(severityNames) {
		return severityNames[m.Severity]
	}
	return strconv.Itoa(m.Severity)
}

// bsdMonths supports the RFC 3164 "Mmm dd hh:mm:ss" timestamp.
var bsdTimeLayouts = []string{"Jan _2 15:04:05", "Jan 2 15:04:05"}

// Parse auto-detects RFC 5424 vs RFC 3164 framing and parses raw into a
// Message. Log-injection defense: control characters (including bare CR/LF)
// inside the text payload are escaped by the caller at render time
// (internal/webui), never stripped here, so the original bytes remain
// available for audit.
func Parse(raw string, remoteAddr string) (Message, error) {
	raw = strings.TrimPrefix(raw, "\uFEFF") // RFC 5424 allows a UTF-8 BOM before MSG; also seen leading a whole line
	orig := raw

	if len(raw) == 0 || raw[0] != '<' {
		return Message{Time: time.Now(), Text: raw, Format: "unstructured", Raw: orig, RemoteAddr: remoteAddr, Facility: -1, Severity: -1}, nil
	}
	end := strings.IndexByte(raw, '>')
	if end < 1 || end > 4 {
		return Message{}, fmt.Errorf("syslogsrv: missing/invalid PRI field")
	}
	pri, err := strconv.Atoi(raw[1:end])
	if err != nil || pri < 0 || pri > 191 {
		return Message{}, fmt.Errorf("syslogsrv: invalid PRI value %q", raw[1:end])
	}
	facility := pri / 8
	severity := pri % 8
	rest := raw[end+1:]

	if isRFC5424(rest) {
		msg, err := parseRFC5424(rest, remoteAddr)
		if err != nil {
			return Message{}, err
		}
		msg.Facility, msg.Severity, msg.Raw = facility, severity, orig
		return msg, nil
	}

	msg := parseRFC3164(rest, remoteAddr)
	msg.Facility, msg.Severity, msg.Raw = facility, severity, orig
	return msg, nil
}

// isRFC5424 checks for "1 " (VERSION SP) immediately after the PRI field.
func isRFC5424(rest string) bool {
	sp := strings.IndexByte(rest, ' ')
	if sp <= 0 {
		return false
	}
	ver := rest[:sp]
	if len(ver) > 2 {
		return false
	}
	for _, c := range ver {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parseRFC5424(rest, remoteAddr string) (Message, error) {
	sp := strings.IndexByte(rest, ' ')
	rest = rest[sp+1:] // drop VERSION

	fields, tail, ok := splitNSpaceFields(rest, 5)
	if !ok {
		return Message{}, fmt.Errorf("syslogsrv: truncated RFC5424 header")
	}
	timestamp, hostname, appName, procID, msgID := fields[0], fields[1], fields[2], fields[3], fields[4]

	sd, msgText := splitStructuredData(tail)

	t := time.Now()
	if timestamp != "-" {
		if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			t = parsed
		}
	}

	return Message{
		Time:           t,
		Hostname:       nilDash(hostname),
		AppName:        nilDash(appName),
		ProcID:         nilDash(procID),
		MsgID:          nilDash(msgID),
		StructuredData: sd,
		Text:           strings.TrimPrefix(msgText, "\uFEFF"),
		Format:         "rfc5424",
		RemoteAddr:     remoteAddr,
	}, nil
}

// splitNSpaceFields splits s on the first n spaces, returning the n fields
// and whatever remains after the last one (which itself may contain spaces).
func splitNSpaceFields(s string, n int) (fields []string, tail string, ok bool) {
	for i := 0; i < n; i++ {
		idx := strings.IndexByte(s, ' ')
		if idx < 0 {
			return nil, "", false
		}
		fields = append(fields, s[:idx])
		s = s[idx+1:]
	}
	return fields, s, true
}

func nilDash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// splitStructuredData parses zero or more "[...]" SD-ELEMENTs (with
// backslash-escaped '"', '\' and ']' inside quoted param values, per
// RFC 5424 §6.3.3) and returns the raw SD text plus the remaining MSG text.
func splitStructuredData(s string) (sd, msg string) {
	if strings.HasPrefix(s, "- ") {
		return "", s[2:]
	}
	if s == "-" {
		return "", ""
	}
	if !strings.HasPrefix(s, "[") {
		return "", s
	}

	i := 0
	for i < len(s) && s[i] == '[' {
		i++
		inQuotes := false
		for i < len(s) {
			switch s[i] {
			case '\\':
				i += 2
				continue
			case '"':
				inQuotes = !inQuotes
			case ']':
				if !inQuotes {
					i++
					goto elementDone
				}
			}
			i++
		}
	elementDone:
	}
	sd = s[:i]
	if i < len(s) && s[i] == ' ' {
		i++
	}
	return sd, s[i:]
}

func parseRFC3164(rest, remoteAddr string) Message {
	rest = strings.TrimLeft(rest, " ")

	var t time.Time
	var parsedOK bool
	if len(rest) >= 15 {
		candidate := rest[:15]
		for _, layout := range bsdTimeLayouts {
			if parsed, err := time.Parse(layout, candidate); err == nil {
				t = time.Date(time.Now().Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), 0, time.Local)
				parsedOK = true
				break
			}
		}
	}
	if !parsedOK {
		return Message{Time: time.Now(), Text: rest, Format: "unstructured", RemoteAddr: remoteAddr}
	}
	rest = strings.TrimLeft(rest[15:], " ")

	hostname := ""
	if sp := strings.IndexByte(rest, ' '); sp > 0 {
		hostname = rest[:sp]
		rest = rest[sp+1:]
	}

	appName, procID := "", ""
	if colon := strings.IndexByte(rest, ':'); colon > 0 && colon < 64 {
		tag := rest[:colon]
		if lb := strings.IndexByte(tag, '['); lb > 0 && strings.HasSuffix(tag, "]") {
			appName = tag[:lb]
			procID = tag[lb+1 : len(tag)-1]
		} else {
			appName = tag
		}
		rest = strings.TrimPrefix(rest[colon+1:], " ")
	}

	return Message{
		Time:       t,
		Hostname:   hostname,
		AppName:    appName,
		ProcID:     procID,
		Text:       rest,
		Format:     "rfc3164",
		RemoteAddr: remoteAddr,
	}
}
