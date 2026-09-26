package rod_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/js"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

func TestPageEvalOnNewDocument(t *testing.T) {
	g := setup(t)

	p := g.newPage()

	p.MustEvalOnNewDocument(`window.rod = 'ok'`)

	// to activate the script
	p.MustNavigate(g.blank())

	g.Eq(p.MustEval("() => rod").String(), "ok")

	g.Panic(func() {
		g.mc.stubErr(1, proto.PageAddScriptToEvaluateOnNewDocument{})
		p.MustEvalOnNewDocument(`1`)
	})
}

func TestPageEval(t *testing.T) {
	g := setup(t)

	page := g.page.MustNavigate(g.blank())

	g.Eq(3, page.MustEval(`
		(a, b) => a + b
	`, 1, 2).Int())

	g.Eq(10, page.MustEval(`(a, b, c, d) => a + b + c + d`, 1, 2, 3, 4).Int())

	g.Eq(page.MustEval(`function() {
		return 11
	}`).Int(), 11)

	g.Eq(page.MustEval(`	 ; () => 1; `).Int(), 1)

	// reuse obj
	obj := page.MustEvaluate(rod.Eval(`() => () => 'ok'`).ByObject())
	g.Eq("ok", page.MustEval(`f => f()`, obj).Str())

	_, err := page.Eval(`10`)
	g.Has(err.Error(), `eval js error: TypeError: 10.apply is not a function`)

	_, err = page.Eval(`() => notExist()`)
	g.Is(err, &rod.EvalError{})
	g.Has(err.Error(), `eval js error: ReferenceError: notExist is not defined`)
}

func TestPageEvaluateRetry(t *testing.T) {
	g := setup(t)

	page := g.page.MustNavigate(g.blank())

	g.mc.stub(1, proto.RuntimeCallFunctionOn{}, func(_ StubSend) (jsonvalue.Value, error) {
		g.mc.stub(1, proto.RuntimeCallFunctionOn{}, func(_ StubSend) (jsonvalue.Value, error) {
			return jsonvalue.New(nil), cdp.ErrCtxNotFound
		})
		return jsonvalue.New(nil), cdp.ErrCtxNotFound
	})
	g.Eq(1, page.MustEval(`() => 1`).Int())
}

func TestPageUpdateJSCtxIDErr(t *testing.T) {
	g := setup(t)

	page := g.page.MustNavigate(g.srcFile("./fixtures/click-iframe.html"))

	g.mc.stub(1, proto.RuntimeCallFunctionOn{}, func(_ StubSend) (jsonvalue.Value, error) {
		g.mc.stubErr(1, proto.RuntimeEvaluate{})
		return jsonvalue.New(nil), cdp.ErrCtxNotFound
	})
	g.Err(page.Eval(`() => 1`))

	frame := page.MustElement("iframe").MustFrame()

	frame.MustReload()
	g.mc.stubErr(1, proto.DOMDescribeNode{})
	g.Err(frame.Element(`button`))

	frame.MustReload()
	g.mc.stubErr(1, proto.DOMResolveNode{})
	g.Err(frame.Element(`button`))
}

func TestPageExpose(t *testing.T) {
	g := setup(t)

	page := g.newPage(g.blank()).MustWaitLoad()

	stop := page.MustExpose("exposedFunc", func(g jsonvalue.Value) (any, error) {
		return g.Get("k").Str(), nil
	}, nil)

	utils.All(func() {
		res := page.MustEval(`() => exposedFunc({k: 'a'})`)
		g.Eq("a", res.Str())
	}, func() {
		res := page.MustEval(`() => exposedFunc({k: 'b'})`)
		g.Eq("b", res.Str())
	})()

	// survive the reload
	page.MustReload().MustWaitLoad()
	res := page.MustEval(`() => exposedFunc({k: 'ok'})`)
	g.Eq("ok", res.Str())

	stop()
	stop()
	g.Panic(func() {
		page.MustReload().MustWaitLoad().MustEval(`() => exposedFunc()`)
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		page.MustExpose("exposedFunc", nil, nil)
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeAddBinding{})
		page.MustExpose("exposedFunc2", nil, nil)
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.PageAddScriptToEvaluateOnNewDocument{})
		page.MustExpose("exposedFunc", nil, nil)
	})
}

