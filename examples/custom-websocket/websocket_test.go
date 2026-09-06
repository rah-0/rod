package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func pipeTransport(t *testing.T) (*WebSocket, net.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	client, server := net.Pipe()
	w := &WebSocket{Context: ctx, Conn: client, Reader: client, Writing: make(chan struct{}, 1)}
	stop := context.AfterFunc(ctx, func() { _ = w.Close() })
	t.Cleanup(func() {
		stop()
		cancel()
		_ = w.Close()
		_ = server.Close()
	})
	return w, server
}

func TestSendCancellationPreservesUnwrittenConnection(t *testing.T) {
	w, server := pipeTransport(t)
	ctx, cancel := context.WithCancel(w.Context)
	cancel()
	if err := w.SendContext(ctx, []byte("unused")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled send: %v", err)
	}
	received := make(chan error, 1)
	go func() {
		payload, err := wsutil.ReadClientText(server)
		if err == nil && string(payload) != "next request" {
			err = fmt.Errorf("unexpected payload: %q", payload)
		}
		received <- err
	}()
	if err := w.Send([]byte("next request")); err != nil {
		t.Fatal(err)
	}
	if err := <-received; err != nil {
		t.Fatal(err)
	}
}

type NotifyConn struct {
	net.Conn
	Writing chan struct{}
	Once    sync.Once
}

func (c *NotifyConn) Write(p []byte) (int, error) {
	c.Once.Do(func() { close(c.Writing) })
	return c.Conn.Write(p)
}

func TestSendCancellationInterruptsBlockedWrite(t *testing.T) {
	w, _ := pipeTransport(t)
	writing := make(chan struct{})
	w.Conn = &NotifyConn{Conn: w.Conn, Writing: writing}
	ctx, cancel := context.WithCancel(w.Context)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- w.SendContext(ctx, []byte("blocked")) }()
	select {
	case <-writing:
	case <-w.Context.Done():
		t.Fatal("write did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked write: %v", err)
		}
	case <-w.Context.Done():
		t.Fatal("cancellation did not interrupt write")
	}
}

func TestConcurrentFrames(t *testing.T) {
	w, server := pipeTransport(t)
	received := make(chan error, 1)
	go func() {
		seen := make(map[string]bool)
		for range 8 {
			message, err := wsutil.ReadClientText(server)
			if err != nil {
				received <- err
				return
			}
			seen[string(message)] = true
		}
		if len(seen) != 8 {
			received <- fmt.Errorf("received %d distinct requests", len(seen))
			return
		}
		received <- nil
	}()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if err := w.Send([]byte(fmt.Sprintf("request-%d", i))); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := <-received; err != nil {
		t.Fatal(err)
	}
}

func TestReadHandlesPing(t *testing.T) {
	w, server := pipeTransport(t)
	result := make(chan error, 1)
	go func() {
		if err := wsutil.WriteServerMessage(server, ws.OpPing, []byte("ping")); err != nil {
			result <- err
			return
		}
		frame, err := ws.ReadFrame(server)
		if err != nil {
			result <- err
			return
		}
		ws.Cipher(frame.Payload, frame.Header.Mask, 0)
		if frame.Header.OpCode != ws.OpPong || !frame.Header.Masked || string(frame.Payload) != "ping" {
			result <- fmt.Errorf("unexpected pong: %+v", frame)
			return
		}
		result <- wsutil.WriteServerText(server, []byte("response"))
	}()
	payload, err := w.Read()
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "response" {
		t.Fatalf("response = %q", payload)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestReadUsesBufferedFragments(t *testing.T) {
	w, _ := pipeTransport(t)
	frames := append(ws.MustCompileFrame(ws.NewFrame(ws.OpText, false, []byte("buffered "))),
		ws.MustCompileFrame(ws.NewFrame(ws.OpContinuation, true, []byte("response")))...)
	w.Reader = bytes.NewReader(frames)
	payload, err := w.Read()
	if err != nil || string(payload) != "buffered response" {
		t.Fatalf("buffered read = %q, %v", payload, err)
	}
}

func TestCloseInterruptsRead(t *testing.T) {
	w, _ := pipeTransport(t)
	result := make(chan error, 1)
	go func() { _, err := w.Read(); result <- err }()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed read succeeded")
		}
	case <-w.Context.Done():
		t.Fatal("close did not interrupt read")
	}
}
