package launcher_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/launcher"
)

// BenchmarkManager measures a complete managed browser lifecycle, including
// navigation, browser shutdown, and the manager's profile cleanup.
func BenchmarkManager(b *testing.B) {
	const managerToken = "test-manager-token-0123456789abcdef0123456789abcdef"
	server := testutil.New(b).Serve()
	server.Route("/", ".html", `<html><body>ok</body></html>`)
	handler := launcher.NewManager(managerToken)
	finished := make(chan struct{}, 1)
	manager := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			defer func() { finished <- struct{}{} }()
		}
		handler.ServeHTTP(w, r)
	}))
	b.Cleanup(manager.Close)
	options := launcher.ManagerClientOptions{TLSConfig: manager.Client().Transport.(*http.Transport).TLSClientConfig}

	for b.Loop() {
		func() {
			l := launcher.MustNewManaged(b.Context(), manager.URL, managerToken, options)
			client := l.MustClient()
			defer client.Close()
			browser := rod.New().Client(client).MustConnect()
			defer browser.MustClose()
			browser.MustPage(server.URL()).MustWaitLoad()
		}()
		<-finished
	}
}
