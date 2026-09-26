package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

type eventBenchmarkClient struct {
	events chan *cdp.Event
}

func (client *eventBenchmarkClient) Event() <-chan *cdp.Event { return client.events }

func (*eventBenchmarkClient) Call(ctx context.Context, _, method string, params any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "Target.attachToTarget":
		request := params.(proto.TargetAttachToTarget)
		return json.Marshal(proto.TargetAttachToTargetResult{SessionID: proto.TargetSessionID(request.TargetID) + "-session"})
	case "Target.setDiscoverTargets", "Page.enable", "Network.enable", "Network.disable", "Runtime.enable", "Runtime.disable":
		return []byte(`{}`), nil
	default:
		return nil, fmt.Errorf("unexpected benchmark protocol call: %s", method)
	}
}

// These benchmarks include CDP-client event ingestion and Rod's event dispatch.
func benchmarkEventBrowser(b *testing.B) (*rod.Browser, *eventBenchmarkClient, context.CancelFunc) {
	b.Helper()
	ctx, cancel := context.WithCancel(b.Context())
	b.Cleanup(cancel)
	client := &eventBenchmarkClient{events: make(chan *cdp.Event)}
	browser := rod.New().NoDefaultDevice().Context(ctx).ControlURL("").Monitor("").Client(client)
	if err := browser.Connect(); err != nil {
		b.Fatal(err)
	}
	return browser, client, cancel
}

func BenchmarkBrowserEvent(b *testing.B) {
	for _, subscribers := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("subscribers=%d", subscribers), func(b *testing.B) {
			browser, client, cancel := benchmarkEventBrowser(b)
			defer cancel()
			streams := make([]<-chan *rod.Message, subscribers)
			for i := range streams {
				streams[i] = browser.Event()
			}
			message := &cdp.Event{Method: "Page.loadEventFired", SessionID: "page"}
			b.ReportAllocs()
			for b.Loop() {
				client.events <- message
				var first *rod.Message
				for _, stream := range streams {
					got := <-stream
					if got == nil || got.Method != message.Method || string(got.SessionID) != message.SessionID {
						b.Fatal("event content changed during dispatch")
					}
					if first == nil {
						first = got
					} else if got != first {
						b.Fatal("subscribers received different message instances")
					}
				}
			}
			b.ReportMetric(float64(subscribers), "deliveries/op")
		})
	}
}

func BenchmarkPageEventRouting(b *testing.B) {
	for _, pages := range []int{1, 8, 32} {
		for _, unrelated := range []int{0, 31} {
			b.Run(fmt.Sprintf("pages=%d/unrelated=%d", pages, unrelated), func(b *testing.B) {
				browser, client, cancel := benchmarkEventBrowser(b)
				defer cancel()
				streams := make([]<-chan *rod.Message, pages)
				messages := make([]*cdp.Event, pages)
				for i := range streams {
					page, err := browser.PageFromTarget(proto.TargetTargetID(fmt.Sprintf("page-%d", i)))
					if err != nil {
						b.Fatal(err)
					}
					streams[i] = page.Event()
					messages[i] = &cdp.Event{Method: "Page.loadEventFired", SessionID: string(page.SessionID)}
				}
				noise := &cdp.Event{Method: "Network.dataReceived", SessionID: "unrelated-page"}
				b.ReportAllocs()
				for b.Loop() {
					for range unrelated {
						client.events <- noise
					}
					for _, message := range messages {
						client.events <- message
					}
					for i, stream := range streams {
						got := <-stream
						if got == nil || got.Method != messages[i].Method || string(got.SessionID) != messages[i].SessionID {
							b.Fatal("event routed to the wrong page")
						}
					}
				}
				b.ReportMetric(float64(pages), "deliveries/op")
				b.ReportMetric(float64(pages+unrelated), "messages/op")
			})
		}
	}
}

