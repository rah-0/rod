package cdp_test

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/utils"
)

func TestWebSocketLargePayload(t *testing.T) {
	g := setup(t)

	ctx := g.Context()
	client, id := newPage(ctx, g)

	const size = 2 * 1024 * 1024

	res, err := client.Call(ctx, id, "Runtime.evaluate", map[string]any{
		"expression":    fmt.Sprintf(`"%s"`, strings.Repeat("a", size)),
		"returnByValue": true,
	})
	g.E(err)
	g.Gt(len(res), size) // 2MB
}

func ConcurrentCall(t *testing.T) {
	t.Helper()

	g := setup(t)

	ctx := g.Context()
	client, id := newPage(ctx, g)

	wg := sync.WaitGroup{}
	for range 30 {
		wg.Go(func() {
			res, err := client.Call(ctx, id, "Runtime.evaluate", map[string]any{
				"expression": `10`,
			})
			g.Nil(err)
			g.Eq(string(res), "{\"result\":{\"type\":\"number\",\"value\":10,\"description\":\"10\"}}")
		})
	}
	wg.Wait()
}

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
		"Sec-WebSocket-Key": {"key"},
	})
	<-wait

	g.Eq(err.Error(), "websocket bad handshake: 200 OK. ")
}

func newPage(ctx context.Context, g testutil.G) (*cdp.Client, string) {
	l := launcher.New()
	u := l.MustLaunch()
	g.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})

	client := cdp.New().Start(cdp.MustConnectWS(u))

	go func() {
		for range client.Event() {
			utils.Noop()
		}
	}()

	file, err := filepath.Abs(filepath.FromSlash("fixtures/basic.html"))
	g.E(err)

	res, err := client.Call(ctx, "", "Target.createTarget", map[string]any{
		"url": "file://" + file,
	})
	g.E(err)

	targetID := jsonvalue.New(res).Get("targetId").String()

	res, err = client.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID,
		"flatten":  true,
	})
	g.E(err)

	sessionID := jsonvalue.New(res).Get("sessionId").String()

	return client, sessionID
}

func TestDuplicatedConnectErr(t *testing.T) {
	g := setup(t)

	l := launcher.New()
	u := l.MustLaunch()
	g.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})

	ws := &cdp.WebSocket{}
	g.E(ws.Connect(g.Context(), u, nil))

	g.Panic(func() {
		_ = ws.Connect(g.Context(), u, nil)
	})
}
