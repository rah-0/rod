package cdp

import (
	"bufio"
	"bytes"
	"testing"
)

func TestWebSocketReadMessageOwnership(t *testing.T) {
	frames := serverFrame(1, true, []byte("first"))
	frames = append(frames, serverFrame(1, false, nil)...)
	frames = append(frames, serverFrame(0, false, []byte("sec"))...)
	frames = append(frames, serverFrame(10, true, []byte("ignored"))...)
	frames = append(frames, serverFrame(0, true, []byte("ond"))...)
	frames = append(frames, serverFrame(1, true, []byte("third"))...)
	ws := &WebSocket{r: bufio.NewReader(bytes.NewReader(frames))}
	var messages [][]byte
	for range 3 {
		message, err := ws.Read()
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	for i, want := range []string{"first", "second", "third"} {
		if string(messages[i]) != want {
			t.Fatalf("message %d after later reads = %q, want %q", i, messages[i], want)
		}
	}
	messages[0][0] = 'F'
	messages[1][0] = 'S'
	if string(messages[2]) != "third" {
		t.Fatalf("mutating earlier results changed the final result to %q", messages[2])
	}
}
