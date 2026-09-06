package rod

import (
	"context"
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
			return []byte(`{"targetInfos":[{"targetId":"normal","type":"page","browserContextId":"default"},{"targetId":"private","type":"page","browserContextId":"incognito"}]}`), nil
		case "Target.getBrowserContexts":
			return []byte(`{"browserContextIds":["incognito"]}`), nil
		case "Target.attachToTarget":
			return []byte(`{"sessionId":"temporary"}`), nil
		case "Page.getFrameTree":
			return []byte(`{"frameTree":{"frame":{"id":"normal"},"childFrames":[{"frame":{"id":"child"}}]}}`), nil
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
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"private","guid":"wrong-context"}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"wrong-context","state":"completed"}`))
				if frame == "new-tab" {
					browser.event.Publish(eventTestMessage("Target.targetCreated", "", `{"targetInfo":{"targetId":"new-tab","browserContextId":"default"}}`))
					browser.event.Publish(eventTestMessage("Target.targetDestroyed", "", `{"targetId":"new-tab"}`))
				}
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"`+frame+`","guid":"first","suggestedFilename":"one.txt"}`))
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"second"}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"second","state":"canceled"}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"first","state":"completed"}`))
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

func TestDownloadBrowserCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newDownloadTestBrowser(t)
		wait, err := browser.WaitDownload("/downloads")
		if err != nil {
			t.Fatal(err)
		}
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"canceled"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"canceled","state":"canceled"}`))
		info, err := wait()
		if info == nil || info.GUID != "canceled" || !errors.Is(err, ErrDownloadCanceled) {
			t.Fatalf("browser canceled: %+v, %v", info, err)
		}
	})
}

func TestDownloadDistinctContexts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newDownloadTestBrowser(t)
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
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"normal"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"private","guid":"private"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"private","state":"completed"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"normal","state":"completed"}`))
		one, err := waitRoot()
		if err != nil || one.GUID != "normal" {
			t.Fatalf("default: %+v, %v", one, err)
		}
		two, err := waitPrivate()
		if err != nil || two.GUID != "private" {
			t.Fatalf("incognito: %+v, %v", two, err)
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
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"`+firstFrame+`","guid":"first"}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"first","state":"completed"}`))
				if _, err := first(); err != nil {
					t.Fatal(err)
				}
				if enabled, _ := browser.states.Load(downloadEventsEnabledKey{}); enabled != true {
					t.Fatal("first completed waiter disabled global download events")
				}
				browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"`+secondFrame+`","guid":"second"}`))
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"second","state":"completed"}`))
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
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"normal"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"normal","state":"completed"}`))
		if _, err := wait(); err != nil {
			t.Fatal(err)
		}
		if enabled, _ := browser.states.Load(downloadEventsEnabledKey{}); enabled != false {
			t.Fatal("last waiter did not restore global events")
		}
	})
}

func TestDownloadDisposedContextIsNotDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newDownloadTestBrowser(t)
		wait, err := browser.WaitDownload("/normal")
		if err != nil {
			t.Fatal(err)
		}
		call := client.call
		client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
			if method == "Target.getBrowserContexts" {
				return []byte(`{"browserContextIds":[]}`), nil
			}
			return call(ctx, session, method, params)
		}
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"private","guid":"closed-context"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"closed-context","state":"completed"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"normal"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"normal","state":"completed"}`))
		info, err := wait()
		if err != nil || info.GUID != "normal" {
			t.Fatalf("download after context disposal: %+v, %v", info, err)
		}
	})
}
