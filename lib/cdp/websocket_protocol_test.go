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
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type addressDialer struct {
	conn    net.Conn
	address string
}

func (d *addressDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.address = address
	return d.conn, nil
}

func TestWebSocketProtocolHandshake(t *testing.T) {
	const override = "MDEyMzQ1Njc4OWFiY2RlZg=="
	var previousKey string
	for _, test := range []struct {
		name      string
		u         string
		header    http.Header
		response  http.Header
		badAccept bool
		wantError bool
		address   string
	}{
		{name: "fresh key", u: "ws://browser.invalid/path", address: "browser.invalid:80"},
		{name: "another fresh key", u: "ws://browser.invalid/path", address: "browser.invalid:80"},
		{name: "secure default port", u: "wss://[::1]/path", address: "[::1]:443"},
		{name: "HTTP alias", u: "http://browser.invalid/path", address: "browser.invalid:80"},
		{name: "canonical override", u: "ws://browser.invalid:9222/path", address: "browser.invalid:9222", header: http.Header{"Sec-Websocket-Key": {override}}},
		{name: "lowercase override", u: "ws://browser.invalid/path", address: "browser.invalid:80", header: http.Header{"sec-websocket-key": {override}}},
		{name: "identical duplicate override", u: "ws://browser.invalid/path", address: "browser.invalid:80", header: http.Header{"Sec-WebSocket-Key": {override}, "sec-websocket-key": {override}}},
		{name: "conflicting overrides", u: "ws://browser.invalid/path", wantError: true, header: http.Header{"Sec-WebSocket-Key": {override}, "sec-websocket-key": {"YWJjZGVmZ2hpamtsbW5vcA=="}}},
		{name: "invalid override", u: "ws://browser.invalid/path", wantError: true, header: http.Header{"Sec-WebSocket-Key": {"nil"}}},
		{name: "invalid accept", u: "ws://browser.invalid/path", badAccept: true, wantError: true},
		{name: "invalid upgrade", u: "ws://browser.invalid/path", response: http.Header{"Upgrade": {"h2c"}}, wantError: true},
		{name: "invalid connection", u: "ws://browser.invalid/path", response: http.Header{"Connection": {"keep-alive"}}, wantError: true},
		{name: "unsupported compression", u: "ws://browser.invalid/path", response: http.Header{"Sec-WebSocket-Extensions": {"permessage-deflate"}}, wantError: true},
		{name: "unrequested protocol", u: "ws://browser.invalid/path", response: http.Header{"Sec-WebSocket-Protocol": {"other"}}, wantError: true},
		{name: "negotiated protocol", u: "ws://browser.invalid/path", address: "browser.invalid:80", header: http.Header{"Sec-WebSocket-Protocol": {"cdp"}}, response: http.Header{"Sec-WebSocket-Protocol": {"cdp"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_ = server.SetDeadline(time.Now().Add(5 * time.Second))
			request := make(chan *http.Request, 1)
			serverDone := make(chan error, 1)
			go func() {
				req, err := http.ReadRequest(bufio.NewReader(server))
				if err != nil {
					request <- nil
					serverDone <- err
					return
				}
				request <- req
				sum := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
				accept := base64.StdEncoding.EncodeToString(sum[:])
				if test.badAccept {
					accept = "invalid"
				}
				response := make(http.Header)
				response.Set("Connection", "keep-alive, Upgrade")
				response.Set("Upgrade", "WebSocket")
				response.Set("Sec-WebSocket-Accept", accept)
				for name, values := range test.response {
					response[http.CanonicalHeaderKey(name)] = values
				}
				var reply bytes.Buffer
				reply.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
				_ = response.Write(&reply)
				reply.WriteString("\r\n")
				_, err = server.Write(reply.Bytes())
				serverDone <- err
			}()
			dialer := &addressDialer{conn: client}
			ws := &WebSocket{Dialer: dialer}
			err := ws.Connect(ctx, test.u, test.header)
			if test.wantError {
				if err == nil {
					t.Fatal("invalid handshake succeeded")
				}
				_ = client.Close()
			} else if err != nil {
				t.Fatal(err)
			}
			req := <-request
			serverErr := <-serverDone
			if test.wantError {
				return
			}
			if serverErr != nil {
				t.Fatal(serverErr)
			}
			if dialer.address != test.address {
				t.Fatalf("dialed %q, want %q", dialer.address, test.address)
			}
			keys := req.Header.Values("Sec-WebSocket-Key")
			if len(keys) != 1 {
				t.Fatalf("wire contains %d keys", len(keys))
			}
			decoded, err := base64.StdEncoding.DecodeString(keys[0])
			if err != nil || len(decoded) != 16 {
				t.Fatalf("invalid nonce %q", keys[0])
			}
			for name := range test.header {
				if strings.EqualFold(name, "Sec-WebSocket-Key") && keys[0] != override {
					t.Fatalf("override was lost: %q", keys[0])
				}
			}
			if test.header == nil {
				if previousKey == keys[0] {
					t.Fatal("handshakes reused a nonce")
				}
				previousKey = keys[0]
			}
			if err := ws.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type frameConn struct {
	io.Reader
	written bytes.Buffer
	closed  bool
}

func (c *frameConn) Write(p []byte) (int, error)    { return c.written.Write(p) }
func (c *frameConn) Close() error                   { c.closed = true; return nil }
func (*frameConn) LocalAddr() net.Addr              { return nil }
func (*frameConn) RemoteAddr() net.Addr             { return nil }
func (*frameConn) SetDeadline(time.Time) error      { return nil }
func (*frameConn) SetReadDeadline(time.Time) error  { return nil }
func (*frameConn) SetWriteDeadline(time.Time) error { return nil }

func serverFrame(opcode byte, fin bool, payload []byte) []byte {
	first := opcode
	if fin {
		first |= 0x80
	}
	frame := []byte{first, byte(len(payload))}
	if len(payload) > 65535 {
		frame = append([]byte{first, 127}, make([]byte, 8)...)
		binary.BigEndian.PutUint64(frame[2:], uint64(len(payload)))
	} else if len(payload) > 125 {
		frame = append([]byte{first, 126}, make([]byte, 2)...)
		binary.BigEndian.PutUint16(frame[2:], uint16(len(payload)))
	}
	return append(frame, payload...)
}

func readClientFrame(t *testing.T, frame []byte) (byte, []byte) {
	t.Helper()
	if len(frame) < 6 || frame[0]&0x80 == 0 || frame[1]&0x80 == 0 {
		t.Fatalf("invalid client frame: %x", frame)
	}
	length, offset := int(frame[1]&127), 2
	if length == 126 {
		length = int(binary.BigEndian.Uint16(frame[2:]))
		offset = 4
	}
	if length == 127 {
		length = int(binary.BigEndian.Uint64(frame[2:]))
		offset = 10
	}
	if len(frame) != offset+4+length {
		t.Fatalf("invalid client frame length: %d", len(frame))
	}
	payload := bytes.Clone(frame[offset+4:])
	for i := range payload {
		payload[i] ^= frame[offset+i%4]
	}
	return frame[0] & 15, payload
}

func TestWebSocketProtocolFrames(t *testing.T) {
	for _, size := range []int{0, 3, 126, 65536} {
		t.Run(fmt.Sprintf("payload=%d", size), func(t *testing.T) {
			payload := []byte(strings.Repeat("x", size))
			conn := &frameConn{Reader: bytes.NewReader(serverFrame(1, true, payload))}
			ws := &WebSocket{conn: conn, r: bufio.NewReader(conn)}
			got, err := ws.Read()
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("read: %d bytes, %v", len(got), err)
			}
			if err := ws.Send(payload); err != nil {
				t.Fatal(err)
			}
			opcode, sent := readClientFrame(t, conn.written.Bytes())
			if opcode != 1 || !bytes.Equal(sent, payload) || !bytes.Equal(payload, []byte(strings.Repeat("x", size))) {
				t.Fatal("send changed its payload")
			}
		})
	}
	frames := append(serverFrame(10, true, []byte("unsolicited")), serverFrame(1, false, []byte("frag"))...)
	frames = append(frames, serverFrame(9, true, []byte("ping"))...)
	frames = append(frames, serverFrame(10, true, nil)...)
	frames = append(frames, serverFrame(0, true, []byte("mented"))...)
	conn := &frameConn{Reader: bytes.NewReader(frames)}
	ws := &WebSocket{conn: conn, r: bufio.NewReader(conn)}
	got, err := ws.Read()
	if err != nil || string(got) != "fragmented" {
		t.Fatalf("fragmented read: %q, %v", got, err)
	}
	opcode, payload := readClientFrame(t, conn.written.Bytes())
	if opcode != 10 || string(payload) != "ping" {
		t.Fatalf("ping response: opcode %d, %q", opcode, payload)
	}
}

func TestWebSocketProtocolClose(t *testing.T) {
	for _, payload := range [][]byte{nil, {3, 232, 'b', 'y', 'e'}} {
		conn := &frameConn{Reader: bytes.NewReader(serverFrame(8, true, payload))}
		ws := &WebSocket{conn: conn, r: bufio.NewReader(conn)}
		_, err := ws.Read()
		if !errors.Is(err, ErrWebSocketClosed) || !conn.closed {
			t.Fatalf("close did not terminate: %v", err)
		}
		opcode, response := readClientFrame(t, conn.written.Bytes())
		if opcode != 8 || !bytes.Equal(response, payload) {
			t.Fatalf("close response: %d %x", opcode, response)
		}
	}
}

func TestWebSocketControlReplyTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		ws := &WebSocket{conn: client, r: bufio.NewReader(client)}
		go func() { _, _ = server.Write(serverFrame(9, true, []byte("ping"))) }()
		start := time.Now()
		if _, err := ws.Read(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stalled pong response: %v", err)
		}
		if time.Since(start) != 5*time.Second {
			t.Fatalf("control reply deadline: %s", time.Since(start))
		}
	})
}

func TestClientWebSocketPeerClose(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	cdp := New().Start(&WebSocket{conn: client, r: bufio.NewReader(client)})
	defer cdp.Close()
	result := make(chan error, 1)
	go func() { _, err := cdp.Call(ctx, "", "Browser.getVersion", nil); result <- err }()
	// This short CDP request fits in one frame. Receiving it ensures the call
	// is pending when the peer sends its close frame.
	var header [6]byte
	if _, err := io.ReadFull(server, header[:]); err != nil {
		t.Fatal(err)
	}
	if header[1]&127 > 125 {
		t.Fatal("request unexpectedly needs an extended frame length")
	}
	if _, err := io.CopyN(io.Discard, server, int64(header[1]&127)); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write(serverFrame(8, true, []byte{3, 232})); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 8)
	if _, err := io.ReadFull(server, ack); err != nil {
		t.Fatal(err)
	}
	opcode, payload := readClientFrame(t, ack)
	if opcode != 8 || !bytes.Equal(payload, []byte{3, 232}) {
		t.Fatal("peer close was not acknowledged")
	}
	if err := <-result; !errors.Is(err, ErrWebSocketClosed) {
		t.Fatalf("pending call: %v", err)
	}
	if _, open := <-cdp.Event(); open {
		t.Fatal("event channel remains open after peer close")
	}
}

func TestWebSocketProtocolMalformedFrames(t *testing.T) {
	for _, frame := range [][]byte{
		{0xc1, 0}, {0x81, 0x80}, {0x89, 126}, {0x09, 0}, {0x80, 0}, {0x83, 0}, {0x82, 0},
		{0x88, 1, 0}, {0x88, 2, 3, 237}, {0x88, 3, 3, 232, 0xff}, {0x81, 1, 0xff},
		{0x81, 126, 0, 1}, {0x81, 127, 0, 0, 0, 0, 0, 0, 0, 1},
		{0x81, 127, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0x81, 127, 0, 0, 0, 0, 0x7f, 0xff, 0xff, 0xff}, // no enormous allocation for absent payload
		{0x01, 0, 0x81, 0},
	} {
		conn := &frameConn{Reader: bytes.NewReader(frame)}
		ws := &WebSocket{conn: conn, r: bufio.NewReader(conn)}
		if _, err := ws.Read(); err == nil || !conn.closed {
			t.Fatalf("accepted malformed frame %x: %v", frame, err)
		}
	}
}
