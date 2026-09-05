package testutil

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAssertions(t *testing.T) {
	g := New(t)
	type namedString string

	g.Eq(1, 1.0)
	g.Eq(namedString("value"), "value")
	g.Eq([]int{1, 2}, []int{1, 2})
	g.Neq(1, 2)
	g.Gt(2, 1)
	g.Gte(2, 2.0)
	g.Lt(time.Second, 2*time.Second)
	g.Lte("a", "a")
	g.InDelta(1.1, 1.0, 0.11)
	g.True(true)
	g.False(false)

	var nilPointer *int
	g.Nil(nilPointer)
	g.NotNil(new(int))
	g.Zero(0)
	g.Regex(`^val`, "value")
	g.Has("value", "alu")
	g.Has([]int{1, 2}, 2)
	g.Has(map[string]int{"answer": 42}, 42)
	g.Len([]int{1, 2}, 2)

	sentinel := errors.New("sentinel")
	g.Err(sentinel)
	g.E(nil)
	g.Is(fmt.Errorf("wrapped: %w", sentinel), sentinel)
	g.Is([]int{}, []string{})

	if value := g.Panic(func() { panic("value") }); value != "value" {
		t.Fatalf("unexpected panic value: %#v", value)
	}

	called := g.Count(2)
	called()
	called()
}

func TestAssertionFailures(t *testing.T) {
	recorder := &recordingTest{name: t.Name()}
	g := New(recorder)

	g.Eq(1, 2)
	if !recorder.failed {
		t.Fatal("Eq did not fail")
	}

	recorder.failed = false
	g.Nil(1)
	if !recorder.failed {
		t.Fatal("Nil did not fail")
	}

	recorder.failed = false
	g.E(errors.New("failure"))
	if !recorder.failed {
		t.Fatal("E did not fail")
	}

	recorder.failed = false
	g.Panic(func() {})
	if !recorder.failed {
		t.Fatal("Panic did not fail")
	}
}

func TestLifecycleHelpers(t *testing.T) {
	initialized := false
	setup := Setup(func(g G) {
		initialized = true
		g.Helper()
	})
	g := setup(t)
	if !initialized || T(t).Testable != t {
		t.Fatal("constructors did not retain the test")
	}

	ctx := g.Context()
	ctx.Cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Context was not canceled")
	}

	timed := g.Timeout(0)
	select {
	case <-timed.Done():
	case <-time.After(time.Second):
		t.Fatal("Timeout did not expire")
	}

	fired := make(chan struct{})
	g.DoAfter(time.Millisecond, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("DoAfter did not run")
	}

	random := g.RandStr(16)
	if len(random) != 16 {
		t.Fatalf("unexpected random string length: %d", len(random))
	}
	if _, err := hex.DecodeString(random); err != nil {
		t.Fatalf("random string is not hexadecimal: %v", err)
	}

	if !g.Run("child", func(child G) { child.True(true) }) {
		t.Fatal("subtest failed")
	}
}

func TestParallelFromArgs(t *testing.T) {
	tests := []struct {
		args []string
		want int
	}{
		{nil, 0},
		{[]string{"-test.parallel=7"}, 7},
		{[]string{"--test.parallel", "8"}, 8},
		{[]string{"-test.parallel=bad"}, 0},
		{[]string{"-test.parallel=2", "-test.parallel=3"}, 3},
	}

	for _, test := range tests {
		if got := parallelFromArgs(test.args); got != test.want {
			t.Errorf("parallelFromArgs(%q) = %d, want %d", test.args, got, test.want)
		}
	}
}

func TestGParallel(t *testing.T) {
	New(t).Parallel()
}