func TestObjectRelease(t *testing.T) {
	g := setup(t)

	res, err := g.page.Evaluate(rod.Eval(`() => document`).ByObject())
	g.E(err)
	g.page.MustRelease(res)
}

func TestPromiseLeak(t *testing.T) {
	g := setup(t)

	/*
		Perform a slow action then navigate the page to another url,
		we can see the slow operation will still be executed.
	*/

	p := g.page.MustNavigate(g.blank())

	utils.All(func() {
		_, err := p.Eval(`() => new Promise(r => setTimeout(() => r(location.href), 1000))`)
		g.Is(err, cdp.ErrCtxDestroyed)
	}, func() {
		utils.Sleep(0.3)
		p.MustNavigate(g.blank())
	})()
}

func TestObjectLeak(t *testing.T) {
	g := setup(t)

	/*
		Seems like it won't leak
	*/

	p := g.page.MustNavigate(g.blank())

	obj := p.MustEvaluate(rod.Eval("() => ({a:1})").ByObject())
	p.MustReload().MustWaitLoad()
	g.Panic(func() {
		p.MustEvaluate(rod.Eval(`obj => obj`, obj))
	})
}

func TestPageObjectErr(t *testing.T) {
	g := setup(t)

	g.Panic(func() {
		g.page.MustObjectToJSON(&proto.RuntimeRemoteObject{
			ObjectID: "not-exists",
		})
	})
	g.Panic(func() {
		g.page.MustElementFromNode(&proto.DOMNode{NodeID: -1})
	})
	g.Panic(func() {
		node := g.page.MustNavigate(g.blank()).MustElement(`body`).MustDescribe()
		g.mc.stubErr(1, proto.DOMResolveNode{})
		g.page.MustElementFromNode(node)
	})
}

func TestGetJSHelperRetry(t *testing.T) {
	g := setup(t)

	g.page.MustNavigate(g.srcFile("fixtures/click.html"))

	g.mc.stub(1, proto.RuntimeCallFunctionOn{}, func(_ StubSend) (jsonvalue.Value, error) {
		return jsonvalue.Value{}, cdp.ErrCtxNotFound
	})
	g.page.MustElements("button")
}

func TestConcurrentEval(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.blank())
	list := make(chan int, 2)

	start := time.Now()
	utils.All(func() {
		list <- p.MustEval(`() => new Promise(r => setTimeout(r, 2500, 2))`).Int()
	}, func() {
		list <- p.MustEval(`() => new Promise(r => setTimeout(r, 1500, 1))`).Int()
	})()
	duration := time.Since(start)

	g.Gt(duration, 1500*time.Millisecond)
	g.Lt(duration, 3000*time.Millisecond)
	g.Eq([]int{<-list, <-list}, []int{1, 2})
}

func TestPageSlowRender(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("./fixtures/slow-render.html"))
	g.Eq(p.MustElement("div").MustText(), "ok")
}

func TestPageIframeReload(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("./fixtures/click-iframe.html"))
	frame := p.MustElement("iframe").MustFrame()
	btn := frame.MustElement("button")
	g.Eq(btn.MustText(), "click me")

	frame.MustReload()
	btn = frame.MustElement("button")
	g.Eq(btn.MustText(), "click me")

	g.Has(*p.MustElement("iframe").MustAttribute("src"), "click.html")
}

func TestPageObjCrossNavigation(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.blank())
	obj := p.MustEvaluate(rod.Eval(`() => ({})`).ByObject())

	g.page.MustNavigate(g.blank())

	_, err := p.Evaluate(rod.Eval(`() => 1`).This(obj))
	g.Is(err, &rod.ObjectNotFoundError{})
	g.Has(err.Error(), "cannot find object: {\"type\":\"object\"")
}

func TestEnsureJSHelperErr(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.blank())

	g.mc.stubErr(2, proto.RuntimeCallFunctionOn{})
	g.Err(p.Elements(`button`))
}

func TestEvalObjectReferenceChainIsTooLong(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.blank())

	obj, err := p.Evaluate(&rod.EvalOptions{
		JS: `() => {
			let a = {b: 1}
			a.c = a
			return a
		}`,
	})
	g.E(err)

	_, err = p.Eval(`a => a`, obj)
	g.Eq(err.Error(), "{-32000 Object reference chain is too long }")

	val := p.MustEval(`a => a.c.c.c.c.b`, obj)
	g.Eq(val.Int(), 1)
}

