// Package cdp for application layer communication with browser.
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/utils"
)

// Request to send to browser.
type Request struct {
	ID        int    `json:"id"`
	SessionID string `json:"sessionId,omitempty"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
}

// Response from browser.
type Response struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Event from browser.
type Event struct {
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// incomingMessage decodes the shared envelope in one pass. Only the fields for
// the selected message kind are delivered to callers.
type incomingMessage struct {
	Response
	Event
}

type messageID struct {
	ID int `json:"id"`
}

func (msg *incomingMessage) decode(data []byte) error {
	if err := json.Unmarshal(data, msg); err == nil {
		return nil
	}

	// Keep the protocol's existing treatment of fields belonging to the other
	// message kind: an event ignores response fields and vice versa. Retrying
	// separately also preserves the original decoding error and its context.
	var id messageID
	if err := json.Unmarshal(data, &id); err != nil {
		return fmt.Errorf("decode CDP message: %w", err)
	}
	*msg = incomingMessage{}
	if id.ID == 0 {
		if err := json.Unmarshal(data, &msg.Event); err != nil {
			return fmt.Errorf("decode CDP event: %w", err)
		}
	} else if err := json.Unmarshal(data, &msg.Response); err != nil {
		return fmt.Errorf("decode CDP response: %w", err)
	}
	return nil
}

// WebSocketable enables you to choose the websocket lib you want to use.
// Such as you can easily wrap gorilla/websocket and use it as the transport layer.
type WebSocketable interface {
	// Send text message only
	Send(data []byte) error
	// Read returns text message only
	Read() ([]byte, error)
}

// Client is a devtools protocol connection instance.
type Client struct {
	count atomic.Uint64

	ws WebSocketable

	pendingMu sync.Mutex
	pending   map[int]chan result
	event     chan *Event // events from browser
	done      chan struct{}
	readErr   error // published by closing done
	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error

	logger utils.Logger
}

// New creates a cdp connection, all messages from Client.Event must be received or they will block the client.
func New() *Client {
	defaults.Load()

	return &Client{
		pending: make(map[int]chan result),
		event:   make(chan *Event),
		done:    make(chan struct{}),
		logger:  defaults.CDP,
	}
}

// Logger sets the logger to log all the requests, responses, and events transferred between Rod and the browser.
// The default format for each type is in file format.go.
func (cdp *Client) Logger(l utils.Logger) *Client {
	cdp.logger = l
	return cdp
}

// Start to browser.
func (cdp *Client) Start(ws WebSocketable) *Client {
	cdp.ws = ws

	go cdp.consumeMessages()

	return cdp
}

type result struct {
	msg json.RawMessage
	err error
}

// Call a method and wait for its response.
func (cdp *Client) Call(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
	req := &Request{
		ID:        int(cdp.count.Add(1)),
		SessionID: sessionID,
		Method:    method,
		Params:    params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal CDP request: %w", err)
	}
	cdp.logger.Println(req)

	select {
	case <-cdp.done:
		return nil, cdp.readErr
	default:
	}

	done := make(chan result, 1)
	cdp.pendingMu.Lock()
	cdp.pending[req.ID] = done
	cdp.pendingMu.Unlock()
	defer func() {
		cdp.pendingMu.Lock()
		delete(cdp.pending, req.ID)
		cdp.pendingMu.Unlock()
	}()

	if sender, ok := cdp.ws.(interface {
		SendContext(context.Context, []byte) error
	}); ok {
		err = sender.SendContext(ctx, data)
	} else {
		err = cdp.ws.Send(data)
	}
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-cdp.done:
		// The reader may have delivered this response before reaching EOF while
		// Send was still returning. Preserve that response over the terminal error.
		select {
		case res := <-done:
			return res.msg, res.err
		default:
			return nil, cdp.readErr
		}
	case res := <-done:
		return res.msg, res.err
	}
}

// Close releases the underlying transport, interrupting pending requests and
// blocked writes. Custom transports must implement io.Closer for this operation.
func (cdp *Client) Close() error {
	cdp.terminate(ErrClientClosed)
	return cdp.closeTransport()
}

func (cdp *Client) terminate(err error) {
	cdp.stopOnce.Do(func() {
		cdp.readErr = err
		close(cdp.done)
	})
}

func (cdp *Client) closeTransport() error {
	if cdp.ws == nil {
		return nil
	}
	cdp.closeOnce.Do(func() {
		if closer, ok := cdp.ws.(io.Closer); ok {
			cdp.closeErr = closer.Close()
		} else {
			cdp.closeErr = ErrTransportNotClosable
		}
	})
	return cdp.closeErr
}

// Event returns a channel that will emit browser devtools protocol events. Must be consumed or will block producer.
func (cdp *Client) Event() <-chan *Event {
	return cdp.event
}

// Consume messages coming from the browser via the websocket.
func (cdp *Client) consumeMessages() {
	var readErr error
	defer func() {
		cdp.terminate(readErr)
		close(cdp.event)
		_ = cdp.closeTransport()
	}()

	for {
		select {
		case <-cdp.done:
			return
		default:
		}
		data, err := cdp.ws.Read()
		if err != nil {
			readErr = err
			return
		}

		var msg incomingMessage
		if err := msg.decode(data); err != nil {
			readErr = err
			return
		}

		if msg.ID == 0 {
			evt := &msg.Event
			cdp.logger.Println(evt)
			select {
			case cdp.event <- evt:
			case <-cdp.done:
				return
			}
			continue
		}

		res := &msg.Response
		cdp.logger.Println(res)

		cdp.pendingMu.Lock()
		done := cdp.pending[res.ID]
		delete(cdp.pending, res.ID)
		cdp.pendingMu.Unlock()
		if done == nil {
			continue
		}
		if res.Error == nil {
			done <- result{res.Result, nil}
		} else {
			done <- result{nil, res.Error}
		}
	}
}
