package rod_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

// instrumentDOMStable counts connected mutation observers and pending timeouts
// created after it runs, keeps weak references to the observers, and records
// the window's own property names.
const instrumentDOMStable = `() => {
	const probe = { observers: 0, timers: new Set(), peak: 0, refs: [] };
	window.domStableProbe = probe;
	const Native = MutationObserver;
	window.MutationObserver = class extends Native {
		constructor(...args) {
			super(...args);
			probe.refs.push(new WeakRef(this));
		}
		observe(...args) {
			if (!this.counted) {
				this.counted = true;
				probe.peak = Math.max(probe.peak, ++probe.observers);
			}
			return super.observe(...args);
		}
		disconnect() {
			if (this.counted) {
				this.counted = false;
				probe.observers--;
			}
			return super.disconnect();
		}
	};
	const setTimeoutNative = window.setTimeout;
	const clearTimeoutNative = window.clearTimeout;
	window.setTimeout = function (fn, delay, ...args) {
		const id = setTimeoutNative(function () {
			probe.timers.delete(id);
			return fn.apply(this, arguments);
		}, delay, ...args);
		probe.timers.add(id);
		return id;
	};
	window.clearTimeout = function (id) {
		probe.timers.delete(id);
		return clearTimeoutNative(id);
	};
	probe.globals = Object.getOwnPropertyNames(window).join();
}`

// assertDOMStableCleanup checks that no observer, timer, or global remains.
func assertDOMStableCleanup(t *testing.T, p *rod.Page) {
	t.Helper()
	// A canceled wait removes its observer after returning.
	if err := p.Wait(rod.Eval(`() => domStableProbe.observers === 0`)); err != nil {
		t.Fatalf("waiting for the observer to disconnect: %v", err)
	}
	state := p.MustEval(`() => ({
		observers: domStableProbe.observers,
		timers: domStableProbe.timers.size,
		globals: Object.getOwnPropertyNames(window).join() === domStableProbe.globals,
	})`)
	if state.Get("observers").Int() != 0 || state.Get("timers").Int() != 0 || !state.Get("globals").Bool() {
		t.Fatalf("DOM stability wait left page state behind: %s", state.JSON("", ""))
	}
}

const mutatingDOM = `<!doctype html><body><p id="text">0</p><script>
	let n = 0;
	setInterval(() => document.getElementById('text').textContent = String(++n), 5);
</script>`

func TestWaitDOMStableQuietPeriod(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><body><p id="text"></p>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustEval(`() => {
		const text = document.getElementById('text');
		let n = 0;
		const timer = setInterval(() => {
			text.textContent = String(++n);
			window.lastChange = performance.now();
		}, 5);
		setTimeout(() => clearInterval(timer), 300);
	}`)
	g.E(p.WaitDOMStable(150*time.Millisecond, 0))
	quiet := p.MustEval(`() => performance.now() - window.lastChange`).Num()
	if quiet < 150 {
		t.Fatalf("wait returned %.1f ms after the last change", quiet)
	}
}

func TestWaitDOMStableProtocolTraffic(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><body><p id="text"></p>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustEval(`() => {
		for (let i = 0; i < 5000; i++) document.body.append(document.createElement('span'));
		const text = document.getElementById('text');
		let n = 0;
		const timer = setInterval(() => text.textContent = String(++n), 5);
		setTimeout(() => clearInterval(timer), 800);
	}`)
	var calls, received atomic.Int64
	var snapshot atomic.Bool
	g.mc.setCall(func(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
		calls.Add(1)
		if strings.HasPrefix(method, "DOMSnapshot.") {
			snapshot.Store(true)
		}
		res, err := g.mc.principal.Call(ctx, sessionID, method, params)
		received.Add(int64(len(res)))
		return res, err
	})
	defer g.mc.resetCall()
	g.E(p.WaitDOMStable(50*time.Millisecond, 0))
	// Installing the helper, creating, starting, and releasing the waiter take
	// a few calls, plus one small check per stability period while the DOM
	// changes. Neither depends on the DOM size.
	if snapshot.Load() || calls.Load() > 8+800/40 || received.Load() > 16<<10 {
		t.Fatalf("wait used %d protocol calls with %d response bytes, DOM snapshots: %v",
			calls.Load(), received.Load(), snapshot.Load())
	}
}

