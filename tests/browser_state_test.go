package rod_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

func TestPageFromTargetConcurrent(t *testing.T) {
	g := setup(t)

	targets := make([]proto.TargetTargetID, 4)
	for i := range targets {
		res, err := proto.TargetCreateTarget{URL: "about:blank"}.Call(g.browser)
		g.E(err)
		targets[i] = res.TargetID
	}
	pages := make([]*rod.Page, 4*len(targets))
	errs := make([]error, len(pages))
	var group sync.WaitGroup
	for i := range pages {
		group.Go(func() {
			pages[i], errs[i] = g.browser.PageFromTarget(targets[i%len(targets)])
		})
	}
	group.Wait()
	g.E(errors.Join(errs...))

	sessions := map[proto.TargetSessionID]proto.TargetTargetID{}
	for i, page := range pages {
		target := targets[i%len(targets)]
		g.Eq(page.TargetID, target)
		if previous, seen := sessions[page.SessionID]; seen {
			g.Eq(previous, target)
		}
		sessions[page.SessionID] = target
	}
	g.Len(sessions, len(targets))
	for _, page := range pages[:len(targets)] {
		g.Eq(page.MustEval(`() => 1 + 1`).Int(), 2)
	}
}

func TestEnableDomainRejectsUntrackedRequest(t *testing.T) {
	g := setup(t)

	_, err := g.page.EnableDomain(&proto.EmulationSetDeviceMetricsOverride{})
	g.True(errors.Is(err, rod.ErrUnsupportedDomain))
	_, err = g.page.DisableDomain(&proto.EmulationSetDeviceMetricsOverride{})
	g.True(errors.Is(err, rod.ErrUnsupportedDomain))
}

func TestLoadStateRetainedCommands(t *testing.T) {
	g := setup(t)

	p := g.newPage()
	g.E(proto.RuntimeEvaluate{Expression: `"retained?"`}.Call(p))
	g.False(p.LoadState(&proto.RuntimeEvaluate{}))

	metrics := proto.EmulationSetDeviceMetricsOverride{Width: 400, Height: 300, DeviceScaleFactor: 1}
	g.E(p.SetViewport(&metrics))
	var loaded proto.EmulationSetDeviceMetricsOverride
	g.True(p.LoadState(&loaded))
	g.Eq(loaded.Width, 400)
	g.E(p.SetViewport(nil))
	g.False(p.LoadState(&proto.EmulationSetDeviceMetricsOverride{}))
}

func TestLoadStateEndsWithDetachedSession(t *testing.T) {
	g := setup(t)

	target, err := proto.TargetCreateTarget{URL: "about:blank"}.Call(g.browser)
	g.E(err)
	attached, err := proto.TargetAttachToTarget{TargetID: target.TargetID, Flatten: new(true)}.Call(g.browser)
	g.E(err)
	raw := g.browser.PageFromSession(attached.SessionID)
	g.E(proto.RuntimeEnable{}.Call(raw))
	g.True(raw.LoadState(&proto.RuntimeEnable{}))

	wait := g.browser.EachEvent(rod.On(func(e *proto.TargetDetachedFromTarget, _ proto.TargetSessionID) bool {
		return e.SessionID == attached.SessionID
	}))
	g.E(proto.TargetCloseTarget{TargetID: target.TargetID}.Call(g.browser))
	g.E(wait())
	g.False(raw.LoadState(&proto.RuntimeEnable{}))
}

func TestDomainSetupAndRestoreErrors(t *testing.T) {
	for _, operation := range []string{"enable", "disable"} {
		for _, phase := range []string{"setup", "setup-and-rollback", "restore"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				browser, _ := newEventTestBrowser(t)
				failures := make(map[string]error)
				client := &sessionTestClient{call: func(_ context.Context, _, method string, _ any) ([]byte, error) {
					return []byte(`{}`), failures[method]
				}}
				browser.Client(client)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				page := browser.PageFromSession("session").Context(ctx)
				change := page.EnableDomain
				setupMethod, restoreMethod := "Network.enable", "Network.disable"
				if operation == "disable" {
					if err := (proto.NetworkEnable{}).Call(page); err != nil {
						t.Fatal(err)
					}
					change = page.DisableDomain
					setupMethod, restoreMethod = restoreMethod, setupMethod
				}
				setupErr, restoreErr := errors.New("setup failed"), errors.New("restoration failed")
				if phase != "restore" {
					failures[setupMethod] = setupErr
					if phase == "setup-and-rollback" {
						failures[restoreMethod] = restoreErr
					}
				}
				restore, err := change(&proto.NetworkEnable{})
				if phase != "restore" {
					if restore != nil || !errors.Is(err, setupErr) {
						t.Fatalf("setup result: restore=%t, err=%v", restore != nil, err)
					}
					if phase == "setup-and-rollback" && !errors.Is(err, restoreErr) {
						t.Fatalf("rollback error was lost: %v", err)
					}
					calls := client.snapshot()
					if calls[len(calls)-1].method != restoreMethod {
						t.Fatalf("uncertain setup did not attempt rollback: %v", calls)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				failures[restoreMethod] = restoreErr
				cancel()
				if err := restore(); !errors.Is(err, restoreErr) {
					t.Fatalf("restore after caller cancellation: %v", err)
				}
				calls := len(client.snapshot())
				if err := restore(); !errors.Is(err, restoreErr) || len(client.snapshot()) != calls {
					t.Fatalf("repeated restore was not idempotent: %v", err)
				}
			})
		}
	}
}

