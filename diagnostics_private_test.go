package rod

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/rah-0/rod/internal/observable"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

type diagnosticsTestClient struct {
	eventTestClient
	browser        *Browser
	fail           string
	stall          bool
	marker         string
	initial        bool
	worldNames     []string
	activeDomains  map[string]bool
	enableStarted  chan struct{}
	enableContinue chan struct{}
}

func (c *diagnosticsTestClient) Call(ctx context.Context, session, method string, params any) ([]byte, error) {
	data, err := c.eventTestClient.Call(ctx, session, method, params)
	if err != nil {
		return nil, err
	}
	if method == "Runtime.enable" && c.enableStarted != nil {
		close(c.enableStarted)
		<-c.enableContinue
	}
	if strings.HasSuffix(method, ".enable") || strings.HasSuffix(method, ".disable") {
		c.mu.Lock()
		if c.activeDomains == nil {
			c.activeDomains = make(map[string]bool)
		}
		c.activeDomains[session+strings.Split(method, ".")[0]] = strings.HasSuffix(method, ".enable")
		c.mu.Unlock()
	}
	if method == c.fail {
		// Simulate a command applied by the browser before its reply is lost.
		return nil, context.Canceled
	}
	switch method {
	case "Runtime.enable":
		if c.initial {
			c.browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", proto.TargetSessionID(session), `{"type":"log","args":[{"type":"string","value":"during enable"}],"executionContextId":0,"timestamp":0}`))
		}
	case "Page.createIsolatedWorld":
		c.worldNames = append(c.worldNames, params.(proto.PageCreateIsolatedWorld).WorldName)
		return []byte(`{"executionContextId":7}`), nil
	case "Runtime.addBinding":
		c.marker = params.(proto.RuntimeAddBinding).Name
	case "Runtime.evaluate":
		req := params.(proto.RuntimeEvaluate)
		if strings.Contains(req.Expression, "](") {
			if c.stall {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			payload, _ := json.Marshal(proto.RuntimeBindingCalled{Name: c.marker, Payload: c.marker, ExecutionContextID: 7})
			c.browser.event.Publish(eventTestMessage("Runtime.bindingCalled", proto.TargetSessionID(session), string(payload)))
			c.browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", proto.TargetSessionID(session), `{"type":"log","args":[{"type":"string","value":"after boundary"}],"executionContextId":0,"timestamp":0}`))
		}
		return []byte(`{"result":{"type":"boolean","value":true}}`), nil
	}
	return data, nil
}

func newDiagnosticsTestPage(t *testing.T) (*Page, *diagnosticsTestClient) {
	t.Helper()
	client := new(diagnosticsTestClient)
	browser := New().Context(t.Context()).Client(client)
	browser.event = observable.New[*Message](t.Context())
	client.browser = browser
	return &Page{browser: browser, ctx: t.Context(), SessionID: "page", FrameID: "frame"}, client
}

func TestDiagnosticsExceptionPreservesStackURL(t *testing.T) {
	d := &PageDiagnostics{options: DiagnosticsOptions{MaxRecords: 10, MaxTextBytes: 1024}}
	d.collect(eventTestMessage("Runtime.exceptionThrown", "", `{"exceptionDetails":{"exceptionId":1,"text":"Uncaught","scriptId":"script","lineNumber":4,"columnNumber":2,"stackTrace":{"callFrames":[{"url":"https://example.test/script.js","lineNumber":8,"columnNumber":6,"functionName":"","scriptId":""}]}},"timestamp":0}`))
	got := d.Snapshot().PageErrors[0].DiagnosticLocation
	if got.URL != "https://example.test/script.js" || got.Line != 5 || got.Column != 3 {
		t.Fatalf("exception source: %+v", got)
	}
}

func TestDiagnosticsLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newDiagnosticsTestPage(t)
		client.initial = true
		d, err := page.StartDiagnostics(DiagnosticsOptions{})
		if err != nil {
			t.Fatal(err)
		}
		page.browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", "other-session", `{"type":"log","args":[{"type":"string","value":"other page"}],"executionContextId":0,"timestamp":0}`))
		page.browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", page.SessionID, `{"type":"error","args":[{"type":"string","value":"console error"}],"executionContextId":0,"timestamp":0}`))
		snapshot, err := d.Stop()
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Console) != 2 || snapshot.Console[0].Text != "during enable" || snapshot.Console[1].Text != "console error" || len(snapshot.PageErrors) != 0 {
			t.Fatalf("snapshot: %+v", snapshot)
		}
		before := len(client.snapshot())
		snapshot.Console[0].Text = "mutated"
		again, err := d.Stop()
		if err != nil || again.Console[0].Text != "during enable" || len(client.snapshot()) != before {
			t.Fatalf("repeated Stop: %+v, %v", again, err)
		}
		synctest.Wait()
		if page.browser.event.Len() != 0 {
			t.Fatal("subscription leaked")
		}
		next, err := page.StartDiagnostics(DiagnosticsOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := next.Stop(); err != nil {
			t.Fatal(err)
		}
		if len(client.worldNames) != 2 || client.worldNames[0] != client.worldNames[1] {
			t.Fatalf("isolated worlds accumulate: %v", client.worldNames)
		}

		calls := client.snapshot()
		if !slices.ContainsFunc(calls, func(c eventTestCall) bool { return c.method == "Runtime.removeBinding" }) {
			t.Fatal("binding not removed")
		}
	})
}