func TestWaitDOMStableDiff(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><body>` + strings.Repeat("<div>item</div>", 500) + `<p id="text">0</p><script>
		let n = 0;
		setInterval(() => document.getElementById('text').textContent = String(++n), 5);
	</script>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()

	for _, diff := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		if err := p.WaitDOMStable(time.Millisecond, diff); err == nil {
			t.Fatalf("diff %v accepted", diff)
		}
	}

	// About 1000 nodes: one changed text node per period is within 5%.
	g.E(p.WaitDOMStable(100*time.Millisecond, 0.05))

	limited := p.Timeout(400 * time.Millisecond)
	if err := limited.WaitDOMStable(100*time.Millisecond, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("continuous changes satisfied diff 0: %v", err)
	}
	limited.CancelTimeout()

	// Replacing half the nodes every 20 ms exceeds 5%.
	p.MustEval(`() => setInterval(() => {
		const items = document.querySelectorAll('div');
		for (let i = 0; i < items.length; i += 2) items[i].replaceWith(items[i].cloneNode(true));
	}, 20)`)
	limited = p.Timeout(400 * time.Millisecond)
	if err := limited.WaitDOMStable(100*time.Millisecond, 0.05); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("large changes satisfied diff 0.05: %v", err)
	}
	limited.CancelTimeout()
}

