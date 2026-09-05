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

	err = cdp.ws.Send(data)
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

// Event returns a channel that will emit browser devtools protocol events. Must be consumed or will block producer.
func (cdp *Client) Event() <-chan *Event {
	return cdp.event
}

// Consume messages coming from the browser via the websocket.
func (cdp *Client) consumeMessages() {
	var readErr error
	defer func() {
		cdp.readErr = readErr
		close(cdp.done)
		close(cdp.event)
		if closer, ok := cdp.ws.(io.Closer); ok {
			_ = closer.Close()
		}
	}()

	for {
		data, err := cdp.ws.Read()
		if err != nil {
			readErr = err
			return
		}

		var id struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(data, &id); err != nil {
			readErr = fmt.Errorf("decode CDP message: %w", err)
			return
		}

		if id.ID == 0 {
			var evt Event
			if err := json.Unmarshal(data, &evt); err != nil {
				readErr = fmt.Errorf("decode CDP event: %w", err)
				return
			}
			cdp.logger.Println(&evt)
			cdp.event <- &evt
			continue
		}

		var res Response
		if err := json.Unmarshal(data, &res); err != nil {
			readErr = fmt.Errorf("decode CDP response: %w", err)
			return
		}

		cdp.logger.Println(&res)

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
