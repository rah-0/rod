package rod_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

func TestTypedPageEventSessionAndDomainRestore(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "initially-disabled"
		if enabled {
			name = "initially-enabled"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, client := connectEventTestBrowser(t)
				const session proto.TargetSessionID = "page-session"
				if enabled {
					restore, err := browser.EnableDomain(session, &proto.NetworkEnable{})
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := restore(); err != nil {
							t.Fatal(err)
						}
					}()
				}
				before := len(client.snapshot())
				page := browser.PageFromSession(session)
				var seen []string
				wait := page.EachEvent(rod.On(func(event *proto.NetworkLoadingFinished, source proto.TargetSessionID) bool {
					seen = append(seen, string(event.RequestID)+":"+string(source))
					return true
				}))
				sendEvent(client.events, "Network.loadingFinished", "other-page", `{"requestId":"wrong","timestamp":0,"encodedDataLength":0}`)
				sendEvent(client.events, "Network.loadingFinished", session, `{"requestId":"matching","timestamp":0,"encodedDataLength":0}`)
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

func TestTypedWaitEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := connectEventTestBrowser(t)
		page := browser.PageFromSession("page")
		var browserEvent, pageEvent proto.NetworkLoadingFinished
		waitBrowser := browser.WaitEvent(&browserEvent)
		waitPage := page.WaitEvent(&pageEvent)
		sendEvent(client.events, "Network.loadingFinished", "other", `{"requestId":"browser-first","timestamp":0,"encodedDataLength":0}`)
		sendEvent(client.events, "Network.loadingFinished", "page", `{"requestId":"page-first","timestamp":0,"encodedDataLength":0}`)
		waitBrowser()
		waitPage()
		if browserEvent.RequestID != "browser-first" || pageEvent.RequestID != "page-first" {
			t.Fatalf("wait results: browser=%q, page=%q", browserEvent.RequestID, pageEvent.RequestID)
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
	message := eventTestMessage(t, "Test.decoded", "", `{"Value":42}`)
	initial := eventTestDecoded{Value: 99, Label: "stale"}
	if ok, err := message.Load(&initial); !ok || err != nil || initial.Value != 42 || initial.Label != "" {
		t.Fatalf("first decode retained preexisting destination fields: %+v, %v", initial, err)
	}
	initial.Value = 99
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			var event eventTestDecoded
			if ok, err := message.Load(&event); !ok || err != nil || event.Value != 42 || event.Label != "" {
				t.Errorf("decoded event = %+v, %v", event, err)
			}
			event.Value = 99
		})
	}
	wg.Wait()
	if got := eventTestDecodeCalls.Load(); got != 1 {
		t.Fatalf("same-type subscribers decoded the message %d times", got)
	}
	var event eventTestDecoded
	if !message.MustLoad(&event) || event.Value != 42 {
		t.Fatalf("destination mutation changed the cached event: %+v", event)
	}

	var alternate eventTestAlternate
	if ok, err := message.Load(&alternate); !ok || err != nil || alternate.Value != 42 {
		t.Fatalf("alternate concrete event could not decode the original JSON: %+v, %v", alternate, err)
	}
	var unmatched proto.PageLoadEventFired
	if ok, err := message.Load(&unmatched); ok || err != nil {
		t.Fatalf("unrelated event type matched the message: %t, %v", ok, err)
	}
	if message.MustLoad(&unmatched) {
		t.Fatal("MustLoad matched an unrelated event type")
	}
}

// A protocol mismatch returns an error instead of panicking, so library
// goroutines that decode events cannot crash the process.
func TestMessageLoadDecodeError(t *testing.T) {
	message := eventTestMessage(t, "Network.loadingFinished", "page", `{"requestId":42}`)
	event := proto.NetworkLoadingFinished{RequestID: "unchanged"}
	ok, err := message.Load(&event)
	var typeErr *json.UnmarshalTypeError
	if ok || !errors.As(err, &typeErr) || event.RequestID != "unchanged" {
		t.Fatalf("Load = %t, %v; event %+v", ok, err, event)
	}
	if !strings.Contains(err.Error(), "Network.loadingFinished") {
		t.Fatalf("decode error omits the event method: %v", err)
	}
	// Failures are not cached; each load reports the error.
	if ok, again := message.Load(&event); ok || again == nil {
		t.Fatalf("repeated Load = %t, %v", ok, again)
	}
	defer func() {
		if recovered := recover(); !errors.As(recovered.(error), &typeErr) {
			t.Fatalf("MustLoad panic = %v", recovered)
		}
	}()
	message.MustLoad(&event)
	t.Fatal("MustLoad did not panic")
}

func TestMessageLoadMissingField(t *testing.T) {
	message := eventTestMessage(t, "Target.targetCreated", "", `{"targetInfo":null}`)
	event := proto.TargetTargetCreated{TargetInfo: &proto.TargetTargetInfo{TargetID: "unchanged"}}
	ok, err := message.Load(&event)
	if ok || event.TargetInfo.TargetID != "unchanged" || !strings.Contains(err.Error(), "Target.targetCreated") {
		t.Fatalf("Load = %t, %v; event %+v", ok, err, event)
	}
	requireMissingField(t, err, "TargetTargetCreated", "targetInfo")
	// Events with their required fields decode.
	var detached proto.TargetDetachedFromTarget
	if ok, err := eventTestMessage(t, "Target.detachedFromTarget", "", `{"sessionId":"session"}`).Load(&detached); !ok || err != nil {
		t.Fatalf("Load = %t, %v", ok, err)
	}
}

func TestPageSessionEndClosesUnreadEvent(t *testing.T) {
	for _, end := range []string{"target-destroyed", "session-detached", "browser-disconnected"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				client := &sessionTestClient{events: make(chan *cdp.Event)}
				browser := rod.New().NoDefaultDevice().Context(ctx).Client(client)
				connectTestBrowser(t, browser)
				page, err := browser.PageFromTarget("target")
				if err != nil {
					t.Fatal(err)
				}
				stream := page.Event()
				client.events <- &cdp.Event{SessionID: string(page.SessionID), Method: "Page.loadEventFired", Params: json.RawMessage(`{"timestamp":0}`)}
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
