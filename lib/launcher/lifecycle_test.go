package launcher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func cleanupPromptly(t *testing.T, l *Launcher) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		l.Kill()
		l.Cleanup()
		l.Cleanup()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("launcher cleanup did not finish")
	}
}

func TestCleanupWithoutProcess(t *testing.T) {
	t.Run("before launch", func(t *testing.T) {
		cleanupPromptly(t, New())
	})
	t.Run("failed start", func(t *testing.T) {
		l := New().Bin(filepath.Join(t.TempDir(), "missing-browser")).Preferences(`{}`)
		if _, err := l.Launch(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing executable error = %v, want os.ErrNotExist", err)
		}
		cleanupPromptly(t, l)
		if _, err := os.Stat(l.Get("user-data-dir")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed launch left an owned profile: %v", err)
		}
		if _, err := l.Launch(); !errors.Is(err, ErrAlreadyLaunched) {
			t.Fatalf("second launch = %v, want ErrAlreadyLaunched", err)
		}
		cleanupPromptly(t, l)
	})
	t.Run("canceled launch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		l := New().Context(ctx)
		if _, err := l.Launch(); !errors.Is(err, context.Canceled) {
			t.Fatalf("launch = %v, want cancellation", err)
		}
		cleanupPromptly(t, l)
	})
	t.Run("preserve caller profile on error", func(t *testing.T) {
		dir := t.TempDir()
		sentinel := filepath.Join(dir, "keep")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		l := New().UserDataDir(dir).Bin(filepath.Join(dir, "missing-browser"))
		if _, err := l.Launch(); err == nil {
			t.Fatal("missing executable unexpectedly launched")
		}
		cleanupPromptly(t, l)
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("caller profile was changed: %v", err)
		}
	})
}

func TestCleanupReusedBrowser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/devtools/browser/test"}`, r.Host)
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprintf("configured=%t", configured), func(t *testing.T) {
			l := New().Bin("unused-browser").RemoteDebuggingPort(port).Preferences(`{}`)
			sentinel := ""
			if configured {
				dir := t.TempDir()
				l.UserDataDir(dir)
				sentinel = filepath.Join(dir, "keep")
				if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := l.Launch(); err != nil {
				t.Fatal(err)
			}
			if l.PID() != 0 {
				t.Fatal("reuse unexpectedly owns a process")
			}
			cleanupPromptly(t, l)
			if _, err := ResolveURL(t.Context(), server.URL); err != nil {
				t.Fatalf("cleanup stopped the reused browser: %v", err)
			}
			if configured {
				if _, err := os.Stat(sentinel); err != nil {
					t.Fatalf("cleanup removed caller profile: %v", err)
				}
			} else if _, err := os.Stat(l.Get("user-data-dir")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("reuse created an unnecessary profile: %v", err)
			}
		})
	}
}

func TestURLParserBoundedOutput(t *testing.T) {
	p := NewURLParser()
	for range 10 {
		_, _ = p.Write([]byte(strings.Repeat("x", maxBrowserOutput/2)))
	}
	if len(p.Buffer) != maxBrowserOutput {
		t.Fatalf("startup output size = %d, want %d", len(p.Buffer), maxBrowserOutput)
	}
	// Output parsing must finish even if cancellation or process exit wins over
	// receiving the URL in Launch. In particular, Cmd.Wait must not get stuck.
	done := make(chan struct{})
	go func() {
		_, _ = p.Write([]byte("\nDevTools listening on ws://127.0.0.1:9222/devtools/browser/test\n"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("URL parser blocked without a receiver")
	}
	if got := <-p.URL; got != "http://127.0.0.1:9222" {
		t.Fatalf("URL = %q", got)
	}
	if p.Buffer != "" {
		t.Fatal("startup output retained after DevTools was found")
	}
}
