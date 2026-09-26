package main_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/launcher"
)

// countingClient counts CDP calls and the response bytes received by Rod, and
// can delay each call by a simulated round trip.
type countingClient struct {
	*cdp.Client
	calls, bytes atomic.Int64
	rtt          atomic.Int64
}

func (c *countingClient) Call(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
	c.calls.Add(1)
	if rtt := time.Duration(c.rtt.Load()); rtt > 0 {
		timer := time.NewTimer(rtt)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
	res, err := c.Client.Call(ctx, sessionID, method, params)
	c.bytes.Add(int64(len(res)))
	return res, err
}

// benchmarkCountingBrowser launches a browser whose CDP calls go through a
// countingClient. It also returns the browser process ID.
func benchmarkCountingBrowser(b *testing.B) (*rod.Browser, *countingClient, int) {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)
	launch := launcher.New().Context(ctx)
	b.Cleanup(func() {
		launch.Kill()
		launch.Cleanup()
	})
	url, err := launch.Launch()
	if err != nil {
		b.Fatal(err)
	}
	client, err := cdp.StartWithURL(ctx, url, nil)
	if err != nil {
		b.Fatal(err)
	}
	counter := &countingClient{Client: client}
	browser := rod.New().Context(ctx).Client(counter)
	if err := browser.Connect(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		counter.rtt.Store(0)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stop := context.AfterFunc(ctx, launch.Kill)
		defer stop()
		_ = browser.Context(ctx).Close()
		_ = client.Close()
	})
	return browser, counter, launch.PID()
}
