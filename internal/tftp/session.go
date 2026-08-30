package tftp

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/kms6402/dodaemon/internal/security"
)

const (
	defaultBlksize = 512
	defaultTimeout = 3 * time.Second
	maxRetries     = 5
)

type negotiated struct {
	blksize int
	timeout time.Duration
	tsize   *int64 // nil unless the client asked for tsize
	oackKV  []string
}

// negotiateOptions applies RFC 2347/2348/2349 clamping: the server may
// only ever accept a value the client offered or something more
// conservative, never demand a larger blksize/timeout than requested.
func (s *Server) negotiateOptions(req *request, fileSizeForRRQ int64) negotiated {
	n := negotiated{blksize: defaultBlksize, timeout: defaultTimeout}

	if v, ok := req.options["blksize"]; ok {
		if bs, err := strconv.Atoi(v); err == nil && bs >= 8 && bs <= 65464 {
			if s.cfg.MaxBlksize > 0 && bs > s.cfg.MaxBlksize {
				bs = s.cfg.MaxBlksize
			}
			n.blksize = bs
			n.oackKV = append(n.oackKV, "blksize", strconv.Itoa(bs))
		}
	}
	if v, ok := req.options["timeout"]; ok {
		if t, err := strconv.Atoi(v); err == nil && t >= 1 && t <= 255 {
			n.timeout = time.Duration(t) * time.Second
			n.oackKV = append(n.oackKV, "timeout", strconv.Itoa(t))
		}
	}
	if _, ok := req.options["tsize"]; ok {
		size := fileSizeForRRQ
		n.tsize = &size
		n.oackKV = append(n.oackKV, "tsize", strconv.FormatInt(size, 10))
	}
	return n
}

func (s *Server) handleSession(ctx context.Context, req *request, remote *net.UDPAddr) {
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		s.log.Error("session socket failed", "remote", remote.String(), "error", err)
		return
	}
	defer conn.Close()

	mode := normalizeOptionName(req.mode)
	if mode != "netascii" && mode != "octet" {
		s.sendError(conn, errIllegalOperation, "unsupported mode")
		return
	}

	path, err := security.SafeJoin(s.cfg.RootDir, req.filename)
	if err != nil {
		s.log.Warn("path traversal attempt blocked", "remote", remote.String(), "filename", req.filename)
		s.sendError(conn, errAccessViolation, "access violation")
		s.publish("error", remote.String(), "blocked path traversal attempt", map[string]any{"filename": req.filename})
		return
	}

	switch req.op {
	case opRRQ:
		s.handleRRQ(ctx, conn, remote, req, path, mode)
	case opWRQ:
		s.handleWRQ(ctx, conn, remote, req, path, mode)
	}
}

func (s *Server) handleRRQ(ctx context.Context, conn *net.UDPConn, remote *net.UDPAddr, req *request, path, mode string) {
	if !s.cfg.AllowRead {
		s.sendError(conn, errAccessViolation, "downloads are disabled on this server")
		s.publish("error", remote.String(), "RRQ rejected: downloads disabled", map[string]any{"file": req.filename})
		return
	}

	f, err := os.Open(path)
	if err != nil {
		s.sendError(conn, errFileNotFound, "file not found")
		s.publish("error", remote.String(), "RRQ file not found", map[string]any{"path": req.filename})
		return
	}
	defer f.Close()

	var fileSize int64
	if fi, err := f.Stat(); err == nil {
		fileSize = fi.Size()
	}

	n := s.negotiateOptions(req, fileSize)
	s.publish("transfer", remote.String(), "TFTP download started", map[string]any{"file": req.filename, "mode": mode, "blksize": n.blksize})

	if len(n.oackKV) > 0 {
		if !s.sendAndWaitACK(conn, encodeOACK(n.oackKV), 0, n.timeout) {
			s.publish("error", remote.String(), "RRQ option negotiation timed out", nil)
			return
		}
	}

	var block uint16 = 1
	reader := io.Reader(f)
	buf := make([]byte, n.blksize)

	for {
		if ctx.Err() != nil {
			return
		}
		nRead, readErr := io.ReadFull(reader, buf)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			s.sendError(conn, errNotDefined, "read error")
			return
		}
		chunk := buf[:nRead]
		if mode == "netascii" {
			// Stateless expansion (LF -> CRLF); no cross-block carry needed.
			// Note: expansion can push a block's wire size slightly above
			// n.blksize, which is an accepted simplification for the rarely
			// used netascii mode — firmware transfers (the primary use case)
			// always use octet mode and are unaffected.
			chunk = toNetascii(chunk)
		}

		if !s.sendAndWaitACK(conn, encodeDATA(block, chunk), block, n.timeout) {
			s.publish("error", remote.String(), "RRQ transfer aborted (no ACK)", map[string]any{"file": req.filename, "block": block})
			return
		}

		if nRead < n.blksize {
			s.publish("transfer", remote.String(), "TFTP download complete", map[string]any{"file": req.filename})
			return
		}
		block++
	}
}