func TestEvalOptionsString(t *testing.T) {
	g := testutil.New(t)
	object := &proto.RuntimeRemoteObject{Description: "button"}
	g.Eq(rod.Eval(`() => this.parentElement`).This(object).String(), "() => this.parentElement() button")
}

// helperBrowser models the helper objects in one page window. Each functions
// object records the helpers installed into it, and a helper call fails like
// Chrome when a dependency is missing from the functions object the helper was
// installed with. While hold is set, each install waits until the test releases it.
type helperBrowser struct {
	sync.Mutex
	hold    bool
	pending []*heldInstall
	arrived int
	handles int
	// functions maps each functions object to the helpers installed into it.
	functions map[proto.RuntimeRemoteObjectID]map[string]bool
	helpers   map[proto.RuntimeRemoteObjectID]installedHelper
	installs  map[string]int
}

// installedHelper is a helper and the functions object it was installed into.
type installedHelper struct {
	name      string
	functions proto.RuntimeRemoteObjectID
}

// heldInstall is an install waiting for release. Its name is js.Functions.Name
// for the creation of a functions object.
type heldInstall struct {
	name    string
	arrival int
	release chan struct{}
}

func newHelperBrowser() *helperBrowser {
	return &helperBrowser{
		hold:      true,
		functions: map[proto.RuntimeRemoteObjectID]map[string]bool{},
		helpers:   map[proto.RuntimeRemoteObjectID]installedHelper{},
		installs:  map[string]int{},
	}
}

var helperBrowserFunctions = map[string]*js.Function{
	js.Element.Name: js.Element, js.ElementX.Name: js.ElementX, js.Selectable.Name: js.Selectable,
}

// wait holds an install until the test releases it.
func (b *helperBrowser) wait(name string) {
	b.Lock()
	if !b.hold {
		b.Unlock()
		return
	}
	b.arrived++
	held := &heldInstall{name: name, arrival: b.arrived, release: make(chan struct{})}
	b.pending = append(b.pending, held)
	b.Unlock()
	<-held.release
}

// releaseNext waits until every other goroutine is blocked, then releases the
// pending install that the order puts first. It reports whether one was pending.
func (b *helperBrowser) releaseNext(order func(a, b *heldInstall) int) bool {
	synctest.Wait()
	b.Lock()
	defer b.Unlock()
	if len(b.pending) == 0 {
		return false
	}
	next := slices.MinFunc(b.pending, order)
	b.pending = slices.DeleteFunc(b.pending, func(held *heldInstall) bool { return held == next })
	close(next.release)
	return true
}

func (b *helperBrowser) handle(name string) proto.RuntimeRemoteObjectID {
	b.handles++
	return proto.RuntimeRemoteObjectID(fmt.Sprintf("%s-%d", name, b.handles))
}

func (b *helperBrowser) Call(_ context.Context, _, method string, params any) ([]byte, error) {
	switch method {
	case "Runtime.evaluate":
		return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
	case "Runtime.releaseObject":
		return []byte(`{}`), nil
	case "Runtime.callFunctionOn":
	default:
		return nil, fmt.Errorf("unexpected request: %s", method)
	}
	req := params.(proto.RuntimeCallFunctionOn)
	if req.FunctionDeclaration == js.Functions.Definition {
		b.wait(js.Functions.Name)
		b.Lock()
		defer b.Unlock()
		b.installs[js.Functions.Name]++
		id := b.handle(js.Functions.Name)
		b.functions[id] = map[string]bool{}
		return fmt.Appendf(nil, `{"result":{"type":"object","objectId":%q}}`, id), nil
	}
	if decl, ok := strings.CutPrefix(req.FunctionDeclaration, "functions => { const f = functions."); ok {
		name, _, _ := strings.Cut(decl, " ")
		b.wait(name)
		b.Lock()
		defer b.Unlock()
		functions := b.functions[req.Arguments[0].ObjectID]
		if functions == nil {
			return nil, fmt.Errorf("install of %s into unknown object %s", name, req.Arguments[0].ObjectID)
		}
		b.installs[name]++
		functions[name] = true
		id := b.handle(name)
		b.helpers[id] = installedHelper{name, req.Arguments[0].ObjectID}
		return fmt.Appendf(nil, `{"result":{"type":"function","objectId":%q}}`, id), nil
	}

	// evalHelper passes the helper as the first argument.
	b.Lock()
	defer b.Unlock()
	helper, ok := b.helpers[req.Arguments[0].ObjectID]
	if !ok {
		return nil, cdp.ErrObjNotFound
	}
	for _, dep := range helperBrowserFunctions[helper.name].Dependencies {
		if !b.functions[helper.functions][dep.Name] {
			return fmt.Appendf(nil, `{"result":{"type":"object","subtype":"error","objectId":"error"},`+
				`"exceptionDetails":{"exceptionId":1,"text":"Uncaught","lineNumber":0,"columnNumber":0,`+
				`"exception":{"type":"object","subtype":"error","description":"TypeError: functions.%s is not a function"}}}`, dep.Name), nil
		}
	}
	return []byte(`{"result":{"type":"object","subtype":"node","objectId":"node"}}`), nil
}