func TestDomainDisableRestoresConfiguration(t *testing.T) {
	browser, client := newEventTestBrowser(t)
	previous := proto.FetchEnable{
		Patterns:           []*proto.FetchRequestPattern{{URLPattern: "https://example.test/*"}},
		HandleAuthRequests: new(true),
	}
	if err := previous.Call(browser); err != nil {
		t.Fatal(err)
	}
	restore, err := browser.DisableDomain("", &proto.FetchEnable{})
	if err != nil {
		t.Fatal(err)
	}
	var current proto.FetchEnable
	if browser.LoadState("", &current) {
		t.Fatal("disabled domain remained enabled")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if !browser.LoadState("", &current) || !reflect.DeepEqual(previous, current) {
		t.Fatalf("previous configuration was not restored: %+v", current)
	}
	calls := len(client.snapshot())
	if err := restore(); err != nil || len(client.snapshot()) != calls {
		t.Fatalf("repeated restoration issued another command: %v", err)
	}
}

func TestDomainRestoreBounded(t *testing.T) {
	for _, operation := range []string{"enable", "disable"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, _ := newEventTestBrowser(t)
				client := new(sessionTestClient)
				browser.Client(client)
				change := browser.EnableDomain
				if operation == "disable" {
					if err := (proto.RuntimeEnable{}).Call(browser); err != nil {
						t.Fatal(err)
					}
					change = browser.DisableDomain
				}
				restore, err := change("", &proto.RuntimeEnable{})
				if err != nil {
					t.Fatal(err)
				}
				client.call = func(ctx context.Context, _, _ string, _ any) ([]byte, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				start := time.Now()
				if err := restore(); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("stalled restoration: %v", err)
				}
				if elapsed := time.Since(start); elapsed != 5*time.Second {
					t.Fatalf("restoration budget: %s", elapsed)
				}
			})
		})
	}
}

func TestSetExtraHeadersDomainErrors(t *testing.T) {
	for _, method := range []string{"Network.enable", "Network.setExtraHTTPHeaders", "Network.disable"} {
		t.Run(method, func(t *testing.T) {
			browser, _ := newEventTestBrowser(t)
			failed := errors.New("protocol request failed")
			client := &sessionTestClient{call: func(_ context.Context, _, request string, _ any) ([]byte, error) {
				if request == method {
					return nil, failed
				}
				return []byte(`{}`), nil
			}}
			browser.Client(client)
			page := browser.PageFromSession("session")
			restore, err := page.SetExtraHeaders([]string{"X-Example", "value"})
			if method == "Network.disable" {
				if err != nil {
					t.Fatal(err)
				}
				if err := restore(); !errors.Is(err, failed) {
					t.Fatalf("header cleanup error was lost: %v", err)
				}
				return
			}
			if restore != nil || !errors.Is(err, failed) {
				t.Fatalf("header setup result: restore=%t, err=%v", restore != nil, err)
			}
			if page.LoadState(&proto.NetworkEnable{}) {
				t.Fatal("failed header setup retained Network enablement")
			}
			if method == "Network.enable" {
				for _, call := range client.snapshot() {
					if call.method == "Network.setExtraHTTPHeaders" {
						t.Fatal("headers were configured after Network setup failed")
					}
				}
			}
		})
	}
}

func attachTestResult(params any) ([]byte, error) {
	target := params.(proto.TargetAttachToTarget).TargetID
	return json.Marshal(proto.TargetAttachToTargetResult{SessionID: proto.TargetSessionID(target) + "-session"})
}

func TestPageAttachConcurrentTargets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		client := &sessionTestClient{events: make(chan *cdp.Event), call: func(_ context.Context, _, method string, params any) ([]byte, error) {
			time.Sleep(10 * time.Millisecond) // Protocol round trip.
			if method == "Target.attachToTarget" {
				return attachTestResult(params)
			}
			return []byte(`{}`), nil
		}}
		// The default device adds three emulation commands to each attachment.
		browser := rod.New().Context(ctx).Client(client)
		connectTestBrowser(t, browser)
		start := time.Now()
		if _, err := browser.PageFromTarget("single"); err != nil {
			t.Fatal(err)
		}
		single := time.Since(start)
		start = time.Now()
		var group sync.WaitGroup
		for i := range 16 {
			group.Go(func() {
				if _, err := browser.PageFromTarget(proto.TargetTargetID(fmt.Sprintf("target-%d", i))); err != nil {
					t.Error(err)
				}
			})
		}
		group.Wait()
		if elapsed := time.Since(start); elapsed != single {
			t.Fatalf("16 concurrent attachments took %v; one took %v", elapsed, single)
		}
	})
}