func TestWaitDOMStableIgnoresUnchangedValues(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><body><p id="text" class="same">text</p>`)).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	p.MustEval(`() => setInterval(() => {
		const text = document.getElementById('text');
		text.setAttribute('class', 'same');
		text.firstChild.data = 'text';
	}, 5)`)
	g.E(p.WaitDOMStable(100*time.Millisecond, 0))
}

func TestWaitDOMStableCleanup(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(mutatingDOM)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustEval(instrumentDOMStable)

	ctx, cancel := context.WithCancel(p.GetContext())
	done := make(chan error, 1)
	go func() { done <- p.Context(ctx).WaitDOMStable(time.Hour, 0) }()
	g.E(p.Wait(rod.Eval(`() => domStableProbe.observers === 1`)))
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait: %v", err)
	}
	assertDOMStableCleanup(t, p)

	limited := p.Timeout(300 * time.Millisecond)
	if err := limited.WaitDOMStable(time.Hour, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired wait: %v", err)
	}
	limited.CancelTimeout()
	assertDOMStableCleanup(t, p)

	// Cancellation after the page creates the waiter, before its handle
	// reaches the caller, leaves nothing to clean up.
	ctx, cancel = context.WithCancel(p.GetContext())
	g.mc.setCall(func(callCtx context.Context, sessionID, method string, params any) ([]byte, error) {
		if isHelperCall(params, "waitDOMStable") {
			g.mc.resetCall()
			if _, err := g.mc.principal.Call(callCtx, sessionID, method, params); err != nil {
				return nil, err
			}
			cancel()
			return nil, context.Canceled
		}
		return g.mc.principal.Call(callCtx, sessionID, method, params)
	})
	if err := p.Context(ctx).WaitDOMStable(time.Hour, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait canceled during creation: %v", err)
	}
	assertDOMStableCleanup(t, p)

	g.E(p.Navigate(g.html(`<!doctype html><body><p>quiet</p>`)))
	p.MustEval(instrumentDOMStable)
	g.E(p.WaitDOMStable(50*time.Millisecond, 0.5))
	assertDOMStableCleanup(t, p)
}

func TestWaitDOMStableConcurrent(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><body><p id="text"></p>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustEval(instrumentDOMStable)
	p.MustEval(`() => {
		const text = document.getElementById('text');
		let n = 0;
		const timer = setInterval(() => text.textContent = String(++n), 5);
		window.stop = () => clearInterval(timer);
	}`)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { errs[i] = p.WaitDOMStable(time.Duration(50+25*i)*time.Millisecond, 0) })
	}
	g.E(p.Wait(rod.Eval(`() => domStableProbe.observers === 4`)))
	p.MustEval(`() => window.stop()`)
	wg.Wait()
	for _, err := range errs {
		g.E(err)
	}
	assertDOMStableCleanup(t, p)
}

// startDOMStableWait starts a wait on an instrumented page and returns after the
// page observes the document.
func startDOMStableWait(g G, p *rod.Page, d time.Duration) <-chan error {
	g.Helper()
	p.MustEval(instrumentDOMStable)
	done := make(chan error, 1)
	go func() { done <- p.WaitDOMStable(d, 0) }()
	g.E(p.Wait(rod.Eval(`() => domStableProbe.observers === 1`)))
	return done
}

func TestWaitDOMStableNavigation(t *testing.T) {
	g := setup(t)
	router := g.Serve()
	router.Route("/first", ".html", `<!doctype html><body><p>first</p>`)
	router.Route("/second", ".html", `<!doctype html><body><p id="text"></p><script>
		const text = document.getElementById('text');
		let n = 0;
		window.lastChange = performance.now();
		const timer = setInterval(() => {
			text.textContent = String(++n);
			window.lastChange = performance.now();
		}, 5);
		setTimeout(() => clearInterval(timer), 400);
	</script>`)
	p := g.newPage(router.URL("/first")).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()
	done := startDOMStableWait(g, p, 300*time.Millisecond)
	p.MustEval(`() => location.href = '/second'`)
	g.E(<-done)
	if !strings.HasSuffix(p.MustInfo().URL, "/second") {
		t.Fatalf("wait ended before navigation: %s", p.MustInfo().URL)
	}
	if quiet := p.MustEval(`() => performance.now() - window.lastChange`).Num(); quiet < 300 {
		t.Fatalf("wait returned %.1f ms after the last change in the new document", quiet)
	}
}

func TestWaitDOMStableBackForwardCache(t *testing.T) {
	g := setup(t)
	router := g.Serve()
	router.Route("/first", ".html", `<!doctype html><body><p>first</p><script>
		window.restored = false;
		addEventListener('pageshow', e => { if (e.persisted) window.restored = true });
	</script>`)
	router.Route("/second", ".html", `<!doctype html><body><p>second</p><script>
		setTimeout(() => history.back(), 50);
	</script>`)
	p := g.newPage(router.URL("/first")).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()
	done := startDOMStableWait(g, p, 300*time.Millisecond)
	p.MustEval(`() => location.href = '/second'`)
	g.E(<-done)
	if !strings.HasSuffix(p.MustInfo().URL, "/first") {
		t.Fatalf("wait ended before returning to the first document: %s", p.MustInfo().URL)
	}
	if !p.MustEval(`() => window.restored`).Bool() {
		t.Skip("the browser did not restore the document from the back/forward cache")
	}
	// The wait in the cached document settled when the document was hidden.
	assertDOMStableCleanup(t, p)
}

func TestWaitDOMStableShadowRoots(t *testing.T) {
	g := setup(t)
	// expectChanging runs change after the wait observes the page, then expects
	// continuous changes inside shadow roots to prevent stability.
	expectChanging := func(name, content, change string) {
		t.Helper()
		p := g.newPage(g.html(content)).Timeout(10 * time.Second)
		defer p.CancelTimeout()
		p.MustEval(instrumentDOMStable)
		limited := p.Timeout(600 * time.Millisecond)
		defer limited.CancelTimeout()
		done := make(chan error, 1)
		go func() { done <- limited.WaitDOMStable(150*time.Millisecond, 0) }()
		g.E(p.Wait(rod.Eval(`() => domStableProbe.observers === 1`)))
		if change != "" {
			p.MustEval(change)
		}
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: changes were not observed: %v", name, err)
		}
		assertDOMStableCleanup(t, p)
	}
	const component = `<script>
		class Ticker extends HTMLElement {
			constructor() {
				super();
				const inner = document.createElement('div');
				this.attachShadow({ mode: 'open' }).append(inner);
				const text = inner.attachShadow({ mode: 'open' });
				let n = 0;
				setInterval(() => text.textContent = String(++n), 5);
			}
		}
	</script>`

	expectChanging("existing nested shadow roots", `<!doctype html><body><div id="host"></div><script>
		const inner = document.createElement('div');
		document.getElementById('host').attachShadow({ mode: 'open' }).append(inner);
		const text = inner.attachShadow({ mode: 'open' });
		let n = 0;
		setInterval(() => text.textContent = String(++n), 5);
	</script>`, "")
	expectChanging("inserted custom element", `<!doctype html><body>`+component+`<script>
		customElements.define('x-ticker', Ticker);
	</script>`, `() => document.body.append(document.createElement('x-ticker'))`)
	expectChanging("upgraded custom element", `<!doctype html><body><x-ticker></x-ticker>`+component,
		`() => customElements.define('x-ticker', Ticker)`)
	expectChanging("shadow root attached to an element in place", `<!doctype html><body><div id="host"></div>`,
		`() => setTimeout(() => {
			const text = document.getElementById('host').attachShadow({ mode: 'open' });
			let n = 0;
			setInterval(() => text.textContent = String(++n), 5);
		}, 50)`)

	// Closed shadow roots are not observed.
	p := g.newPage(g.html(`<!doctype html><body><div id="host"></div><script>
		const text = document.getElementById('host').attachShadow({ mode: 'closed' });
		let n = 0;
		setInterval(() => text.textContent = String(++n), 5);
	</script>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	g.E(p.WaitDOMStable(100*time.Millisecond, 0))
}

