package cdp

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/rah-0/rod/lib/utils"
)

type incomingMessageTest struct {
	name         string
	message      string
	wantEvent    *Event
	wantResponse *Response
	wantError    string
}

func TestClientIncomingMessageKinds(t *testing.T) {
	tests := []incomingMessageTest{
		{
			name:      "event",
			message:   `{"method":"Page.loadEventFired","sessionId":"session","params":{"timestamp":42}}`,
			wantEvent: &Event{Method: "Page.loadEventFired", SessionID: "session", Params: json.RawMessage(`{"timestamp":42}`)},
		},
		{
			name:         "response",
			message:      `{"id":1,"result":{"value":42}}`,
			wantResponse: &Response{ID: 1, Result: json.RawMessage(`{"value":42}`)},
		},
		{
			name:         "protocol error",
			message:      `{"id":1,"error":{"code":-1,"message":"failed","data":"detail"}}`,
			wantResponse: &Response{ID: 1, Error: &Error{Code: -1, Message: "failed", Data: "detail"}},
		},
		{
			name:      "event ignores response fields",
			message:   `{"error":42,"method":"Page.loadEventFired","result":{"unused":true},"params":{}}`,
			wantEvent: &Event{Method: "Page.loadEventFired", Params: json.RawMessage(`{}`)},
		},
		{
			name:         "response ignores event fields",
			message:      `{"method":42,"sessionId":false,"params":{"unused":true},"id":1,"result":true}`,
			wantResponse: &Response{ID: 1, Result: json.RawMessage(`true`)},
		},
		{
			name:      "duplicate null event fields",
			message:   `{"method":"Page.loadEventFired","method":null,"sessionId":"session","sessionId":null,"params":{}}`,
			wantEvent: &Event{Method: "Page.loadEventFired", SessionID: "session", Params: json.RawMessage(`{}`)},
		},
		{
			name:         "duplicate null response ID",
			message:      `{"id":1,"id":null,"result":true}`,
			wantResponse: &Response{ID: 1, Result: json.RawMessage(`true`)},
		},
		{
			name:      "invalid ID after irrelevant field",
			message:   `{"method":42,"id":"bad"}`,
			wantError: "decode CDP message:",
		},
		{
			name:      "invalid event after irrelevant field",
			message:   `{"error":42,"method":42}`,
			wantError: "decode CDP event:",
		},
		{
			name:      "invalid response after irrelevant field",
			message:   `{"method":42,"id":1,"error":"bad"}`,
			wantError: "decode CDP response:",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ws := &protocolSocket{incoming: make(chan []byte, 1), closed: make(chan struct{})}
			ws.incoming <- []byte(test.message)
			close(ws.incoming)
			client := New().Logger(utils.LoggerQuiet)
			response := make(chan result, 1)
			client.pending[1] = response
			client.Start(ws)
			var events []*Event
			for event := range client.Event() {
				events = append(events, event)
			}
			<-ws.closed
			if test.wantError != "" {
				if client.readErr == nil || !strings.HasPrefix(client.readErr.Error(), test.wantError) {
					t.Fatalf("read error = %v, want prefix %q", client.readErr, test.wantError)
				}
				if _, ok := errors.AsType[*json.UnmarshalTypeError](client.readErr); !ok {
					t.Fatalf("read error = %v, want wrapped JSON type error", client.readErr)
				}
			} else if !errors.Is(client.readErr, io.EOF) {
				t.Fatalf("read error = %v, want EOF", client.readErr)
			}
			if test.wantEvent != nil {
				if len(events) != 1 || !reflect.DeepEqual(events[0], test.wantEvent) {
					t.Fatalf("events = %+v, want %+v", events, test.wantEvent)
				}
			} else if len(events) != 0 {
				t.Fatalf("unexpected events: %+v", events)
			}
			if test.wantResponse != nil {
				select {
				case got := <-response:
					if !reflect.DeepEqual(got.msg, test.wantResponse.Result) {
						t.Fatalf("result = %s, want %s", got.msg, test.wantResponse.Result)
					}
					if test.wantResponse.Error == nil {
						if got.err != nil {
							t.Fatalf("response error = %v", got.err)
						}
					} else if !errors.Is(got.err, test.wantResponse.Error) {
						t.Fatalf("response error = %v, want %v", got.err, test.wantResponse.Error)
					}
				default:
					t.Fatal("response was not delivered")
				}
			} else if len(response) != 0 {
				t.Fatal("unexpected response")
			}
		})
	}
}
