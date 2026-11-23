package tftp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/kms6402/dodaemon/internal/security"
)

const defaultBlksize = 512

// maxWindowsize caps the RFC 7440 windowsize option. A window lets the
// sender stream several blocks per ACK instead of one, which is what makes
// TFTP usable over links with any real latency; 64 blocks is far past the
// point of diminishing returns and still small enough that a lost block
// only costs a modest retransmit burst.
const maxWindowsize = 64

// fileBufSize is the read-ahead / write-behind buffer around the file, so
// a 512-byte-block transfer isn't one disk syscall per packet.
const fileBufSize = 256 << 10

// recvBufSize must hold any datagram a peer could send: on Windows a UDP
// read into a smaller buffer fails with WSAEMSGSIZE instead of truncating,
// which used to abort a transfer whenever the client sent an ERROR packet
// or a stray larger datagram while we were waiting for a 4-byte ACK.
const recvBufSize = 65536

type negotiated struct {
	blksize    int
	windowsize int
	timeout    time.Duration
	tsize      *int64 // nil unless the client asked for tsize
	oackKV     []string
}

// negotiateOptions applies RFC 2347/2348/2349 clamping: the server may
// only ever accept a value the client offered or something more
// conservative, never demand a larger blksize/timeout than requested.
func (s *Server) negotiateOptions(req *request, fileSizeForRRQ int64) negotiated {
	n := negotiated{blksize: defaultBlksize, windowsize: 1, timeout: time.Duration(s.cfg.TimeoutSec) * time.Second}

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
	if v, ok := req.options["windowsize"]; ok {
		if ws, err := strconv.Atoi(v); err == nil && ws >= 1 && ws <= 65535 {
			if ws > maxWindowsize {
				ws = maxWindowsize
			}
			n.windowsize = ws
			n.oackKV = append(n.oackKV, "windowsize", strconv.Itoa(ws))
		}
	}
	if v, ok := req.options["tsize"]; ok {
		size := fileSizeForRRQ
		// For WRQ, fileSizeForRRQ is 0 (the server doesn't know the size in
		// advance) and the client is the one declaring its upload size via
		// tsize (RFC 2349) — echo that back rather than always OACKing 0,
		// so both sides (and our own progress tracking) agree on the total.
		if size == 0 {
			if parsed, err := strconv.ParseInt(v, 10, 64); err == nil && parsed >= 0 {
				size = parsed
			}
		}
		n.tsize = &size
		n.oackKV = append(n.oackKV, "tsize", strconv.FormatInt(size, 10))
	}
	return n
}

// progressInterval throttles how often "진행" events are published during a
// transfer — frequent enough for the active-session UI to feel live,
// infrequent enough not to flood the event bus on fast local transfers.
const progressInterval = 200 * time.Millisecond

