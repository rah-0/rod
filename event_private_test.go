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

func TestTypedEventDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newEventTestBrowser(t)
		var seen []string
		wait := browser.EachEvent(
			On(func(event *proto.NetworkRequestWillBeSent, session proto.TargetSessionID) bool {
				seen = append(seen, "sent:"+string(event.RequestID)+":"+string(session))
				return false
			}),
			On(func(event *proto.NetworkLoadingFinished, session proto.TargetSessionID) bool {
				seen = append(seen, "finished:"+string(event.RequestID)+":"+string(session))
				return true
			}),
		)
		browser.event.Publish(eventTestMessage("Network.requestWillBeSent", "first", `{"requestId":"one","request":{"url":"/one","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"initiator":{"type":"other"},"loaderId":"","documentURL":"","timestamp":0,"wallTime":0}`))
		browser.event.Publish(eventTestMessage("Page.loadEventFired", "ignored", `{"timestamp":0}`))
		browser.event.Publish(eventTestMessage("Network.requestWillBeSent", "second", `{"requestId":"two","request":{"url":"/two","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"initiator":{"type":"other"},"loaderId":"","documentURL":"","timestamp":0,"wallTime":0}`))
		browser.event.Publish(eventTestMessage("Network.loadingFinished", "second", `{"requestId":"two","timestamp":0,"encodedDataLength":0}`))
		browser.event.Publish(eventTestMessage("Network.requestWillBeSent", "after-stop", `{"requestId":"three","request":{"url":"/three","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"initiator":{"type":"other"},"loaderId":"","documentURL":"","timestamp":0,"wallTime":0}`))
		wait()
		synctest.Wait()

		want := []string{"sent:one:first", "sent:two:second", "finished:two:second"}
		if !slices.Equal(seen, want) {
			t.Fatalf("dispatch = %v, want %v", seen, want)
		}
		if got := client.snapshot(); !slices.Equal(got, []eventTestCall{
			{method: "Network.enable"}, {method: "Network.disable"},
		}) {
			t.Fatalf("domain calls = %v", got)
		}
		if browser.event.Len() != 0 {
			t.Fatal("stopped handler retained its subscription")
		}
	})
}

func TestTypedEventCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newEventTestBrowser(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		wait := browser.Context(ctx).EachEvent(On(func(_ *proto.PageLoadEventFired, _ proto.TargetSessionID) bool {
			t.Error("canceled subscription invoked a callback")
			return false
		}))
		done := make(chan struct{})
		go func() {
			wait()
			close(done)
		}()
		synctest.Wait()
		cancel()
		<-done
		synctest.Wait()
		if browser.event.Len() != 0 {
			t.Fatal("canceled handler retained its subscription")
		}
	})
}

func TestTypedTargetDestroyedCancelsPage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newEventTestBrowser(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		page := &Page{
			browser: browser, ctx: ctx, sessionCancel: cancel,
			SessionID: "page-session", TargetID: "page-target",
		}
		page.initEvents()
		browser.event.Publish(eventTestMessage("Target.targetDestroyed", "", `{"targetId":"other-target"}`))
		synctest.Wait()
		if ctx.Err() != nil {
			t.Fatal("another target's destruction canceled the page")
		}
		browser.event.Publish(eventTestMessage("Target.targetDestroyed", "", `{"targetId":"page-target"}`))
		synctest.Wait()
		if ctx.Err() != context.Canceled {
			t.Fatal("target destruction did not cancel the page")
		}
	})
}

func TestTypedEventDecodeErrorEndsWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newEventTestBrowser(t)
		called := false
		wait := browser.EachEvent(On(func(*proto.NetworkLoadingFinished, proto.TargetSessionID) bool {
			called = true
			return false
		}))
		browser.event.Publish(eventTestMessage("Network.loadingFinished", "page", `{"requestId":42}`))
		var typeErr *json.UnmarshalTypeError
		if err := wait(); !errors.As(err, &typeErr) || called {
			t.Fatalf("wait = %v, callback called = %t", err, called)
		}
		synctest.Wait()
		if browser.event.Len() != 0 {
			t.Fatal("failed wait retained its subscription")
		}
		if got := client.snapshot(); !slices.Equal(got, []eventTestCall{{method: "Network.enable"}, {method: "Network.disable"}}) {
			t.Fatalf("domain calls = %v", got)
		}
	})
}

