package main

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/rah-0/rod/lib/cdp"
)

var (
	_ cdp.WebSocketable = (*WebSocket)(nil)
	_ io.Closer         = (*WebSocket)(nil)
)

// WebSocket adapts gobwas/ws to Rod. One CDP reader consumes messages, while
// concurrent requests and control replies share a gate that protects frames.
type WebSocket struct {
	Context   context.Context
	Conn      net.Conn
	Reader    io.Reader
	Writing   chan struct{}
	CloseOnce sync.Once
	CloseErr  error
}

// NewWebSocket uses ctx for dialing and the lifetime of the custom transport.
func NewWebSocket(ctx context.Context, endpoint string) (*WebSocket, error) {
	conn, buffered, _, err := ws.Dial(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	w := &WebSocket{Context: ctx, Conn: conn, Reader: conn, Writing: make(chan struct{}, 1)}
	if buffered != nil {
		// The server may send its first event with the handshake response.
		w.Reader = buffered
	}
	context.AfterFunc(ctx, func() { _ = w.Close() })
	return w, nil
}

// Close releases pending reads and writes and is safe to repeat concurrently.
func (w *WebSocket) Close() error {
	w.CloseOnce.Do(func() { w.CloseErr = w.Conn.Close() })
	return w.CloseErr
}

// Send implements the base transport interface. Rod uses SendContext when the
// optional context-aware method is available.
func (w *WebSocket) Send(data []byte) error {
	return w.SendContext(w.Context, data)
}

// SendContext preserves the connection if canceled before writing. Canceling an
// active write closes it because another frame cannot safely follow a partial one.
func (w *WebSocket) SendContext(ctx context.Context, data []byte) error {
	return w.write(ctx, func() error { return wsutil.WriteClientText(w.Conn, data) })
}

func (w *WebSocket) write(ctx context.Context, send func() error) error {
	select {
	case w.Writing <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.Writing }()
	if err := ctx.Err(); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = w.Close()
		close(interrupted)
	})
	err := send()
	if !stop() {
		<-interrupted
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		_ = w.Close()
	}
	return err
}

// Read handles fragmented text and serializes ping/close replies with data writes.
func (w *WebSocket) Read() ([]byte, error) {
	reader := wsutil.NewReader(w.Reader, ws.StateClientSide)
	reader.CheckUTF8 = true
	control := func(header ws.Header, payload io.Reader) error {
		return w.write(w.Context, func() error {
			return (wsutil.ControlHandler{
				Src: payload, Dst: w.Conn, State: ws.StateClientSide,
				DisableSrcCiphering: true,
			}).Handle(header)
		})
	}
	reader.OnIntermediate = control
	for {
		header, err := reader.NextFrame()
		if err != nil {
			return nil, err
		}
		if header.OpCode.IsControl() {
			if err := control(header, reader); err != nil {
				return nil, err
			}
			continue
		}
		if header.OpCode == ws.OpText {
			return io.ReadAll(reader)
		}
		if err := reader.Discard(); err != nil {
			return nil, err
		}
	}
}
