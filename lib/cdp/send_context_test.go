package cdp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// readPipeFrame reads one small client frame from the peer side of a pipe.
func readPipeFrame(t *testing.T, server net.Conn) (byte, []byte) {
	t.Helper()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 6)
	if _, err := io.ReadFull(server, header); err != nil {
		t.Fatal(err)
	}
	if header[1]&127 > 125 {
		t.Fatalf("unexpected extended frame header %x", header)
	}
	frame := make([]byte, 6+int(header[1]&127))
	copy(frame, header)
	if _, err := io.ReadFull(server, frame[6:]); err != nil {
		t.Fatal(err)
	}
	return readClientFrame(t, frame)
}

func TestWebSocketSendContext(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ws := &WebSocket{conn: client}
	// The peer accepts no bytes, so the deadline expires while building the
	// frame or before any byte of it is written.
	large := []byte(strings.Repeat("x", 20<<20))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := ws.SendContext(ctx, large); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("send = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("write ignored context")
	}
	if bytes.ContainsFunc(large, func(r rune) bool { return r != 'x' }) {
		t.Fatal("send changed its payload")
	}
	// An abandoned frame with no transmitted bytes must not cost the
	// connection. The next frame starts cleanly on the stream.
	done := make(chan error, 1)
	go func() { done <- ws.SendContext(t.Context(), []byte("ok")) }()
	opcode, payload := readPipeFrame(t, server)
	if opcode != 1 || string(payload) != "ok" {
		t.Fatalf("next frame: opcode %d, %q", opcode, payload)
	}
	if err := <-done; err != nil {
		t.Fatalf("connection closed after an unwritten frame: %v", err)
	}
}

func TestWebSocketSendTimeoutPreservesTransport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		ws := &WebSocket{conn: client}
		// The peer reads nothing, so the write times out before the
		// connection accepts any byte of the frame.
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		if err := ws.SendContext(ctx, []byte("blocked frame")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("send = %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- ws.SendContext(t.Context(), []byte("ok")) }()
		opcode, payload := readPipeFrame(t, server)
		if opcode != 1 || string(payload) != "ok" {
			t.Fatalf("next frame: opcode %d, %q", opcode, payload)
		}
		if err := <-done; err != nil {
			t.Fatalf("connection closed after an unwritten frame: %v", err)
		}
	})
}

func TestWebSocketPartialSendClosesTransport(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ws := &WebSocket{conn: client}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		// Accept only the first byte of the frame, then interrupt the write.
		if _, err := server.Read(make([]byte, 1)); err == nil {
			cancel()
		}
	}()
	if err := ws.SendContext(ctx, []byte("partially sent frame")); !errors.Is(err, context.Canceled) {
		t.Fatalf("send = %v", err)
	}
	// A partial frame cannot be completed, so no later frame may follow it.
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := server.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("interrupted connection remains open: %v", err)
	}
	if err := ws.Send([]byte("later")); err == nil {
		t.Fatal("send after a partial frame succeeded")
	}
}

func TestWebSocketShortWriteClosesTransport(t *testing.T) {
	conn := &shortWriteConn{}
	ws := &WebSocket{conn: conn}
	if err := ws.Send([]byte("frame")); !errors.Is(err, io.ErrShortWrite) || !conn.closed {
		t.Fatalf("short write = %v, closed %t", err, conn.closed)
	}
}

type shortWriteConn struct{ frameConn }

func (*shortWriteConn) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestWebSocketTLSSendTimeoutClosesTransport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		certificate, roots := testCertificate(t)
		serverTLS := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{certificate}})
		// The server completes the handshake but never reads application data.
		go func() { _ = serverTLS.Handshake() }()
		clientTLS := tls.Client(client, &tls.Config{RootCAs: roots, ServerName: "browser.test"})
		if err := clientTLS.Handshake(); err != nil {
			t.Fatal(err)
		}
		ws := &WebSocket{conn: clientTLS}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := ws.SendContext(ctx, []byte("blocked frame")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("send = %v", err)
		}
		// TLS rejects all writes after a timeout, even without transmitted bytes.
		if err := ws.Send([]byte("later")); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("send after TLS write timeout = %v, want closed connection", err)
		}
	})
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"browser.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: private}, roots
}

func TestWebSocketCanceledSendPreservesTransport(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ws := &WebSocket{conn: client}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ws.SendContext(ctx, []byte("unused")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- ws.SendContext(t.Context(), []byte("ok")) }()
	if _, err := server.Read(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketMaskBytes(t *testing.T) {
	mask := [4]byte{0x12, 0x34, 0x56, 0x78}
	for size := range 40 {
		src := make([]byte, size)
		for i := range src {
			src[i] = byte(i * 7)
		}
		dst := make([]byte, size)
		maskBytes(dst, src, mask)
		for i := range src {
			if dst[i] != src[i]^mask[i%4] {
				t.Fatalf("size %d byte %d = %#x, want %#x", size, i, dst[i], src[i]^mask[i%4])
			}
		}
	}
}
