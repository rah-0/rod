package rod

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

type pageWaitOpenResult struct {
	page *Page
	err  error
}

func TestPageWaitOpenTermination(t *testing.T) {
	for _, end := range []string{"target-destroyed", "session-detached", "caller-canceled", "caller-cause", "caller-deadline", "browser-disconnected"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browserCtx, stop := context.WithCancel(t.Context())
				defer stop()
				client := &sessionTestClient{events: make(chan *cdp.Event)}
				browser := New().NoDefaultDevice().Context(browserCtx).Client(client)
				browser.initEvents()
				ctx, cancel := context.WithCancelCause(browserCtx)
				defer cancel(nil)
				if end == "caller-deadline" {
					var cancelDeadline context.CancelFunc
					ctx, cancelDeadline = context.WithTimeout(ctx, time.Second)
					defer cancelDeadline()
				}
				page, err := browser.Context(ctx).PageFromTarget("opener")
				if err != nil {
					t.Fatal(err)
				}
				wait := page.WaitOpen()
				done := make(chan pageWaitOpenResult, 1)
				go func() {
					popup, err := wait()
					done <- pageWaitOpenResult{popup, err}
				}()
				synctest.Wait()
				if got := browser.event.Len(); got != 2 {
					t.Fatalf("pending subscriptions = %d, want opener and popup wait", got)
				}

				wantErr := context.Canceled
				wantSubscriptions := 1 // The opener retains its session event stream.
				switch end {
				case "target-destroyed":
					client.events <- &cdp.Event{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"opener"}`)}
					wantSubscriptions = 0
				case "session-detached":
					client.events <- &cdp.Event{Method: "Target.detachedFromTarget", Params: json.RawMessage(`{"sessionId":"opener-session"}`)}
					wantSubscriptions = 0
				case "caller-canceled":
					cancel(nil)
				case "caller-cause":
					wantErr = errors.New("caller stopped waiting")
					cancel(wantErr)
				case "caller-deadline":
					wantErr = context.DeadlineExceeded
					time.Sleep(time.Second)
				case "browser-disconnected":
					wantErr = ErrBrowserDisconnected
					wantSubscriptions = 0
					close(client.events)
				}
				synctest.Wait()
				select {
				case result := <-done:
					if result.page != nil || !errors.Is(result.err, wantErr) {
						t.Fatalf("WaitOpen = %v, %v; want nil, %v", result.page, result.err, wantErr)
					}
				default:
					t.Fatal("WaitOpen remained pending after termination")
				}
				if got := browser.event.Len(); got != wantSubscriptions {
					t.Fatalf("remaining subscriptions = %d, want %d", got, wantSubscriptions)
				}
				if browserCtx.Err() != nil {
					t.Fatal("termination canceled the browser caller context")
				}
				if end == "target-destroyed" || end == "session-detached" {
					if ctx.Err() != nil || browser.connectionCtx.Err() != nil {
						t.Fatal("session termination canceled the caller or browser connection")
					}
				}
			})
		})
	}
}

func TestPageWaitOpenSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browserCtx, stop := context.WithCancel(t.Context())
		defer stop()
		client := &sessionTestClient{events: make(chan *cdp.Event)}
		browser := New().NoDefaultDevice().Context(browserCtx).Client(client)
		browser.initEvents()
		ctx, cancel := context.WithCancel(browserCtx)
		defer cancel()
		page, err := browser.Context(ctx).PageFromTarget("opener")
		if err != nil {
			t.Fatal(err)
		}
		wait := page.WaitOpen()
		done := make(chan pageWaitOpenResult, 1)
		go func() {
			popup, err := wait()
			done <- pageWaitOpenResult{popup, err}
		}()
		for _, params := range []string{
			`{"targetInfo":{"targetId":"no-opener"}}`,
			`{"targetInfo":{"targetId":"unrelated","openerId":"other"}}`,
		} {
			client.events <- &cdp.Event{Method: "Target.targetCreated", Params: json.RawMessage(params)}
			synctest.Wait()
			select {
			case result := <-done:
				t.Fatalf("unrelated target completed WaitOpen: %v, %v", result.page, result.err)
			default:
			}
		}
		client.events <- &cdp.Event{Method: "Target.targetCreated", Params: json.RawMessage(`{"targetInfo":{"targetId":"popup","openerId":"opener"}}`)}
		synctest.Wait()
		var popup *Page
		select {
		case result := <-done:
			if result.err != nil || result.page == nil || result.page.TargetID != "popup" {
				t.Fatalf("matching WaitOpen = %v, %v", result.page, result.err)
			}
			popup = result.page
		default:
			t.Fatal("matching target did not complete WaitOpen")
		}
		if got := browser.event.Len(); got != 2 {
			t.Fatalf("successful wait subscriptions = %d, want opener and popup sessions", got)
		}
		if err := popup.GetContext().Err(); err != nil {
			t.Fatalf("wait cleanup canceled the popup context: %v", err)
		}
		if err := (proto.RuntimeEnable{}).Call(popup); err != nil {
			t.Fatalf("popup operation after wait cleanup: %v", err)
		}
		client.events <- &cdp.Event{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"opener"}`)}
		synctest.Wait()
		if got := browser.event.Len(); got != 1 {
			t.Fatalf("subscriptions after opener termination = %d, want popup session", got)
		}
		if err := popup.Mouse.MoveTo(proto.Point{X: 7, Y: 9}); err != nil {
			t.Fatalf("popup operation after opener termination: %v", err)
		}
		cancel()
		if err := (proto.RuntimeDisable{}).Call(popup); !errors.Is(err, context.Canceled) {
			t.Fatalf("popup operation after caller cancellation: %v", err)
		}
		if browser.connectionCtx.Err() != nil || popup.sessionCtx.Err() != nil {
			t.Fatal("caller cancellation terminated the browser or popup session")
		}
	})
}

func TestPageWaitOpenUnusedCancellation(t *testing.T) {
	for _, end := range []string{"target-destroyed", "session-detached", "caller-canceled"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browserCtx, stop := context.WithCancel(t.Context())
				defer stop()
				client := &sessionTestClient{events: make(chan *cdp.Event)}
				browser := New().NoDefaultDevice().Context(browserCtx).Client(client)
				browser.initEvents()
				ctx, cancel := context.WithCancel(browserCtx)
				defer cancel()
				page, err := browser.Context(ctx).PageFromTarget("opener")
				if err != nil {
					t.Fatal(err)
				}
				_ = page.WaitOpen()
				wantSubscriptions := 0
				switch end {
				case "target-destroyed":
					client.events <- &cdp.Event{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"opener"}`)}
				case "session-detached":
					client.events <- &cdp.Event{Method: "Target.detachedFromTarget", Params: json.RawMessage(`{"sessionId":"opener-session"}`)}
				case "caller-canceled":
					cancel()
					wantSubscriptions = 1
				}
				synctest.Wait()
				if got := browser.event.Len(); got != wantSubscriptions {
					t.Fatalf("unused wait subscriptions = %d, want %d", got, wantSubscriptions)
				}
			})
		})
	}
}
