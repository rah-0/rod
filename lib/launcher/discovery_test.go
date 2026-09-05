package launcher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveURLDiscovery(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"valid", 200, `{"webSocketDebuggerUrl":"ws://localhost:9222/devtools/browser/test"}`, ""},
		{"HTTP failure", 503, `{}`, "HTTP status 503"},
		{"malformed JSON", 200, `{`, "decode browser discovery"},
		{"invalid field type", 200, `{"webSocketDebuggerUrl":42}`, "decode browser discovery"},
		{"missing URL", 200, `{}`, "invalid browser WebSocket URL"},
		{"invalid URL", 200, `{"webSocketDebuggerUrl":"ws://%zz"}`, "parse browser WebSocket URL"},
		{"non websocket", 200, `{"webSocketDebuggerUrl":"http://localhost/"}`, "invalid browser WebSocket URL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/json/version" {
					t.Errorf("path = %q", r.URL.Path)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			got, err := ResolveURL(t.Context(), server.URL+"/ignored")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ResolveURL = %q, %v, want %q", got, err, test.wantErr)
				}
			} else if want := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/test"; err != nil || got != want {
				t.Fatalf("ResolveURL = %q, %v, want %q", got, err, want)
			}
		})
	}
}

func TestResolveURLCancellation(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if phase == "body" {
					_, _ = io.WriteString(w, `{"webSocketDebuggerUrl":`)
					w.(http.Flusher).Flush()
				}
				cancel()
				<-r.Context().Done()
			}))
			defer server.Close()
			_, err := ResolveURL(ctx, server.URL)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("ResolveURL error = %v", err)
			}
		})
	}
}

func TestResolveURLReadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, `{`)
	}))
	defer server.Close()
	_, err := ResolveURL(t.Context(), server.URL)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ResolveURL error = %v, want unexpected EOF", err)
	}
}
