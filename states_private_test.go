package rod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"weak"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

func attachTestResult(params any) ([]byte, error) {
	target := params.(proto.TargetAttachToTarget).TargetID
	return json.Marshal(proto.TargetAttachToTargetResult{SessionID: proto.TargetSessionID(target) + "-session"})
}

func TestPageAttachSharesTargetAttachment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		var attaches atomic.Int32
		client := &sessionTestClient{events: make(chan *cdp.Event), call: func(_ context.Context, _, method string, params any) ([]byte, error) {
			time.Sleep(10 * time.Millisecond)
			if method == "Target.attachToTarget" {
				attaches.Add(1)
				return attachTestResult(params)
			}
			return []byte(`{}`), nil
		}}
		browser := New().NoDefaultDevice().Context(ctx).Client(client)
		browser.initEvents()
		pages := make([]*Page, 8)
		var group sync.WaitGroup
		for i := range pages {
			group.Go(func() {
				page, err := browser.PageFromTarget("target")
				if err != nil {
					t.Error(err)
				}
				pages[i] = page
			})
		}
		group.Wait()
		if got := attaches.Load(); got != 1 {
			t.Fatalf("concurrent callers attached %d times", got)
		}
		for _, page := range pages {
			if page == nil || page.root != pages[0].root || page.root != browser.loadCachedPage("target") {
				t.Fatal("concurrent callers received different page attachments")
			}
		}
	})
}

func TestPageAttachRetriesAfterCanceledAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		var attaches atomic.Int32
		client := &sessionTestClient{events: make(chan *cdp.Event), call: func(ctx context.Context, _, method string, params any) ([]byte, error) {
			if method != "Target.attachToTarget" {
				return []byte(`{}`), nil
			}
			if attaches.Add(1) == 1 {
				<-ctx.Done() // The first attempt lasts until its caller cancels.
				return nil, ctx.Err()
			}
			return attachTestResult(params)
		}}
		browser := New().NoDefaultDevice().Context(ctx).Client(client)
		browser.initEvents()
		firstCtx, cancelFirst := context.WithCancel(ctx)
		first := make(chan error, 1)
		go func() {
			_, err := browser.Context(firstCtx).PageFromTarget("target")
			first <- err
		}()
		synctest.Wait()
		type result struct {
			page *Page
			err  error
		}
		second := make(chan result, 1)
		go func() {
			page, err := browser.PageFromTarget("target")
			second <- result{page, err}
		}()
		synctest.Wait()
		if attaches.Load() != 1 {
			t.Fatal("a waiting caller started a second attachment")
		}
		cancelFirst()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled attachment: %v", err)
		}
		got := <-second
		if got.err != nil {
			t.Fatalf("waiting caller inherited another caller's cancellation: %v", got.err)
		}
		if attaches.Load() != 2 || got.page.root != browser.loadCachedPage("target") {
			t.Fatal("waiting caller did not attach after the canceled attempt")
		}
	})
}

func TestRetainedCommandsExcludeTransientParameters(t *testing.T) {
	browser, _ := newEventTestBrowser(t)
	payload := &proto.RuntimeCallArgument{UnserializableValue: proto.RuntimeUnserializableValue(strings.Repeat("x", 1<<20))}
	collected := weak.Make(payload)
	commands := []proto.Request{
		proto.RuntimeCallFunctionOn{FunctionDeclaration: "value => value", Arguments: []*proto.RuntimeCallArgument{payload}},
		proto.FetchFulfillRequest{RequestID: "request", ResponseCode: 200, Body: []byte("body")},
		proto.PageSetDocumentContent{FrameID: "frame", HTML: "<p>document</p>"},
		proto.InputInsertText{Text: "text"},
		proto.TargetCreateTarget{URL: "about:blank", BrowserContextID: "context"},
	}
	for _, command := range commands {
		if _, err := browser.Call(t.Context(), "session", command.ProtoReq(), command); err != nil {
			t.Fatal(err)
		}
		if browser.LoadState("session", command) {
			t.Errorf("%s parameters were retained", command.ProtoReq())
		}
	}
	browser.states.Range(func(key, _ any) bool {
		t.Errorf("transient commands retained state under %#v", key)
		return true
	})
	commands, payload = nil, nil
	runtime.GC()
	if collected.Value() != nil {
		t.Fatal("completed command parameters remained reachable")
	}
}

