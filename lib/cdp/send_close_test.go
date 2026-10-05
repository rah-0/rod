package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// completedWriteConn lets the reader finish before a successful write returns.
type completedWriteConn struct {
	net.Conn
	ctx      context.Context
	readDone <-chan struct{}
	reset    chan error
}

func (c *completedWriteConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil {
		select {
		case <-c.readDone:
		case <-c.ctx.Done():
		}
	}
	return n, err
}

func (c *completedWriteConn) SetWriteDeadline(deadline time.Time) error {
	err := c.Conn.SetWriteDeadline(deadline)
	if deadline.IsZero() {
		c.reset <- err
	}
	return err
}

func TestClientCallPeerCloseAfterWrite(t *testing.T) {
	for _, name := range []string{"response", "no_response"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			raw, err := new(net.Dialer).DialContext(ctx, "tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			server, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			if err := server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}

			client := New()
			conn := &completedWriteConn{Conn: raw, ctx: ctx, readDone: client.done, reset: make(chan error, 1)}
			client.Start(&WebSocket{conn: conn, r: bufio.NewReader(conn)})
			callDone := make(chan result, 1)
			t.Cleanup(func() {
				cancel()
				_ = client.Close()
				select {
				case <-callDone:
				case <-time.After(5 * time.Second):
					t.Error("Call did not stop during cleanup")
				}
			})
			go func() {
				defer close(callDone)
				data, err := client.Call(ctx, "", "Browser.close", nil)
				callDone <- result{msg: data, err: err}
			}()

			opcode, payload := readPipeFrame(t, server)
			var request Request
			if err := json.Unmarshal(payload, &request); err != nil {
				t.Fatal(err)
			}
			if opcode != 1 || request.Method != "Browser.close" {
				t.Fatalf("request: opcode %d, method %q", opcode, request.Method)
			}
			if name == "response" {
				data, err := json.Marshal(Response{ID: request.ID, Result: json.RawMessage(`{}`)})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := server.Write(serverFrame(1, true, data)); err != nil {
					t.Fatal(err)
				}
			}
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}

			select {
			case got := <-callDone:
				if name == "response" {
					if got.err != nil || string(got.msg) != "{}" {
						t.Fatalf("Call = %s, %v; want response {}, nil", got.msg, got.err)
					}
				} else if !errors.Is(got.err, io.EOF) {
					t.Fatalf("Call = %s, %v; want EOF without a response", got.msg, got.err)
				}
			case <-ctx.Done():
				t.Fatal("Call did not finish after peer closed")
			}
			select {
			case err := <-conn.reset:
				if !errors.Is(err, net.ErrClosed) {
					t.Fatalf("deadline reset = %v, want net.ErrClosed", err)
				}
			default:
				t.Fatal("write deadline was not reset")
			}
		})
	}
}
