package cdp_test

import (
	"net/http"
	"testing"

	"github.com/rah-0/rod/lib/cdp"
)

func TestWebSocketHeader(t *testing.T) {
	g := setup(t)

	s := g.Serve()

	wait := make(chan struct{})
	s.Mux.HandleFunc("/a", func(_ http.ResponseWriter, r *http.Request) {
		g.Eq(r.Header.Get("Test"), "header")
		g.Eq(r.Host, "test.com")
		g.Eq(r.URL.Query().Get("q"), "ok")
		close(wait)
	})

	ws := cdp.WebSocket{}
	err := ws.Connect(g.Context(), s.URL("/a?q=ok"), http.Header{
		"Host":              {"test.com"},
		"Test":              {"header"},
		"Sec-WebSocket-Key": {"MDEyMzQ1Njc4OWFiY2RlZg=="},
	})
	<-wait

	g.Eq(err.Error(), "websocket bad handshake: 200 OK. ")
}
