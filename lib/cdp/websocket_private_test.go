package cdp

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod/internal/testutil"
)

var setup = testutil.Setup(nil)

func TestWebSocketErr(t *testing.T) {
	g := setup(t)

	ws := WebSocket{}
	g.Err(ws.Connect(g.Context(), "://", nil))

	ws.Dialer = &net.Dialer{}
	ws.initDialer(nil)

	u, err := url.Parse("wss://no-exist")
	g.E(err)
	ws.Dialer = nil
	ws.initDialer(u)

	mc := &MockConn{}
	ws.conn = mc
	g.Err(ws.Send([]byte("test")))

	mc.errOnCount = 1
	mc.frame = []byte{0, 127, 1}
	ws.r = bufio.NewReader(mc)
	g.Err(ws.Read())

	mc.errOnCount = 1
	mc.frame = []byte{0}
	ws.r = bufio.NewReader(mc)
	g.Err(ws.Read())

	g.Err(ws.handshake(g.Timeout(0), nil, nil))

	mc.errOnCount = 1
	g.Err(ws.handshake(g.Context(), u, nil))
}

func TestWebSocketTLSCancellation(t *testing.T) {
	for _, phase := range []string{"before_dial", "during_handshake"} {
		t.Run(phase, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			clientHello := make(chan error, 1)
			serverDone := make(chan struct{})
			connectDone := make(chan struct{})
			t.Cleanup(func() {
				_ = listener.Close()
				<-serverDone
				<-connectDone
			})

			go func() {
				defer close(serverDone)
				conn, err := listener.Accept()
				if err != nil {
					clientHello <- err
					return
				}
				defer func() { _ = conn.Close() }()
				stop := context.AfterFunc(t.Context(), func() { _ = conn.Close() })
				defer stop()

				var firstByte [1]byte
				_, err = conn.Read(firstByte[:])
				clientHello <- err
				// Keep the TLS handshake unanswered until test cleanup.
				<-t.Context().Done()
			}()

			if phase == "before_dial" {
				cancel()
			}
			result := make(chan error, 1)
			go func() {
				defer close(connectDone)
				result <- new(WebSocket).Connect(ctx, "wss://"+listener.Addr().String(), nil)
			}()

			if phase == "during_handshake" {
				select {
				case err := <-clientHello:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("TLS client did not start its handshake")
				}
				cancel()
			}

			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Connect error = %v, want context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("TLS connection ignored cancellation")
			}
		})
	}
}

type MockConn struct {
	sync.Mutex
	errOnCount int
	frame      []byte
}

func (c *MockConn) checkErr(d int) error {
	c.Lock()
	defer c.Unlock()

	if c.errOnCount == 0 {
		return errors.New("err")
	}
	c.errOnCount += d
	return nil
}

func (c *MockConn) Read(b []byte) (int, error) {
	if err := c.checkErr(-1); err != nil {
		return 0, err
	}

	return copy(b, c.frame), nil
}

func (c *MockConn) Write(b []byte) (int, error) {
	if err := c.checkErr(-1); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *MockConn) Close() error {
	return c.checkErr(0)
}

func (c *MockConn) LocalAddr() net.Addr {
	return nil
}

func (c *MockConn) RemoteAddr() net.Addr {
	return nil
}

func (c *MockConn) SetDeadline(_ time.Time) error {
	return nil
}

func (c *MockConn) SetReadDeadline(_ time.Time) error {
	return nil
}

func (c *MockConn) SetWriteDeadline(_ time.Time) error {
	return nil
}
