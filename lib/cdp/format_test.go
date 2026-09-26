package cdp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rah-0/rod/lib/utils"
)

func TestFormatSessionID(t *testing.T) {
	for id, want := range map[string]string{
		"":                 "@00000000",
		"a":                "@a",
		"0123456":          "@0123456",
		"01234567":         "@01234567",
		"0123456789abcdef": "@01234567",
	} {
		if got := fSessionID(id); got != want {
			t.Errorf("fSessionID(%q) = %q, want %q", id, got, want)
		}
	}
	if got := (Request{ID: 1, SessionID: "short", Method: "Page.enable"}).String(); got != "=> #1 @short Page.enable null" {
		t.Errorf("request = %q", got)
	}
}

func TestClientLogsShortSessionID(t *testing.T) {
	ws := &protocolSocket{incoming: make(chan []byte, 1), closed: make(chan struct{})}
	logged := make(chan string, 1)
	// The reader goroutine formats events. fmt recovers a String panic into a
	// corrupted line; loggers calling String directly would crash the process.
	client := New().Logger(utils.Log(func(args ...any) {
		logged <- fmt.Sprint(args...)
	})).Start(ws)
	t.Cleanup(func() { close(ws.incoming) })
	defer client.Close()
	ws.incoming <- []byte(`{"sessionId":"s1","method":"Target.detachedFromTarget","params":{}}`)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case event := <-client.Event():
		if event.SessionID != "s1" {
			t.Fatalf("event session = %q", event.SessionID)
		}
	case <-ctx.Done():
		t.Fatal("event was not delivered")
	}
	if got := <-logged; got != "<- @s1 Target.detachedFromTarget {}" {
		t.Fatalf("logged %q", got)
	}
}
