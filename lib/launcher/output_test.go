package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/launcher/flags"
)

func TestOutputTail(t *testing.T) {
	for _, limit := range []int{0, 1, 64, maxBrowserOutput} {
		l := New().OutputTail(limit)
		if l.Output() != "" {
			t.Fatal("new output is nonempty")
		}
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				for range 10 {
					_, _ = l.output.Write([]byte(strings.Repeat("x", 100)))
					if len(l.Output()) > limit {
						t.Error("output exceeded limit")
					}
				}
			})
		}
		wg.Wait()
		_, _ = l.output.Write([]byte(strings.Repeat("a", maxBrowserOutput) + "end"))
		expected := strings.Repeat("a", maxBrowserOutput) + "end"
		expected = expected[len(expected)-limit:]
		if actual := l.Output(); actual != expected {
			t.Fatalf("tail size %d, expected size %d", len(actual), len(expected))
		}
	}
}

func TestCleanupContextBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := New()
		if err := l.CleanupContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		l.isLaunched.Store(true)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		if err := l.CleanupContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if time.Since(start) != time.Second {
			t.Fatal("cleanup did not honor deadline")
		}
		l.cleanupErr = errors.New("fixture profile removal failed")
		close(l.exit)
		if err := l.CleanupContext(ctx); !errors.Is(err, l.cleanupErr) {
			t.Fatalf("lost cleanup error: %v", err)
		}
	})
}

func TestOwnedProfileCollision(t *testing.T) {
	dir := t.TempDir()
	l := New().Bin("unused-browser")
	// Simulate a pre-existing path at the randomly selected default location.
	l.Flags[flags.UserDataDir] = []string{dir}
	l.ownedUserDataDir = dir
	marker := filepath.Join(dir, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.LaunchNew(t.Context()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("collision = %v", err)
	}
	if err := l.CleanupContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("collision removed unrelated data: %v", err)
	}
}