func (*helperBrowser) Event() <-chan *cdp.Event { return nil }

// Concurrent first queries in a new window install each helper once, into the
// functions object that holds its dependencies, whatever order Chrome answers in.
func TestJSHelperConcurrentInstall(t *testing.T) {
	isCreation := func(held *heldInstall) bool { return held.name == js.Functions.Name }
	for _, test := range []struct {
		name string
		// order is the order in which Chrome answers the pending installs.
		order func(a, b *heldInstall) int
	}{
		{
			// Chrome answers dependency installs first, then the newest
			// functions object, then the oldest helper install. Separate
			// installs then cache the first query's functions object after the
			// second query cached the dependency in its own, and cache last the
			// helper installed into the object without the dependency.
			name: "reported order",
			order: func(a, b *heldInstall) int {
				rank := func(held *heldInstall) int {
					switch {
					case held.name == js.Selectable.Name:
						return 0
					case isCreation(held):
						return 1
					}
					return 2
				}
				if rank(a) != rank(b) {
					return rank(a) - rank(b)
				}
				if isCreation(a) {
					return b.arrival - a.arrival
				}
				return a.arrival - b.arrival
			},
		},
		{
			// Separate installs cache a functions object without the
			// dependency last, so a helper installed later fails.
			name:  "newest first",
			order: func(a, b *heldInstall) int { return b.arrival - a.arrival },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser := newHelperBrowser()
				page := rod.New().Context(t.Context()).Client(browser).PageFromSession("session")
				var wg sync.WaitGroup
				errs := make([]error, 2)
				for i := range errs {
					wg.Go(func() {
						_, errs[i] = page.Element("#who")
					})
				}
				for browser.releaseNext(test.order) {
				}
				wg.Wait()
				if err := errors.Join(errs...); err != nil {
					t.Fatalf("concurrent first queries: %v", err)
				}

				browser.Lock()
				browser.hold = false
				browser.Unlock()
				if _, err := page.Element("#who"); err != nil {
					t.Fatalf("cached helper: %v", err)
				}
				// A new helper is installed into the cached functions object.
				if _, err := page.ElementX("//p"); err != nil {
					t.Fatalf("helper installed after the race: %v", err)
				}
				want := map[string]int{js.Functions.Name: 1, js.Selectable.Name: 1, js.Element.Name: 1, js.ElementX.Name: 1}
				if !maps.Equal(browser.installs, want) {
					t.Fatalf("installs = %v, want %v", browser.installs, want)
				}
			})
		})
	}
}

// A query waiting for an install in the same context stops waiting when its
// own context ends, and the install still completes.
func TestJSHelperInstallWaitCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser := newHelperBrowser()
		page := rod.New().Context(t.Context()).Client(browser).PageFromSession("session")
		installing := make(chan error, 1)
		go func() {
			_, err := page.Element("#who")
			installing <- err
		}()
		ctx, cancel := context.WithCancel(t.Context())
		waiting := make(chan error, 1)
		synctest.Wait()
		go func() {
			_, err := page.Context(ctx).Element("#who")
			waiting <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-waiting; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting query = %v", err)
		}
		oldest := func(a, b *heldInstall) int { return a.arrival - b.arrival }
		for browser.releaseNext(oldest) {
		}
		if err := <-installing; err != nil {
			t.Fatal(err)
		}
		if browser.installs[js.Functions.Name] != 1 || browser.installs[js.Element.Name] != 1 {
			t.Fatalf("installs = %v", browser.installs)
		}
	})
}
