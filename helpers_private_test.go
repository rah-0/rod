package rod

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"runtime"
	"slices"
	"sync"
	"testing"
	"weak"

	"github.com/rah-0/rod/internal/goroutines"
	"github.com/rah-0/rod/internal/observable"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/proto"
)

// TestMain fails the run when the package tests leave goroutines running.
func TestMain(m *testing.M) {
	defaults.Load()
	if code := m.Run(); code != 0 {
		os.Exit(code)
	}
	if err := goroutines.Check(0, goroutines.Functions("internal/poll.runtime_pollWait")); err != nil {
		log.Fatal(err)
	}
}

type eventTestCall struct {
	session string
	method  string
}

// eventTestClient records the commands it receives and answers each with an
// empty result. It sends no events.
type eventTestClient struct {
	mu    sync.Mutex
	calls []eventTestCall
}

func (client *eventTestClient) Call(ctx context.Context, session, method string, _ any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client.mu.Lock()
	client.calls = append(client.calls, eventTestCall{session, method})
	client.mu.Unlock()
	return []byte("{}"), nil
}

func (*eventTestClient) Event() <-chan *cdp.Event { return nil }

func (client *eventTestClient) snapshot() []eventTestCall {
	client.mu.Lock()
	defer client.mu.Unlock()
	return slices.Clone(client.calls)
}

// newEventTestBrowser returns an unconnected browser that sends its commands to
// a new eventTestClient. Tests publish its events directly.
func newEventTestBrowser(t *testing.T) (*Browser, *eventTestClient) {
	t.Helper()
	client := new(eventTestClient)
	browser := New().Context(t.Context()).Client(client)
	browser.event = observable.New[*Message](t.Context())
	return browser, client
}

// eventTestMessage returns a message with the default strict decoding.
func eventTestMessage(method string, session proto.TargetSessionID, data string) *Message {
	return &Message{Method: method, SessionID: session, data: json.RawMessage(data)}
}

// publishUnused publishes an event that the subscriptions under test do not use.
// The returned weak pointer is cleared once no subscription queue retains it.
func publishUnused(browser *Browser, method string, session proto.TargetSessionID) weak.Pointer[Message] {
	msg := eventTestMessage(method, session, `{}`)
	browser.event.Publish(msg)
	return weak.Make(msg)
}

// requireReleased fails when a subscription still retains an event published
// by publishUnused.
func requireReleased(t *testing.T, events ...weak.Pointer[Message]) {
	t.Helper()
	runtime.GC()
	for _, event := range events {
		if event.Value() != nil {
			t.Fatal("an internal subscription retained an event it does not use")
		}
	}
}

// sessionTestClient records each command and answers it with call when set.
// Without call, it attaches a target to the session "<target>-session". It
// delivers the events that a test sends to events.
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

// lifecycleRegressionClient answers each command with call.
type lifecycleRegressionClient struct {
	call func(context.Context, string, any) ([]byte, error)
}

func (c *lifecycleRegressionClient) Call(ctx context.Context, _ string, method string, args any) ([]byte, error) {
	return c.call(ctx, method, args)
}

func (*lifecycleRegressionClient) Event() <-chan *cdp.Event { return nil }

// resolutionClient answers CDP calls with a function and records their order.
type resolutionClient struct {
	sync.Mutex
	calls   []string
	respond func(method string, params any) ([]byte, error)
}

func (c *resolutionClient) Call(_ context.Context, _, method string, params any) ([]byte, error) {
	c.Lock()
	c.calls = append(c.calls, method)
	c.Unlock()
	return c.respond(method, params)
}

func (*resolutionClient) Event() <-chan *cdp.Event { return nil }

var errOtherContextForTest = &cdp.Error{
	Code: -32000, Message: "Argument should belong to the same JavaScript world as target object",
}

// errMalformedWindowForTest marks a case whose endpoint returns a window without a handle.
var errMalformedWindowForTest = errors.New("malformed window")

// isContextCheck reports whether a request tests which context owns an argument.
func isContextCheck(params any) (proto.RuntimeCallFunctionOn, bool) {
	req, ok := params.(proto.RuntimeCallFunctionOn)
	return req, ok && req.FunctionDeclaration == `function() {}` && len(req.Arguments) == 1
}

// resolutionPage returns a page whose helper cache knows the given contexts.
func resolutionPage(t *testing.T, client *resolutionClient, contexts ...proto.RuntimeRemoteObjectID) *Page {
	t.Helper()
	page := New().Context(t.Context()).Client(client).PageFromSession("session")
	page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{}
	for _, id := range contexts {
		page.helpers.contexts[id] = map[string]proto.RuntimeRemoteObjectID{}
	}
	return page
}
