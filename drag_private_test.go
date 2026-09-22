package rod

import (
	"context"
	"errors"
	"math"
	"reflect"
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

func TestDragCommandSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client, log := newDragTestPage(t)
		if err := page.Keyboard.Press(input.ShiftLeft); err != nil {
			t.Fatal(err)
		}
		data := &proto.InputDragData{
			Items: []*proto.InputDragDataItem{
				{MIMEType: "text/plain", Data: "original"},
				{MIMEType: "text/html", Data: "<b>original</b>", BaseURL: "https://example.test/"},
				{MIMEType: "text/uri-list", Data: "https://example.test/", Title: "Original"},
			},
			DragOperationsMask: 17,
		}
		drag, err := page.Drag(proto.Point{X: 1, Y: 2}, data)
		if err != nil {
			t.Fatal(err)
		}
		defer drag.Cancel()
		data.Items[0].Data = "changed"
		data.Items[1].BaseURL = "https://changed.test/"
		data.Items[2].Title = "Changed"
		data.Items = nil
		data.DragOperationsMask = 0
		if err := page.Context(t.Context()).Keyboard.Release(input.ShiftLeft); err != nil {
			t.Fatal(err)
		}
		if err := page.Keyboard.Press(input.ControlLeft); err != nil {
			t.Fatal(err)
		}
		if err := drag.MoveTo(proto.Point{X: 30, Y: 40}); err != nil {
			t.Fatal(err)
		}
		if err := page.Keyboard.Release(input.ControlLeft); err != nil {
			t.Fatal(err)
		}
		if err := drag.Drop(); err != nil {
			t.Fatal(err)
		}
		if err := drag.Cancel(); err != nil {
			t.Fatal(err)
		}
		wantData := &proto.InputDragData{
			Items: []*proto.InputDragDataItem{
				{MIMEType: "text/plain", Data: "original"},
				{MIMEType: "text/html", Data: "<b>original</b>", BaseURL: "https://example.test/"},
				{MIMEType: "text/uri-list", Data: "https://example.test/", Title: "Original"},
			},
			DragOperationsMask: 17,
		}
		want := []proto.InputDispatchDragEvent{
			{Type: proto.InputDispatchDragEventTypeDragEnter, X: 1, Y: 2, Data: wantData, Modifiers: input.ShiftLeft.Modifier()},
			{Type: proto.InputDispatchDragEventTypeDragOver, X: 30, Y: 40, Data: wantData, Modifiers: input.ControlLeft.Modifier()},
			{Type: proto.InputDispatchDragEventTypeDragOver, X: 30, Y: 40, Data: wantData},
			{Type: proto.InputDispatchDragEventTypeDrop, X: 30, Y: 40, Data: wantData},
		}
		if got := log.snapshot(); !reflect.DeepEqual(got, want) {
			t.Fatalf("drag commands = %+v, want %+v", got, want)
		}
		for _, call := range client.snapshot() {
			if call.session != "drag-session" || (call.method != "Input.dispatchDragEvent" && call.method != "Input.dispatchKeyEvent") {
				t.Fatalf("unexpected drag side effect: %+v", call)
			}
		}
		for _, err := range []error{drag.MoveTo(proto.Point{}), drag.Drop()} {
			if !errors.Is(err, ErrDragEnded) {
				t.Fatalf("operation after Drop = %v", err)
			}
		}
		if len(log.snapshot()) != len(want) {
			t.Fatal("ended drag dispatched additional events")
		}
		if page.GetContext().Err() != nil {
			t.Fatal("Drop canceled the caller's page context")
		}
	})
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

func TestDragSessionLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, _, log := newDragTestPage(t)
		drag, err := page.Drag(proto.Point{}, dragTestData())
		if err != nil {
			t.Fatal(err)
		}
		defer drag.Cancel()
		for _, view := range []*Page{page.Context(t.Context()), page.browser.Context(t.Context()).PageFromSession(page.SessionID)} {
			if other, err := view.Drag(proto.Point{}, dragTestData()); other != nil || !errors.Is(err, ErrDragInProgress) {
				t.Fatalf("same-session drag = %v, %v", other, err)
			}
		}
		if len(log.snapshot()) != 1 {
			t.Fatal("conflicting drag dispatched commands")
		}
		other, err := page.browser.PageFromSession("other-session").Drag(proto.Point{}, dragTestData())
		if err != nil {
			t.Fatalf("independent session blocked: %v", err)
		}
		if err := other.Cancel(); err != nil {
			t.Fatal(err)
		}
		if err := drag.Cancel(); err != nil {
			t.Fatal(err)
		}
		next, err := page.Context(t.Context()).Drag(proto.Point{}, dragTestData())
		if err != nil {
			t.Fatalf("Cancel retained session lease: %v", err)
		}
		if err := next.Cancel(); err != nil {
			t.Fatal(err)
		}
	})
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

