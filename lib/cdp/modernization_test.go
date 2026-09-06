package cdp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type pipeDialer struct{ conn net.Conn }

func (d pipeDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return d.conn, nil
}

type trackedConn struct {
	net.Conn
	closed   atomic.Bool
	deadline time.Time
}

func (c *trackedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func (c *trackedConn) SetDeadline(deadline time.Time) error {
	c.deadline = deadline
	return c.Conn.SetDeadline(deadline)
}

func TestWebSocketHandshakeLifecycle(t *testing.T) {
	for _, phase := range []string{"stalled_write", "stalled_read", "rejected", "deadline"} {
		t.Run(phase, func(t *testing.T) {
			client, server := net.Pipe()
			conn := &trackedConn{Conn: client}
			defer conn.Close()
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if phase == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
			}
			ready := make(chan struct{})
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				if phase != "stalled_write" {
					_, _ = http.ReadRequest(bufio.NewReader(server))
				}
				close(ready)
				if phase == "rejected" {
					_, _ = io.WriteString(server, "HTTP/1.1 403 Forbidden\r\nContent-Length: 6\r\n\r\ndenied")
					return
				}
				<-ctx.Done()
			}()
			ws := &WebSocket{Dialer: pipeDialer{conn}}
			result := make(chan error, 1)
			go func() { result <- ws.Connect(ctx, "ws://browser", nil) }()
			<-ready
			if phase == "stalled_write" || phase == "stalled_read" {
				cancel()
			}
			select {
			case err := <-result:
				switch phase {
				case "rejected":
					handshake, ok := errors.AsType[*BadHandshakeError](err)
					if !ok || handshake.Body != "denied" {
						t.Fatalf("Connect error = %v, want rejected handshake body", err)
					}
				case "deadline":
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("Connect error = %v, want deadline exceeded", err)
					}
				default:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("Connect error = %v, want canceled", err)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("handshake ignored cancellation")
			}
			if !conn.closed.Load() || ws.conn != nil {
				t.Fatal("failed handshake retained its connection")
			}
			<-serverDone
		})
	}
}

func TestWebSocketEstablishmentContext(t *testing.T) {
	client, server := net.Pipe()
	conn := &trackedConn{Conn: client}
	defer conn.Close()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			serverDone <- err
			return
		}
		hash := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, err = fmt.Fprintf(server, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(hash[:]))
		if err == nil {
			frame := make([]byte, 8)
			_, err = io.ReadFull(server, frame)
		}
		serverDone <- err
	}()
	ws := &WebSocket{Dialer: pipeDialer{conn}}
	if err := ws.Connect(ctx, "ws://browser", nil); err != nil {
		t.Fatal(err)
	}
	if !conn.deadline.IsZero() {
		t.Fatal("successful handshake retained an establishment deadline")
	}
	cancel()
	if err := ws.Send([]byte("ok")); err != nil {
		t.Fatalf("establishment cancellation closed the connected socket: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

type protocolSocket struct {
	sent        chan []byte
	incoming    chan []byte
	closed      chan struct{}
	sendBarrier <-chan struct{}
}

func (s *protocolSocket) Send(data []byte) error {
	s.sent <- data
	if s.sendBarrier != nil {
		<-s.sendBarrier
	}
	return nil
}
func (s *protocolSocket) Read() ([]byte, error) {
	data, ok := <-s.incoming
	if !ok {
		return nil, io.EOF
	}
	return data, nil
}
func (s *protocolSocket) Close() error { close(s.closed); return nil }

func TestClientMarshalError(t *testing.T) {
	client := New()
	_, err := client.Call(t.Context(), "", "test", make(chan int))
	if _, ok := errors.AsType[*json.UnsupportedTypeError](err); !ok {
		t.Fatalf("Call error = %v, want unsupported type", err)
	}
}

func TestClientMalformedMessage(t *testing.T) {
	for _, message := range []string{`{`, `{"id":"bad"}`, `{"method":42}`, `{"id":1,"error":"bad"}`} {
		t.Run(message, func(t *testing.T) {
			ws := &protocolSocket{sent: make(chan []byte, 2), incoming: make(chan []byte, 1), closed: make(chan struct{})}
			client := New().Start(ws)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			result := make(chan error, 2)
			for range 2 {
				go func() { _, err := client.Call(ctx, "", "test", nil); result <- err }()
			}
			for range 2 {
				<-ws.sent
			}
			ws.incoming <- []byte(message)
			for range 2 {
				err := <-result
				if err == nil || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("pending Call error = %v, want decoding error", err)
				}
			}
			if _, ok := <-client.Event(); ok {
				t.Fatal("events remained open")
			}
			_, err := client.Call(ctx, "", "later", nil)
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("later Call error = %v, want terminal decoding error", err)
			}
			<-ws.closed
			if len(ws.sent) != 0 {
				t.Fatal("sent a request after the reader terminated")
			}
		})
	}
}

