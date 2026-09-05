package goroutines

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultCheckTimeout = 3 * time.Second
	maximumPollDelay    = 300 * time.Millisecond
)

// Wait waits for non-ignored goroutines to exit until ctx is done. It returns
// the traces that remain at that point. Unlike the runtime goroutineleak profile,
// this also reports reachable goroutines that outlive their expected lifecycle.
// Stack ancestry remains available to isolate work owned by an individual test.
func Wait(ctx context.Context, ignores ...Ignore) (remaining Traces) {
	if ctx == nil {
		ctx = context.Background()
	}

	ignore := Combine(ignores...)
	delay := time.Microsecond
	for {
		traces := Snapshot(true)
		if len(traces) != 0 {
			traces = traces[1:]
		}
		remaining = traces.Filter(ignore)
		if len(remaining) == 0 {
			return remaining
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return remaining
		case <-timer.C:
		}

		if delay < maximumPollDelay {
			delay *= 2
			if delay > maximumPollDelay {
				delay = maximumPollDelay
			}
		}
	}
}

// Check reports goroutines that remain after timeout. A non-positive timeout
// uses three seconds.
func Check(timeout time.Duration, ignores ...Ignore) error {
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if traces := Wait(ctx, ignores...); traces.Any() {
		return fmt.Errorf("leaking goroutines: %s", traces)
	}
	return nil
}

// Test is the testing functionality required by CheckLeak.
type Test interface {
	Helper()
	Fail()
	Failed() bool
	Cleanup(func())
	Logf(format string, args ...any)
}

// CheckLeak registers a per-test goroutine leak check. With no explicit ignore
// rule, goroutines outside the test's ancestry are ignored.
func CheckLeak(test Test, timeout time.Duration, ignores ...Ignore) {
	test.Helper()

	if len(ignores) == 0 {
		ignores = []Ignore{NonChildren()}
	}

	test.Cleanup(func() {
		test.Helper()
		if test.Failed() {
			return
		}

		if err := Check(timeout, ignores...); err != nil {
			test.Logf("%v", err)
			test.Fail()
		}
	})
}
