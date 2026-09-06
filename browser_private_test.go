package rod

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/launcher/flags"
)

func TestImplicitBrowserProcessCleanup(t *testing.T) {
	for _, name := range []string{"close", "expired operation", "stalled close", "concurrent close", "owner cancellation"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			browser := New().Context(ctx)
			if err := browser.Connect(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(browser.process.shutdown)
			if browser.process == nil || browser.process.launcher.PID() == 0 {
				t.Fatal("implicit browser launch is not owned")
			}

			profile := browser.process.launcher.Get(flags.UserDataDir)
			if _, err := os.Stat(profile); err != nil {
				t.Fatalf("implicit browser profile was not created: %v", err)
			}
			switch name {
			case "close":
				if err := browser.Close(); err != nil {
					t.Fatal(err)
				}
			case "expired operation":
				operation, stop := context.WithCancel(ctx)
				stop()
				if err := browser.Context(operation).Close(); err != nil {
					t.Fatal(err)
				}
			case "stalled close":
				browser.client = &browserLifecycleClient{call: func(ctx context.Context, _ string) ([]byte, error) {
					// A transport write can block without observing cancellation.
					// Only actual process cleanup releases this simulated write.
					<-browser.process.done
					return nil, ctx.Err()
				}}
				if err := browser.Close(); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("stalled close returned %v", err)
				}
			case "concurrent close":
				var wg sync.WaitGroup
				for range 3 {
					wg.Go(func() { _ = browser.Close() })
				}
				wg.Wait()
			case "owner cancellation":
				cancel()
				select {
				case <-browser.process.done:
				case <-time.After(10 * time.Second):
					t.Fatal("canceled owner left its browser running")
				}
			}
			if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("implicit browser profile was not removed: %v", err)
			}
		})
	}
}

func TestOwnedBrowserCloseExpiredContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &browserLifecycleClient{call: func(ctx context.Context, method string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("cleanup reused canceled operation context: %v", err)
		}
		if method != "Browser.close" {
			t.Fatalf("unexpected cleanup request: %s", method)
		}
		return nil, nil
	}}
	browser := New().Context(ctx).Client(client)
	browser.process = stoppedBrowserProcess()
	if err := browser.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestImplicitBrowserConnectPanicCleanup(t *testing.T) {
	browser := New().Monitor("invalid address")
	func() {
		defer func() {
			if recover() == nil {
				t.Error("invalid monitor address did not panic")
			}
		}()
		if err := browser.Connect(); err != nil {
			t.Fatal(err)
		}
	}()
	t.Cleanup(browser.process.shutdown)
	if browser.process == nil {
		t.Fatal("monitor initialization did not reach implicit launch")
	}
	select {
	case <-browser.process.done:
	default:
		t.Fatal("failed connection left its browser running")
	}
	profile := browser.process.launcher.Get(flags.UserDataDir)
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed connection left its profile: %v", err)
	}
}

func TestOwnedBrowserCloseTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &browserLifecycleClient{call: func(ctx context.Context, _ string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		browser := New().Client(client)
		browser.process = stoppedBrowserProcess()
		start := time.Now()
		if err := browser.Close(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stalled close returned %v", err)
		}
		if elapsed := time.Since(start); elapsed != 5*time.Second {
			t.Fatalf("stalled close took %s", elapsed)
		}
	})
}

func TestAttachedBrowserCloseContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &browserLifecycleClient{call: func(ctx context.Context, _ string) ([]byte, error) {
		return nil, ctx.Err()
	}}
	browser := New().Context(ctx).Client(client)
	if err := browser.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("attached browser close returned %v", err)
	}

	browser.process = &localBrowserProcess{done: make(chan struct{})}
	browser.BrowserContextID = "incognito"
	if err := browser.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("incognito close returned %v", err)
	}
}

// These context tests represent a process that has already exited. Real process
// ownership and profile removal are checked by TestImplicitBrowserProcessCleanup.
func stoppedBrowserProcess() *localBrowserProcess {
	process := &localBrowserProcess{done: make(chan struct{})}
	process.stopOnce.Do(func() {})
	close(process.done)
	return process
}

type browserLifecycleClient struct {
	call func(context.Context, string) ([]byte, error)
}

func (client *browserLifecycleClient) Call(ctx context.Context, _, method string, _ any) ([]byte, error) {
	return client.call(ctx, method)
}

func (*browserLifecycleClient) Event() <-chan *cdp.Event { return nil }