func TestTypedEventBufferedDuringEnable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newEventTestBrowser(t)
		browser.Client(&sessionTestClient{call: func(_ context.Context, _, method string, _ any) ([]byte, error) {
			if method == "Network.enable" {
				browser.event.Publish(eventTestMessage("Network.loadingFinished", "other", `{"requestId":"wrong-session","timestamp":0,"encodedDataLength":0}`))
				browser.event.Publish(eventTestMessage("Network.loadingFailed", "page", `{"requestId":"wrong-method","timestamp":0,"type":"","errorText":""}`))
				browser.event.Publish(eventTestMessage("Network.loadingFinished", "page", `{"requestId":"first","timestamp":0,"encodedDataLength":0}`))
				browser.event.Publish(eventTestMessage("Network.loadingFinished", "page", `{"requestId":"second","timestamp":0,"encodedDataLength":0}`))
			}
			return []byte(`{}`), nil
		}})
		var seen []proto.NetworkRequestID
		wait := browser.eachEvent("page", On(func(event *proto.NetworkLoadingFinished, _ proto.TargetSessionID) bool {
			seen = append(seen, event.RequestID)
			return len(seen) == 2
		}))
		if err := wait(); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(seen, []proto.NetworkRequestID{"first", "second"}) {
			t.Fatalf("buffered events = %v", seen)
		}
		synctest.Wait()
		if browser.event.Len() != 0 {
			t.Fatal("completed buffered-event wait retained its subscription")
		}
	})
}

func TestTypedUnusedWaitCancellation(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "last-owner"
		if shared {
			name = "shared-owner"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, client := newEventTestBrowser(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				unused := browser.Context(ctx).EachEvent(On(func(_ *proto.FetchRequestPaused, _ proto.TargetSessionID) bool {
					t.Error("canceled unused wait invoked its callback")
					return true
				}))
				var other func() error
				if shared {
					other = browser.EachEvent(On(func(_ *proto.FetchAuthRequired, _ proto.TargetSessionID) bool { return true }))
				}
				cancel()
				synctest.Wait()
				var state proto.FetchEnable
				if enabled := browser.LoadState("", &state); enabled != shared {
					t.Fatalf("enabled after unused wait cancellation = %t, want %t", enabled, shared)
				}
				if shared {
					browser.event.Publish(eventTestMessage("Fetch.authRequired", "", `{"requestId":"r","request":{"url":"/","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"authChallenge":{"origin":"http://example.test","scheme":"","realm":""},"frameId":"","resourceType":""}`))
					other()
					if browser.LoadState("", &state) {
						t.Fatal("completed last listener retained Fetch")
					}
				}
				before := len(client.snapshot())
				unused()
				synctest.Wait()
				if len(client.snapshot()) != before {
					t.Fatal("late wait invocation restored domains twice")
				}
				if browser.event.Len() != 0 {
					t.Fatal("canceled unused wait retained subscription")
				}
			})
		})
	}
}

func TestEventWaitReportsSetupAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newEventTestBrowser(t)
		failed := errors.New("enable rejected")
		client := &sessionTestClient{call: func(_ context.Context, _, method string, _ any) ([]byte, error) {
			if method == "Network.enable" {
				return nil, failed
			}
			return []byte(`{}`), nil
		}}
		browser.Client(client)
		wait := browser.EachEvent(On(func(*proto.NetworkLoadingFinished, proto.TargetSessionID) bool { return true }))
		if err := wait(); !errors.Is(err, failed) {
			t.Fatalf("setup error: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		wait = browser.Context(ctx).EachEvent(On(func(*proto.TargetTargetCreated, proto.TargetSessionID) bool { return true }))
		cancel()
		if err := wait(); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
		synctest.Wait()
		if browser.event.Len() != 0 {
			t.Fatal("failed event wait retained subscriptions")
		}
	})
}