func BenchmarkEachEventRouting(b *testing.B) {
	for _, subscribers := range []int{1, 8, 32} {
		for _, unrelated := range []int{0, 31} {
			b.Run(fmt.Sprintf("subscribers=%d/unrelated=%d", subscribers, unrelated), func(b *testing.B) {
				browser, client, cancel := benchmarkEventBrowser(b)
				ack := make(chan bool, subscribers)
				done := make(chan error, subscribers)
				sessions := make([]proto.TargetSessionID, subscribers)
				for i := range sessions {
					sessions[i] = proto.TargetSessionID(fmt.Sprintf("page-%d", i))
					wait := browser.PageFromSession(sessions[i]).EachEvent(rod.On(func(event *proto.NetworkLoadingFinished, session proto.TargetSessionID) bool {
						valid := session == sessions[i] && event.RequestID == "request" && event.Timestamp == 1 && event.EncodedDataLength == 100
						select {
						case ack <- valid:
						case <-browser.GetContext().Done():
						}
						return false
					}))
					go func() { done <- wait() }()
				}
				defer func() {
					cancel()
					for range subscribers {
						if err := <-done; !errors.Is(err, context.Canceled) {
							b.Errorf("event subscription cleanup: %v", err)
						}
					}
				}()
				data := json.RawMessage(`{"requestId":"request","timestamp":1,"encodedDataLength":100}`)
				noiseSession := &cdp.Event{Method: "Network.loadingFinished", SessionID: "unrelated-page", Params: data}
				noiseMethod := &cdp.Event{Method: "Network.dataReceived", SessionID: string(sessions[0])}
				messages := make([]*cdp.Event, subscribers)
				for i, session := range sessions {
					messages[i] = &cdp.Event{Method: "Network.loadingFinished", SessionID: string(session), Params: data}
				}
				b.ReportAllocs()
				for b.Loop() {
					for i := range unrelated {
						if i%2 == 0 {
							client.events <- noiseSession
						} else {
							client.events <- noiseMethod
						}
					}
					for _, message := range messages {
						client.events <- message
					}
					for range subscribers {
						select {
						case valid := <-ack:
							if !valid {
								b.Fatal("event content or source session changed during dispatch")
							}
						case err := <-done:
							done <- err
							b.Fatalf("event subscription ended before delivery: %v", err)
						}
					}
				}
				b.ReportMetric(float64(subscribers), "deliveries/op")
				b.ReportMetric(float64(subscribers+unrelated), "messages/op")
			})
		}
	}
}

// Decoding cost of events that carry nested protocol objects, including
// Rod's event dispatch to one EachEvent subscriber.
func BenchmarkEachEventDecode(b *testing.B) {
	b.Run("event=Network.requestWillBeSent", func(b *testing.B) {
		benchmarkEachEventDecode(b, `{"requestId":"1.1","loaderId":"loader","documentURL":"http://127.0.0.1:8080/","request":{"url":"http://127.0.0.1:8080/app.js","method":"GET","headers":{"Accept":"*/*","Referer":"http://127.0.0.1:8080/","User-Agent":"Mozilla/5.0"},"mixedContentType":"none","initialPriority":"High","referrerPolicy":"strict-origin-when-cross-origin"},"timestamp":1,"wallTime":1,"initiator":{"type":"parser","url":"http://127.0.0.1:8080/","lineNumber":3,"columnNumber":10},"redirectHasExtraInfo":false,"type":"Script","frameId":"frame","hasUserGesture":false}`,
			func(event *proto.NetworkRequestWillBeSent) bool {
				return event.Request.URL == "http://127.0.0.1:8080/app.js" && event.Initiator.Type == proto.NetworkInitiatorTypeParser
			})
	})
	b.Run("event=Runtime.consoleAPICalled", func(b *testing.B) {
		benchmarkEachEventDecode(b, `{"type":"log","args":[{"type":"string","value":"rod-benchmark"},{"type":"number","value":1,"description":"1"},{"type":"object","className":"Object","description":"Object","objectId":"1.2.3","preview":{"type":"object","description":"Object","overflow":false,"properties":[{"name":"a","type":"number","value":"1"},{"name":"b","type":"string","value":"x"}]}}],"executionContextId":1,"timestamp":1,"stackTrace":{"callFrames":[{"functionName":"run","scriptId":"5","url":"http://127.0.0.1:8080/app.js","lineNumber":1,"columnNumber":2},{"functionName":"","scriptId":"5","url":"http://127.0.0.1:8080/app.js","lineNumber":9,"columnNumber":0}]}}`,
			func(event *proto.RuntimeConsoleAPICalled) bool {
				return len(event.Args) == 3 && event.Args[0].Value.Str() == "rod-benchmark" && len(event.StackTrace.CallFrames) == 2
			})
	})
}

func benchmarkEachEventDecode[E proto.Event](b *testing.B, params string, valid func(*E) bool) {
	browser, client, cancel := benchmarkEventBrowser(b)
	ack := make(chan bool, 1)
	done := make(chan error, 1)
	var event E
	session := proto.TargetSessionID("page")
	wait := browser.PageFromSession(session).EachEvent(rod.On(func(event *E, _ proto.TargetSessionID) bool {
		select {
		case ack <- valid(event):
		case <-browser.GetContext().Done():
		}
		return false
	}))
	go func() { done <- wait() }()
	defer func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			b.Errorf("event subscription cleanup: %v", err)
		}
	}()
	message := &cdp.Event{Method: event.ProtoEvent(), SessionID: string(session), Params: json.RawMessage(params)}
	b.SetBytes(int64(len(params)))
	b.ReportAllocs()
	for b.Loop() {
		client.events <- message
		select {
		case valid := <-ack:
			if !valid {
				b.Fatal("event content changed during dispatch")
			}
		case err := <-done:
			done <- err
			b.Fatalf("event subscription ended before delivery: %v", err)
		}
	}
}