func TestDiagnosticsSetupFailure(t *testing.T) {
	for _, method := range []string{"Runtime.enable", "Network.enable"} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page, client := newDiagnosticsTestPage(t)
				client.fail = method
				if d, err := page.StartDiagnostics(DiagnosticsOptions{}); err == nil || d != nil {
					t.Fatalf("start = %v, %v", d, err)
				}
				synctest.Wait()
				if page.browser.event.Len() != 0 {
					t.Fatal("setup failure leaked subscription")
				}
				var state proto.RuntimeEnable
				if page.LoadState(&state) {
					t.Fatal("setup failure retained owned Runtime domain")
				}
				client.mu.Lock()
				defer client.mu.Unlock()
				for domain, enabled := range client.activeDomains {
					if enabled {
						t.Errorf("uncertain setup left %s enabled", domain)
					}
				}

			})
		})
	}
}

func TestDiagnosticsIncompleteStop(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page, client := newDiagnosticsTestPage(t)
				ctx, cancel := context.WithCancel(page.ctx)
				defer cancel()
				page.ctx = ctx
				client.initial, client.stall = true, true
				d, err := page.StartDiagnostics(DiagnosticsOptions{StopTimeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if cancelFirst {
					cancel()
					synctest.Wait()
				}
				start := time.Now()
				snapshot, err := d.Stop()
				if !errors.Is(err, ErrDiagnosticsIncomplete) || len(snapshot.Console) != 1 {
					t.Fatalf("stop = %+v, %v", snapshot, err)
				}
				if time.Since(start) > 2*time.Second {
					t.Fatal("stop exceeded drain and cleanup budgets")
				}
				synctest.Wait()
				if page.browser.event.Len() != 0 {
					t.Fatal("canceled collector retained subscription")
				}
			})
		})
	}
}

