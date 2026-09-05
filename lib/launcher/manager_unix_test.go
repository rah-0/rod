//go:build !windows

package launcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerLaunchStopsWhenRequestIsCanceled(t *testing.T) {
	temp := t.TempDir()
	started := filepath.Join(temp, "started")
	helper := filepath.Join(temp, "hung-browser")
	script := "#!/bin/sh\n: > \"$ROD_MANAGER_TEST_STARTED\"\nwhile :; do sleep 60; done\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	m := NewManager(managerTestToken)
	m.userDataRoot = filepath.Join(temp, "profiles")
	profile := make(chan string, 1)
	m.BeforeLaunch = func(l *Launcher, _ http.ResponseWriter, _ *http.Request) {
		profile <- l.Get("user-data-dir")
		l.Bin(helper).Env(append(os.Environ(), "ROD_MANAGER_TEST_STARTED="+started)...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req := httptest.NewRequest("GET", "http://manager/", nil).WithContext(ctx)
	req.Header.Set(HeaderName, string(New().JSON()))

	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		m.launch(httptest.NewRecorder(), req)
	}()

	var profilePath string
	select {
	case profilePath = <-profile:
	case <-time.After(5 * time.Second):
		t.Fatal("manager did not create a profile")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case recovered := <-done:
		if recovered == nil || !strings.Contains(fmt.Sprint(recovered), context.Canceled.Error()) {
			t.Fatalf("manager launch panic = %v, want context cancellation", recovered)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("manager launch did not stop after request cancellation")
	}

	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Fatalf("managed profile was not removed: %v", err)
	}
}