func (s *Server) handleWRQ(ctx context.Context, conn *net.UDPConn, remote *net.UDPAddr, req *request, path, mode string) {
	if !s.cfg.AllowWrite {
		s.sendError(conn, errAccessViolation, "uploads are disabled on this server")
		s.publish("error", remote.String(), "WRQ rejected: uploads disabled", map[string]any{"file": req.filename})
		return
	}

	n := s.negotiateOptions(req, 0)

	f, err := os.Create(path)
	if err != nil {
		s.sendError(conn, errAccessViolation, "cannot create file")
		return
	}
	defer f.Close()

	s.publish("transfer", remote.String(), "TFTP upload started", map[string]any{"file": req.filename, "mode": mode, "blksize": n.blksize})

	var expected uint16 = 1
	first := true
	var carryCR bool

	ack := func(block uint16) { conn.Write(encodeACK(block)) }

	if len(n.oackKV) > 0 {
		conn.Write(encodeOACK(n.oackKV))
	} else {
		ack(0)
	}

	buf := make([]byte, 65507)
	for {
		if ctx.Err() != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(n.timeout))
		nRead, err := conn.Read(buf)
		if err != nil {
			if first {
				// Retry the initial OACK/ACK(0) once more before giving up.
				if len(n.oackKV) > 0 {
					conn.Write(encodeOACK(n.oackKV))
				} else {
					ack(0)
				}
				conn.SetReadDeadline(time.Now().Add(n.timeout))
				nRead, err = conn.Read(buf)
				if err != nil {
					s.publish("error", remote.String(), "WRQ timed out waiting for first DATA", nil)
					return
				}
			} else {
				s.publish("error", remote.String(), "WRQ transfer aborted (timeout)", map[string]any{"file": req.filename})
				return
			}
		}

		block, data, perr := parseDATA(buf[:nRead])
		if perr != nil {
			if _, msg, oerr := parseERROR(buf[:nRead]); oerr == nil {
				s.log.Info("client aborted upload", "remote", remote.String(), "message", msg)
				return
			}
			s.sendError(conn, errIllegalOperation, "expected DATA packet")
			return
		}
		first = false

		if block != expected {
			ack(expected - 1) // duplicate/out-of-order: re-ack the last good block
			continue
		}

		out := data
		if mode == "netascii" {
			out, carryCR = fromNetascii(data, carryCR)
		}
		if _, err := f.Write(out); err != nil {
			s.sendError(conn, errDiskFull, "write error")
			s.publish("error", remote.String(), "WRQ write failed", map[string]any{"file": req.filename, "error": err.Error()})
			return
		}
		ack(block)

		if len(data) < n.blksize {
			s.publish("transfer", remote.String(), "TFTP upload complete", map[string]any{"file": req.filename})
			return
		}
		expected++
	}
}

// sendAndWaitACK sends pkt and waits for an ACK matching wantBlock, retrying
// on timeout up to maxRetries times.
func (s *Server) sendAndWaitACK(conn *net.UDPConn, pkt []byte, wantBlock uint16, timeout time.Duration) bool {
	buf := make([]byte, 4)
	for attempt := 0; attempt < maxRetries; attempt++ {
		if _, err := conn.Write(pkt); err != nil {
			return false
		}
		conn.SetReadDeadline(time.Now().Add(timeout))
		nRead, err := conn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return false
		}
		block, perr := parseACK(buf[:nRead])
		if perr == nil && block == wantBlock {
			return true
		}
		// Stray/duplicate ACK for an old block: keep waiting within this attempt budget.
	}
	return false
}

func (s *Server) sendError(conn *net.UDPConn, code uint16, msg string) {
	conn.Write(encodeERROR(code, msg))
}