func TestRetainedCommandRules(t *testing.T) {
	browser, _ := newEventTestBrowser(t)
	call := func(method string, params any) {
		t.Helper()
		if _, err := browser.Call(t.Context(), "session", method, params); err != nil {
			t.Fatal(err)
		}
	}
	metrics := &proto.EmulationSetDeviceMetricsOverride{Width: 800, Height: 600}
	call(metrics.ProtoReq(), metrics)
	metrics.Width = 1 // The retained command is a copy.
	var loadedMetrics proto.EmulationSetDeviceMetricsOverride
	if !browser.LoadState("session", &loadedMetrics) || loadedMetrics.Width != 800 {
		t.Fatalf("retained metrics: %+v", loadedMetrics)
	}
	call((proto.EmulationClearDeviceMetricsOverride{}).ProtoReq(), nil)
	if browser.LoadState("session", &proto.EmulationSetDeviceMetricsOverride{}) {
		t.Fatal("cleared metrics override remained retained")
	}

	// Raw parameters load as their protocol type.
	call("Network.enable", map[string]any{"maxTotalBufferSize": 10})
	var network proto.NetworkEnable
	if !browser.LoadState("session", &network) || network.MaxTotalBufferSize == nil || *network.MaxTotalBufferSize != 10 {
		t.Fatalf("raw Network.enable: %+v", network)
	}
	call("Page.setLifecycleEventsEnabled", json.RawMessage(`{"enabled":true}`))
	var lifecycle proto.PageSetLifecycleEventsEnabled
	if !browser.LoadState("session", &lifecycle) || !lifecycle.Enabled {
		t.Fatalf("raw lifecycle setting: %+v", lifecycle)
	}
	call("Page.enable", nil)
	if !browser.LoadState("session", &proto.PageEnable{}) || !browser.LoadState("session", proto.PageEnable{}) {
		t.Fatal("Page.enable without parameters was not retained")
	}
	call("Network.disable", nil)
	if browser.LoadState("session", &proto.NetworkEnable{}) {
		t.Fatal("disabled domain remained enabled")
	}
	if browser.LoadState("other", &proto.PageEnable{}) || browser.LoadState("", &proto.PageEnable{}) {
		t.Fatal("session command leaked into another scope")
	}
	call("Runtime.evaluate", proto.RuntimeEvaluate{Expression: "1"})
	if browser.LoadState("session", &proto.RuntimeEvaluate{}) {
		t.Fatal("Runtime.evaluate parameters were retained")
	}
}

func TestRetainedCommandsAreCopies(t *testing.T) {
	browser, _ := newEventTestBrowser(t)
	var sent []any
	browser.Client(&sessionTestClient{call: func(_ context.Context, _, method string, params any) ([]byte, error) {
		if method == "Fetch.enable" {
			sent = append(sent, params)
		}
		return []byte(`{}`), nil
	}})
	req := &proto.FetchEnable{Patterns: []*proto.FetchRequestPattern{{URLPattern: "retained"}}}
	if _, err := browser.Call(t.Context(), "", req.ProtoReq(), req); err != nil {
		t.Fatal(err)
	}
	req.Patterns[0].URLPattern = "changed by the caller"
	loaded := proto.FetchEnable{HandleAuthRequests: new(true)}
	if !browser.LoadState("", &loaded) || len(loaded.Patterns) != 1 || loaded.Patterns[0].URLPattern != "retained" {
		t.Fatalf("the caller's request changed the retained command: %+v", loaded)
	}
	if loaded.HandleAuthRequests != nil {
		t.Fatal("LoadState kept a field that the retained command omits")
	}
	loaded.Patterns[0].URLPattern = "changed through a loaded copy"
	var again proto.FetchEnable
	if !browser.LoadState("", &again) || len(again.Patterns) != 1 || again.Patterns[0].URLPattern != "retained" {
		t.Fatalf("a loaded copy shares the retained command: %+v", again)
	}
	restore, err := browser.DisableDomain("", &proto.FetchEnable{})
	if err != nil {
		t.Fatal(err)
	}
	sent = nil
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("restore sent %d Fetch.enable commands", len(sent))
	}
	if restored, ok := sent[0].(proto.FetchEnable); !ok || len(restored.Patterns) != 1 || restored.Patterns[0].URLPattern != "retained" {
		t.Fatalf("restore sent %#v", sent[0])
	}
}

func TestDomainHelpersRejectUntrackedRequests(t *testing.T) {
	browser, client := newEventTestBrowser(t)
	for _, req := range []proto.Request{&proto.EmulationSetDeviceMetricsOverride{}, proto.RuntimeEvaluate{}} {
		if _, err := browser.EnableDomain("", req); !errors.Is(err, rod.ErrUnsupportedDomain) {
			t.Fatalf("EnableDomain(%s): %v", req.ProtoReq(), err)
		}
		if _, err := browser.DisableDomain("", req); !errors.Is(err, rod.ErrUnsupportedDomain) {
			t.Fatalf("DisableDomain(%s): %v", req.ProtoReq(), err)
		}
	}
	if calls := client.snapshot(); len(calls) != 0 {
		t.Fatalf("rejected requests sent commands: %v", calls)
	}
}