// requireMissingObject fails unless err reports that typ lacks the object at path.
func requireMissingObject(t *testing.T, err error, typ, path string) {
	t.Helper()
	missing, ok := errors.AsType[*proto.MissingFieldError](err)
	if !ok || !errors.Is(err, proto.ErrMissingField) || missing.Type != typ || missing.Path != path {
		t.Fatalf("error = %v, want %s without %s", err, typ, path)
	}
}

// Responses and events without objects that the protocol requires fail with
// proto.ErrMissingField instead of reaching code that dereferences them.
func TestMissingProtocolObjects(t *testing.T) {
	respond := func(t *testing.T, method, response string) *Browser {
		browser, _ := newEventTestBrowser(t)
		return browser.Client(&sessionTestClient{call: func(_ context.Context, _, called string, _ any) ([]byte, error) {
			if called == method {
				return []byte(response), nil
			}
			return []byte(`{}`), nil
		}})
	}
	t.Run("Page.Eval", func(t *testing.T) {
		page := respond(t, "Runtime.callFunctionOn", `{}`).PageFromSession("session")
		*page.jsCtxID = "window"
		res, err := page.Eval(`() => 1`)
		if res != nil {
			t.Fatalf("result = %+v", res)
		}
		requireMissingObject(t, err, "RuntimeCallFunctionOnResult", "result")
	})
	t.Run("Page.ObjectToJSON", func(t *testing.T) {
		page := respond(t, "Runtime.callFunctionOn", `{"result":null}`).PageFromSession("session")
		_, err := page.ObjectToJSON(&proto.RuntimeRemoteObject{ObjectID: "object"})
		requireMissingObject(t, err, "RuntimeCallFunctionOnResult", "result")
	})
	t.Run("Page.Info", func(t *testing.T) {
		info, err := respond(t, "Target.getTargetInfo", `{}`).PageFromSession("session").Info()
		if info != nil {
			t.Fatalf("info = %+v", info)
		}
		requireMissingObject(t, err, "TargetGetTargetInfoResult", "targetInfo")
	})
	for response, path := range map[string]string{`{}`: "node", `{"node":{"shadowRoots":[null]}}`: "node.shadowRoots[0]"} {
		t.Run("Element.ShadowRoot/"+path, func(t *testing.T) {
			page := respond(t, "DOM.describeNode", response).PageFromSession("session")
			element := &Element{page: page, ctx: t.Context(), Object: &proto.RuntimeRemoteObject{ObjectID: "element"}}
			_, err := element.ShadowRoot()
			requireMissingObject(t, err, "DOMDescribeNodeResult", path)
		})
	}
	t.Run("Browser.Pages", func(t *testing.T) {
		_, err := respond(t, "Target.getTargets", `{"targetInfos":[{"targetId":"page","type":"other","title":"","url":"","attached":false},null]}`).Pages()
		requireMissingObject(t, err, "TargetGetTargetsResult", "targetInfos[1]")
	})
	t.Run("Page.WaitOpen", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			browser, _ := newEventTestBrowser(t)
			wait := browser.PageFromSession("session").WaitOpen()
			browser.event.Publish(eventTestMessage("Target.targetCreated", "", `{}`))
			page, err := wait()
			if page != nil {
				t.Fatal("opened a page from a malformed event")
			}
			requireMissingObject(t, err, "TargetTargetCreated", "targetInfo")
		})
	})
	t.Run("Page.Reload", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			browser, _ := newEventTestBrowser(t)
			browser.Client(&sessionTestClient{call: func(_ context.Context, _, method string, _ any) ([]byte, error) {
				if method == "Runtime.callFunctionOn" {
					browser.event.Publish(eventTestMessage("Page.frameNavigated", "session", `{"type":"Navigation"}`))
					return []byte(`{"result":{"type":"undefined"}}`), nil
				}
				return []byte(`{}`), nil
			}})
			page := browser.PageFromSession("session")
			*page.jsCtxID = "window"
			requireMissingObject(t, page.Reload(), "PageFrameNavigated", "frame")
		})
	})
}