func TestDragCancelInterruptsCommand(t *testing.T) {
	for _, operation := range []string{"move", "drop"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page, client, log := newDragTestPage(t)
				entered := make(chan struct{})
				client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
					res, err := log.record(ctx, session, method, params)
					if method == "Input.dispatchDragEvent" && params.(proto.InputDispatchDragEvent).Type == proto.InputDispatchDragEventTypeDragOver {
						close(entered)
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return res, err
				}
				drag, err := page.Drag(proto.Point{}, dragTestData())
				if err != nil {
					t.Fatal(err)
				}
				pending := make(chan error, 1)
				go func() {
					if operation == "move" {
						pending <- drag.MoveTo(proto.Point{X: 20, Y: 30})
					} else {
						pending <- drag.Drop()
					}
				}()
				<-entered
				canceled := make(chan error, 1)
				go func() { canceled <- drag.Cancel() }()
				synctest.Wait()
				for name, done := range map[string]<-chan error{"command": pending, "cancel": canceled} {
					select {
					case err := <-done:
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("%s = %v, want canceled", name, err)
						}
					default:
						t.Fatalf("%s remained blocked", name)
					}
				}
				if got := log.snapshot(); len(got) != 3 || got[2].Type != proto.InputDispatchDragEventTypeDragCancel {
					t.Fatalf("interrupted command cleanup = %+v", got)
				}
				if page.GetContext().Err() != nil {
					t.Fatal("Cancel canceled the caller's page context")
				}
			})
		})
	}
}

func TestDragCancelWithBlockedKeyboard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client, log := newDragTestPage(t)
		if err := page.Keyboard.Press(input.ShiftLeft); err != nil {
			t.Fatal(err)
		}
		drag, err := page.Drag(proto.Point{}, dragTestData())
		if err != nil {
			t.Fatal(err)
		}
		keyCtx, stopKey := context.WithCancel(t.Context())
		defer stopKey()
		keyStarted := make(chan struct{})
		client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
			if method == "Input.dispatchKeyEvent" {
				close(keyStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return log.record(ctx, session, method, params)
		}
		keyDone := make(chan error, 1)
		go func() { keyDone <- page.Context(keyCtx).Keyboard.Press(input.ControlLeft) }()
		<-keyStarted
		cancelDone := make(chan error, 1)
		go func() { cancelDone <- drag.Cancel() }()
		synctest.Wait()
		select {
		case err := <-cancelDone:
			if err != nil {
				t.Fatalf("Cancel while a key request is blocked = %v", err)
			}
		default:
			t.Fatal("drag cleanup waited for a blocked keyboard request")
		}
		commands := log.snapshot()
		if len(commands) != 2 || commands[1].Type != proto.InputDispatchDragEventTypeDragCancel || commands[1].Modifiers != input.ShiftLeft.Modifier() {
			t.Fatalf("cleanup lost the held modifier or dispatched unexpected events: %+v", commands)
		}
		stopKey()
		if err := <-keyDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked key request = %v", err)
		}
	})
}

type dragStartResult struct {
	drag *Drag
	err  error
}

