package rod_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

// The fake clients in this file let browser-free tests drive the protocol
// through the exported API: rod.Browser.Client installs a client, and
// connectTestBrowser starts the browser's event loop on the events a test sends.

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

// newEventTestBrowser returns an unconnected browser that sends its commands
// to a new eventTestClient.
func newEventTestBrowser(t *testing.T) (*rod.Browser, *eventTestClient) {
	t.Helper()
	client := new(eventTestClient)
	return rod.New().Context(t.Context()).Client(client), client
}

// connectTestBrowser connects browser to its fake client without a monitor.
// The browser then receives the events that the client delivers.
func connectTestBrowser(t *testing.T, browser *rod.Browser) *rod.Browser {
	t.Helper()
	if err := browser.Monitor("").Connect(); err != nil {
		t.Fatal(err)
	}
	return browser
}

// connectEventTestBrowser returns a browser connected to a new sessionTestClient.
func connectEventTestBrowser(t *testing.T) (*rod.Browser, *sessionTestClient) {
	t.Helper()
	client := &sessionTestClient{events: make(chan *cdp.Event)}
	return connectTestBrowser(t, rod.New().Context(t.Context()).Client(client)), client
}

// sendEvent delivers a protocol event to the browser connected to events.
func sendEvent(events chan<- *cdp.Event, method string, session proto.TargetSessionID, data string) {
	events <- &cdp.Event{SessionID: string(session), Method: method, Params: json.RawMessage(data)}
}

// eventTestMessage returns the message that a browser receives for an event.
func eventTestMessage(t *testing.T, method string, session proto.TargetSessionID, data string) *rod.Message {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &sessionTestClient{events: make(chan *cdp.Event)}
	stream := connectTestBrowser(t, rod.New().Context(ctx).Client(client)).Event()
	sendEvent(client.events, method, session, data)
	return <-stream
}

// callTestClient answers commands with call and delivers the events that a
// test sends to events.
type callTestClient struct {
	call   func(context.Context, string, any) ([]byte, error)
	events chan *cdp.Event
}

func (c *callTestClient) Call(ctx context.Context, _ string, method string, args any) ([]byte, error) {
	return c.call(ctx, method, args)
}

func (c *callTestClient) Event() <-chan *cdp.Event { return c.events }