func TestWaitDOMStableFrames(t *testing.T) {
	g := setup(t)
	router := g.Serve()
	router.Route("/frame", ".html", mutatingDOM)
	router.Route("/", ".html", `<!doctype html><body><iframe src="/frame"></iframe>`)
	p := g.newPage(router.URL("/")).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	frame := p.MustElement("iframe").MustFrame()
	frame.MustElement("#text")

	// The page's wait does not observe iframe documents.
	g.E(p.WaitDOMStable(100*time.Millisecond, 0))

	limited := frame.Timeout(400 * time.Millisecond)
	defer limited.CancelTimeout()
	if err := limited.WaitDOMStable(100*time.Millisecond, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("frame changes were not observed: %v", err)
	}
}

// isDOMStableStart reports whether a protocol call starts a DOM stability wait
// in the page.
func isDOMStableStart(params any) bool {
	call, ok := params.(proto.RuntimeCallFunctionOn)
	return ok && strings.Contains(call.FunctionDeclaration, "this.start()")
}

// isHelperCall reports whether a protocol call runs the named JavaScript helper.
func isHelperCall(params any, name string) bool {
	call, ok := params.(proto.RuntimeCallFunctionOn)
	return ok && strings.Contains(call.FunctionDeclaration, "/* "+name+" */")
}

// stubDOMStableStart replaces the next call that starts a DOM stability wait in
// the page. send performs the original call.
func (g G) stubDOMStableStart(fn func(send StubSend) (jsonvalue.Value, error)) {
	g.mc.setCall(func(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
		if isDOMStableStart(params) {
			g.mc.resetCall()
			value, err := fn(func() (jsonvalue.Value, error) {
				data, err := g.mc.principal.Call(ctx, sessionID, method, params)
				return jsonvalue.New(data), err
			})
			if err != nil {
				return nil, err
			}
			return value.MarshalJSON()
		}
		return g.mc.principal.Call(ctx, sessionID, method, params)
	})
}