func TestHTTPHelpers(t *testing.T) {
	g := New(t)
	router := g.Serve()
	router.Route("/text", ".txt", "hello")
	router.Route("/json", ".json", map[string]int{"answer": 42})

	headers := http.Header{
		"Host":   {"example.test"},
		"X-Test": {"value"},
	}
	router.Mux.HandleFunc("/request", func(w http.ResponseWriter, request *http.Request) {
		if request.Host != "example.test" || request.Header.Get("X-Test") != "value" {
			http.Error(w, "unexpected headers", http.StatusBadRequest)
			return
		}
		if request.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "unexpected content type", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	})

	response := g.Req(http.MethodPost, router.URL("/request"), headers, ReqMIME(".json"), map[string]int{"a": 1})
	if response.StatusCode != http.StatusCreated || response.String() != "created" {
		t.Fatalf("unexpected response: status=%d body=%q", response.StatusCode, response.String())
	}
	if headers.Get("Host") != "example.test" {
		t.Fatal("Req mutated its caller's headers")
	}

	if got := g.Req("", router.URL("/text")).String(); got != "hello" {
		t.Fatalf("unexpected text response: %q", got)
	}
	jsonValue := g.Req("", router.URL("/json")).JSON().(map[string]any)
	if jsonValue["answer"] != float64(42) {
		t.Fatalf("unexpected JSON response: %#v", jsonValue)
	}

	file := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	router.Route("/file", file)
	if got := g.Req("", router.URL("/file")).String(); got != "fixture" {
		t.Fatalf("unexpected file response: %q", got)
	}

	requestError := g.Req(http.MethodGet, "://bad")
	if requestError.Err() == nil {
		t.Fatal("Req did not preserve the request construction error")
	}
	encodeError := g.Req(http.MethodPost, router.URL(), make(chan struct{}))
	if encodeError.Err() == nil {
		t.Fatal("Req did not preserve the body encoding error")
	}
}

func TestResponseBodyCanBeReadRepeatedly(t *testing.T) {
	g := New(t)
	router := g.Serve().Route("/", "", strings.NewReader("body"))
	response := g.Req("", router.URL())
	if first, second := response.String(), response.String(); first != "body" || second != first {
		t.Fatalf("body reads differ: %q and %q", first, second)
	}
}

func TestContextImplementsContext(t *testing.T) {
	var _ context.Context = New(t).Context()
}

type recordingTest struct {
	name     string
	failed   bool
	skipped  bool
	logs     []string
	cleanups []func()
}

func (test *recordingTest) Name() string { return test.name }

func (test *recordingTest) Skipped() bool { return test.skipped }

func (test *recordingTest) Failed() bool { return test.failed }

func (test *recordingTest) Cleanup(cleanup func()) {
	test.cleanups = append(test.cleanups, cleanup)
}

func (test *recordingTest) FailNow() { test.failed = true }

func (test *recordingTest) Fail() { test.failed = true }

func (test *recordingTest) Helper() {}

func (test *recordingTest) Logf(format string, args ...any) {
	test.logs = append(test.logs, fmt.Sprintf(format, args...))
}

func (test *recordingTest) SkipNow() { test.skipped = true }

type normalSuite struct {
	G
	calls *atomic.Int64
}

func (suite normalSuite) Alpha() { suite.calls.Add(1) }

func (suite normalSuite) Beta(_ int) { suite.calls.Add(1) }

type onlySuite struct {
	G
	calls *atomic.Int64
}

func (suite onlySuite) Regular() { suite.calls.Add(100) }

func (suite onlySuite) Selected(_ Only) { suite.calls.Add(1) }

type skipSuite struct {
	G
	called *atomic.Bool
}

func (suite skipSuite) Omitted(_ Skip) { suite.called.Store(true) }

func TestEach(t *testing.T) {
	var normalCalls atomic.Int64
	if count := Each(t, normalSuite{calls: &normalCalls}); count != 2 || normalCalls.Load() != 2 {
		t.Fatalf("normal suite: count=%d calls=%d", count, normalCalls.Load())
	}

	var factoryCalls atomic.Int64
	if count := Each(t, func(child Testable) normalSuite {
		return normalSuite{G: New(child), calls: &factoryCalls}
	}); count != 2 || factoryCalls.Load() != 2 {
		t.Fatalf("factory suite: count=%d calls=%d", count, factoryCalls.Load())
	}

	var onlyCalls atomic.Int64
	if count := Each(t, onlySuite{calls: &onlyCalls}); count != 1 || onlyCalls.Load() != 1 {
		t.Fatalf("only suite: count=%d calls=%d", count, onlyCalls.Load())
	}

	var skippedCall atomic.Bool
	if count := Each(t, skipSuite{called: &skippedCall}); count != 1 || skippedCall.Load() {
		t.Fatalf("skip suite: count=%d called=%t", count, skippedCall.Load())
	}
}
