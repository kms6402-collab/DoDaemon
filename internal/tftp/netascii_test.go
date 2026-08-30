package tftp

import "testing"

func TestToNetascii(t *testing.T) {
	got := toNetascii([]byte("line1\nline2\nline3"))
	want := "line1\r\nline2\r\nline3"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToNetascii_NoNewlines(t *testing.T) {
	in := []byte("no newlines here")
	got := toNetascii(in)
	if string(got) != string(in) {
		t.Errorf("got %q, want unchanged %q", got, in)
	}
}

func TestFromNetascii_Basic(t *testing.T) {
	out, carry := fromNetascii([]byte("line1\r\nline2\r\n"), false)
	if carry {
		t.Error("unexpected carry")
	}
	if string(out) != "line1\nline2\n" {
		t.Errorf("got %q", out)
	}
}

func TestFromNetascii_CRSplitAcrossBlocks(t *testing.T) {
	// A CR landing exactly at the end of a DATA block must be carried into
	// the next block to correctly detect a following LF.
	out1, carry1 := fromNetascii([]byte("line1\r"), false)
	if !carry1 {
		t.Fatal("expected carry after trailing CR")
	}
	if string(out1) != "line1" {
		t.Errorf("got %q", out1)
	}

	out2, carry2 := fromNetascii([]byte("\nline2"), carry1)
	if carry2 {
		t.Error("unexpected carry")
	}
	if string(out2) != "\nline2" {
		t.Errorf("got %q", out2)
	}
}

func TestFromNetascii_BareCRNotFollowedByLF(t *testing.T) {
	// netascii allows CR NUL to represent a lone CR; our simplified
	// converter treats a CR not followed by LF as a literal CR.
	out, carry := fromNetascii([]byte("a\rb"), false)
	if carry {
		t.Error("unexpected carry")
	}
	if string(out) != "a\rb" {
		t.Errorf("got %q", out)
	}
}

func TestNetascii_RoundTrip(t *testing.T) {
	original := "config line one\nconfig line two\nconfig line three\n"
	expanded := toNetascii([]byte(original))
	contracted, carry := fromNetascii(expanded, false)
	if carry {
		t.Error("unexpected carry at end of full buffer")
	}
	if string(contracted) != original {
		t.Errorf("round trip mismatch: got %q, want %q", contracted, original)
	}
}
