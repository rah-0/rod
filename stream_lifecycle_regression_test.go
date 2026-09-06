package rod

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod/internal/observable"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

type lifecycleRegressionClient struct {
	call func(context.Context, string, any) ([]byte, error)
}

func (c *lifecycleRegressionClient) Call(ctx context.Context, _ string, method string, args any) ([]byte, error) {
	return c.call(ctx, method, args)
}

func (*lifecycleRegressionClient) Event() <-chan *cdp.Event { return nil }

func TestStreamReaderFinalData(t *testing.T) {
	for _, response := range []string{
		`{"data":"last bytes","eof":true}`,
		`{"data":"bGFzdCBieXRlcw==","base64Encoded":true,"eof":true}`,
	} {
		calls := 0
		client := &lifecycleRegressionClient{call: func(context.Context, string, any) ([]byte, error) {
			calls++
			return []byte(response), nil
		}}
		reader := NewStreamReader(client, "stream")
		if n, err := reader.Read(nil); n != 0 || err != nil || calls != 0 {
			t.Fatalf("empty read: n=%d err=%v calls=%d", n, err, calls)
		}
		data, err := io.ReadAll(reader)
		if err != nil || string(data) != "last bytes" || calls != 1 {
			t.Fatalf("final data: %q err=%v calls=%d", data, err, calls)
		}
		if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) || calls != 1 {
			t.Fatalf("read after EOF: n=%d err=%v calls=%d", n, err, calls)
		}
	}
}

func TestStreamReaderDrainsBufferedDataBeforeError(t *testing.T) {
	calls := 0
	disconnected := errors.New("stream disconnected")
	client := &lifecycleRegressionClient{call: func(context.Context, string, any) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"data":"abcdef","eof":false}`), nil
		}
		return nil, disconnected
	}}
	reader := NewStreamReader(client, "stream")
	buffer := make([]byte, 2)
	for _, want := range []string{"ab", "cd", "ef"} {
		if n, err := reader.Read(buffer); n != 2 || err != nil || string(buffer) != want || calls != 1 {
			t.Fatalf("buffered read: %q n=%d err=%v calls=%d, want %q", buffer, n, err, calls, want)
		}
	}
	if _, err := reader.Read(buffer); !errors.Is(err, disconnected) || calls != 2 {
		t.Fatalf("read after buffer: err=%v calls=%d", err, calls)
	}
}

func TestStreamReaderAdvancesExplicitOffset(t *testing.T) {
	calls := 0
	client := &lifecycleRegressionClient{call: func(_ context.Context, method string, params any) ([]byte, error) {
		request := params.(proto.IORead)
		if method != "IO.read" || request.Offset == nil || request.Size == nil || *request.Size != 2 {
			t.Fatalf("read request: %s %+v", method, request)
		}
		calls++
		if calls == 1 {
			if *request.Offset != 7 {
				t.Fatalf("initial offset = %d", *request.Offset)
			}
			return []byte(`{"data":"AP8B","base64Encoded":true,"eof":false}`), nil
		}
		if *request.Offset != 10 {
			t.Fatalf("next offset = %d, want decoded-byte offset 10", *request.Offset)
		}
		return []byte(`{"data":"","eof":true}`), nil
	}}
	reader := NewStreamReader(client, "stream")
	reader.Offset = new(7)
	buffer := make([]byte, 2)
	if n, err := reader.Read(buffer); n != 2 || err != nil || *reader.Offset != 10 {
		t.Fatalf("first read: n=%d err=%v offset=%d", n, err, *reader.Offset)
	}
	if n, err := reader.Read(buffer); n != 1 || err != nil || calls != 1 || *reader.Offset != 10 {
		t.Fatalf("buffered read: n=%d err=%v calls=%d offset=%d", n, err, calls, *reader.Offset)
	}
	if _, err := reader.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("final read: %v", err)
	}
}

func TestPageCloseRequiresClosureEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	browser := New().Context(ctx).Client(&lifecycleRegressionClient{call: func(_ context.Context, method string, _ any) ([]byte, error) {
		if method == "Page.close" {
			cancel()
		}
		return []byte(`{}`), nil
	}})
	browser.event = observable.New[*Message](ctx)
	page := &Page{browser: browser, ctx: ctx, TargetID: "still-alive", SessionID: "session"}
	browser.states.Store(page.TargetID, page)
	if err := page.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("closure without target event returned %v", err)
	}
	if _, present := browser.states.Load(page.TargetID); !present {
		t.Fatal("unconfirmed closure removed the cached target")
	}
}

func TestElementEqualReturnsEvaluationError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	browser := New().Context(ctx).Client(&lifecycleRegressionClient{call: func(ctx context.Context, _ string, _ any) ([]byte, error) {
		return nil, ctx.Err()
	}})
	page := &Page{browser: browser, ctx: ctx, helpers: &jsHelperCache{}}
	element := &Element{ctx: ctx, page: page, Object: &proto.RuntimeRemoteObject{ObjectID: "element"}}
	if equal, err := element.Equal(element); equal || !errors.Is(err, context.Canceled) {
		t.Fatalf("Equal on canceled context = %v, %v", equal, err)
	}
}

func TestPageWaitNavigationIgnoresChildFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		browser := New().Context(ctx).Client(&lifecycleRegressionClient{call: func(context.Context, string, any) ([]byte, error) {
			return []byte(`{}`), nil
		}})
		browser.event = observable.New[*Message](ctx)
		page := &Page{browser: browser, ctx: ctx, SessionID: "session", FrameID: "main"}
		wait := page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		result := make(chan error, 1)
		go func() { result <- wait() }()
		browser.event.Publish(&Message{
			SessionID: "session", Method: "Page.lifecycleEvent",
			data: []byte(`{"frameId":"child","name":"load"}`),
		})
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("child frame completed the main-frame wait: %v", err)
		default:
		}
		browser.event.Publish(&Message{
			SessionID: "session", Method: "Page.lifecycleEvent",
			data: []byte(`{"frameId":"main","name":"load"}`),
		})
		if err := <-result; err != nil {
			t.Fatalf("main frame lifecycle event: %v", err)
		}
	})
}