// stubDOMStableStartErr makes the next DOM stability wait fail to start.
func (g G) stubDOMStableStartErr() {
	g.stubDOMStableStart(func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(nil), errors.New("mock error")
	})
}

func TestWaitDOMStableErrors(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	p.MustEval(instrumentDOMStable)

	injected := errors.New("start failed")
	g.stubDOMStableStart(func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(nil), injected
	})
	if err := p.WaitDOMStable(time.Millisecond, 0); !errors.Is(err, injected) {
		t.Fatalf("start error: %v", err)
	}

	g.mc.stubErr(1, proto.RuntimeReleaseObject{})
	if err := p.WaitDOMStable(time.Millisecond, 0); err == nil {
		t.Fatal("cleanup error was lost")
	}
	assertDOMStableCleanup(t, p)

	p.MustEval(`() => window.MutationObserver = function () { throw new Error('observer unavailable') }`)
	var evalErr *rod.EvalError
	if err := p.WaitDOMStable(time.Millisecond, 0); !errors.As(err, &evalErr) {
		t.Fatalf("page exception: %v", err)
	}
}

// isDOMStableCheck reports whether a protocol call checks a running DOM
// stability wait in the page.
func isDOMStableCheck(params any) bool {
	call, ok := params.(proto.RuntimeCallFunctionOn)
	return ok && strings.Contains(call.FunctionDeclaration, "this.check()")
}

func TestWaitDOMStableRejectsMissingResults(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	p.MustEval(instrumentDOMStable)

	// empty makes the next matching call return a response without a result.
	empty := func(match func(params any) bool) {
		g.mc.setCall(func(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
			if match(params) {
				g.mc.resetCall()
				return []byte(`{}`), nil
			}
			return g.mc.principal.Call(ctx, sessionID, method, params)
		})
	}
	for name, match := range map[string]func(any) bool{
		"create": func(params any) bool { return isHelperCall(params, "waitDOMStable") },
		"start":  isDOMStableStart,
		"check":  isDOMStableCheck,
	} {
		empty(match)
		if err := p.WaitDOMStable(time.Millisecond, 0); err == nil {
			t.Fatalf("%s: a response without a result was accepted", name)
		}
		g.mc.resetCall()
		assertDOMStableCleanup(t, p)
	}
}

func TestWaitDOMStableWithoutPageScripts(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><body><p id="text">text</p>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()
	g.E(proto.EmulationSetScriptExecutionDisabled{Value: true}.Call(p))
	defer func() { g.E(proto.EmulationSetScriptExecutionDisabled{Value: false}.Call(p)) }()

	// The stability period does not depend on page timers or event loops.
	limited := p.Timeout(3 * time.Second)
	defer limited.CancelTimeout()
	g.E(limited.WaitDOMStable(100*time.Millisecond, 0))
	g.E(limited.WaitStable(100 * time.Millisecond))

	// Changes are still observed.
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- limited.WaitDOMStable(300*time.Millisecond, 0) }()
	time.Sleep(150 * time.Millisecond)
	p.MustEval(`() => document.getElementById('text').textContent = 'changed'`)
	g.E(<-done)
	if elapsed := time.Since(start); elapsed < 450*time.Millisecond {
		t.Fatalf("wait returned %v after starting, before a period without changes", elapsed)
	}
}

