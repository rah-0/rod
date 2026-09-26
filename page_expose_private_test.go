package rod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"weak"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

type exposeTestClient struct {
	sessionTestClient
	binding string
	// deliveries receives the replies of each delivery; release unblocks it.
	deliveries chan exposeTestDelivery
	release    chan struct{}
}

// exposeTestDelivery is one reply round trip as the page receives it.
type exposeTestDelivery struct {
	context proto.RuntimeExecutionContextID
	// replies holds "callback=result" for each reply, or "callback!error".
	replies []string
}

func newExposeTestPage(t *testing.T) (*Page, *exposeTestClient) {
	t.Helper()
	client := &exposeTestClient{
		sessionTestClient: sessionTestClient{events: make(chan *cdp.Event)},
		deliveries:        make(chan exposeTestDelivery, 16),
		release:           make(chan struct{}),
	}
	client.call = func(ctx context.Context, _, method string, params any) ([]byte, error) {
		switch method {
		case "Target.attachToTarget":
			return []byte(`{"sessionId":"page-session"}`), nil
		case "Runtime.addBinding":
			client.binding = params.(proto.RuntimeAddBinding).Name
		case "Runtime.evaluate":
			return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
		case "Page.addScriptToEvaluateOnNewDocument":
			return []byte(`{"identifier":"script"}`), nil
		case "Runtime.callFunctionOn":
			if request := params.(proto.RuntimeCallFunctionOn); request.ExecutionContextID != 0 {
				delivery, err := decodeExposeDelivery(request)
				if err != nil {
					return nil, err
				}
				client.deliveries <- delivery
				select {
				case <-client.release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return []byte(`{"result":{"type":"undefined"}}`), nil
		}
		return []byte(`{}`), nil
	}
	browser := New().NoDefaultDevice().Context(t.Context()).Client(client)
	browser.initEvents()
	page, err := browser.PageFromTarget("target")
	if err != nil {
		t.Fatal(err)
	}
	return page, client
}

// decodeExposeDelivery decodes a reply request as the page function would.
func decodeExposeDelivery(request proto.RuntimeCallFunctionOn) (exposeTestDelivery, error) {
	delivery := exposeTestDelivery{context: request.ExecutionContextID}
	if request.FunctionDeclaration != exposeReplyJS || len(request.Arguments) != 1 {
		return delivery, fmt.Errorf("unexpected reply request %+v", request)
	}
	data, err := json.Marshal(request.Arguments[0].Value)
	if err != nil {
		return delivery, err
	}
	var batch [][3]json.RawMessage
	if err := json.Unmarshal(data, &batch); err != nil {
		return delivery, err
	}
	for _, reply := range batch {
		var callback string
		var failure *string
		if err := json.Unmarshal(reply[0], &callback); err != nil {
			return delivery, err
		}
		if err := json.Unmarshal(reply[2], &failure); err != nil {
			return delivery, err
		}
		if failure != nil {
			delivery.replies = append(delivery.replies, callback+"!"+*failure)
		} else {
			delivery.replies = append(delivery.replies, callback+"="+string(reply[1]))
		}
	}
	return delivery, nil
}

// invoke simulates the page calling the exposed function from execution context 5.
func (client *exposeTestClient) invoke(binding string, index int, request any) {
	client.invokeFrom(5, binding, fmt.Sprintf("%s_cb%d", binding, index), request)
}

func (client *exposeTestClient) invokeFrom(contextID proto.RuntimeExecutionContextID, binding, callback string, request any) {
	payload, _ := json.Marshal(map[string]any{"req": request, "cb": callback})
	params, _ := json.Marshal(proto.RuntimeBindingCalled{Name: binding, Payload: string(payload), ExecutionContextID: contextID})
	client.events <- &cdp.Event{SessionID: "page-session", Method: "Runtime.bindingCalled", Params: params}
}

// next returns the next delivery and lets its round trip finish.
func (client *exposeTestClient) next() exposeTestDelivery {
	delivery := <-client.deliveries
	client.release <- struct{}{}
	return delivery
}

// Calls run in page order while an earlier reply round trip is still pending,
// and the replies that queue meanwhile reach the page together, in order.
func TestExposeRepliesDoNotDelayLaterCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		var calls []int
		stop, err := page.Expose("goFn", func(value jsonvalue.Value) (any, error) {
			calls = append(calls, value.Int())
			if value.Int() == 3 {
				return nil, fmt.Errorf("call %d failed", value.Int())
			}
			return value.Int() * 10, nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(client.release)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		}()
		client.invoke(client.binding, 0, 1)
		first := <-client.deliveries // The first reply round trip is now pending.
		for index := 1; index < 3; index++ {
			client.invoke(client.binding, index, index+1)
		}
		// Undecodable and foreign binding calls are skipped without stopping the exposure.
		client.events <- &cdp.Event{SessionID: "page-session", Method: "Runtime.bindingCalled", Params: json.RawMessage(`{"name":1}`)}
		client.invoke("_other", 3, 99)
		client.invoke(client.binding, 4, 4)
		synctest.Wait()
		if !slices.Equal(calls, []int{1, 2, 3, 4}) {
			t.Fatalf("calls while the first reply is pending = %v", calls)
		}
		client.release <- struct{}{}
		cb := client.binding + "_cb"
		got := []exposeTestDelivery{first, client.next()}
		want := []exposeTestDelivery{
			{5, []string{cb + "0=10"}},
			{5, []string{cb + "1=20", cb + "2!call 3 failed", cb + "4=40"}},
		}
		synctest.Wait()
		if len(client.deliveries) != 0 {
			t.Fatalf("unexpected delivery %v", <-client.deliveries)
		}
		for i := range want {
			if got[i].context != want[i].context || !slices.Equal(got[i].replies, want[i].replies) {
				t.Fatalf("deliveries = %v, want %v", got, want)
			}
		}
	})
}

