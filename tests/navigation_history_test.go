package rod_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

func TestWaitNavigationIgnoresExistingLifecycle(t *testing.T) {
	g := setup(t)
	page := g.page.MustNavigate(g.blank()).MustWaitLoad()
	ctx, cancel := context.WithTimeout(page.GetContext(), time.Second)
	defer cancel()
	wait := page.Context(ctx).WaitNavigation(proto.PageLifecycleEventNameLoad)
	if ctx.Err() != nil {
		t.Fatal("lifecycle setup exceeded the wait budget")
	}
	if err := wait(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("already loaded document satisfied a new navigation wait: %v", err)
	}
	ctx, stop := context.WithTimeout(page.GetContext(), 5*time.Second)
	defer stop()
	page = page.Context(ctx)
	wait = page.WaitNavigation(proto.PageLifecycleEventNameLoad)
	// Changing the query creates a new document in the same local fixture origin.
	g.E(page.Navigate(g.blank() + "?navigation=next"))
	g.E(wait())
	g.Eq(page.MustEval(`() => location.search`).Str(), "?navigation=next")
}

func TestPageReloadWaitErrors(t *testing.T) {
	for _, phase := range []string{"setup", "cancellation"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				setupErr := errors.New("Page.enable failed")
				browser := rod.New().Context(t.Context()).Client(&callTestClient{call: func(_ context.Context, method string, _ any) ([]byte, error) {
					if method == "Runtime.evaluate" {
						return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
					}
					if phase == "setup" && method == "Page.enable" {
						return nil, setupErr
					}
					if method == "Runtime.callFunctionOn" {
						if phase == "cancellation" {
							cancel()
						}
						return []byte(`{"result":{"type":"undefined"}}`), nil
					}
					return []byte(`{}`), nil
				}})
				connectTestBrowser(t, browser)
				page := browser.PageFromSession("session").Context(ctx)
				page.FrameID = "frame"
				want := setupErr
				if phase == "cancellation" {
					want = context.Canceled
				}
				if err := page.Reload(); !errors.Is(err, want) {
					t.Fatalf("Reload = %v, want %v", err, want)
				}
			})
		})
	}
}

func TestPageWaitNavigationIgnoresChildFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		events := make(chan *cdp.Event)
		browser := rod.New().Context(ctx).Client(&callTestClient{events: events, call: func(context.Context, string, any) ([]byte, error) {
			return []byte(`{}`), nil
		}})
		connectTestBrowser(t, browser)
		page := browser.PageFromSession("session")
		page.FrameID = "main"
		wait := page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		result := make(chan error, 1)
		go func() { result <- wait() }()
		sendEvent(events, "Page.lifecycleEvent", "session", `{"frameId":"child","name":"load","loaderId":"","timestamp":0}`)
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("child frame completed the main-frame wait: %v", err)
		default:
		}
		sendEvent(events, "Page.lifecycleEvent", "session", `{"frameId":"main","name":"load","loaderId":"","timestamp":0}`)
		if err := <-result; err != nil {
			t.Fatalf("main frame lifecycle event: %v", err)
		}
	})
}
