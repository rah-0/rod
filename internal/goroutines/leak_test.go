package goroutines

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func blockForTest(started chan<- struct{}, done <-chan struct{}) {
	close(started)
	<-done
}

func TestWaitReturnsLiveTraceAtDeadline(t *testing.T) {
	baseline := Current()
	started := make(chan struct{})
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		blockForTest(started, done)
	}()
	<-started
	t.Cleanup(func() {
		close(done)
		<-finished
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	remaining := Wait(ctx, baseline)
	if !containsFunction(remaining, ".blockForTest") {
		t.Fatalf("blocked goroutine missing from diagnostics:\n%s", remaining)
	}
}

func TestCheckIncludesStackDiagnostics(t *testing.T) {
	baseline := Current()
	started := make(chan struct{})
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		blockForTest(started, done)
	}()
	<-started
	t.Cleanup(func() {
		close(done)
		<-finished
	})

	err := Check(25*time.Millisecond, baseline)
	if err == nil {
		t.Fatal("Check did not report a blocked goroutine")
	}
	if !strings.Contains(err.Error(), "leaking goroutines:") ||
		!strings.Contains(err.Error(), ".blockForTest") {
		t.Fatalf("Check error lacks stack diagnostics:\n%s", err)
	}

	if err := Check(time.Millisecond, func(*Trace) bool { return true }); err != nil {
		t.Fatalf("Check with all traces ignored: %v", err)
	}
}

func TestCheckLeak(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		fake := new(fakeTest)
		CheckLeak(fake, time.Millisecond, func(*Trace) bool { return true })
		fake.runCleanup()
		if fake.failed || len(fake.logs) != 0 {
			t.Fatalf("clean check failed: %#v", fake)
		}
	})

	t.Run("leak", func(t *testing.T) {
		baseline := Current()
		fake := new(fakeTest)
		CheckLeak(fake, 25*time.Millisecond, baseline)

		started := make(chan struct{})
		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			blockForTest(started, done)
		}()
		<-started
		fake.runCleanup()
		close(done)
		<-finished

		if !fake.failed {
			t.Fatal("leak check did not fail the test")
		}
		if len(fake.logs) != 1 || !strings.Contains(fake.logs[0], ".blockForTest") {
			t.Fatalf("leak check logs = %#v", fake.logs)
		}
	})

	t.Run("already failed", func(t *testing.T) {
		fake := &fakeTest{failed: true}
		CheckLeak(fake, time.Millisecond, func(*Trace) bool { return false })
		fake.runCleanup()
		if len(fake.logs) != 0 {
			t.Fatalf("failed test produced leak diagnostics: %#v", fake.logs)
		}
	})
}

func containsFunction(traces Traces, suffix string) bool {
	for _, trace := range traces {
		for _, stack := range trace.Stacks {
			if strings.HasSuffix(stack.Func, suffix) {
				return true
			}
		}
	}
	return false
}

type fakeTest struct {
	cleanup func()
	failed  bool
	helpers int
	logs    []string
}

func (f *fakeTest) Helper() {
	f.helpers++
}

func (f *fakeTest) Fail() {
	f.failed = true
}

func (f *fakeTest) Failed() bool {
	return f.failed
}

func (f *fakeTest) Cleanup(cleanup func()) {
	f.cleanup = cleanup
}

func (f *fakeTest) Logf(format string, args ...interface{}) {
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func (f *fakeTest) runCleanup() {
	if f.cleanup != nil {
		f.cleanup()
	}
}