// exposePanicMarshaler panics while its value is encoded as a reply.
type exposePanicMarshaler struct{}

func (exposePanicMarshaler) MarshalJSON() ([]byte, error) { panic("marshal failed") }

// collect returns the replies of the next deliveries until it has n of them.
func (client *exposeTestClient) collect(n int) []string {
	var replies []string
	for len(replies) < n {
		replies = append(replies, client.next().replies...)
	}
	return replies
}

// A call that panics or ends its goroutine rejects only its own promise, with a
// message that carries no details, and later calls are still handled in order.
// onError receives what fn did not return, in call order.
func TestExposeRecoversFailedCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		secret := errors.New("secret panic value")
		var reported []error
		stop, err := page.Expose("goFn", func(value jsonvalue.Value) (any, error) {
			switch value.Str() {
			case "panic":
				panic(secret)
			case "index":
				return []string{}[len(value.Str())], nil
			case "goexit":
				runtime.Goexit()
			case "marshal":
				return exposePanicMarshaler{}, nil
			case "nan":
				return math.NaN(), nil
			}
			return value.Str(), nil
		}, func(err error) { reported = append(reported, err) })
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(client.release)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		}()
		requests := []string{"a", "panic", "b", "index", "goexit", "c", "marshal", "nan", "d"}
		for index, request := range requests {
			client.invoke(client.binding, index, request)
		}
		cb := client.binding + "_cb"
		got := client.collect(len(requests))
		want := []string{
			cb + `0="a"`, cb + "1!" + exposeFailure, cb + `2="b"`, cb + "3!" + exposeFailure,
			cb + "4!" + exposeFailure, cb + `5="c"`, cb + "6!" + exposeFailure, "", cb + `8="d"`,
		}
		if !strings.HasPrefix(got[7], cb+"7!encode exposed function response: ") {
			t.Fatalf("reply to an unencodable result = %q", got[7])
		}
		want[7] = got[7]
		if !slices.Equal(got, want) {
			t.Fatalf("replies = %q\nwant %q", got, want)
		}
		synctest.Wait()
		var outOfRange runtime.Error
		var unsupported *json.UnsupportedValueError
		if len(reported) != 5 ||
			!errors.Is(reported[0], &TryError{}) || !errors.Is(reported[0], secret) ||
			!errors.Is(reported[1], &TryError{}) || !errors.As(reported[1], &outOfRange) ||
			!errors.Is(reported[2], ErrExposedFunctionExited) ||
			!errors.Is(reported[3], &TryError{}) || !strings.Contains(reported[3].Error(), "marshal failed") ||
			!errors.As(reported[4], &unsupported) {
			t.Fatalf("reported errors = %v", reported)
		}
	})
}

