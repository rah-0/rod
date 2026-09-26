package cdp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// frameHeader returns a server frame header announcing size payload bytes.
func frameHeader(opcode byte, fin bool, size uint64) []byte {
	if fin {
		opcode |= 0x80
	}
	header := []byte{opcode, 127, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint64(header[2:], size)
	return header
}

func TestWebSocketMessageSizeLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		limit int64
		input []byte
		want  string
	}{
		{name: "at limit", limit: 1000, input: serverFrame(1, true, bytes.Repeat([]byte("a"), 1000)), want: strings.Repeat("a", 1000)},
		{name: "announced beyond limit", limit: 1000, input: serverFrame(1, true, bytes.Repeat([]byte("a"), 1001))},
		{name: "reassembled beyond limit", limit: 1000, input: append(
			serverFrame(1, false, bytes.Repeat([]byte("a"), 600)),
			serverFrame(0, true, bytes.Repeat([]byte("b"), 600))...)},
		{name: "control frames excluded", limit: 4, input: bytes.Join([][]byte{
			serverFrame(1, false, []byte("ab")),
			serverFrame(10, true, []byte("control frame payload")),
			serverFrame(0, true, []byte("cd")),
		}, nil), want: "abcd"},
		{name: "default for zero", input: frameHeader(1, true, DefaultMaxMessageSize+1)},
		{name: "default for negative", limit: -1, input: frameHeader(1, true, DefaultMaxMessageSize+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := &frameConn{Reader: bytes.NewReader(test.input)}
			ws := &WebSocket{conn: conn, r: bufio.NewReader(conn), MaxMessageSize: test.limit}
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			got, err := ws.Read()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			if test.want != "" {
				if err != nil || string(got) != test.want {
					t.Fatalf("Read = %d bytes, %v", len(got), err)
				}
				return
			}
			if !errors.Is(err, ErrWebSocketMessageTooLarge) || !conn.closed {
				t.Fatalf("Read = %d bytes, %v; closed %t", len(got), err, conn.closed)
			}
			// The limit applies before the announced payload is allocated.
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
				t.Fatalf("rejected message allocated %d bytes", allocated)
			}
			opcode, status := readClientFrame(t, conn.written.Bytes())
			if opcode != 8 || !bytes.Equal(status, []byte{0x03, 0xf1}) {
				t.Fatalf("close frame: opcode %d, payload %x; want status 1009", opcode, status)
			}
		})
	}
}

func TestWebSocketUnlimitedMessageSizeHeader(t *testing.T) {
	// Only a header arrives. Its length fits an unlimited MaxMessageSize but
	// exceeds any possible allocation.
	conn := &frameConn{Reader: bytes.NewReader(frameHeader(1, true, 1<<62))}
	ws := &WebSocket{conn: conn, r: bufio.NewReader(conn), MaxMessageSize: math.MaxInt64}
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := ws.Read()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if !errors.Is(err, io.ErrUnexpectedEOF) || got != nil || !conn.closed {
		t.Fatalf("Read = %d bytes, %v; closed %t", len(got), err, conn.closed)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > maxPayloadPrealloc+16<<20 {
		t.Fatalf("header allocated %d bytes", allocated)
	}
}

func TestWebSocketAppendPayload(t *testing.T) {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i)
	}
	for _, test := range []struct {
		name     string
		message  []byte
		size     int
		prealloc int
	}{
		{name: "within prealloc", size: 1000, prealloc: 1000},
		{name: "beyond prealloc", size: 1000, prealloc: 7},
		{name: "fragment within prealloc", message: []byte("abc"), size: 1000, prealloc: 4096},
		{name: "fragment beyond prealloc", message: []byte("abc"), size: 1000, prealloc: 2},
		{name: "empty", size: 0, prealloc: 16},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := append(slices.Clone(test.message), data[:test.size]...)
			got, err := appendPayload(bytes.NewReader(data), test.message, test.size, test.prealloc)
			if err != nil || got == nil || !bytes.Equal(got, want) {
				t.Fatalf("appendPayload = %d bytes, %v", len(got), err)
			}
		})
	}
}

func TestWebSocketAppendPayloadGrowsWithData(t *testing.T) {
	const prealloc, sent = 1 << 10, 64 << 10
	reader := &capReader{Reader: bytes.NewReader(make([]byte, sent))}
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	// The announced length is far beyond the data the peer sends.
	_, err := appendPayload(reader, nil, 1<<40, prealloc)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("appendPayload = %v, want unexpected EOF", err)
	}
	// Every buffer offered to the reader is at most the larger of the
	// preallocation and the bytes already received.
	if reader.read != sent || reader.largest > max(prealloc, sent) {
		t.Fatalf("read %d bytes; largest buffer %d", reader.read, reader.largest)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("%d received bytes allocated %d bytes", sent, allocated)
	}
}

// capReader records the bytes read and the largest buffer offered to it.
type capReader struct {
	io.Reader
	read, largest int
}

