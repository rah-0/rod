package cdp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
)

func TestWebSocketRejectedHandshakeBodyLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		body := strings.Repeat("x", 4096)
		serverDone := make(chan error, 1)
		go func() {
			if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
				serverDone <- err
				return
			}
			_, err := io.WriteString(server, "HTTP/1.1 403 Forbidden\r\nContent-Length: 1048576\r\n\r\n"+body)
			serverDone <- err
		}()
		ws := &WebSocket{Dialer: pipeDialer{client}}
		connected := make(chan error, 1)
		go func() { connected <- ws.Connect(ctx, "ws://browser.invalid", nil) }()
		if err := <-serverDone; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-connected:
			handshake, ok := errors.AsType[*BadHandshakeError](err)
			if !ok || handshake.Body != body {
				t.Fatalf("Connect = %v, want rejected handshake with bounded body", err)
			}
		default:
			t.Fatal("rejected handshake kept draining the body after its diagnostic limit")
		}
		if _, err := server.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("rejected handshake retained its socket: %v", err)
		}
	})
}
