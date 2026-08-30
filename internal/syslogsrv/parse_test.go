package syslogsrv

import "testing"

func TestParse_RFC5424Basic(t *testing.T) {
	raw := "<134>1 2026-08-28T12:00:00Z myhost myapp 1234 - - RFC5424 test message"
	m, err := Parse(raw, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != "rfc5424" {
		t.Errorf("format = %q, want rfc5424", m.Format)
	}
	if m.Facility != 16 || m.Severity != 6 { // 134 = 16*8 + 6
		t.Errorf("facility=%d severity=%d, want 16/6", m.Facility, m.Severity)
	}
	if m.Hostname != "myhost" || m.AppName != "myapp" || m.ProcID != "1234" {
		t.Errorf("got hostname=%q app=%q proc=%q", m.Hostname, m.AppName, m.ProcID)
	}
	if m.Text != "RFC5424 test message" {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParse_RFC5424StructuredData(t *testing.T) {
	raw := `<14>1 2026-08-28T12:00:01Z host2 svc - - [exampleSDID@32473 iut="3" eventSource="App"] structured data test`
	m, err := Parse(raw, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	want := `[exampleSDID@32473 iut="3" eventSource="App"]`
	if m.StructuredData != want {
		t.Errorf("structured data = %q, want %q", m.StructuredData, want)
	}
	if m.Text != "structured data test" {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParse_RFC5424StructuredDataWithEscapedBracket(t *testing.T) {
	raw := `<14>1 2026-08-28T12:00:01Z host2 svc - - [id@1 k="a\]b"] msg after`
	m, err := Parse(raw, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "msg after" {
		t.Errorf("text = %q, want %q (escaped ']' inside quotes must not end the element early)", m.Text, "msg after")
	}
}

func TestParse_RFC5424NilFields(t *testing.T) {
	raw := "<14>1 2026-08-28T12:00:01Z - - - - - no metadata at all"
	m, err := Parse(raw, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Hostname != "" || m.AppName != "" || m.ProcID != "" || m.MsgID != "" {
		t.Errorf("nil-dash fields should map to empty string, got %+v", m)
	}
	if m.Text != "no metadata at all" {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParse_RFC3164Basic(t *testing.T) {
	raw := "<13>Aug 28 12:00:00 myhost sshd[999]: RFC3164 test message"
	m, err := Parse(raw, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != "rfc3164" {
		t.Errorf("format = %q, want rfc3164", m.Format)
	}
	if m.Facility != 1 || m.Severity != 5 { // 13 = 1*8 + 5
		t.Errorf("facility=%d severity=%d, want 1/5", m.Facility, m.Severity)
	}
	if m.Hostname != "myhost" || m.AppName != "sshd" || m.ProcID != "999" {
		t.Errorf("got hostname=%q app=%q proc=%q", m.Hostname, m.AppName, m.ProcID)
	}
	if m.Text != "RFC3164 test message" {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParse_RFC3164SingleDigitDay(t *testing.T) {
	// RFC 3164 pads single-digit days with an extra space: "Aug  9 ...".
	raw := "<13>Aug  9 07:00:00 myhost app: message"
	m, err := Parse(raw, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != "rfc3164" {
		t.Errorf("format = %q, want rfc3164", m.Format)
	}
	if m.Hostname != "myhost" {
		t.Errorf("hostname = %q", m.Hostname)
	}
}

func TestParse_UnstructuredFallback(t *testing.T) {
	m, err := Parse("just a plain line with no PRI", "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != "unstructured" {
		t.Errorf("format = %q, want unstructured", m.Format)
	}
	if m.Text != "just a plain line with no PRI" {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParse_InvalidPRI(t *testing.T) {
	cases := []string{
		"<>no digits",
		"<999>out of range",
		"<abc>not a number",
	}
	for _, raw := range cases {
		if _, err := Parse(raw, "127.0.0.1:1"); err == nil {
			t.Errorf("Parse(%q) expected error, got none", raw)
		}
	}
}

func TestFacilitySeverityNames(t *testing.T) {
	m := Message{Facility: 4, Severity: 3}
	if m.FacilityName() != "auth" {
		t.Errorf("FacilityName() = %q, want auth", m.FacilityName())
	}
	if m.SeverityName() != "err" {
		t.Errorf("SeverityName() = %q, want err", m.SeverityName())
	}
}
