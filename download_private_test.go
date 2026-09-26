package rod

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod/lib/proto"
)

func newDownloadTestBrowser(t *testing.T) (*Browser, *sessionTestClient) {
	t.Helper()
	browser, _ := newEventTestBrowser(t)
	client := &sessionTestClient{call: func(_ context.Context, _, method string, _ any) ([]byte, error) {
		switch method {
		case "Target.getTargets":
			return []byte(`{"targetInfos":[{"targetId":"normal","type":"page","browserContextId":"default","title":"","url":"","attached":false},{"targetId":"private","type":"page","browserContextId":"incognito","title":"","url":"","attached":false}]}`), nil
		case "Target.getBrowserContexts":
			return []byte(`{"browserContextIds":["incognito"]}`), nil
		case "Target.attachToTarget":
			return []byte(`{"sessionId":"temporary"}`), nil
		case "Page.getFrameTree":
			return []byte(`{"frameTree":{"frame":{"id":"normal","loaderId":"","url":"","securityOrigin":"","mimeType":""},"childFrames":[{"frame":{"id":"child","loaderId":"","url":"","securityOrigin":"","mimeType":""}}]}}`), nil
		default:
			return []byte(`{}`), nil
		}
	}}
	return browser.Client(client), client
}

func TestDownloadContextAndGUID(t *testing.T) {
	for _, frame := range []string{"normal", "child", "new-tab"} {
		t.Run(frame, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, client := newDownloadTestBrowser(t)
				wait, err := browser.WaitDownload("/downloads")
				if err != nil {
					t.Fatal(err)
				}
				var behavior proto.BrowserSetDownloadBehavior
				if !browser.LoadState("", &behavior) || (behavior.EventsEnabled == nil || !*behavior.EventsEnabled) {
					t.Fatal("Browser download events were not enabled")
				}
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"private","guid":"wrong-context","url":"","suggestedFilename":""}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"wrong-context","state":"completed","totalBytes":0,"receivedBytes":0}`))
				if frame == "new-tab" {
					browser.event.Publish(eventTestMessage("Target.targetCreated", "", `{"targetInfo":{"targetId":"new-tab","browserContextId":"default","type":"","title":"","url":"","attached":false}}`))
					browser.event.Publish(eventTestMessage("Target.targetDestroyed", "", `{"targetId":"new-tab"}`))
				}
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"`+frame+`","guid":"first","url":"","suggestedFilename":"one.txt"}`))
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"second","url":"","suggestedFilename":""}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"second","state":"canceled","totalBytes":0,"receivedBytes":0}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"first","state":"completed","totalBytes":0,"receivedBytes":0}`))
				info, err := wait()
				if err != nil || info == nil || info.GUID != "first" || info.SuggestedFilename != "one.txt" {
					t.Fatalf("download: %+v, %v", info, err)
				}
				if !browser.LoadState("", &behavior) || behavior.Behavior != proto.BrowserSetDownloadBehaviorBehaviorDefault || (behavior.EventsEnabled != nil && *behavior.EventsEnabled) {
					t.Fatalf("restored behavior: %+v", behavior)
				}
				if frame == "child" && !slices.ContainsFunc(client.snapshot(), func(call eventTestCall) bool { return call.method == "Target.detachFromTarget" }) {
					t.Fatal("temporary frame lookup session leaked")
				}
				synctest.Wait()
				if browser.event.Len() != 0 {
					t.Fatal("download subscription leaked")
				}
			})
		})
	}
}

func TestDownloadUnusedCancellationRestoresBehavior(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newDownloadTestBrowser(t)
		old := proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorDeny}
		if err := old.Call(browser); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		wait, err := browser.Context(ctx).WaitDownload("/downloads")
		if err != nil {
			t.Fatal(err)
		}
		if another, err := browser.WaitDownload("/other"); another != nil || !errors.Is(err, ErrDownloadInProgress) {
			t.Fatalf("overlap returned wait=%t, error=%v", another != nil, err)
		}
		cancel()
		synctest.Wait()
		var state proto.BrowserSetDownloadBehavior
		if !browser.LoadState("", &state) || state.Behavior != old.Behavior || (state.EventsEnabled != nil && *state.EventsEnabled) {
			t.Fatalf("cancellation restoration: %+v", state)
		}
		if info, err := wait(); info != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled wait: %+v, %v", info, err)
		}
		if _, active := browser.states.Load(downloadWaitKey("")); active {
			t.Fatal("canceled wait retained ownership")
		}
	})
}

func TestDownloadSetupFailureRollsBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newDownloadTestBrowser(t)
		call := client.call
		failed := false
		client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
			if method == "Browser.setDownloadBehavior" && !failed {
				failed = true
				return nil, context.Canceled
			}
			return call(ctx, session, method, params)
		}
		if wait, err := browser.WaitDownload("/downloads"); wait != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("setup returned wait=%t, error=%v", wait != nil, err)
		}
		var restored proto.BrowserSetDownloadBehavior
		if !browser.LoadState("", &restored) || restored.Behavior != proto.BrowserSetDownloadBehaviorBehaviorDefault {
			t.Fatal("uncertain setup was not restored")
		}
		if _, active := browser.states.Load(downloadWaitKey("")); active {
			t.Fatal("failed setup retained ownership")
		}
		synctest.Wait()
		if browser.event.Len() != 0 {
			t.Fatal("failed setup retained subscription")
		}
	})
}

func TestDownloadSharedEventsRestoration(t *testing.T) {
	for _, firstPrivate := range []bool{false, true} {
		for _, initiallyEnabled := range []bool{false, true} {
			synctest.Test(t, func(t *testing.T) {
				browser, _ := newDownloadTestBrowser(t)
				if err := (proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorDefault, EventsEnabled: new(initiallyEnabled)}).Call(browser); err != nil {
					t.Fatal(err)
				}
				private := browser.Context(t.Context())
				private.BrowserContextID = "incognito"
				waitRoot, err := browser.WaitDownload("/normal")
				if err != nil {
					t.Fatal(err)
				}
				waitPrivate, err := private.WaitDownload("/private")
				if err != nil {
					t.Fatal(err)
				}
				first, second := waitRoot, waitPrivate
				firstFrame, secondFrame := "normal", "private"
				if firstPrivate {
					first, second = second, first
					firstFrame, secondFrame = secondFrame, firstFrame
				}
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"`+firstFrame+`","guid":"first","url":"","suggestedFilename":""}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"first","state":"completed","totalBytes":0,"receivedBytes":0}`))
				if _, err := first(); err != nil {
					t.Fatal(err)
				}
				if enabled, _ := browser.states.Load(downloadEventsEnabledKey{}); enabled != true {
					t.Fatal("first completed waiter disabled global download events")
				}
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"`+secondFrame+`","guid":"second","url":"","suggestedFilename":""}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"second","state":"completed","totalBytes":0,"receivedBytes":0}`))
				if _, err := second(); err != nil {
					t.Fatal(err)
				}
				if enabled, _ := browser.states.Load(downloadEventsEnabledKey{}); enabled != initiallyEnabled {
					t.Fatalf("last waiter restored events to %v, want %t", enabled, initiallyEnabled)
				}
			})
		}
	}
}

func TestDownloadFailedSetupPreservesSharedEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newDownloadTestBrowser(t)
		wait, err := browser.WaitDownload("/normal")
		if err != nil {
			t.Fatal(err)
		}
		private := browser.Context(t.Context())
		private.BrowserContextID = "incognito"
		call, fail := client.call, true
		client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
			if method == "Browser.setDownloadBehavior" && fail {
				fail = false
				return nil, context.Canceled
			}
			return call(ctx, session, method, params)
		}
		if other, err := private.WaitDownload("/private"); other != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("failed setup: wait=%t, error=%v", other != nil, err)
		}
		if enabled, _ := browser.states.Load(downloadEventsEnabledKey{}); enabled != true {
			t.Fatal("failed setup disabled another waiter's events")
		}
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"normal","url":"","suggestedFilename":""}`))
		browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"normal","state":"completed","totalBytes":0,"receivedBytes":0}`))
		if _, err := wait(); err != nil {
			t.Fatal(err)
		}
		if enabled, _ := browser.states.Load(downloadEventsEnabledKey{}); enabled != false {
			t.Fatal("last waiter did not restore global events")
		}
	})
}

func TestDownloadUndecodableEvent(t *testing.T) {
	for _, event := range []string{"Target.targetCreated", "Browser.downloadWillBegin", "Browser.downloadProgress"} {
		t.Run(event, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, _ := newDownloadTestBrowser(t)
				wait, err := browser.WaitDownload("/downloads")
				if err != nil {
					t.Fatal(err)
				}
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"guid","url":"","suggestedFilename":""}`))
				browser.event.Publish(eventTestMessage(event, "", `{"targetInfo":1,"guid":1}`))
				var typeErr *json.UnmarshalTypeError
				if _, err := wait(); !errors.As(err, &typeErr) {
					t.Fatalf("wait error = %v", err)
				}
				synctest.Wait()
				if browser.event.Len() != 0 {
					t.Fatal("failed download wait retained its subscription")
				}
			})
		})
	}
}

func TestDownloadSubscriptionSkipsUnusedEvents(t *testing.T) {
	browser, _ := newDownloadTestBrowser(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The wait is never invoked, so every accepted event stays queued.
	if _, err := browser.Context(ctx).WaitDownload("/downloads"); err != nil {
		t.Fatal(err)
	}
	requireReleased(t,
		publishUnused(browser, "Target.targetDestroyed", ""),
		publishUnused(browser, "Network.dataReceived", "page"),
	)
}