// publishProgress emits a throttled transfer-progress event; *last tracks
// the previous publish time across calls within one session's loop. total
// of 0 means "unknown" (an in-progress TFTP upload with no tsize option) —
// subscribers show a byte counter instead of a percentage in that case.
func (s *Server) publishProgress(remote, file string, done, total int64, last *time.Time) {
	now := time.Now()
	if now.Sub(*last) < progressInterval {
		return
	}
	*last = now
	s.publish("transfer", remote, "TFTP 전송 진행", map[string]any{
		"file": file, "bytes_done": done, "bytes_total": total, "progress": true,
	})
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
		s.publish("error", remote.String(), "RRQ file not found", map[string]any{"file": req.filename})
		return
	}
	defer f.Close()

	var fileSize int64
	if fi, err := f.Stat(); err == nil {
		if fi.IsDir() {
			s.sendError(conn, errFileNotFound, "not a file")
			s.publish("error", remote.String(), "RRQ target is a directory", map[string]any{"file": req.filename})
			return
		}
		fileSize = fi.Size()
	}

	n := s.negotiateOptions(req, fileSize)
	s.publish("transfer", remote.String(), "TFTP download started", map[string]any{"file": req.filename, "mode": mode, "blksize": n.blksize, "windowsize": n.windowsize})

	rbuf := make([]byte, recvBufSize)

	if len(n.oackKV) > 0 {
		// The client answers our OACK with ACK(0); expressed as "one block
		// past 65535" so awaitACK's wraparound arithmetic covers it too.
		oack := encodeOACK(n.oackKV)
		acked := false
		for attempt := 0; attempt < s.cfg.MaxRetries && !acked; attempt++ {
			if _, err := conn.Write(oack); err != nil {
				break
			}
			k, status := s.awaitACK(conn, rbuf, 0xFFFF, 1, n.timeout)
			if status == ackFatal {
				s.publish("error", remote.String(), "RRQ aborted by client during option negotiation", map[string]any{"file": req.filename})
				return
			}
			acked = k == 1
		}
		if !acked {
			s.publish("error", remote.String(), "RRQ option negotiation timed out", map[string]any{"file": req.filename})
			return
		}
	}

	type outBlock struct {
		pkt []byte
		n   int // file bytes in this block (pre-netascii)
	}

	reader := bufio.NewReaderSize(f, fileBufSize)
	var (
		lastAcked    uint16 // wire number of the last block the client confirmed
		inFlight     []outBlock
		eof          bool
		retries      int
		bytesSent    int64
		lastProgress = time.Now()
	)

	for {
		if ctx.Err() != nil {
			return
		}
		// Top the window up with fresh blocks.
		for !eof && len(inFlight) < n.windowsize {
			buf := make([]byte, n.blksize)
			nRead, readErr := io.ReadFull(reader, buf)
			if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
				s.sendError(conn, errNotDefined, "read error")
				s.publish("error", remote.String(), "RRQ read failed", map[string]any{"file": req.filename, "error": readErr.Error()})
				return
			}
			chunk := buf[:nRead]
			if mode == "netascii" {
				// Stateless expansion (LF -> CRLF); can push a block's wire
				// size slightly above blksize — an accepted simplification
				// for the rarely used netascii mode (firmware transfers use
				// octet and are unaffected).
				chunk = toNetascii(chunk)
			}
			block := lastAcked + uint16(len(inFlight)) + 1 // wraps 65535 -> 0 like other servers
			pkt := encodeDATA(block, chunk)
			inFlight = append(inFlight, outBlock{pkt: pkt, n: nRead})
			if nRead < n.blksize {
				eof = true
			}
			if _, err := conn.Write(pkt); err != nil {
				s.publish("error", remote.String(), "RRQ send failed", map[string]any{"file": req.filename, "error": err.Error()})
				return
			}
		}

		k, status := s.awaitACK(conn, rbuf, lastAcked, len(inFlight), n.timeout)
		switch status {
		case ackFatal:
			s.publish("error", remote.String(), "RRQ aborted by client", map[string]any{"file": req.filename, "block": lastAcked + 1})
			return
		case ackTimeout:
			retries++
			if retries >= s.cfg.MaxRetries {
				s.publish("error", remote.String(), "RRQ transfer aborted (no ACK)", map[string]any{"file": req.filename, "block": lastAcked + 1})
				return
			}
			s.log.Debug("rrq retransmit", "remote", remote.String(), "block", lastAcked+1, "attempt", retries)
			for _, b := range inFlight {
				conn.Write(b.pkt)
			}
			continue
		}

		retries = 0
		for _, b := range inFlight[:k] {
			bytesSent += int64(b.n)
		}
		lastAcked += uint16(k)
		inFlight = inFlight[k:]
		s.publishProgress(remote.String(), req.filename, bytesSent, fileSize, &lastProgress)

		if eof && len(inFlight) == 0 {
			s.publish("transfer", remote.String(), "TFTP download complete", map[string]any{"file": req.filename, "bytes": bytesSent})
			return
		}
		// A partial ACK inside a window (RFC 7440 §4) means the client lost
		// the next block: restart the window from there.
		for _, b := range inFlight {
			conn.Write(b.pkt)
		}
	}
}

type ackStatus int

const (
	ackOK ackStatus = iota
	ackTimeout
	ackFatal
)

// awaitACK waits up to timeout for an ACK confirming between 1 and
// maxAhead blocks past base (mod 2^16) and returns how many it confirms.
//
// A stale/duplicate ACK (for base itself or older) is ignored *without*
// retransmitting and without restarting the timer. The old code resent the
// current block on every stray ACK, so one delayed ACK made the server send
// every following block twice for the rest of the transfer (the classic
// "Sorcerer's Apprentice" bug) and could burn through the retry budget and
// abort a healthy transfer.
func (s *Server) awaitACK(conn *net.UDPConn, buf []byte, base uint16, maxAhead int, timeout time.Duration) (int, ackStatus) {
	deadline := time.Now().Add(timeout)
	for {
		conn.SetReadDeadline(deadline)
		nRead, err := conn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return 0, ackTimeout
			}
			// Anything else on a connected UDP socket (e.g. an ICMP-driven
			// reset) is treated like a lost packet: the retry loop decides.
			if time.Now().After(deadline) {
				return 0, ackTimeout
			}
			continue
		}
		pkt := buf[:nRead]
		if block, perr := parseACK(pkt); perr == nil {
			if k := int(block - base); k >= 1 && k <= maxAhead {
				return k, ackOK
			}
			continue // stale ACK: keep waiting on the same deadline
		}
		if _, msg, perr := parseERROR(pkt); perr == nil {
			s.log.Info("client aborted transfer", "remote", conn.RemoteAddr().String(), "message", msg)
			return 0, ackFatal
		}
		// Unknown/garbage datagram: ignore.
	}
}

