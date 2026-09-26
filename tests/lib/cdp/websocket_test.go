package cdp_test

import (
	"context"
	"errors"
	"fmt"
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

func TestWebSocketMessageSizeLimit(t *testing.T) {
	g := setup(t)

	ctx := g.Context()
	client, id := newPageWith(ctx, g, &cdp.WebSocket{MaxMessageSize: 64 * 1024})

	_, err := client.Call(ctx, id, "Runtime.evaluate", map[string]any{
		"expression":    `"a".repeat(1024 * 1024)`,
		"returnByValue": true,
	})
	g.True(errors.Is(err, cdp.ErrWebSocketMessageTooLarge))

	// The oversized response terminates the connection for every caller.
	_, err = client.Call(ctx, "", "Browser.getVersion", nil)
	g.True(errors.Is(err, cdp.ErrWebSocketMessageTooLarge))
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

func newPage(ctx context.Context, g testutil.G) (*cdp.Client, string) {
	return newPageWith(ctx, g, &cdp.WebSocket{})
}

func newPageWith(ctx context.Context, g testutil.G, ws *cdp.WebSocket) (*cdp.Client, string) {
	l := launcher.New()
	g.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	u := l.MustLaunch()

	g.E(ws.Connect(ctx, u, nil))
	client := cdp.New().Start(ws)

	go func() {
		for range client.Event() {
			utils.Noop()
		}
	}()

	file, err := filepath.Abs(filepath.FromSlash("../../../lib/cdp/fixtures/basic.html"))
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
	g.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	u := l.MustLaunch()

	ws := &cdp.WebSocket{}
	g.E(ws.Connect(g.Context(), u, nil))

	g.Panic(func() {
		_ = ws.Connect(g.Context(), u, nil)
	})
}