func FuzzWebSocketFrame(f *testing.F) {
	for _, seed := range [][]byte{{}, {0x81}, {0x81, 0}, {0x81, 2, 'o', 'k'}, {0x81, 126, 0, 2, 'o', 'k'}, {0x81, 127, 0, 0, 0, 0, 0, 0, 0, 2, 'o', 'k'}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, frame []byte) {
		// Advertised lengths must never cause allocation before payload arrives.
		if len(frame) > 4096 {
			return
		}
		ws := &WebSocket{r: bufio.NewReader(bytes.NewReader(frame))}
		data, _ := ws.read()
		if len(data) > 4096 {
			t.Fatal("frame exceeded fuzz allocation bound")
		}
	})
}

type benchmarkSocket struct{ requests chan []byte }

func (s *benchmarkSocket) Send(data []byte) error {
	s.requests <- data
	return nil
}

func (s *benchmarkSocket) Read() ([]byte, error) {
	data, ok := <-s.requests
	if !ok {
		return nil, io.EOF
	}
	var request struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, err
	}
	return json.Marshal(Response{ID: request.ID, Result: json.RawMessage(`true`)})
}

// BenchmarkClientCallParallel measures concurrent request registration and
// response dispatch, including JSON encoding, through an in-memory transport.
func BenchmarkClientCallParallel(b *testing.B) {
	ws := &benchmarkSocket{requests: make(chan []byte, 128)}
	client := New().Start(ws)
	b.Cleanup(func() {
		close(ws.requests)
		for range client.Event() {
		}
	})
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			result, err := client.Call(b.Context(), "", "test.method", nil)
			if err != nil || string(result) != "true" {
				b.Errorf("Call = %q, %v", result, err)
				return
			}
		}
	})
}

func TestClientResponseBeforeEOF(t *testing.T) {
	barrier := make(chan struct{})
	defer close(barrier)
	ws := &protocolSocket{
		sent: make(chan []byte, 1), incoming: make(chan []byte, 1),
		closed: make(chan struct{}), sendBarrier: barrier,
	}
	client := New().Start(ws)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	completed := make(chan result, 1)
	go func() {
		data, err := client.Call(ctx, "", "test", nil)
		completed <- result{data, err}
	}()
	<-ws.sent
	ws.incoming <- []byte(`{"id":1,"result":true}`)
	close(ws.incoming)
	select {
	case <-ws.closed:
	case <-ctx.Done():
		t.Fatal("response dispatch waited for Send to return")
	}
	barrier <- struct{}{}
	got := <-completed
	if got.err != nil || string(got.msg) != "true" {
		t.Fatalf("Call = %s, %v; want response received before EOF", got.msg, got.err)
	}
}

func TestClientPendingResponseRouting(t *testing.T) {
	ws := &protocolSocket{
		sent: make(chan []byte, 3), incoming: make(chan []byte, 8),
		closed: make(chan struct{}),
	}
	client := New().Start(ws)
	t.Cleanup(func() { close(ws.incoming); <-ws.closed })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	canceledContext, stop := context.WithCancel(ctx)
	canceledCall := make(chan error, 1)
	go func() { _, err := client.Call(canceledContext, "", "canceled", nil); canceledCall <- err }()
	<-ws.sent
	stop()
	if err := <-canceledCall; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Call error = %v", err)
	}

	completed := make(chan result, 2)
	for range 2 {
		go func() {
			data, err := client.Call(ctx, "", "live", nil)
			completed <- result{data, err}
		}()
	}
	for range 2 {
		<-ws.sent
	}
	// Late canceled replies, unknown IDs, and duplicate replies must not block
	// later requests. Replies for the live calls arrive out of request order.
	for _, response := range []string{
		`{"id":1,"result":"canceled"}`,
		`{"id":999,"result":"unknown"}`,
		`{"id":3,"result":3}`,
		`{"id":3,"result":"duplicate"}`,
		`{"id":2,"result":2}`,
	} {
		ws.incoming <- []byte(response)
	}
	seen := map[string]bool{}
	for range 2 {
		got := <-completed
		if got.err != nil {
			t.Fatal(got.err)
		}
		seen[string(got.msg)] = true
	}
	if len(seen) != 2 || !seen["2"] || !seen["3"] {
		t.Fatalf("live replies = %v, want 2 and 3", seen)
	}
}
