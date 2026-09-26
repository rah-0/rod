package rod

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/proto"
)

type dragTestLog struct {
	sync.Mutex
	events []proto.InputDispatchDragEvent
}

func (log *dragTestLog) record(_ context.Context, _, method string, params any) ([]byte, error) {
	if method == "Input.dispatchDragEvent" {
		log.Lock()
		log.events = append(log.events, params.(proto.InputDispatchDragEvent))
		log.Unlock()
	}
	return []byte(`{}`), nil
}

func (log *dragTestLog) snapshot() []proto.InputDispatchDragEvent {
	log.Lock()
	defer log.Unlock()
	return slices.Clone(log.events)
}

func newDragTestPage(t *testing.T) (*Page, *sessionTestClient, *dragTestLog) {
	t.Helper()
	log := new(dragTestLog)
	client := &sessionTestClient{call: log.record}
	browser := New().Context(t.Context()).Client(client)
	return browser.PageFromSession("drag-session"), client, log
}

func dragTestData() *proto.InputDragData {
	return &proto.InputDragData{
		Items:              []*proto.InputDragDataItem{{MIMEType: "text/plain", Data: "payload"}},
		DragOperationsMask: 1,
	}
}

type dragInvalidCase struct {
	name     string
	position proto.Point
	data     *proto.InputDragData
	iframe   bool
}

func TestDragValidation(t *testing.T) {
	for _, test := range []dragInvalidCase{
		{name: "nil data"},
		{name: "files", data: &proto.InputDragData{Files: []string{"file.txt"}}},
		{name: "nil item", data: &proto.InputDragData{Items: []*proto.InputDragDataItem{nil}}},
		{name: "nan x", data: dragTestData(), position: proto.Point{X: math.NaN()}},
		{name: "nan y", data: dragTestData(), position: proto.Point{Y: math.NaN()}},
		{name: "infinite x", data: dragTestData(), position: proto.Point{X: math.Inf(1)}},
		{name: "infinite y", data: dragTestData(), position: proto.Point{Y: math.Inf(-1)}},
		{name: "iframe", data: dragTestData(), iframe: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page, _, log := newDragTestPage(t)
				if test.iframe {
					page.element = &Element{}
				}
				if drag, err := page.Drag(test.position, test.data); drag != nil || !errors.Is(err, ErrInvalidDrag) {
					t.Fatalf("invalid drag = %v, %v", drag, err)
				}
				if len(log.snapshot()) != 0 {
					t.Fatal("invalid drag dispatched a command")
				}
				page.element = nil
				drag, err := page.Drag(proto.Point{}, dragTestData())
				if err != nil {
					t.Fatalf("invalid drag retained the session lease: %v", err)
				}
				if err := drag.MoveTo(proto.Point{X: math.Inf(1)}); !errors.Is(err, ErrInvalidDrag) {
					t.Fatalf("invalid move = %v", err)
				}
				if len(log.snapshot()) != 1 {
					t.Fatal("invalid move dispatched a command")
				}
				if err := drag.Drop(); err != nil {
					t.Fatalf("invalid move ended the drag: %v", err)
				}
			})
		})
	}
}

func TestDragAutomaticCancellation(t *testing.T) {
	for _, end := range []string{"caller", "cause", "deadline", "session"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page, client, log := newDragTestPage(t)
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				wantErr := context.Canceled
				if end == "deadline" {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, time.Second)
					defer stop()
					wantErr = context.DeadlineExceeded
				}
				if end == "session" {
					page.sessionCtx = ctx
					page.browser.states.Store(sessionStateKey(page.SessionID), ctx)
				} else {
					page = page.Context(ctx)
				}
				client.call = func(callCtx context.Context, session, method string, params any) ([]byte, error) {
					if method == "Input.dispatchDragEvent" && params.(proto.InputDispatchDragEvent).Type == proto.InputDispatchDragEventTypeDragCancel {
						if callCtx.Err() != nil {
							t.Error("cleanup reused canceled caller context")
						}
						deadline, ok := callCtx.Deadline()
						if !ok || time.Until(deadline) != 5*time.Second {
							t.Errorf("cleanup deadline = %v, bounded=%t", deadline, ok)
						}
					}
					return log.record(callCtx, session, method, params)
				}
				drag, err := page.Drag(proto.Point{X: 3, Y: 4}, dragTestData())
				if err != nil {
					t.Fatal(err)
				}
				switch end {
				case "cause":
					wantErr = errors.New("caller stopped dragging")
					cancel(wantErr)
				case "deadline":
					time.Sleep(time.Second)
				default:
					cancel(nil)
				}
				synctest.Wait()
				if _, retained := page.browser.states.Load(dragStateKey(page.SessionID)); retained {
					t.Fatal("automatic cancellation retained the drag lease")
				}
				if err := drag.Cancel(); !errors.Is(err, wantErr) {
					t.Fatalf("terminal Cancel = %v, want %v", err, wantErr)
				}
				for _, err := range []error{drag.MoveTo(proto.Point{}), drag.Drop()} {
					if !errors.Is(err, ErrDragEnded) || !errors.Is(err, wantErr) {
						t.Fatalf("operation after cancellation = %v, want ended and %v", err, wantErr)
					}
				}
				wantCount := 2
				if end == "session" {
					wantCount = 1 // A dead session cannot receive a cleanup command.
				}
				if got := log.snapshot(); len(got) != wantCount || got[0].Type != proto.InputDispatchDragEventTypeDragEnter || (wantCount == 2 && got[1].Type != proto.InputDispatchDragEventTypeDragCancel) {
					t.Fatalf("automatic cleanup commands = %+v", got)
				}
			})
		})
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
