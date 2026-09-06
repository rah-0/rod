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
	"testing"
	"time"
)

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