func TestDiagnosticsCorrelationAndRevocation(t *testing.T) {
	d := &PageDiagnostics{options: DiagnosticsOptions{MaxRecords: 20, MaxTextBytes: 100, MaxRequests: 2, MaxPreviewDepth: 3}, requests: make(map[[sha256.Size]byte]diagnosticRequest)}
	collect := func(method, data string) { d.collect(eventTestMessage(method, "page", data)) }
	collect("Runtime.exceptionThrown", `{"exceptionDetails":{"exceptionId":1,"text":"rejection","lineNumber":0,"columnNumber":1},"timestamp":0}`)
	collect("Runtime.exceptionThrown", `{"exceptionDetails":{"exceptionId":2,"text":"uncaught","url":"script.js","lineNumber":2,"columnNumber":3},"timestamp":0}`)
	collect("Runtime.exceptionRevoked", `{"exceptionId":1,"reason":""}`)
	collect("Runtime.exceptionThrown", `{"exceptionDetails":{"exceptionId":3,"text":"anonymous script","lineNumber":4,"columnNumber":2},"timestamp":0}`)
	if location := d.Snapshot().PageErrors[1].DiagnosticLocation; location.URL != "" || location.Line != 5 || location.Column != 3 {
		t.Fatalf("anonymous source coordinates: %+v", location)
	}
	collect("Runtime.exceptionRevoked", `{"exceptionId":3,"reason":""}`)
	collect("Runtime.exceptionThrown", `{"exceptionDetails":{"exceptionId":4,"text":"missing source","lineNumber":-1,"columnNumber":-1},"timestamp":0}`)
	if location := d.Snapshot().PageErrors[1].DiagnosticLocation; location != (DiagnosticLocation{}) {
		t.Fatalf("missing source coordinates: %+v", location)
	}
	collect("Runtime.exceptionRevoked", `{"exceptionId":4,"reason":""}`)

	collect("Network.requestWillBeSent", `{"requestId":"redirect","request":{"url":"/first","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"initiator":{"type":"other"},"type":"Document","loaderId":"","documentURL":"","timestamp":0,"wallTime":0}`)
	collect("Network.requestWillBeSent", `{"requestId":"redirect","request":{"url":"/final","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"initiator":{"type":"other"},"redirectResponse":{"url":"/first","status":302,"statusText":"","headers":{},"mimeType":"","charset":"","connectionReused":false,"connectionId":0,"encodedDataLength":0,"securityState":""},"type":"Document","loaderId":"","documentURL":"","timestamp":0,"wallTime":0}`)
	collect("Network.responseReceived", `{"requestId":"redirect","response":{"url":"/final","status":500,"statusText":"","headers":{},"mimeType":"","charset":"","connectionReused":false,"connectionId":0,"encodedDataLength":0,"securityState":""},"type":"Document","loaderId":"","timestamp":0}`)
	collect("Network.loadingFailed", `{"requestId":"redirect","type":"Document","errorText":"body failed","timestamp":0}`)
	collect("Network.requestWillBeSent", `{"requestId":"missing","request":{"url":"/missing","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"initiator":{"type":"other"},"type":"Fetch","loaderId":"","documentURL":"","timestamp":0,"wallTime":0}`)
	collect("Network.responseReceived", `{"requestId":"missing","response":{"url":"/missing","status":404,"statusText":"","headers":{},"mimeType":"","charset":"","connectionReused":false,"connectionId":0,"encodedDataLength":0,"securityState":""},"type":"Fetch","loaderId":"","timestamp":0}`)
	collect("Network.loadingFinished", `{"requestId":"missing","timestamp":0,"encodedDataLength":0}`)
	collect("Network.requestWillBeSent", `{"requestId":"blocked","request":{"url":"/blocked","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"initiator":{"type":"other"},"type":"Image","loaderId":"","documentURL":"","timestamp":0,"wallTime":0}`)
	collect("Network.loadingFailed", `{"requestId":"blocked","type":"Image","errorText":"blocked","blockedReason":"inspector","canceled":true,"timestamp":0}`)
	collect("Network.loadingFailed", `{"requestId":"unknown","type":"Fetch","errorText":"connection refused","timestamp":0}`)
	collect("Network.responseReceived", `{"requestId":"in-flight","response":{"url":"/already-started","status":200,"statusText":"","headers":{},"mimeType":"","charset":"","connectionReused":false,"connectionId":0,"encodedDataLength":0,"securityState":""},"type":"Fetch","loaderId":"","timestamp":0}`)
	collect("Network.loadingFailed", `{"requestId":"in-flight","type":"Fetch","errorText":"body interrupted","timestamp":0}`)
	s := d.Snapshot()
	if len(s.PageErrors) != 1 || s.PageErrors[0].ExceptionID != 2 || s.PageErrors[0].Line != 3 || s.PageErrors[0].Column != 4 {
		t.Fatalf("page errors: %+v", s.PageErrors)
	}
	if len(s.ResourceFailures) != 5 || s.ResourceFailures[0].URL != "/final" || s.ResourceFailures[0].Status != 500 || s.ResourceFailures[0].ErrorText != "body failed" || s.ResourceFailures[1].Status != 404 || !s.ResourceFailures[2].Canceled || s.ResourceFailures[2].BlockedReason != "inspector" || s.ResourceFailures[3].URL != "" {
		t.Fatalf("resources: %+v", s.ResourceFailures)
	}
	if s.ResourceFailures[4].URL != "/already-started" || s.ResourceFailures[4].Status != 200 {
		t.Fatalf("unknown response tracking: %+v", s.ResourceFailures[4])
	}
	if len(d.requests) != 0 {
		t.Fatal("completed requests retained")
	}
}