// onError that ends its goroutine with runtime.Goexit does not stop the
// exposure.
func TestExposeOnErrorGoexit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		var reported []error
		stop, err := page.Expose("goFn", func(value jsonvalue.Value) (any, error) {
			if value.Str() == "goexit" {
				runtime.Goexit()
			}
			if value.Str() == "panic" {
				panic("call failed")
			}
			return value.Str(), nil
		}, func(err error) {
			reported = append(reported, err)
			runtime.Goexit()
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(client.release)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		}()
		for index, request := range []string{"panic", "a", "goexit", "b"} {
			client.invoke(client.binding, index, request)
		}
		cb := client.binding + "_cb"
		got := client.collect(4)
		want := []string{cb + "0!" + exposeFailure, cb + `1="a"`, cb + "2!" + exposeFailure, cb + `3="b"`}
		if !slices.Equal(got, want) {
			t.Fatalf("replies = %q\nwant %q", got, want)
		}
		synctest.Wait()
		if len(reported) != 2 || !errors.Is(reported[0], &TryError{}) || !errors.Is(reported[1], ErrExposedFunctionExited) {
			t.Fatalf("reported errors = %v", reported)
		}
	})
}

// Queued replies share a round trip only while they target the same execution
// context and fit the batch size, so the global reply order is kept.
func TestExposeReplyBatchLen(t *testing.T) {
	reply := func(contextID proto.RuntimeExecutionContextID, size int) exposeReply {
		return exposeReply{contextID: contextID, data: make(json.RawMessage, size)}
	}
	half := exposeReplyBatchBytes / 2
	for _, test := range []struct {
		name  string
		queue []exposeReply
		want  int
	}{
		{"one", []exposeReply{reply(1, 10)}, 1},
		{"same context", []exposeReply{reply(1, 10), reply(1, 10), reply(1, 10)}, 3},
		{"context change", []exposeReply{reply(1, 10), reply(1, 10), reply(2, 10), reply(1, 10)}, 2},
		{"exactly full", []exposeReply{reply(1, half), reply(1, exposeReplyBatchBytes-half-1)}, 2},
		{"over the limit", []exposeReply{reply(1, half), reply(1, exposeReplyBatchBytes-half)}, 1},
		{"large reply alone", []exposeReply{reply(1, exposeReplyBatchBytes+1), reply(1, 10)}, 1},
		{"after a large reply", []exposeReply{reply(1, 10), reply(1, exposeReplyBatchBytes)}, 1},
	} {
		if got := exposeReplyBatchLen(test.queue); got != test.want {
			t.Errorf("%s: batch length = %d, want %d", test.name, got, test.want)
		}
	}
}

// Replies for different execution contexts go in separate round trips in call
// order.
func TestExposeRepliesFollowContexts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		stop, err := page.Expose("goFn", func(value jsonvalue.Value) (any, error) {
			return value.Int(), nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(client.release)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		}()
		cb := client.binding + "_cb"
		client.invoke(client.binding, 0, 0)
		first := <-client.deliveries // The first reply round trip is now pending.
		for index, contextID := range []proto.RuntimeExecutionContextID{6, 6, 5, 5} {
			client.invokeFrom(contextID, client.binding, fmt.Sprint(cb, index+1), index+1)
		}
		synctest.Wait()
		client.release <- struct{}{}
		got := []exposeTestDelivery{first, client.next(), client.next()}
		want := []exposeTestDelivery{
			{5, []string{cb + "0=0"}},
			{6, []string{cb + "1=1", cb + "2=2"}},
			{5, []string{cb + "3=3", cb + "4=4"}},
		}
		synctest.Wait()
		if len(client.deliveries) != 0 {
			t.Fatalf("unexpected delivery %v", <-client.deliveries)
		}
		for i := range want {
			if got[i].context != want[i].context || !slices.Equal(got[i].replies, want[i].replies) {
				t.Fatalf("deliveries = %v, want %v", got, want)
			}
		}
	})
}

