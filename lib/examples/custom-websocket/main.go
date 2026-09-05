// Package main ...
package main

import (
	"context"
	"fmt"
	"log"
	"net"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/launcher"
)

func main() {
	l := launcher.New()
	u := l.MustLaunch()
	defer func() {
		l.Kill()
		l.Cleanup()
	}()

	w := NewWebSocket(u)

	client := cdp.New().Start(w)

	browser := rod.New().Client(client).MustConnect()
	defer browser.MustClose()

	p := browser.MustPage("http://example.com")

	fmt.Println(p.MustInfo().Title)
}

// WebSocket is a custom websocket that uses gobwas/ws as the transport layer.
type WebSocket struct {
	conn net.Conn
}

// NewWebSocket ...
func NewWebSocket(u string) *WebSocket {
	conn, _, _, err := ws.Dial(context.Background(), u)
	if err != nil {
		log.Fatal(err)
	}
	return &WebSocket{conn}
}

// Send ...
func (w *WebSocket) Send(b []byte) error {
	return wsutil.WriteClientText(w.conn, b)
}

// Read ...
func (w *WebSocket) Read() ([]byte, error) {
	return wsutil.ReadServerText(w.conn)
}