func TestDiagnosticsRetentionAndConcurrentSnapshots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, _ := newDiagnosticsTestPage(t)
		d, err := page.StartDiagnostics(DiagnosticsOptions{MaxRecords: 2, MaxTextBytes: 8, MaxRequests: 1})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() {
				for range 100 {
					snapshot := d.Snapshot()
					if len(snapshot.Console) > 0 {
						snapshot.Console[0].Text = "changed"
					}
				}
			})
		}
		for range 10 {
			page.browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", page.SessionID, `{"type":"log","args":[{"type":"string","value":"long console text"}],"executionContextId":0,"timestamp":0}`))
		}
		page.browser.event.Publish(eventTestMessage("Network.requestWillBeSent", page.SessionID, `{"requestId":"1","request":{"url":"/1","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"initiator":{"type":"other"},"loaderId":"","documentURL":"","timestamp":0,"wallTime":0}`))
		page.browser.event.Publish(eventTestMessage("Network.requestWillBeSent", page.SessionID, `{"requestId":"2","request":{"url":"/2","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"initiator":{"type":"other"},"loaderId":"","documentURL":"","timestamp":0,"wallTime":0}`))
		s, err := d.Stop()
		wg.Wait()
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Console) != 2 || s.DroppedConsole != 8 || s.Console[0].Text != "long ..." || s.DroppedRequests != 1 {
			t.Fatalf("retention = %+v", s)
		}
		capped := &PageDiagnostics{options: DiagnosticsOptions{MaxRecords: 1, MaxTextBytes: 100, MaxRequests: 1}, requests: make(map[[sha256.Size]byte]diagnosticRequest)}
		capped.collect(eventTestMessage("Network.loadingFailed", "page", `{"requestId":"retained","errorText":"failed","timestamp":0,"type":""}`))
		capped.collect(eventTestMessage("Network.responseReceived", "page", `{"requestId":"dropped","response":{"url":"/failed","status":500,"statusText":"","headers":{},"mimeType":"","charset":"","connectionReused":false,"connectionId":0,"encodedDataLength":0,"securityState":""},"loaderId":"","timestamp":0,"type":""}`))
		capped.collect(eventTestMessage("Network.loadingFailed", "page", `{"requestId":"dropped","errorText":"body failure","timestamp":0,"type":""}`))
		if report := capped.Snapshot(); report.DroppedResourceFailures != 1 || len(report.ResourceFailures) != 1 || len(capped.requests) != 0 {
			t.Fatalf("counted correlated failure twice: %+v", report)
		}

	})
}