// Only callback names that the page-side function creates are answered, so a
// page cannot make each reply carry an arbitrary name.
func TestExposeCallbackNames(t *testing.T) {
	for callback, want := range map[string]bool{
		"_bind_cb0":                          true,
		"_bind_cb18446744073709551615":       true,
		"_bind_cb":                           false,
		"_bind_cbx":                          false,
		"_bind_cb1x":                         false,
		"_bind_cb-1":                         false,
		"_bind_cb1e+21":                      false,
		"_other_cb1":                         false,
		"_bind_cb" + strings.Repeat("1", 21): false,
	} {
		if got := exposeCallback("_bind", callback); got != want {
			t.Errorf("exposeCallback(%q) = %v, want %v", callback, got, want)
		}
	}
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		var calls []int
		stop, err := page.Expose("goFn", func(value jsonvalue.Value) (any, error) {
			calls = append(calls, value.Int())
			return nil, nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(client.release)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		}()
		client.invokeFrom(5, client.binding, client.binding+"_cb0"+strings.Repeat("\x01", 64), 1)
		client.invoke(client.binding, 1, 2)
		synctest.Wait()
		if !slices.Equal(calls, []int{2}) {
			t.Fatalf("calls = %v, want only the call with a valid callback name", calls)
		}
		if got := client.next(); !slices.Equal(got.replies, []string{client.binding + "_cb1=null"}) {
			t.Fatalf("replies = %v", got.replies)
		}
	})
}

// The exposure subscribes only to binding calls, so other page events do not
// wait in memory behind a slow callback.
func TestExposeSubscriptionSkipsUnusedEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		blocked := make(chan struct{})
		stop, err := page.Expose("goFn", func(jsonvalue.Value) (any, error) {
			<-blocked
			return nil, nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		client.invoke(client.binding, 0, nil)
		synctest.Wait()
		unused := []weak.Pointer[Message]{
			publishUnused(page.browser, "Runtime.consoleAPICalled", page.SessionID),
			publishUnused(page.browser, "Network.dataReceived", page.SessionID),
		}
		synctest.Wait() // The page forwards its own session's events.
		requireReleased(t, unused...)
		close(blocked)
		close(client.release)
		if err := stop(); err != nil {
			t.Fatal(err)
		}
	})
}

// The page function resolves the replies of a batch in order, including those
// after a page callback that throws or no longer exists.
func TestExposeReplyJS(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	browser := New().Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := browser.Close(); err != nil {
			t.Error(err)
		}
	}()
	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.Eval(`() => {
		globalThis.received = [];
		globalThis.first = (result, error) => received.push(["first", result, error]);
		globalThis.throws = result => { received.push(["throws", result]); throw new Error("page callback failed"); };
		globalThis.last = (result, error) => received.push(["last", result, error]);
	}`); err != nil {
		t.Fatal(err)
	}
	batch := []json.RawMessage{
		json.RawMessage(`["first",{"n":1},null]`),
		json.RawMessage(`["throws",2,null]`),
		json.RawMessage(`["missing",3,null]`),
		json.RawMessage(`["last",null,"call failed"]`),
	}
	if _, err := page.Evaluate(Eval(exposeReplyJS, batch)); err != nil {
		t.Fatal(err)
	}
	received, err := page.Eval(`() => received`)
	if err != nil {
		t.Fatal(err)
	}
	want := `[["first",{"n":1},null],["throws",2],["last",null,"call failed"]]`
	if got := received.Value.JSON("", ""); got != want {
		t.Fatalf("received = %s, want %s", got, want)
	}
}