func TestDragCanceledStart(t *testing.T) {
	for _, phase := range []string{"before-enter", "during-enter"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page, client, log := newDragTestPage(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				entered := make(chan struct{})
				client.call = func(callCtx context.Context, session, method string, params any) ([]byte, error) {
					res, err := log.record(callCtx, session, method, params)
					if method == "Input.dispatchDragEvent" && params.(proto.InputDispatchDragEvent).Type == proto.InputDispatchDragEventTypeDragEnter {
						close(entered)
						<-callCtx.Done()
						return nil, callCtx.Err()
					}
					return res, err
				}
				if phase == "before-enter" {
					cancel()
				}
				done := make(chan dragStartResult, 1)
				go func() {
					drag, err := page.Context(ctx).Drag(proto.Point{}, dragTestData())
					done <- dragStartResult{drag, err}
				}()
				if phase == "during-enter" {
					<-entered
					cancel()
				}
				result := <-done
				synctest.Wait()
				if result.drag != nil || !errors.Is(result.err, context.Canceled) {
					t.Fatalf("canceled start = %v, %v", result.drag, result.err)
				}
				wantCount := 0
				if phase == "during-enter" {
					wantCount = 2
				}
				if commands := log.snapshot(); len(commands) != wantCount || (wantCount == 2 && commands[1].Type != proto.InputDispatchDragEventTypeDragCancel) {
					t.Fatalf("canceled start commands = %+v", commands)
				}
				client.call = log.record
				next, err := page.Drag(proto.Point{}, dragTestData())
				if err != nil {
					t.Fatalf("canceled start retained the session lease: %v", err)
				}
				if err := next.Cancel(); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestDragCleanupBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client, log := newDragTestPage(t)
		cleaning := make(chan struct{})
		client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
			res, err := log.record(ctx, session, method, params)
			if method == "Input.dispatchDragEvent" && params.(proto.InputDispatchDragEvent).Type == proto.InputDispatchDragEventTypeDragCancel {
				close(cleaning)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return res, err
		}
		drag, err := page.Drag(proto.Point{}, dragTestData())
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		done := make(chan error, 1)
		go func() { done <- drag.Cancel() }()
		<-cleaning
		if other, err := page.Context(t.Context()).Drag(proto.Point{}, dragTestData()); other != nil || !errors.Is(err, ErrDragInProgress) {
			t.Fatalf("drag during cleanup = %v, %v", other, err)
		}
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked cleanup = %v", err)
		}
		if elapsed := time.Since(started); elapsed != 5*time.Second {
			t.Fatalf("cleanup took %s, want 5s", elapsed)
		}
		if err := drag.Cancel(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("repeated cleanup = %v", err)
		}
		if len(log.snapshot()) != 2 {
			t.Fatal("repeated cleanup dispatched additional commands")
		}
		client.call = log.record
		next, err := page.Drag(proto.Point{}, dragTestData())
		if err != nil {
			t.Fatalf("cleanup deadline retained the drag lease: %v", err)
		}
		if err := next.Cancel(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDragProtocolFailureRollback(t *testing.T) {
	for _, phase := range []string{"enter", "move", "drop-over", "drop", "cancel"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page, client, log := newDragTestPage(t)
				failure := errors.New("drag dispatch rejected")
				cleanupFailure := errors.New("drag cancellation rejected")
				failType := proto.InputDispatchDragEventTypeDragOver
				switch phase {
				case "enter":
					failType = proto.InputDispatchDragEventTypeDragEnter
				case "drop":
					failType = proto.InputDispatchDragEventTypeDrop
				case "cancel":
					failType = proto.InputDispatchDragEventTypeDragCancel
				}
				client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
					res, err := log.record(ctx, session, method, params)
					if method == "Input.dispatchDragEvent" {
						kind := params.(proto.InputDispatchDragEvent).Type
						if kind == failType {
							return nil, failure
						}
						if kind == proto.InputDispatchDragEventTypeDragCancel {
							return nil, cleanupFailure
						}
					}
					return res, err
				}
				drag, err := page.Drag(proto.Point{}, dragTestData())
				if phase != "enter" {
					if err != nil {
						t.Fatal(err)
					}
					switch phase {
					case "move":
						err = drag.MoveTo(proto.Point{X: 5, Y: 6})
					case "cancel":
						err = drag.Cancel()
					default:
						err = drag.Drop()
					}
				} else if drag != nil {
					t.Fatal("failed drag enter returned a drag")
				}
				if !errors.Is(err, failure) || (phase != "cancel" && !errors.Is(err, cleanupFailure)) {
					t.Fatalf("dispatch failure lost its cause or cleanup failure: %v", err)
				}
				commands := log.snapshot()
				if commands[len(commands)-1].Type != proto.InputDispatchDragEventTypeDragCancel {
					t.Fatalf("failure did not attempt cancellation: %+v", commands)
				}
				if drag != nil {
					if err := drag.Drop(); !errors.Is(err, ErrDragEnded) || !errors.Is(err, failure) {
						t.Fatalf("operation after failure = %v", err)
					}
					if err := drag.Cancel(); !errors.Is(err, failure) {
						t.Fatalf("repeated Cancel lost failure: %v", err)
					}
					if len(log.snapshot()) != len(commands) {
						t.Fatal("failed drag repeated cleanup")
					}
				}
				client.call = log.record
				next, err := page.Drag(proto.Point{}, dragTestData())
				if err != nil {
					t.Fatalf("protocol failure retained the drag lease: %v", err)
				}
				if err := next.Cancel(); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
