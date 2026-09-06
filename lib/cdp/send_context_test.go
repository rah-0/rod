package cdp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestWebSocketSendContext(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ws := &WebSocket{conn: client}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := ws.SendContext(ctx, []byte("blocked frame")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("send = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("write ignored context")
	}
	// A canceled partial write must close the transport so no later frame can
	// append to it and silently corrupt the stream.
	if _, err := server.Read(make([]byte, 1)); err == nil {
		t.Fatal("interrupted connection remains open")
	}
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
