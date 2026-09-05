// Package testutil provides the small set of testing helpers used by Rod.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Testable is the subset of testing.T and testing.B needed by G.
type Testable interface {
	Name() string
	Context() context.Context
	Skipped() bool
	Failed() bool
	Cleanup(func())
	FailNow()
	Fail()
	Helper()
	Logf(format string, args ...any)
	SkipNow()
}

// G groups assertions and lifecycle helpers for a test or benchmark.
type G struct {
	Testable
}

// Setup returns a constructor that optionally initializes each G.
func Setup(initialize func(G)) func(Testable) G {
	return func(t Testable) G {
		g := New(t)
		if initialize != nil {
			initialize(g)
		}
		return g
	}
}

// T is a shorthand for New.
func T(t Testable) G {
	return New(t)
}

// New constructs a helper for t.
func New(t Testable) G {
	if t == nil {
		panic("testutil: nil Testable")
	}
	return G{Testable: t}
}

// Parallel reports an explicitly configured -test.parallel value.
// It returns zero when the flag was not provided or is invalid.
func Parallel() int {
	return parallelFromArgs(os.Args[1:])
}

func parallelFromArgs(args []string) int {
	parallel := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := ""

		switch {
		case strings.HasPrefix(arg, "-test.parallel="):
			value = strings.TrimPrefix(arg, "-test.parallel=")
		case strings.HasPrefix(arg, "--test.parallel="):
			value = strings.TrimPrefix(arg, "--test.parallel=")
		case arg == "-test.parallel" || arg == "--test.parallel":
			if i+1 < len(args) {
				i++
				value = args[i]
			}
		default:
			continue
		}

		if n, err := strconv.Atoi(value); err == nil {
			parallel = n
		}
	}
	return parallel
}

// Context is canceled explicitly through Cancel or automatically at cleanup.
type Context struct {
	context.Context
	Cancel context.CancelFunc
}

// Context returns a cancelable child of the native test context.
func (g G) Context() Context {
	g.Helper()
	ctx, cancel := context.WithCancel(g.Testable.Context())
	g.Cleanup(cancel)
	return Context{Context: ctx, Cancel: cancel}
}

// Timeout returns a context with a deadline tied to the test lifecycle.
func (g G) Timeout(d time.Duration) Context {
	g.Helper()
	ctx, cancel := context.WithTimeout(g.Testable.Context(), d)
	g.Cleanup(cancel)
	return Context{Context: ctx, Cancel: cancel}
}

// DoAfter runs do after d unless the test finishes or the returned function is called.
func (g G) DoAfter(d time.Duration, do func()) context.CancelFunc {
	g.Helper()
	ctx, cancel := context.WithCancel(g.Testable.Context())
	g.Cleanup(cancel)

	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			do()
		}
	}()

	return cancel
}

// RandStr returns length hexadecimal characters sourced from crypto/rand.
func (g G) RandStr(length int) string {
	g.Helper()
	if length < 0 {
		g.Fatalf("testutil: negative random string length: %d", length)
		return ""
	}
	buf := make([]byte, (length+1)/2)
	if _, err := rand.Read(buf); err != nil {
		g.Fatalf("testutil: random bytes: %v", err)
		return ""
	}
	return hex.EncodeToString(buf)[:length]
}

// Run executes f as a subtest or sub-benchmark.
func (g G) Run(name string, f func(G)) bool {
	g.Helper()
	return run(g, name, f)
}

// Parallel marks the current test parallel and returns g.
func (g G) Parallel() G {
	g.Helper()
	t, ok := g.Testable.(interface{ Parallel() })
	if !ok {
		g.Fatalf("testutil: %T does not support parallel tests", g.Testable)
		return g
	}
	t.Parallel()
	return g
}

// Fatal logs a message and stops the current test goroutine.
func (g G) Fatal(args ...any) {
	g.Helper()
	g.Log(args...)
	g.FailNow()
}

// Fatalf logs a formatted message and stops the current test goroutine.
func (g G) Fatalf(format string, args ...any) {
	g.Helper()
	g.Logf(format, args...)
	g.FailNow()
}

// Log logs a message.
func (g G) Log(args ...any) {
	g.Helper()
	g.Logf("%s", fmt.Sprintln(args...))
}

// Error logs a message and marks the test failed.
func (g G) Error(args ...any) {
	g.Helper()
	g.Log(args...)
	g.Fail()
}

// Errorf logs a formatted message and marks the test failed.
func (g G) Errorf(format string, args ...any) {
	g.Helper()
	g.Logf(format, args...)
	g.Fail()
}

// Skip logs a message and stops the current test as skipped.
func (g G) Skip(args ...any) {
	g.Helper()
	g.Log(args...)
	g.SkipNow()
}

// Skipf logs a formatted message and stops the current test as skipped.
func (g G) Skipf(format string, args ...any) {
	g.Helper()
	g.Logf(format, args...)
	g.SkipNow()
}
