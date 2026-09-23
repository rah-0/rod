package launcher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type launcherTrackedContext struct {
	context.Context
	active atomic.Int64
}

// Hide the embedded cancellation implementation so context uses AfterFunc to
// register children, allowing the tests to account for their release.
func (ctx *launcherTrackedContext) Value(any) any { return nil }

func (ctx *launcherTrackedContext) AfterFunc(callback func()) func() bool {
	ctx.active.Add(1)
	var once sync.Once
	release := func() { once.Do(func() { ctx.active.Add(-1) }) }
	stop := context.AfterFunc(ctx.Context, func() {
		release()
		callback()
	})
	return func() bool {
		stopped := stop()
		if stopped {
			release()
		}
		return stopped
	}
}

func TestLauncherContextReplacementReleasesParent(t *testing.T) {
	ctx := &launcherTrackedContext{Context: t.Context()}
	l := New()
	t.Cleanup(func() { l.ctxCancel() })
	for range 100 {
		l.Context(ctx)
	}
	if got := ctx.active.Load(); got != 1 {
		t.Fatalf("parent retains %d cancellation registrations, want only the current context", got)
	}
	l.ctxCancel()
	if got := ctx.active.Load(); got != 0 {
		t.Fatalf("parent retains %d cancellation registrations after cancellation", got)
	}
}

func TestFailedManagedInitializationReleasesParent(t *testing.T) {
	for _, response := range []string{"unauthorized", "server error", "invalid JSON"} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				switch response {
				case "unauthorized":
					w.WriteHeader(http.StatusUnauthorized)
				case "server error":
					w.WriteHeader(http.StatusInternalServerError)
				case "invalid JSON":
					_, _ = io.WriteString(w, `{`)
				}
			}))
			defer server.Close()
			ctx := &launcherTrackedContext{Context: t.Context()}
			for range 100 {
				if _, err := NewManaged(ctx, server.URL, "test-token"); err == nil {
					t.Fatal("initialization unexpectedly succeeded")
				}
			}
			if got := ctx.active.Load(); got != 0 {
				t.Fatalf("failed initialization retained %d parent cancellation registrations", got)
			}
		})
	}
}

func TestManagedInitializationCancellation(t *testing.T) {
	for _, phase := range []string{"headers", "body", "deadline"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if phase == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 30*time.Millisecond)
				defer stop()
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if phase == "body" {
					_, _ = io.WriteString(w, `{"flags":`)
					w.(http.Flusher).Flush()
				}
				if phase != "deadline" {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			_, err := NewManaged(ctx, server.URL, "test-token")
			want := context.Canceled
			if phase == "deadline" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("initialization = %v, want %v", err, want)
			}
		})
	}
}

func TestFormatArgsPreservesProfileOwnership(t *testing.T) {
	parent := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, parent)
	if err != nil {
		t.Fatal(err)
	}
	previous := DefaultUserDataDirPrefix
	DefaultUserDataDirPrefix = relative
	t.Cleanup(func() { DefaultUserDataDirPrefix = previous })
	l := New().Preferences(`{}`).Bin(filepath.Join(parent, "missing-browser"))
	profile := l.Get("user-data-dir")
	first := l.FormatArgs()
	if !reflect.DeepEqual(first, l.FormatArgs()) || l.Get("user-data-dir") != profile {
		t.Fatal("FormatArgs mutated launch options")
	}
	if _, err := l.LaunchNew(t.Context()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated profile remains: %v", err)
	}
}

func TestFreeBSDDiscovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FreeBSD PATH lookup uses POSIX executable names")
	}
	for _, name := range []string{"chrome", "chromium", "missing"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			if name != "missing" {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			path, found := lookPath("freebsd")
			if name == "missing" {
				if found {
					t.Fatalf("unexpected executable: %s", path)
				}
				return
			}
			if !found || path != filepath.Join(dir, name) {
				t.Fatalf("discovery = %q, %t", path, found)
			}
		})
	}
}