func TestDetachedSessionReleasesState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		events := make(chan *cdp.Event)
		browser := New().Context(ctx).Client(&sessionTestClient{events: events})
		browser.initEvents()
		// PageFromSession has no page event loop that terminates the session.
		raw := browser.PageFromSession("raw")
		if err := (proto.RuntimeEnable{}).Call(raw); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.EnableDomain(&proto.NetworkEnable{}); err != nil {
			t.Fatal(err)
		}
		if _, err := browser.Call(ctx, "other", "Runtime.enable", nil); err != nil {
			t.Fatal(err)
		}
		events <- &cdp.Event{Method: "Target.detachedFromTarget", Params: json.RawMessage(`{"sessionId":"raw"}`)}
		synctest.Wait()
		if raw.LoadState(&proto.RuntimeEnable{}) {
			t.Fatal("a detached session retained its commands")
		}
		if _, retained := browser.states.Load(sessionCommandsKey("raw")); retained {
			t.Fatal("a detached session retained its domain leases")
		}
		if !browser.LoadState("other", &proto.RuntimeEnable{}) {
			t.Fatal("detaching one session removed another session's commands")
		}
	})
}

func TestDisposedContextReleasesState(t *testing.T) {
	browser, _ := newEventTestBrowser(t)
	browser.Client(&sessionTestClient{call: func(_ context.Context, _, method string, _ any) ([]byte, error) {
		switch method {
		case "Target.createBrowserContext":
			return []byte(`{"browserContextId":"private"}`), nil
		case "Target.createTarget":
			return []byte(`{"targetId":"target"}`), nil
		}
		return []byte(`{}`), nil
	}})
	allow := proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorAllow}
	if err := allow.Call(browser); err != nil {
		t.Fatal(err)
	}
	retained := func() (keys []string) {
		browser.states.Range(func(key, _ any) bool {
			keys = append(keys, fmt.Sprintf("%#v", key))
			return true
		})
		slices.Sort(keys)
		return keys
	}
	before := retained()
	incognito, err := browser.Incognito()
	if err != nil {
		t.Fatal(err)
	}
	deny := proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorDeny, BrowserContextID: incognito.BrowserContextID}
	if err := deny.Call(incognito); err != nil {
		t.Fatal(err)
	}
	if _, err := (proto.TargetCreateTarget{URL: "about:blank", BrowserContextID: incognito.BrowserContextID}).Call(incognito); err != nil {
		t.Fatal(err)
	}
	var behavior proto.BrowserSetDownloadBehavior
	if !incognito.LoadState("", &behavior) || !reflect.DeepEqual(behavior, deny) {
		t.Fatalf("incognito download behavior: %+v", behavior)
	}
	if !browser.LoadState("", &behavior) || !reflect.DeepEqual(behavior, allow) {
		t.Fatalf("default context download behavior: %+v", behavior)
	}
	if err := incognito.Close(); err != nil {
		t.Fatal(err)
	}
	if incognito.LoadState("", &proto.BrowserSetDownloadBehavior{}) {
		t.Fatal("disposed context retained its download behavior")
	}
	if after := retained(); !slices.Equal(after, before) {
		t.Fatalf("disposed context changed retained state from %v to %v", before, after)
	}
}

func TestSessionTerminationKeepsOtherState(t *testing.T) {
	browser, _ := newEventTestBrowser(t)
	call := func(session proto.TargetSessionID, req proto.Request) {
		t.Helper()
		if _, err := browser.Call(t.Context(), string(session), req.ProtoReq(), req); err != nil {
			t.Fatal(err)
		}
	}
	call("", proto.FetchEnable{})
	call("other", proto.RuntimeEnable{})
	call("session", proto.RuntimeEnable{})
	page := &Page{browser: browser, ctx: t.Context(), SessionID: "session", TargetID: "target"}
	page.root = page
	page.terminateSession()
	if browser.LoadState("session", &proto.RuntimeEnable{}) {
		t.Fatal("terminated session retained its commands")
	}
	if !browser.LoadState("other", &proto.RuntimeEnable{}) || !browser.LoadState("", &proto.FetchEnable{}) {
		t.Fatal("session termination removed state of another scope")
	}
	(&Page{browser: browser, ctx: t.Context()}).terminateSession()
	if !browser.LoadState("", &proto.FetchEnable{}) {
		t.Fatal("a page view without a session removed browser-level commands")
	}
}

