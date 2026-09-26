package rod_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

// Calls the page does not await run in call order, none are lost, and their
// promises resolve in that order.
func TestExposeUnawaitedCallsKeepOrder(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(20 * time.Second)
	defer p.CancelTimeout()
	var calls []int
	stop, err := p.Expose("burst", func(value jsonvalue.Value) (any, error) {
		calls = append(calls, value.Get("index").Int())
		return value.Get("index").Int(), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Eval(`async () => {
		const payload = 'x'.repeat(16 * 1024);
		const resolved = [];
		await Promise.all(Array.from({length: 200}, (_, index) =>
			burst({index, payload}).then(value => resolved.push(value))));
		return resolved;
	}`)
	if err != nil {
		t.Fatal(err)
	}
	g.E(stop())
	want := make([]int, 200)
	for index := range want {
		want[index] = index
	}
	var resolved []int
	for _, value := range result.Value.Arr() {
		resolved = append(resolved, value.Int())
	}
	if !slices.Equal(calls, want) || !slices.Equal(resolved, want) {
		t.Fatalf("calls = %v\nresolved = %v", calls, resolved)
	}
}

// Replies that queue while an earlier reply is in flight reach the page in one
// round trip and resolve in call order, including rejected calls.
func TestExposeQueuedRepliesShareRoundTrip(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(20 * time.Second)
	defer p.CancelTimeout()
	const calls = 8
	firstSent, handled := make(chan struct{}), make(chan struct{})
	var lock sync.Mutex
	var batches []int
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		// Expose replies address an execution context rather than an object.
		if request, ok := params.(proto.RuntimeCallFunctionOn); ok && request.ExecutionContextID != 0 && request.ObjectID == "" {
			data, err := json.Marshal(request.Arguments[0].Value)
			if err != nil {
				return nil, err
			}
			var batch []json.RawMessage
			if err := json.Unmarshal(data, &batch); err != nil {
				return nil, err
			}
			lock.Lock()
			batches = append(batches, len(batch))
			first := len(batches) == 1
			lock.Unlock()
			if first {
				// Hold the first reply until the last call has been handled.
				close(firstSent)
				select {
				case <-handled:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		return g.mc.principal.Call(ctx, session, method, params)
	})
	defer g.mc.resetCall()
	stop, err := p.Expose("queued", func(value jsonvalue.Value) (any, error) {
		index := value.Int()
		switch index {
		case 1:
			<-firstSent // The first reply goes alone.
		case calls:
			close(handled)
		}
		if index%3 == 2 {
			return nil, errors.New("rejected")
		}
		return index, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Eval(`async calls => {
		const settled = [];
		await Promise.all(Array.from({length: calls + 1}, (_, index) =>
			queued(index).then(value => settled.push(value), error => settled.push(String(error)))));
		return settled;
	}`, calls)
	if err != nil {
		t.Fatal(err)
	}
	g.E(stop())
	var want []string
	for index := range calls + 1 {
		if index%3 == 2 {
			want = append(want, "rejected")
		} else {
			want = append(want, jsonvalue.New(index).String())
		}
	}
	var settled []string
	for _, value := range result.Value.Arr() {
		settled = append(settled, value.String())
	}
	lock.Lock()
	defer lock.Unlock()
	if !slices.Equal(settled, want) {
		t.Fatalf("settled = %v, want %v", settled, want)
	}
	// The first reply goes alone; the calls handled meanwhile share the next
	// round trip. The reply to the last call may follow separately.
	if len(batches) < 2 || batches[0] != 1 || batches[1] < calls-1 {
		t.Fatalf("reply batch sizes = %v", batches)
	}
}

// A panic in the exposed function, here from a Must helper given a page
// argument, rejects that call with a generic message instead of ending the
// process, and later calls still work.
func TestExposePanicRejectsCall(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><p>text</p>`)).Timeout(20 * time.Second)
	defer p.CancelTimeout()
	reported := make(chan error, 1)
	stop, err := p.Expose("lookup", func(value jsonvalue.Value) (any, error) {
		return p.MustElement(value.Str()).MustText(), nil
	}, func(err error) { reported <- err })
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Eval(`async () => {
		const settled = [];
		try { settled.push(await lookup("#")) } catch (error) { settled.push(String(error)) }
		settled.push(await lookup("p"));
		return settled;
	}`)
	if err != nil {
		t.Fatal(err)
	}
	g.E(stop())
	g.Eq(result.Value.Join("|"), "exposed function failed|text")
	err = <-reported
	if evalErr, ok := errors.AsType[*rod.EvalError](err); !errors.Is(err, &rod.TryError{}) || !ok || !strings.Contains(evalErr.Error(), "SyntaxError") {
		t.Fatalf("reported error = %v", err)
	}
	g.Len(reported, 0)
}

// A WithPanic fail function that ends the goroutine with runtime.Goexit
// rejects the call and does not stop later calls.
func TestExposeGoexitRejectsCall(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><p>text</p>`)).Timeout(20 * time.Second)
	defer p.CancelTimeout()
	exiting := p.WithPanic(func(any) { runtime.Goexit() })
	reported := make(chan error, 1)
	stop, err := p.Expose("lookup", func(value jsonvalue.Value) (any, error) {
		return exiting.MustElement(value.Str()).MustText(), nil
	}, func(err error) { reported <- err })
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Eval(`async () => {
		const settled = [];
		try { settled.push(await lookup("#")) } catch (error) { settled.push(String(error)) }
		settled.push(await lookup("p"));
		return settled;
	}`)
	if err != nil {
		t.Fatal(err)
	}
	g.E(stop())
	g.Eq(result.Value.Join("|"), "exposed function failed|text")
	g.Is(<-reported, rod.ErrExposedFunctionExited)
	g.Len(reported, 0)
}

// Unawaited calls that panic, exit or return settle in call order, and each
// failure is reported once, in call order.
func TestExposeFailedCallsKeepOrder(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(20 * time.Second)
	defer p.CancelTimeout()
	exiting := p.WithPanic(func(any) { runtime.Goexit() })
	// The last call returns, so every failure is reported before it settles.
	const calls = 61
	var reported []error
	stop, err := p.Expose("mixed", func(value jsonvalue.Value) (any, error) {
		switch value.Int() % 3 {
		case 1:
			panic(value.Int())
		case 2:
			exiting.MustElement("#")
		}
		return value.Int(), nil
	}, func(err error) { reported = append(reported, err) })
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Eval(`async calls => {
		const settled = [];
		await Promise.all(Array.from({length: calls}, (_, index) =>
			mixed(index).then(value => settled.push(value), error => settled.push(String(error)))));
		return settled;
	}`, calls)
	if err != nil {
		t.Fatal(err)
	}
	g.E(stop())
	var want, settled []string
	for index := range calls {
		if index%3 == 0 {
			want = append(want, jsonvalue.New(index).String())
		} else {
			want = append(want, "exposed function failed")
		}
	}
	for _, value := range result.Value.Arr() {
		settled = append(settled, value.String())
	}
	if !slices.Equal(settled, want) {
		t.Fatalf("settled = %v, want %v", settled, want)
	}
	if len(reported) != calls*2/3 {
		t.Fatalf("reported %d errors: %v", len(reported), reported)
	}
	for index, err := range reported {
		call := index/2*3 + 1 + index%2
		if call%3 == 1 {
			if tryErr, ok := errors.AsType[*rod.TryError](err); !ok || tryErr.Value != call {
				t.Fatalf("error for call %d = %v", call, err)
			}
		} else if !errors.Is(err, rod.ErrExposedFunctionExited) {
			t.Fatalf("error for call %d = %v", call, err)
		}
	}
}
