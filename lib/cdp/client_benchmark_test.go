package cdp

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/rah-0/rod/lib/utils"
)

type incomingMessageBenchmark struct {
	name     string
	message  string
	response bool
}

// incomingBenchmarkSocket reuses a frame to isolate client decoding and dispatch
// from network I/O and message construction. Responses use one pending request at
// a time, acknowledging its result before delivering the next frame.
type incomingBenchmarkSocket struct {
	message   []byte
	remaining int
	client    *Client
	result    chan result
	pending   bool
}

func (s *incomingBenchmarkSocket) Send([]byte) error { return io.ErrClosedPipe }
func (s *incomingBenchmarkSocket) Close() error      { return nil }

func (s *incomingBenchmarkSocket) Read() ([]byte, error) {
	if s.pending {
		<-s.result
		s.pending = false
	}
	if s.remaining == 0 {
		return nil, io.EOF
	}
	s.remaining--
	if s.client != nil {
		s.client.pending[1] = s.result
		s.pending = true
	}
	return s.message, nil
}

func BenchmarkClientIncoming(b *testing.B) {
	benchmarks := []incomingMessageBenchmark{
		{
			name:    "EventSmall",
			message: `{"sessionId":"0123456789abcdef","method":"Page.lifecycleEvent","params":{"frameId":"frame-1","loaderId":"loader-1","name":"DOMContentLoaded","timestamp":123.456}}`,
		},
		{
			name: "EventLarge",
			message: `{"sessionId":"0123456789abcdef","method":"Network.requestWillBeSent","params":{"requestId":"request-1","loaderId":"loader-1","documentURL":"https://example.test/","request":{"url":"https://example.test/upload","method":"POST","headers":{"Content-Type":"text/plain"},"postData":"` +
				strings.Repeat("request body data ", 2048) + `"},"timestamp":123.456,"wallTime":123456.789,"initiator":{"type":"script"},"type":"Fetch"}}`,
		},
		{
			name:     "ResponseSmall",
			message:  `{"id":1,"result":{"result":{"type":"number","value":42,"description":"42"}}}`,
			response: true,
		},
		{
			name: "ResponseLarge",
			message: `{"id":1,"result":{"result":[` +
				strings.Repeat(`{"name":"field","value":{"type":"string","value":"property contents"},"configurable":true,"enumerable":true},`, 320) +
				`{"name":"last","value":{"type":"number","value":42},"configurable":true,"enumerable":true}]}}`,
			response: true,
		},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			client := New().Logger(utils.LoggerQuiet)
			socket := &incomingBenchmarkSocket{
				message:   []byte(benchmark.message),
				remaining: b.N,
			}
			if benchmark.response {
				socket.client = client
				socket.result = make(chan result, 1)
			}
			b.SetBytes(int64(len(socket.message)))
			b.ReportAllocs()
			b.ResetTimer()
			if benchmark.response {
				client.ws = socket
				client.consumeMessages()
			} else {
				client.Start(socket)
				for range client.Event() {
				}
			}
			b.StopTimer()
			if !errors.Is(client.readErr, io.EOF) {
				b.Fatalf("receive messages: %v", client.readErr)
			}
		})
	}
}
