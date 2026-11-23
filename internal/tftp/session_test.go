package tftp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kms6402/dodaemon/internal/config"
)

// startTestServer runs a TFTP server on an ephemeral loopback port and
// returns its address.
func startTestServer(t *testing.T, root string, allowWrite bool) *net.UDPAddr {
	t.Helper()
	s := New(config.TFTPConfig{
		Listen: "127.0.0.1:0", RootDir: root, AllowRead: true, AllowWrite: allowWrite,
		MaxBlksize: 65464, TimeoutSec: 1, MaxRetries: 3,
	}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Start(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	for i := 0; i < 200; i++ {
		s.mu.Lock()
		c := s.conn
		s.mu.Unlock()
		if c != nil {
			return c.LocalAddr().(*net.UDPAddr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return nil
}

func encodeReq(op opcode, file string, opts ...string) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, uint16(op))
	b = append(b, file...)
	b = append(b, 0)
	b = append(b, "octet"...)
	b = append(b, 0)
	for _, o := range opts {
		b = append(b, o...)
		b = append(b, 0)
	}
	return b
}

func readPkt(t *testing.T, c *net.UDPConn) ([]byte, *net.UDPAddr) {
	t.Helper()
	buf := make([]byte, 70000)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, from, err := c.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf[:n], from
}

func writeRandomFile(t *testing.T, dir, name string, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

// download runs an RRQ as a client and returns the received bytes and the
// number of DATA packets that arrived. dupAcks sends every ACK twice, to
// provoke the Sorcerer's Apprentice bug if it ever comes back.
func download(t *testing.T, srv *net.UDPAddr, file string, blksize, window int, dupAcks bool) ([]byte, int) {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var opts []string
	if blksize > 0 {
		opts = append(opts, "blksize", itoa(blksize))
	} else {
		blksize = 512
	}
	if window > 1 {
		opts = append(opts, "windowsize", itoa(window))
	} else {
		window = 1
	}
	c.WriteToUDP(encodeReq(opRRQ, file, opts...), srv)

	sendACK := func(to *net.UDPAddr, b uint16) {
		c.WriteToUDP(encodeACK(b), to)
		if dupAcks {
			c.WriteToUDP(encodeACK(b), to)
		}
	}

	var out bytes.Buffer
	var expected uint16 = 1
	packets, inWindow := 0, 0
	for {
		pkt, from := readPkt(t, c)
		op := opcode(binary.BigEndian.Uint16(pkt[:2]))
		switch op {
		case opOACK:
			sendACK(from, 0)
			continue
		case opERROR:
			_, msg, _ := parseERROR(pkt)
			t.Fatalf("server error: %s", msg)
		}
		block, data, err := parseDATA(pkt)
		if err != nil {
			t.Fatalf("unexpected packet %v", pkt[:4])
		}
		packets++
		if block != expected {
			continue
		}
		out.Write(data)
		expected++
		inWindow++
		last := len(data) < blksize
		if last || inWindow == window {
			sendACK(from, block)
			inWindow = 0
		}
		if last {
			return out.Bytes(), packets
		}
	}
}

func itoa(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRRQ_DefaultBlocksizeExactMultiple(t *testing.T) {
	root := t.TempDir()
	want := writeRandomFile(t, root, "f.bin", 512*20) // needs a trailing empty block
	srv := startTestServer(t, root, false)
	got, _ := download(t, srv, "f.bin", 0, 0, false)
	if !bytes.Equal(got, want) {
		t.Fatalf("content mismatch: got %d bytes want %d", len(got), len(want))
	}
}

func TestRRQ_BlksizeAndWindowsize(t *testing.T) {
	root := t.TempDir()
	want := writeRandomFile(t, root, "fw.bin", 1468*300+123)
	srv := startTestServer(t, root, false)
	got, packets := download(t, srv, "fw.bin", 1468, 16, false)
	if !bytes.Equal(got, want) {
		t.Fatalf("content mismatch: got %d bytes want %d", len(got), len(want))
	}
	if packets != 301 {
		t.Fatalf("expected 301 DATA packets (no retransmits), got %d", packets)
	}
}

// A duplicated ACK must never make the server send a block twice.
func TestRRQ_DuplicateACKsDoNotCauseRetransmits(t *testing.T) {
	root := t.TempDir()
	want := writeRandomFile(t, root, "dup.bin", 512*100+7)
	srv := startTestServer(t, root, false)
	got, packets := download(t, srv, "dup.bin", 0, 0, true)
	if !bytes.Equal(got, want) {
		t.Fatal("content mismatch")
	}
	if packets != 101 {
		t.Fatalf("expected 101 DATA packets, got %d (duplicate ACKs triggered retransmits)", packets)
	}
}

// A retransmitted RRQ from the same client port must not spawn a second
// parallel session.
func TestRRQ_RetransmittedRequestIgnored(t *testing.T) {
	root := t.TempDir()
	writeRandomFile(t, root, "r.bin", 100)
	srv := startTestServer(t, root, false)

	c, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer c.Close()
	req := encodeReq(opRRQ, "r.bin")
	c.WriteToUDP(req, srv)
	c.WriteToUDP(req, srv)

	pkt, from := readPkt(t, c)
	if _, _, err := parseDATA(pkt); err != nil {
		t.Fatal("expected DATA")
	}
	buf := make([]byte, 1000)
	c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		_, other, err := c.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if other.Port != from.Port {
			t.Fatalf("second session started from port %d", other.Port)
		}
	}
	c.WriteToUDP(encodeACK(1), from)
}

func TestWRQ_Windowsize(t *testing.T) {
	root := t.TempDir()
	srv := startTestServer(t, root, true)
	payload := make([]byte, 1024*50+10)
	rand.Read(payload)

	c, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer c.Close()
	c.WriteToUDP(encodeReq(opWRQ, "up.bin", "blksize", "1024", "windowsize", "4"), srv)
	pkt, from := readPkt(t, c)
	if opcode(binary.BigEndian.Uint16(pkt[:2])) != opOACK {
		t.Fatalf("expected OACK, got %v", pkt)
	}

	blocks := (len(payload) / 1024) + 1
	var acked uint16
	for b := 1; b <= blocks; {
		// send up to 4 blocks, then expect an ACK for the last of them
		end := b + 3
		if end > blocks {
			end = blocks
		}
		for i := b; i <= end; i++ {
			lo := (i - 1) * 1024
			hi := lo + 1024
			if hi > len(payload) {
				hi = len(payload)
			}
			c.WriteToUDP(encodeDATA(uint16(i), payload[lo:hi]), from)
		}
		ackPkt, _ := readPkt(t, c)
		blk, err := parseACK(ackPkt)
		if err != nil {
			t.Fatalf("expected ACK, got %v", ackPkt)
		}
		acked = blk
		if int(acked) != end {
			t.Fatalf("expected ACK %d, got %d", end, acked)
		}
		b = end + 1
	}

	time.Sleep(50 * time.Millisecond)
	got, err := os.ReadFile(filepath.Join(root, "up.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("uploaded content mismatch: %d vs %d bytes", len(got), len(payload))
	}
}