func (s *Server) handleWRQ(ctx context.Context, conn *net.UDPConn, remote *net.UDPAddr, req *request, path, mode string) {
	if !s.cfg.AllowWrite {
		s.sendError(conn, errAccessViolation, "uploads are disabled on this server")
		s.publish("error", remote.String(), "WRQ rejected: uploads disabled", map[string]any{"file": req.filename})
		return
	}

	n := s.negotiateOptions(req, 0)
	var totalSize int64
	if n.tsize != nil {
		totalSize = *n.tsize
	}

	f, err := os.Create(path)
	if err != nil {
		s.sendError(conn, errAccessViolation, "cannot create file")
		s.publish("error", remote.String(), "WRQ cannot create file", map[string]any{"file": req.filename, "error": err.Error()})
		return
	}
	completed := false
	defer func() {
		f.Close()
		if !completed {
			// Never leave a truncated upload behind looking like a valid
			// file (e.g. a half-received firmware image).
			os.Remove(path)
		}
	}()
	w := bufio.NewWriterSize(f, fileBufSize)

	s.publish("transfer", remote.String(), "TFTP upload started", map[string]any{"file": req.filename, "mode": mode, "blksize": n.blksize, "windowsize": n.windowsize})

	var initial []byte
	if len(n.oackKV) > 0 {
		initial = encodeOACK(n.oackKV)
	} else {
		initial = encodeACK(0)
	}
	conn.Write(initial)

	var (
		expected     uint16 = 1
		lastAck      uint16 // last block we ACKed (0 = the initial ACK/OACK)
		sinceAck     int    // in-order blocks received since lastAck
		gapAcked     bool   // already re-ACKed lastAck for the current gap
		gotAny       bool
		retries      int
		carryCR      bool
		bytesRecv    int64
		lastProgress = time.Now()
	)
	ack := func(block uint16) {
		conn.Write(encodeACK(block))
		lastAck, sinceAck, gapAcked = block, 0, false
	}

	buf := make([]byte, recvBufSize)
	for {
		if ctx.Err() != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(n.timeout))
		nRead, err := conn.Read(buf)
		if err != nil {
			var ne net.Error
			if !(errors.As(err, &ne) && ne.Timeout()) {
				continue // non-timeout socket error: behave as if the packet was lost
			}
			retries++
			if retries > s.cfg.MaxRetries {
				msg := "WRQ transfer aborted (timeout)"
				if !gotAny {
					msg = "WRQ timed out waiting for first DATA"
				}
				s.publish("error", remote.String(), msg, map[string]any{"file": req.filename, "block": expected})
				return
			}
			// Our last ACK (or OACK) may have been lost — repeat it so the
			// client resumes instead of both sides waiting each other out.
			if !gotAny {
				conn.Write(initial)
			} else {
				conn.Write(encodeACK(lastAck))
			}
			continue
		}

		block, data, perr := parseDATA(buf[:nRead])
		if perr != nil {
			if _, msg, oerr := parseERROR(buf[:nRead]); oerr == nil {
				s.log.Info("client aborted upload", "remote", remote.String(), "message", msg)
				s.publish("error", remote.String(), "WRQ aborted by client", map[string]any{"file": req.filename, "message": msg})
				return
			}
			continue // stray non-DATA datagram (e.g. a retransmitted WRQ): ignore
		}
		gotAny = true

		if block != expected {
			// Duplicate or out-of-order: tell the sender where we are, once
			// per gap, so a window isn't answered with a burst of identical
			// ACKs that would each trigger another full retransmission.
			// A repeat of exactly the last ACKed block means our ACK was
			// lost, so that one is always answered (RFC 1350).
			if !gapAcked || block == lastAck {
				conn.Write(encodeACK(lastAck))
				gapAcked = true
			}
			continue
		}
		retries = 0

		out := data
		if mode == "netascii" {
			out, carryCR = fromNetascii(data, carryCR)
		}
		if _, err := w.Write(out); err != nil {
			s.sendError(conn, errDiskFull, "write error")
			s.publish("error", remote.String(), "WRQ write failed", map[string]any{"file": req.filename, "error": err.Error()})
			return
		}
		bytesRecv += int64(len(data))
		expected++
		sinceAck++
		final := len(data) < n.blksize

		if final {
			if err := w.Flush(); err != nil {
				s.sendError(conn, errDiskFull, "write error")
				s.publish("error", remote.String(), "WRQ write failed", map[string]any{"file": req.filename, "error": err.Error()})
				return
			}
			ack(block)
			completed = true
			s.publish("transfer", remote.String(), "TFTP upload complete", map[string]any{"file": req.filename, "bytes": bytesRecv})
			s.dally(conn, buf, block, n.timeout)
			return
		}
		if sinceAck >= n.windowsize {
			ack(block)
		}
		s.publishProgress(remote.String(), req.filename, bytesRecv, totalSize, &lastProgress)
	}
}

// dally keeps the session socket open briefly after the final ACK of an
// upload (RFC 1350 §6): if that ACK was lost, the client retransmits the
// last DATA block and must get the ACK again, otherwise it reports a
// failed transfer even though the file was saved.
func (s *Server) dally(conn *net.UDPConn, buf []byte, lastBlock uint16, timeout time.Duration) {
	if timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		nRead, err := conn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return
			}
			continue
		}
		if block, _, perr := parseDATA(buf[:nRead]); perr == nil && block == lastBlock {
			conn.Write(encodeACK(lastBlock))
		}
	}
}

func (s *Server) sendError(conn *net.UDPConn, code uint16, msg string) {
	conn.Write(encodeERROR(code, msg))
}
