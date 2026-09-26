package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

// latencyClient answers every protocol command after a fixed round-trip delay.
type latencyClient struct {
	delay  time.Duration
	events chan *cdp.Event
}

func (client *latencyClient) Event() <-chan *cdp.Event { return client.events }

func (client *latencyClient) Call(ctx context.Context, _, method string, params any) ([]byte, error) {
	if client.delay > 0 {
		timer := time.NewTimer(client.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	} else if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "Target.attachToTarget":
		request := params.(proto.TargetAttachToTarget)
		return json.Marshal(proto.TargetAttachToTargetResult{SessionID: proto.TargetSessionID(request.TargetID) + "-session"})
	case "Runtime.callFunctionOn":
		return []byte(`{"result":{"type":"number","value":16,"description":"16"}}`), nil
	}
	return []byte(`{}`), nil
}

func benchmarkLatencyBrowser(b *testing.B, delay time.Duration) *rod.Browser {
	b.Helper()
	ctx, cancel := context.WithCancel(b.Context())
	b.Cleanup(cancel)
	client := &latencyClient{delay: delay, events: make(chan *cdp.Event)}
	browser := rod.New().Context(ctx).ControlURL("").Monitor("").Client(client)
	if err := browser.Connect(); err != nil {
		b.Fatal(err)
	}
	return browser
}

// Attaching includes the default device emulation and Page.enable, each with a
// simulated 10 ms protocol round trip. Concurrent attachments use distinct targets.
func BenchmarkPageFromTarget(b *testing.B) {
	for _, pages := range []int{1, 16} {
		b.Run(fmt.Sprintf("pages=%d", pages), func(b *testing.B) {
			browser := benchmarkLatencyBrowser(b, 10*time.Millisecond)
			next := 0
			for b.Loop() {
				var group sync.WaitGroup
				errs := make(chan error, pages)
				for range pages {
					next++
					target := proto.TargetTargetID(fmt.Sprintf("target-%d", next))
					group.Go(func() {
						if _, err := browser.PageFromTarget(target); err != nil {
							errs <- err
						}
					})
				}
				group.Wait()
				close(errs)
				for err := range errs {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(pages), "pages/op")
		})
	}
}

// Browser.Call overhead for commands whose parameters are not retained state.
func BenchmarkBrowserCall(b *testing.B) {
	for _, size := range []int{16, 1 << 20} {
		b.Run(fmt.Sprintf("argument=%d", size), func(b *testing.B) {
			browser := benchmarkLatencyBrowser(b, 0)
			page, err := browser.PageFromTarget("target")
			if err != nil {
				b.Fatal(err)
			}
			request := proto.RuntimeCallFunctionOn{
				FunctionDeclaration: "value => value.length",
				Arguments:           []*proto.RuntimeCallArgument{{UnserializableValue: proto.RuntimeUnserializableValue(strings.Repeat("x", size))}},
			}
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := request.Call(page); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
