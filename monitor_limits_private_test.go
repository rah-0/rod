package rod

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMonitorHeaderDeadline(t *testing.T) {
	u, closeServer := serve("127.0.0.1:0", func(net.Addr) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	defer closeServer()
	conn, err := net.Dial("tcp", strings.TrimPrefix(u, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nX-Slow: "); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	res, err := client.Get(u)
	if err != nil {
		t.Fatalf("healthy client blocked: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatal(res.Status)
	}
	_, err = http.ReadResponse(bufio.NewReader(conn), nil)
	if err == nil {
		t.Fatal("incomplete header was accepted")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("monitor did not enforce its header deadline")
	}
}

func TestMonitorBodyDeadline(t *testing.T) {
	u, closeServer := serve("127.0.0.1:0", func(net.Addr) http.Handler {
		return http.HandlerFunc(http.NotFound)
	})
	defer closeServer()
	conn, err := net.Dial("tcp", strings.TrimPrefix(u, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// A complete header clears ReadHeaderTimeout. Even a rejected request may
	// drain its unread body before the server can reuse or close the connection.
	if _, err := fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 16\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	res, err := client.Get(u)
	if err != nil {
		t.Fatalf("healthy client blocked by withheld body: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatal(res.Status)
	}
	if _, err := io.ReadAll(conn); err != nil {
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatal("monitor retained a request with a withheld body past its read deadline")
		}
	}
}
