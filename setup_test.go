package rod_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/goroutines"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

var TimeoutEach = flag.Duration("timeout-each", time.Minute, "timeout for each test")

var testerPool rod.Pool[G]

const maxBrowserProcesses = 4

func TestMain(m *testing.M) {
	defaults.Load()
	testerPool = newTesterPool()

	code := m.Run()
	testerPool.Cleanup(func(g *G) {
		_ = g.browser.Close()
		g.launcher.Kill()
		g.launcher.Cleanup()
	})
	if code != 0 {
		os.Exit(code)
	}

	if err := goroutines.Check(0, goroutines.Functions("internal/poll.runtime_pollWait")); err != nil {
		log.Fatal(err)
	}
}

// G is a tester. Testers are thread-safe, they shouldn't race each other.
type G struct {
	testutil.G

	// mock client for proxy the cdp requests
	mc *MockClient

	// a random browser instance from the pool. If you have changed state of it, you must reset it
	// or it may affect other test cases.
	browser *rod.Browser

	// a random page instance from the pool. If you have changed state of it, you must reset it
	// or it may affect other test cases.
	page *rod.Page

	// launcher owns the local browser process and its temporary profile.
	launcher *launcher.Launcher

	// use it to cancel the TimeoutEach for current test case
	cancelTimeout func()
}

// If we don't use pool to cache, the total time will be much longer.
func newTesterPool() rod.Pool[G] {
	parallel := testutil.Parallel()
	if parallel == 0 {
		parallel = runtime.GOMAXPROCS(0)
	}
	parallel = min(parallel, maxBrowserProcesses)

	fmt.Println("parallel test", parallel)

	return rod.NewPool[G](parallel)
}

func newTester(t *testing.T) *G {
	l := launcher.New().Set("proxy-bypass-list", "<-loopback>").NoSandbox(true)
	u := l.MustLaunch()

	mc := newMockClient(t, u)

	browser := rod.New().Client(mc).MustConnect().MustIgnoreCertErrors(false)

	pages := browser.MustPages()

	var page *rod.Page
	if pages.Empty() {
		page = browser.MustPage()
	} else {
		page = pages.First()
	}

	return &G{
		mc:       mc,
		browser:  browser,
		page:     page,
		launcher: l,
	}
}

func setup(t *testing.T) G {
	t.Helper()

	if testutil.Parallel() != 1 {
		t.Parallel()
	}

	tester := testerPool.MustGet(func() *G { return newTester(t) })
	t.Cleanup(func() { testerPool.Put(tester) })

	tester.G = testutil.New(t)
	tester.mc.t = t
	tester.mc.log.SetOutput(openTestLog(t, "cdp.log"))
	t.Cleanup(func() { tester.mc.log.SetOutput(io.Discard) })

	tester.checkLeaking()

	tester.page.MustNavigate("")

	return *tester
}

func openTestLog(t *testing.T, name string) *os.File {
	t.Helper()
	path := filepath.Join(t.ArtifactDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.Close()
		if !t.Failed() {
			_ = os.Remove(path)
		}
	})
	return f
}

func (g G) enableCDPLog() {
	g.mc.principal.Logger(rod.DefaultLogger)
}

func (g G) dump(args ...any) {
	g.Log(utils.Dump(args...))
}

func (g G) blank() string {
	return g.srcFile("./fixtures/blank.html")
}

func (g G) html(content string) string {
	return g.Serve().Route("/", "", content).URL()
}

// Get abs file path from fixtures folder, such as "file:///a/b/click.html".
// Usually the path can be used for html src attribute like:
//
//	<img src="file:///a/b">
func (g G) srcFile(path string) string {
	g.Helper()
	f, err := filepath.Abs(slash(path))
	g.E(err)
	return "file://" + f
}

func (g G) newPage(u ...string) *rod.Page {
	g.Helper()
	p := g.browser.MustPage(u...)
	g.Cleanup(func() {
		if !g.Failed() {
			p.MustClose()
		}
	})
	return p
}

func (g *G) checkLeaking() {
	ig := goroutines.Combine(goroutines.Current(), goroutines.NonChildren())
	goroutines.CheckLeak(g.Testable, 0, ig)

	self := goroutines.Snapshot(false)[0]
	g.cancelTimeout = g.DoAfter(*TimeoutEach, func() {
		t := goroutines.Snapshot(true).Filter(func(t *goroutines.Trace) bool {
			if t.GoroutineID == self.GoroutineID {
				return false
			}
			return ig(t)
		}).String()
		panic(fmt.Sprintf(`[rod_test.TimeoutEach] %s timeout after %v
running goroutines: %s`, g.Name(), *TimeoutEach, t))
	})

	g.Cleanup(func() {
		if g.Failed() {
			return
		}

		g.closeExtraPages()

		if g.browser.LoadState(g.page.SessionID, &proto.FetchEnable{}) {
			g.Logf("leaking FetchEnable")
			g.FailNow()
		}

		g.mc.setCall(nil)
	})
}

