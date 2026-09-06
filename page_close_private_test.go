package rod

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod/lib/cdp"
)

func TestPageCloseAcknowledgementAfterSessionTermination(t *testing.T) {
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
		client.call = func(ctx context.Context, session, method string, _ any) ([]byte, error) {
			if method != "Page.close" || session != string(page.SessionID) {
				t.Fatalf("unexpected close request: %s %s", session, method)
			}
			client.events <- &cdp.Event{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"target"}`)}
			synctest.Wait() // End the shared session before acknowledging closure.
			if page.sessionCtx.Err() == nil {
				t.Fatal("destruction did not terminate the session")
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []byte(`{}`), nil
		}
		if err := page.Close(); err != nil {
			t.Fatalf("successful closure returned an error: %v", err)
		}
		if browser.loadCachedPage(page.TargetID) != nil || browser.sessionContext(page.SessionID) != nil {
			t.Fatal("closed target retained its attachment")
		}
	})
}
