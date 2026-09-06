package rod

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/proto"
)

type sessionTestClient struct {
	eventTestClient
	call   func(context.Context, string, string, any) ([]byte, error)
	events chan *cdp.Event
}

func (c *sessionTestClient) Call(ctx context.Context, session, method string, params any) ([]byte, error) {
	data, err := c.eventTestClient.Call(ctx, session, method, params)
	if err != nil {
		return nil, err
	}
	if c.call != nil {
		return c.call(ctx, session, method, params)
	}
	if method == "Target.attachToTarget" {
		return json.Marshal(proto.TargetAttachToTargetResult{SessionID: proto.TargetSessionID(params.(proto.TargetAttachToTarget).TargetID) + "-session"})
	}
	return data, nil
}

func (c *sessionTestClient) Event() <-chan *cdp.Event { return c.events }

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

func TestBrowserDisconnectClosesUnreadEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &sessionTestClient{events: make(chan *cdp.Event)}
		browser := New().Context(t.Context()).Client(client)
		browser.initEvents()
		stream := browser.Event()
		wait := browser.EachEvent(On(func(*proto.NetworkLoadingFinished, proto.TargetSessionID) bool { return true }))
		client.events <- &cdp.Event{Method: "Page.loadEventFired", Params: json.RawMessage(`{}`)}
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
		stream := second.Event()
		client.events <- &cdp.Event{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"target"}`)}
		for range stream {
		}
		synctest.Wait()
		if browser.loadCachedPage("target") != nil || browser.sessionContext(second.SessionID) != nil {
			t.Fatal("terminated target or session remained cached")
		}
		browser.states.Range(func(key, _ any) bool {
			if key, ok := key.(stateKey); ok && key.sessionID == second.SessionID {
				t.Errorf("retained session command: %+v", key)
			}
			if key, ok := key.(domainLeaseKey); ok && key.sessionID == second.SessionID {
				t.Errorf("retained session lease: %+v", key)
			}
			return true
		})
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
			_, _ = browser.Call(t.Context(), string(page.SessionID), "Runtime.evaluate", proto.RuntimeEvaluate{Expression: "retained"})
			close(done)
		}()
		<-started
		page.terminateSession()
		close(finish)
		<-done
		if browser.LoadState(page.SessionID, &proto.RuntimeEvaluate{}) {
			t.Fatal("late reply repopulated a terminated session")
		}
	})
}

func TestPageAttachLockCancellation(t *testing.T) {
	browser := New()
	browser.targetsLock <- struct{}{}
	defer func() { <-browser.targetsLock }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := browser.Context(ctx).PageFromTarget("target"); !errors.Is(err, context.Canceled) {
		t.Fatalf("contended attach: %v", err)
	}
}

func TestKeyboardStateCommitsAfterSuccess(t *testing.T) {
	browser, _ := newEventTestBrowser(t)
	failed := errors.New("dispatch failed")
	fail := true
	client := &sessionTestClient{call: func(context.Context, string, string, any) ([]byte, error) {
		if fail {
			return nil, failed
		}
		return []byte(`{}`), nil
	}}
	browser.Client(client)
	page := (&Page{browser: browser, ctx: t.Context()}).newKeyboard().newMouse().newTouch()
	if err := page.Keyboard.Press(input.ShiftLeft); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if page.Keyboard.getModifiers() != 0 {
		t.Fatal("failed press retained a modifier")
	}
	fail = false
	if err := page.Keyboard.Press(input.ShiftLeft); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := page.Keyboard.Release(input.ShiftLeft); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if page.Keyboard.getModifiers() == 0 {
		t.Fatal("failed release forgot a held modifier")
	}
	if err := page.Mouse.Down(proto.InputMouseButtonLeft, 1); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if len(page.Mouse.buttons) != 0 {
		t.Fatal("failed mouse press retained a button")
	}
	fail = false
	if err := page.Mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := page.Mouse.Up(proto.InputMouseButtonLeft, 1); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if len(page.Mouse.buttons) != 1 {
		t.Fatal("failed mouse release forgot a held button")
	}
}

func TestKeyActionsFailureReleasesOwnedKeys(t *testing.T) {
	browser, _ := newEventTestBrowser(t)
	failed := errors.New("second key failed")
	client := &sessionTestClient{call: func(_ context.Context, _, method string, params any) ([]byte, error) {
		if method == "Input.dispatchKeyEvent" && params.(proto.InputDispatchKeyEvent).Code == "KeyA" {
			return nil, failed
		}
		return []byte(`{}`), nil
	}}
	browser.Client(client)
	page := (&Page{browser: browser, ctx: t.Context()}).newKeyboard()
	if err := page.KeyActions().Press(input.ShiftLeft).Type(input.KeyA).Do(); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if page.Keyboard.getModifiers() != 0 {
		t.Fatal("failed sequence retained its modifier")
	}
}