func (g *G) closeExtraPages() {
	res, err := proto.TargetGetTargets{}.Call(g.browser)
	g.E(err)

	closing := map[proto.TargetTargetID]struct{}{}
	for _, info := range res.TargetInfos {
		if info.Type != proto.TargetTargetInfoTypePage || info.TargetID == g.page.TargetID {
			continue
		}

		_, err := proto.TargetCloseTarget{TargetID: info.TargetID}.Call(g.browser)
		if err != nil {
			if targetAlreadyGone(err) {
				continue
			}
			g.E(err)
		}
		closing[info.TargetID] = struct{}{}
	}

	deadline := time.Now().Add(5 * time.Second)
	for len(closing) != 0 {
		res, err := proto.TargetGetTargets{}.Call(g.browser)
		g.E(err)

		for targetID := range closing {
			found := false
			for _, info := range res.TargetInfos {
				if info.TargetID == targetID {
					found = true
					break
				}
			}
			if !found {
				delete(closing, targetID)
			}
		}

		if len(closing) == 0 {
			return
		}
		if time.Now().After(deadline) {
			g.Fatalf("timed out waiting for %d page targets to close", len(closing))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func targetAlreadyGone(err error) bool {
	protocolErr, ok := errors.AsType[*cdp.Error](err)
	return ok &&
		protocolErr.Code == -32602 &&
		protocolErr.Message == "No target with given id found"
}

type Call func(ctx context.Context, sessionID, method string, params any) ([]byte, error)

var _ rod.CDPClient = &MockClient{}

type MockClient struct {
	sync.RWMutex
	t         testutil.Testable
	log       *log.Logger
	principal *cdp.Client
	call      Call
	event     <-chan *cdp.Event
}

func newMockClient(t *testing.T, u string) *MockClient {
	logger := log.New(openTestLog(t, "cdp-init.log"), "", log.Ltime)
	client := cdp.New().Logger(utils.MultiLogger(defaults.CDP, logger)).Start(cdp.MustConnectWS(u))

	return &MockClient{principal: client, log: logger}
}

func (mc *MockClient) Event() <-chan *cdp.Event {
	if mc.event != nil {
		return mc.event
	}
	return mc.principal.Event()
}

func (mc *MockClient) Call(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
	return mc.getCall()(ctx, sessionID, method, params)
}

func (mc *MockClient) getCall() Call {
	mc.RLock()
	defer mc.RUnlock()

	if mc.call == nil {
		return mc.principal.Call
	}
	return mc.call
}

func (mc *MockClient) setCall(fn Call) {
	mc.Lock()
	defer mc.Unlock()

	if mc.call != nil {
		mc.t.Logf("leaking MockClient.stub")
		mc.t.Fail()
	}
	mc.call = fn
}

func (mc *MockClient) resetCall() {
	mc.Lock()
	defer mc.Unlock()
	mc.call = nil
}

// Use it to find out which cdp call to intercept. Put a print like log.Println("*****") after the cdp call you want to intercept.
// The output of the test should has something like:
//
//	[stubCounter] begin
//	[stubCounter] 1, proto.DOMResolveNode{}
//	[stubCounter] 1, proto.RuntimeCallFunctionOn{}
//	[stubCounter] 2, proto.RuntimeCallFunctionOn{}
//	01:49:43 *****
//
// So the 3rd call is the one we want to intercept, then you can use the output with s.at or s.errorAt.
func (mc *MockClient) stubCounter() {
	l := sync.Mutex{}
	mCount := map[string]int{}

	fmt.Fprintln(os.Stdout, "[stubCounter] begin")

	mc.setCall(func(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
		l.Lock()
		mCount[method]++
		m := fmt.Sprintf("%d, proto.%s{}", mCount[method], proto.GetType(method).Name())
		_, _ = fmt.Fprintln(os.Stdout, "[stubCounter]", m)
		l.Unlock()

		return mc.principal.Call(ctx, sessionID, method, params)
	})
}

type StubSend func() (jsonvalue.Value, error)

// When call the cdp.Client.Call the nth time use fn instead.
// Use p to filter method.
func (mc *MockClient) stub(nth int, p proto.Request, fn func(send StubSend) (jsonvalue.Value, error)) {
	if p == nil {
		mc.t.Logf("p must be specified")
		mc.t.FailNow()
	}

	var count atomic.Int64

	mc.setCall(func(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
		if method == p.ProtoReq() {
			if int(count.Add(1)) == nth {
				mc.resetCall()
				j, err := fn(func() (jsonvalue.Value, error) {
					b, err := mc.principal.Call(ctx, sessionID, method, params)
					return jsonvalue.New(b), err
				})
				if err != nil {
					return nil, err
				}
				return j.MarshalJSON()
			}
		}
		return mc.principal.Call(ctx, sessionID, method, params)
	})
}

// When call the cdp.Client.Call the nth time return error.
// Use p to filter method.
func (mc *MockClient) stubErr(nth int, p proto.Request) {
	mc.stub(nth, p, func(_ StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(nil), errors.New("mock error")
	})
}

type MockRoundTripper struct {
	res *http.Response
	err error
}

func (mrt *MockRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return mrt.res, mrt.err
}

type MockReader struct {
	err error
}

func (mr *MockReader) Read(_ []byte) (n int, err error) {
	return 0, mr.err
}

func TestLintIgnore(t *testing.T) {
	t.Skip()

	_ = rod.Try(func() {
		tt := G{}
		tt.dump()
		tt.enableCDPLog()

		mc := &MockClient{}
		mc.stubCounter()
	})
}

var slash = filepath.FromSlash
