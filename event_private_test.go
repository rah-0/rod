package rod

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod/internal/observable"
	"github.com/rah-0/rod/lib/cdp"
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
		browser.event.Publish(eventTestMessage("Network.requestWillBeSent", "first", `{"requestId":"one"}`))
		browser.event.Publish(eventTestMessage("Page.loadEventFired", "ignored", `{}`))
		browser.event.Publish(eventTestMessage("Network.requestWillBeSent", "second", `{"requestId":"two"}`))
		browser.event.Publish(eventTestMessage("Network.loadingFinished", "second", `{"requestId":"two"}`))
		browser.event.Publish(eventTestMessage("Network.requestWillBeSent", "after-stop", `{"requestId":"three"}`))
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

func TestTypedPageEventSessionAndDomainRestore(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "initially-disabled"
		if enabled {
			name = "initially-enabled"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, client := newEventTestBrowser(t)
				const session proto.TargetSessionID = "page-session"
				if enabled {
					browser.EnableDomain(session, &proto.NetworkEnable{})
				}
				before := len(client.snapshot())
				page := &Page{browser: browser, ctx: browser.ctx, SessionID: session}
				var seen []string
				wait := page.EachEvent(On(func(event *proto.NetworkLoadingFinished, source proto.TargetSessionID) bool {
					seen = append(seen, string(event.RequestID)+":"+string(source))
					return true
				}))
				browser.event.Publish(eventTestMessage("Network.loadingFinished", "other-page", `{"requestId":"wrong"}`))
				browser.event.Publish(eventTestMessage("Network.loadingFinished", session, `{"requestId":"matching"}`))
				wait()
				synctest.Wait()

				if !slices.Equal(seen, []string{"matching:page-session"}) {
					t.Fatalf("page dispatched events from the wrong session: %v", seen)
				}
				var state proto.NetworkEnable
				stillEnabled := browser.LoadState(session, &state)
				if stillEnabled != enabled {
					t.Fatalf("domain enabled = %t, originally %t", stillEnabled, enabled)
				}
				calls := client.snapshot()[before:]
				if enabled {
					if len(calls) != 0 {
						t.Fatalf("already-enabled domain changed: %v", calls)
					}
				} else if !slices.Equal(calls, []eventTestCall{
					{session: string(session), method: "Network.enable"},
					{session: string(session), method: "Network.disable"},
				}) {
					t.Fatalf("domain calls = %v", calls)
				}
			})
		})
	}
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

func TestTypedWaitEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newEventTestBrowser(t)
		page := &Page{browser: browser, ctx: browser.ctx, SessionID: "page"}
		var browserEvent, pageEvent proto.NetworkLoadingFinished
		waitBrowser := browser.WaitEvent(&browserEvent)
		waitPage := page.WaitEvent(&pageEvent)
		browser.event.Publish(eventTestMessage("Network.loadingFinished", "other", `{"requestId":"browser-first"}`))
		browser.event.Publish(eventTestMessage("Network.loadingFinished", "page", `{"requestId":"page-first"}`))
		waitBrowser()
		waitPage()
		if browserEvent.RequestID != "browser-first" || pageEvent.RequestID != "page-first" {
			t.Fatalf("wait results: browser=%q, page=%q", browserEvent.RequestID, pageEvent.RequestID)
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

var eventTestDecodeCalls atomic.Int64

type eventTestDecoded struct {
	Value int
	Label string
}

func (eventTestDecoded) ProtoEvent() string { return "Test.decoded" }

func (event *eventTestDecoded) UnmarshalJSON(data []byte) error {
	eventTestDecodeCalls.Add(1)
	type plain eventTestDecoded
	return json.Unmarshal(data, (*plain)(event))
}

type eventTestAlternate eventTestDecoded

func (eventTestAlternate) ProtoEvent() string { return "Test.decoded" }

func TestTypedMessageLoad(t *testing.T) {
	eventTestDecodeCalls.Store(0)
	message := eventTestMessage("Test.decoded", "", `{"Value":42}`)
	initial := eventTestDecoded{Value: 99, Label: "stale"}
	if !message.Load(&initial) || initial.Value != 42 || initial.Label != "" {
		t.Fatalf("first decode retained preexisting destination fields: %+v", initial)
	}
	initial.Value = 99
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			var event eventTestDecoded
			if !message.Load(&event) || event.Value != 42 || event.Label != "" {
				t.Errorf("decoded event = %+v", event)
			}
			event.Value = 99
		})
	}
	wg.Wait()
	if got := eventTestDecodeCalls.Load(); got != 1 {
		t.Fatalf("same-type subscribers decoded the message %d times", got)
	}
	var event eventTestDecoded
	message.Load(&event)
	if event.Value != 42 {
		t.Fatalf("destination mutation changed the cached event: %+v", event)
	}

	var alternate eventTestAlternate
	if !message.Load(&alternate) || alternate.Value != 42 {
		t.Fatalf("alternate concrete event could not decode the original JSON: %+v", alternate)
	}
	var unmatched proto.PageLoadEventFired
	if message.Load(&unmatched) {
		t.Fatal("unrelated event type matched the message")
	}
}

type eventTestCall struct {
	session string
	method  string
}

type eventTestClient struct {
	mu    sync.Mutex
	calls []eventTestCall
}

func (client *eventTestClient) Call(ctx context.Context, session, method string, _ any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client.mu.Lock()
	client.calls = append(client.calls, eventTestCall{session, method})
	client.mu.Unlock()
	return []byte("{}"), nil
}

func (*eventTestClient) Event() <-chan *cdp.Event { return nil }

func (client *eventTestClient) snapshot() []eventTestCall {
	client.mu.Lock()
	defer client.mu.Unlock()
	return slices.Clone(client.calls)
}

func newEventTestBrowser(t *testing.T) (*Browser, *eventTestClient) {
	t.Helper()
	client := new(eventTestClient)
	browser := New().Context(t.Context()).Client(client)
	browser.event = observable.New[*Message](t.Context())
	return browser, client
}

func eventTestMessage(method string, session proto.TargetSessionID, data string) *Message {
	return &Message{Method: method, SessionID: session, data: json.RawMessage(data)}
}