func TestWaitDOMStableCancelsBlockedPage(t *testing.T) {
	g := setup(t)
	for _, block := range []struct {
		name, js string
		dialog   bool
	}{
		{"dialog", `() => setTimeout(() => alert('blocked'))`, true},
		{"long task", `() => setTimeout(() => {
			const end = performance.now() + 1000;
			while (performance.now() < end);
		})`, false},
	} {
		p := g.newPage(g.html(mutatingDOM)).Timeout(10 * time.Second)
		p.MustEval(instrumentDOMStable)
		ctx, cancel := context.WithCancel(p.GetContext())
		done := make(chan error, 1)
		go func() { done <- p.Context(ctx).WaitDOMStable(50*time.Millisecond, 0) }()
		g.E(p.Wait(rod.Eval(`() => domStableProbe.observers === 1`)))

		var handle func(*proto.PageHandleJavaScriptDialog) error
		if block.dialog {
			var wait func() (*proto.PageJavascriptDialogOpening, error)
			wait, handle = p.HandleDialog()
			p.MustEval(block.js)
			_, err := wait()
			g.E(err)
		} else {
			p.MustEval(block.js)
		}
		// Let the wait reach the blocked page.
		time.Sleep(200 * time.Millisecond)

		start := time.Now()
		cancel()
		err := <-done
		elapsed := time.Since(start)
		if !errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || elapsed > 500*time.Millisecond {
			t.Fatalf("%s: wait returned %v after cancellation: %v", block.name, elapsed, err)
		}
		if handle != nil {
			g.E(handle(&proto.PageHandleJavaScriptDialog{Accept: true}))
		}
		// Cleanup completes once the page responds.
		assertDOMStableCleanup(t, p)
		p.CancelTimeout()
	}
}

func TestWaitDOMStableIgnoresTransientChanges(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><body><p id="text" class="a">text</p>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	// Each run inserts and removes a probe element and restores the class and
	// text it changes.
	p.MustEval(`() => setInterval(() => {
		const probe = document.createElement('div');
		probe.append(document.createElement('span'));
		document.body.append(probe);
		probe.getBoundingClientRect();
		probe.remove();
		const text = document.getElementById('text');
		text.classList.add('b');
		text.classList.remove('b');
		text.firstChild.data = 'changed';
		text.firstChild.data = 'text';
	}, 20)`)
	for _, diff := range []float64{0, 0.01} {
		limited := p.Timeout(2 * time.Second)
		g.E(limited.WaitDOMStable(200*time.Millisecond, diff))
		limited.CancelTimeout()
	}
}

func TestWaitDOMStableReleasesPageState(t *testing.T) {
	g := setup(t)
	// Custom elements that are never defined must not keep finished waits alive.
	p := g.newPage(g.html(`<!doctype html><body><x-never-a></x-never-a><x-never-b><span></span></x-never-b>`)).
		Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustEval(instrumentDOMStable)
	for i := range 20 {
		g.E(p.WaitDOMStable(time.Millisecond, float64(i%2)/2))
	}
	g.E(proto.HeapProfilerCollectGarbage{}.Call(p))
	state := p.MustEval(`() => ({
		created: domStableProbe.refs.length,
		reachable: domStableProbe.refs.filter(ref => ref.deref()).length,
	})`)
	if state.Get("created").Int() != 20 || state.Get("reachable").Int() != 0 {
		t.Fatalf("finished waits remain reachable in the page: %s", state.JSON("", ""))
	}
	assertDOMStableCleanup(t, p)
}

func TestWaitDOMStablePageResults(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second)
	defer p.CancelTimeout()

	// check makes the next check of a running wait return value.
	check := func(value any) {
		remoteType := "number"
		switch value.(type) {
		case string:
			remoteType = "string"
		case bool:
			remoteType = "boolean"
		}
		g.mc.setCall(func(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
			if isDOMStableCheck(params) {
				g.mc.resetCall()
				return jsonvalue.New(map[string]any{"result": map[string]any{"type": remoteType, "value": value}}).MarshalJSON()
			}
			return g.mc.principal.Call(ctx, sessionID, method, params)
		})
	}

	// A waiter that ended itself because a check was overdue starts again.
	check("expired")
	g.E(p.WaitDOMStable(time.Millisecond, 0))

	for _, value := range []any{"failed", 0, -1, math.MaxFloat64, true} {
		check(value)
		if err := p.WaitDOMStable(time.Millisecond, 0); err == nil {
			t.Fatalf("page result %v accepted", value)
		}
		g.mc.resetCall()
	}
}