func (r *capReader) Read(p []byte) (int, error) {
	r.largest = max(r.largest, len(p))
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

// handshakeResponse writes a valid upgrade response for req with extra headers.
func handshakeResponse(w io.Writer, req *http.Request, extra string) error {
	hash := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, err := fmt.Fprintf(w, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n%s\r\n",
		base64.StdEncoding.EncodeToString(hash[:]), extra)
	return err
}

func TestWebSocketHandshakeResponseLimit(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_ = server.SetDeadline(time.Now().Add(10 * time.Second))
	accepted := make(chan int, 1)
	go func() {
		total := 0
		defer func() { accepted <- total }()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		// A valid response with a 4 MiB header line. A pipe write reports only
		// the bytes the client consumed.
		var response bytes.Buffer
		_ = handshakeResponse(&response, req, "X-Padding: "+strings.Repeat("a", 4<<20)+"\r\n")
		total, _ = server.Write(response.Bytes())
	}()
	ws := &WebSocket{Dialer: pipeDialer{client}}
	err := ws.Connect(ctx, "ws://browser.invalid", nil)
	if !errors.Is(err, ErrWebSocketProtocol) || !strings.Contains(err.Error(), "handshake response exceeds") {
		t.Fatalf("Connect = %v, want handshake size error", err)
	}
	if consumed := <-accepted; consumed > maxHandshakeResponse {
		t.Fatalf("read %d handshake bytes, limit %d", consumed, maxHandshakeResponse)
	}
	if ws.conn != nil {
		t.Fatal("failed handshake retained its connection")
	}
}

func TestWebSocketHandshakeLimitEndsAfterUpgrade(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_ = server.SetDeadline(time.Now().Add(10 * time.Second))
	message := strings.Repeat("m", maxHandshakeResponse+1)
	serverDone := make(chan error, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err == nil {
			var response bytes.Buffer
			_ = handshakeResponse(&response, req, "")
			response.Write(serverFrame(1, true, []byte(message)))
			_, err = server.Write(response.Bytes())
		}
		serverDone <- err
	}()
	ws := &WebSocket{Dialer: pipeDialer{client}}
	if err := ws.Connect(ctx, "ws://browser.invalid", nil); err != nil {
		t.Fatal(err)
	}
	got, err := ws.Read()
	if err != nil || string(got) != message {
		t.Fatalf("Read = %d bytes, %v", len(got), err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketHandshakeTimeout(t *testing.T) {
	for _, test := range []struct {
		name     string
		timeout  time.Duration
		parent   time.Duration
		want     time.Duration
		ownCause bool
	}{
		{name: "default", want: DefaultHandshakeTimeout, ownCause: true},
		{name: "configured", timeout: 2 * time.Second, want: 2 * time.Second, ownCause: true},
		{name: "earlier context deadline", timeout: time.Minute, parent: time.Second, want: time.Second},
		{name: "disabled", timeout: -1, parent: 2 * time.Minute, want: 2 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, server := net.Pipe()
				conn := &trackedConn{Conn: client}
				defer conn.Close()
				defer server.Close()
				go func() {
					// Accept the request but never answer it.
					_, _ = io.Copy(io.Discard, server)
				}()
				ctx := context.Background()
				if test.parent > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, test.parent)
					defer cancel()
				}
				ws := &WebSocket{Dialer: pipeDialer{conn}, HandshakeTimeout: test.timeout}
				start := time.Now()
				err := ws.Connect(ctx, "ws://browser.invalid", nil)
				if elapsed := time.Since(start); elapsed != test.want {
					t.Fatalf("Connect returned after %s, want %s", elapsed, test.want)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Connect = %v, want deadline exceeded", err)
				}
				if test.ownCause != strings.Contains(err.Error(), "WebSocket handshake exceeded") {
					t.Fatalf("Connect error %q misreports the expired deadline", err)
				}
				if !conn.closed.Load() || ws.conn != nil {
					t.Fatal("timed-out handshake retained its connection")
				}
			})
		})
	}
}

func TestWebSocketHandshakeTimeoutEndsAfterConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		frames := make(chan []byte, 1)
		go func() {
			reader := bufio.NewReader(server)
			req, err := http.ReadRequest(reader)
			if err != nil || handshakeResponse(server, req, "") != nil {
				return
			}
			time.Sleep(time.Minute)
			if _, err := server.Write(serverFrame(1, true, []byte("late event"))); err != nil {
				return
			}
			frame := make([]byte, 6+len("late request"))
			if _, err := io.ReadFull(reader, frame); err == nil {
				frames <- frame
			}
		}()
		ws := &WebSocket{Dialer: pipeDialer{client}, HandshakeTimeout: time.Second}
		if err := ws.Connect(context.Background(), "ws://browser.invalid", nil); err != nil {
			t.Fatal(err)
		}
		// Reading and writing long after the handshake timeout must succeed.
		got, err := ws.Read()
		if err != nil || string(got) != "late event" {
			t.Fatalf("Read = %q, %v", got, err)
		}
		if err := ws.Send([]byte("late request")); err != nil {
			t.Fatal(err)
		}
		if _, payload := readClientFrame(t, <-frames); string(payload) != "late request" {
			t.Fatalf("server received %q", payload)
		}
	})
}

func TestWebSocketHandshakeTimeoutTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		// Accept the TCP connection but never answer the upgrade.
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	ws := &WebSocket{HandshakeTimeout: 100 * time.Millisecond}
	start := time.Now()
	err = ws.Connect(context.Background(), "ws://"+listener.Addr().String(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Connect = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("handshake timeout took %s", elapsed)
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not accept the connection")
	}
}
