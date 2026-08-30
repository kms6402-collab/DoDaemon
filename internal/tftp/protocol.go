// Package tftp implements an RFC 1350 TFTP server with the RFC 2347/2348/2349
// option extensions (blksize, timeout, tsize), built directly on net.UDPConn
// per docs/PLAN.md §2.2/§5.3.
package tftp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

type opcode uint16

const (
	opRRQ   opcode = 1
	opWRQ   opcode = 2
	opDATA  opcode = 3
	opACK   opcode = 4
	opERROR opcode = 5
	opOACK  opcode = 6
)

// Standard TFTP error codes (RFC 1350 §5).
const (
	errNotDefined       uint16 = 0
	errFileNotFound     uint16 = 1
	errAccessViolation  uint16 = 2
	errDiskFull         uint16 = 3
	errIllegalOperation uint16 = 4
	errUnknownTID       uint16 = 5
	errFileExists       uint16 = 6
	errNoSuchUser       uint16 = 7
	errOptionNegotiate  uint16 = 8
)

var errMalformedPacket = errors.New("tftp: malformed packet")

type request struct {
	op       opcode
	filename string
	mode     string // "netascii" | "octet"
	options  map[string]string
}

func parseRequest(b []byte) (*request, error) {
	if len(b) < 4 {
		return nil, errMalformedPacket
	}
	op := opcode(binary.BigEndian.Uint16(b[0:2]))
	if op != opRRQ && op != opWRQ {
		return nil, errMalformedPacket
	}
	fields, err := splitNulTerminated(b[2:])
	if err != nil || len(fields) < 2 {
		return nil, errMalformedPacket
	}

	req := &request{op: op, filename: fields[0], mode: fields[1], options: map[string]string{}}

	// Remaining fields come in option/value pairs (RFC 2347).
	rest := fields[2:]
	for i := 0; i+1 < len(rest); i += 2 {
		req.options[normalizeOptionName(rest[i])] = rest[i+1]
	}
	return req, nil
}

func normalizeOptionName(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

func splitNulTerminated(b []byte) ([]string, error) {
	var out []string
	for len(b) > 0 {
		idx := bytes.IndexByte(b, 0)
		if idx < 0 {
			return nil, errMalformedPacket
		}
		out = append(out, string(b[:idx]))
		b = b[idx+1:]
	}
	return out, nil
}

func encodeDATA(block uint16, data []byte) []byte {
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint16(buf[0:2], uint16(opDATA))
	binary.BigEndian.PutUint16(buf[2:4], block)
	copy(buf[4:], data)
	return buf
}

func encodeACK(block uint16) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint16(buf[0:2], uint16(opACK))
	binary.BigEndian.PutUint16(buf[2:4], block)
	return buf
}

func encodeERROR(code uint16, msg string) []byte {
	buf := make([]byte, 0, 5+len(msg))
	tmp := make([]byte, 2)
	binary.BigEndian.PutUint16(tmp, uint16(opERROR))
	buf = append(buf, tmp...)
	binary.BigEndian.PutUint16(tmp, code)
	buf = append(buf, tmp...)
	buf = append(buf, msg...)
	buf = append(buf, 0)
	return buf
}

// encodeOACK emits options in a stable order (the caller passes the order).
func encodeOACK(orderedKV []string) []byte {
	buf := make([]byte, 2)
	binary.BigEndian.PutUint16(buf, uint16(opOACK))
	for _, s := range orderedKV {
		buf = append(buf, s...)
		buf = append(buf, 0)
	}
	return buf
}

func parseDATA(b []byte) (block uint16, data []byte, err error) {
	if len(b) < 4 {
		return 0, nil, errMalformedPacket
	}
	if opcode(binary.BigEndian.Uint16(b[0:2])) != opDATA {
		return 0, nil, errMalformedPacket
	}
	return binary.BigEndian.Uint16(b[2:4]), b[4:], nil
}

func parseACK(b []byte) (block uint16, err error) {
	if len(b) < 4 {
		return 0, errMalformedPacket
	}
	if opcode(binary.BigEndian.Uint16(b[0:2])) != opACK {
		return 0, errMalformedPacket
	}
	return binary.BigEndian.Uint16(b[2:4]), nil
}

func parseERROR(b []byte) (code uint16, msg string, err error) {
	if len(b) < 4 {
		return 0, "", errMalformedPacket
	}
	if opcode(binary.BigEndian.Uint16(b[0:2])) != opERROR {
		return 0, "", errMalformedPacket
	}
	code = binary.BigEndian.Uint16(b[2:4])
	msg = string(bytes.TrimRight(b[4:], "\x00"))
	return code, msg, nil
}

func errName(code uint16) string {
	switch code {
	case errFileNotFound:
		return "file not found"
	case errAccessViolation:
		return "access violation"
	case errDiskFull:
		return "disk full"
	case errIllegalOperation:
		return "illegal TFTP operation"
	case errUnknownTID:
		return "unknown transfer ID"
	case errFileExists:
		return "file already exists"
	case errNoSuchUser:
		return "no such user"
	case errOptionNegotiate:
		return "option negotiation failed"
	default:
		return fmt.Sprintf("error %d", code)
	}
}
