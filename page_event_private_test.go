package rod

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod/lib/cdp"
)

func TestPageSessionEndClosesUnreadEvent(t *testing.T) {
	for _, end := range []string{"target-destroyed", "session-detached", "browser-disconnected"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				client := &sessionTestClient{events: make(chan *cdp.Event)}
				browser := New().NoDefaultDevice().Context(ctx).Client(client)
				browser.initEvents()
				page, err := browser.PageFromTarget("target")
				if err != nil {
					t.Fatal(err)
				}
				stream := page.Event()
				client.events <- &cdp.Event{SessionID: string(page.SessionID), Method: "Page.loadEventFired", Params: json.RawMessage(`{}`)}
				synctest.Wait() // The subscription is waiting for its unread consumer.
				switch end {
				case "target-destroyed":
					client.events <- &cdp.Event{SessionID: "parent-session", Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"target"}`)}
				case "session-detached":
					client.events <- &cdp.Event{SessionID: "parent-session", Method: "Target.detachedFromTarget", Params: json.RawMessage(`{"sessionId":"target-session"}`)}
				default:
					close(client.events)
				}
				synctest.Wait()
				if _, open := <-stream; open {
					t.Fatal("ended session retained an unread event forwarder")
				}
				if err := page.GetContext().Err(); err != nil {
					t.Fatalf("session termination canceled the caller context: %v", err)
				}
			})
		})
	}
}