func TestDiagnosticsValueFormatting(t *testing.T) {
	primitive := func(value any) *proto.RuntimeRemoteObject {
		return &proto.RuntimeRemoteObject{Value: jsonvalue.New(value)}
	}
	for _, tc := range []struct {
		object *proto.RuntimeRemoteObject
		want   string
	}{
		{primitive("hello"), "hello"}, {primitive(false), "false"}, {primitive(42), "42"},
		{&proto.RuntimeRemoteObject{Type: "undefined"}, "undefined"},
		{&proto.RuntimeRemoteObject{Type: "object", Subtype: "null"}, "null"},
		{&proto.RuntimeRemoteObject{Type: "number", UnserializableValue: "NaN"}, "NaN"},
		{&proto.RuntimeRemoteObject{Type: "bigint", UnserializableValue: "42n"}, "42n"},
		{&proto.RuntimeRemoteObject{Type: "number", UnserializableValue: "-0"}, "-0"},
		{&proto.RuntimeRemoteObject{Type: "number", UnserializableValue: "Infinity"}, "Infinity"},
		{&proto.RuntimeRemoteObject{Type: "symbol", Description: "Symbol(key)"}, "Symbol(key)"},
	} {
		r := diagnosticText{limit: 100, depth: 3}
		r.object(tc.object)
		if got := r.String(); got != tc.want {
			t.Errorf("render = %q, want %q", got, tc.want)
		}
	}
	key := &proto.RuntimeObjectPreview{Type: "string", Description: "key"}
	value := &proto.RuntimeObjectPreview{Type: "number", Description: "3"}
	for _, tc := range []struct {
		preview *proto.RuntimeObjectPreview
		want    string
	}{
		{&proto.RuntimeObjectPreview{Type: "object", Subtype: "map", Entries: []*proto.RuntimeEntryPreview{{Key: key, Value: value}}, Overflow: true}, `Map{"key" => 3, ...}`},
		{&proto.RuntimeObjectPreview{Type: "object", Subtype: "set", Entries: []*proto.RuntimeEntryPreview{{Value: value}}}, `Set{3}`},
		{&proto.RuntimeObjectPreview{Type: "object", Properties: []*proto.RuntimePropertyPreview{{Name: "getter", Type: "accessor"}, {Name: "name", Type: "string", Value: "value"}}}, `{getter: accessor, name: "value"}`},
	} {
		r := diagnosticText{limit: 100, depth: 3}
		r.preview(tc.preview, 0)
		if got := r.String(); got != tc.want {
			t.Errorf("preview = %q, want %q", got, tc.want)
		}
	}
	cycle := &proto.RuntimeObjectPreview{Type: "object"}
	cycle.Properties = []*proto.RuntimePropertyPreview{{Name: "self", ValuePreview: cycle}}
	r := diagnosticText{limit: 100, depth: 2}
	r.preview(cycle, 0)
	if r.String() != "{self: {self: ...}}" {
		t.Fatalf("cyclic preview = %q", r.String())
	}
	for limit := 1; limit < 15; limit++ {
		r := diagnosticText{limit: limit}
		r.write(strings.Repeat("é", 20))
		if got := r.String(); len(got) > limit || !utf8.ValidString(got) || !strings.HasSuffix(got, ".") {
			t.Fatalf("invalid truncation: %q", got)
		}
	}
}

func TestDiagnosticsDomainTransitionCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newDiagnosticsTestPage(t)
		client.enableStarted, client.enableContinue = make(chan struct{}), make(chan struct{})
		restored := make(chan error, 1)
		go func() {
			release, err := page.EnableDomain(&proto.RuntimeEnable{})
			if err == nil {
				err = release()
			}
			restored <- err
		}()
		<-client.enableStarted
		ctx, cancel := context.WithTimeout(page.ctx, time.Second)
		defer cancel()
		clone := *page
		clone.ctx = ctx
		if _, err := clone.StartDiagnostics(DiagnosticsOptions{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting acquisition ignored context: %v", err)
		}
		close(client.enableContinue)
		if err := <-restored; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if page.browser.event.Len() != 0 {
			t.Fatal("waiting acquisition leaked subscription")
		}
	})
}

// A recorder queues only its own page's recorded event types, so traffic on
// other pages and unrecorded methods never waits in its subscription.
func TestDiagnosticsSubscriptionSkipsUnusedEvents(t *testing.T) {
	page, _ := newDiagnosticsTestPage(t)
	d, err := page.StartDiagnostics(DiagnosticsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Hold the collector inside a recorded event so accepted events stay queued.
	d.mu.Lock()
	page.browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", page.SessionID, `{"type":"log","args":[],"executionContextId":0,"timestamp":0}`))
	requireReleased(t,
		publishUnused(page.browser, "Runtime.consoleAPICalled", "other-page"),
		publishUnused(page.browser, "Network.dataReceived", page.SessionID),
		publishUnused(page.browser, "Runtime.bindingCalled", page.SessionID),
	)
	d.mu.Unlock()
	if _, err := d.Stop(); err != nil {
		t.Fatal(err)
	}
}
