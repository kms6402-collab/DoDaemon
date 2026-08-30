package tftp

import (
	"bytes"
	"testing"
)

func TestParseRequest_RRQBasic(t *testing.T) {
	pkt := append([]byte{0, byte(opRRQ)}, []byte("firmware.bin\x00octet\x00")...)
	req, err := parseRequest(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if req.op != opRRQ || req.filename != "firmware.bin" || req.mode != "octet" {
		t.Errorf("got %+v", req)
	}
	if len(req.options) != 0 {
		t.Errorf("expected no options, got %v", req.options)
	}
}

func TestParseRequest_WithOptions(t *testing.T) {
	pkt := append([]byte{0, byte(opWRQ)}, []byte("cfg.bin\x00octet\x00blksize\x001024\x00tsize\x000\x00timeout\x005\x00")...)
	req, err := parseRequest(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if req.options["blksize"] != "1024" || req.options["tsize"] != "0" || req.options["timeout"] != "5" {
		t.Errorf("options not parsed correctly: %v", req.options)
	}
}

func TestParseRequest_CaseInsensitiveOptionNames(t *testing.T) {
	pkt := append([]byte{0, byte(opRRQ)}, []byte("f\x00octet\x00BLKSIZE\x00777\x00")...)
	req, err := parseRequest(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if req.options["blksize"] != "777" {
		t.Errorf("expected normalized lowercase option key, got %v", req.options)
	}
}

func TestParseRequest_Malformed(t *testing.T) {
	cases := [][]byte{
		{},
		{0, 1}, // no filename/mode
		append([]byte{0, byte(opRRQ)}, []byte("nomode\x00")...), // missing terminating NUL for mode
	}
	for _, pkt := range cases {
		if _, err := parseRequest(pkt); err == nil {
			t.Errorf("expected error for malformed packet %x", pkt)
		}
	}
}

func TestEncodeDecodeDATA(t *testing.T) {
	pkt := encodeDATA(42, []byte("hello world"))
	block, data, err := parseDATA(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if block != 42 || string(data) != "hello world" {
		t.Errorf("got block=%d data=%q", block, data)
	}
}

func TestEncodeDecodeACK(t *testing.T) {
	pkt := encodeACK(7)
	block, err := parseACK(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if block != 7 {
		t.Errorf("got block=%d, want 7", block)
	}
}

func TestEncodeDecodeERROR(t *testing.T) {
	pkt := encodeERROR(errFileNotFound, "no such file")
	code, msg, err := parseERROR(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if code != errFileNotFound || msg != "no such file" {
		t.Errorf("got code=%d msg=%q", code, msg)
	}
}

func TestEncodeOACK(t *testing.T) {
	pkt := encodeOACK([]string{"blksize", "1024", "tsize", "40"})
	if !bytes.HasPrefix(pkt, []byte{0, byte(opOACK)}) {
		t.Fatalf("OACK packet missing opcode prefix: %x", pkt)
	}
	if !bytes.Contains(pkt, []byte("blksize\x001024\x00")) {
		t.Errorf("OACK payload missing expected option pair: %x", pkt)
	}
}
