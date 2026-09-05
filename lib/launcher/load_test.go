package launcher_test

import (
	"net/http"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/launcher"
)

// BenchmarkManager measures a complete managed browser lifecycle, including
// navigation, browser shutdown, and the manager's profile cleanup.
func BenchmarkManager(b *testing.B) {
	const managerToken = "test-manager-token-0123456789abcdef0123456789abcdef"
	server := testutil.New(b).Serve()
	server.Route("/", ".html", `<html><body>ok</body></html>`)
	manager := testutil.New(b).Serve()
	handler := launcher.NewManager(managerToken)
	finished := make(chan struct{}, 1)
	manager.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			defer func() { finished <- struct{}{} }()
		}
		handler.ServeHTTP(w, r)
	})

	for b.Loop() {
		func() {
			l := launcher.MustNewManaged(manager.URL(), managerToken)
			u, header := l.ClientHeader()
			ws := new(cdp.WebSocket)
			if err := ws.Connect(b.Context(), u, header); err != nil {
				b.Fatal(err)
			}
			defer ws.Close()
			client := cdp.New().Start(ws)
			browser := rod.New().Client(client).MustConnect()
			defer browser.MustClose()
			browser.MustPage(server.URL()).MustWaitLoad()
		}()
		<-finished
	}
}