func TestIncognitoCycleReleasesState(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	browser := New().Context(ctx)
	if err := browser.Launch(launcher.New()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := browser.Close(); err != nil {
			t.Error(err)
		}
	}()
	cycle := func() {
		incognito, err := browser.Incognito()
		if err != nil {
			t.Fatal(err)
		}
		deny := proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorDeny, BrowserContextID: incognito.BrowserContextID}
		if err := deny.Call(incognito); err != nil {
			t.Fatal(err)
		}
		page, err := incognito.Page(proto.TargetCreateTarget{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := page.Eval(`value => value.length`, strings.Repeat("x", 1<<10)); err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(page.Close(), incognito.Close()); err != nil {
			t.Fatal(err)
		}
	}
	retained := func() (keys []string) {
		browser.states.Range(func(key, _ any) bool {
			keys = append(keys, fmt.Sprintf("%#v", key))
			return true
		})
		slices.Sort(keys)
		return keys
	}
	cycle()
	before := retained()
	for range 5 {
		cycle()
	}
	if after := retained(); !slices.Equal(after, before) {
		t.Fatalf("incognito cycles changed retained state from %v to %v", before, after)
	}
}

func TestSharedDomainProtocolScope(t *testing.T) {
	browser, client := newEventTestBrowser(t)
	incognito := browser.Context(t.Context())
	incognito.BrowserContextID = "private"
	first, err := browser.acquireDomain("", &proto.FetchEnable{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := incognito.acquireDomain("", &proto.FetchEnable{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !incognito.LoadState("", &proto.FetchEnable{}) {
		t.Fatal("one browser context disabled another observer's Fetch domain")
	}
	if err := second(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(client.snapshot(), []eventTestCall{{method: "Fetch.enable"}, {method: "Fetch.disable"}}) {
		t.Fatalf("domain transitions: %v", client.snapshot())
	}
}

func TestSharedLifecycleSetting(t *testing.T) {
	for _, initiallyEnabled := range []bool{false, true} {
		browser, _ := newEventTestBrowser(t)
		if err := (proto.PageSetLifecycleEventsEnabled{Enabled: initiallyEnabled}).Call(browser); err != nil {
			t.Fatal(err)
		}
		first, err := browser.acquireDomain("", &proto.PageSetLifecycleEventsEnabled{Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		second, err := browser.acquireDomain("", &proto.PageSetLifecycleEventsEnabled{Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := first(t.Context()); err != nil {
			t.Fatal(err)
		}
		var state proto.PageSetLifecycleEventsEnabled
		if !browser.LoadState("", &state) || !state.Enabled {
			t.Fatal("first waiter disabled lifecycle events")
		}
		if err := second(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !browser.LoadState("", &state) || state.Enabled != initiallyEnabled {
			t.Fatalf("restored lifecycle setting: %+v", state)
		}
	}
}

func TestBrowserDisconnectClosesUnreadEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &sessionTestClient{events: make(chan *cdp.Event)}
		browser := New().Context(t.Context()).Client(client)
		browser.initEvents()
		stream := browser.Event()
		wait := browser.EachEvent(On(func(*proto.NetworkLoadingFinished, proto.TargetSessionID) bool { return true }))
		client.events <- &cdp.Event{Method: "Page.loadEventFired", Params: json.RawMessage(`{"timestamp":0}`)}
		synctest.Wait() // Event is forwarding to an unread destination.
		close(client.events)
		synctest.Wait()
		if _, open := <-stream; open {
			t.Fatal("disconnect left an unread browser event open")
		}
		if err := wait(); !errors.Is(err, ErrBrowserDisconnected) {
			t.Fatalf("disconnected wait: %v", err)
		}
		browser.states.Range(func(key, _ any) bool { t.Errorf("disconnected browser retained state: %+v", key); return true })
	})
}

func TestPageSessionViewsAndEviction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		client := &sessionTestClient{events: make(chan *cdp.Event)}
		browser := New().NoDefaultDevice().Context(ctx).Client(client)
		browser.initEvents()
		firstCtx, cancelFirst := context.WithCancel(ctx)
		first, err := browser.Context(firstCtx).PageFromTarget("target")
		if err != nil {
			t.Fatal(err)
		}
		cancelFirst()
		second, err := browser.PageFromTarget("target")
		if err != nil {
			t.Fatal(err)
		}
		if first == second || first.SessionID != second.SessionID || second.ctx.Err() != nil || second.browser != browser {
			t.Fatal("cached attachment retained its first caller context")
		}
		if err := second.Keyboard.Press(input.ShiftLeft); err != nil {
			t.Fatal(err)
		}
		if err := first.Keyboard.Press(input.KeyA); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled keyboard: %v", err)
		}
		if first.Keyboard.getModifiers() != second.Keyboard.getModifiers() || second.Keyboard.getModifiers() == 0 {
			t.Fatal("page views lost shared keyboard state")
		}
		point := proto.Point{X: 7, Y: 9}
		if err := second.Mouse.MoveTo(point); err != nil {
			t.Fatal(err)
		}
		if first.Mouse.Position() != point {
			t.Fatal("page views lost shared mouse position")
		}
		if err := first.Mouse.MoveTo(proto.Point{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled mouse: %v", err)
		}
		if err := first.Touch.End(); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled touch: %v", err)
		}
		if !second.LoadState(&proto.PageEnable{}) {
			t.Fatal("attachment did not retain Page.enable")
		}
		release, err := second.EnableDomain(&proto.NetworkEnable{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = release() }()
		stream := second.Event()
		client.events <- &cdp.Event{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"target"}`)}
		for range stream {
		}
		synctest.Wait()
		if browser.loadCachedPage("target") != nil || browser.sessionContext(second.SessionID) != nil {
			t.Fatal("terminated target or session remained cached")
		}
		if _, retained := browser.states.Load(sessionCommandsKey(second.SessionID)); retained {
			t.Error("terminated session retained its commands or domain leases")
		}
		if err := (proto.RuntimeEnable{}).Call(second.Context(context.Background())); !errors.Is(err, context.Canceled) {
			t.Fatalf("terminated session accepted a new operation: %v", err)
		}
	})
}

func TestSessionLateReplyDoesNotRestoreState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newEventTestBrowser(t)
		ctx, cancel := context.WithCancel(t.Context())
		page := &Page{browser: browser, ctx: ctx, sessionCtx: ctx, sessionCancel: cancel, SessionID: "session", TargetID: "target"}
		page.root = page
		browser.states.Store(sessionStateKey(page.SessionID), ctx)
		browser.cachePage(page)
		started, finish := make(chan struct{}), make(chan struct{})
		client := &sessionTestClient{call: func(context.Context, string, string, any) ([]byte, error) {
			close(started)
			<-finish // A response can race with target destruction.
			return []byte(`{}`), nil
		}}
		browser.Client(client)
		done := make(chan struct{})
		go func() {
			_, _ = browser.Call(t.Context(), string(page.SessionID), "Runtime.enable", proto.RuntimeEnable{})
			close(done)
		}()
		<-started
		page.terminateSession()
		close(finish)
		<-done
		if browser.LoadState(page.SessionID, &proto.RuntimeEnable{}) {
			t.Fatal("late reply repopulated a terminated session")
		}
		if _, retained := browser.states.Load(sessionCommandsKey(page.SessionID)); retained {
			t.Fatal("late reply recreated retained session commands")
		}
	})
}

func TestPageAttachLockCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser := New()
		attaching := make(chan struct{}) // Another caller is attaching this target.
		browser.states.Store(pageAttachKey("target"), attaching)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := browser.Context(ctx).PageFromTarget("target"); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled attach: %v", err)
		}
		ctx, cancel = context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			_, err := browser.Context(ctx).PageFromTarget("target")
			result <- err
		}()
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("attach did not wait for the pending attachment: %v", err)
		default:
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("contended attach: %v", err)
		}
	})
}
