package rod_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/js"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

func TestEvalPreservesRemotePrimitiveArguments(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	for _, expression := range []string{`42`, `"text"`, `false`, `null`, `undefined`, `NaN`, `Infinity`, `-Infinity`, `-0`, `123n`} {
		value, err := p.Eval(`() => ` + expression)
		if err != nil {
			t.Fatal(err)
		}
		result, err := p.Eval(`value => Object.is(value, `+expression+`)`, value)
		if err != nil || !result.Value.Bool() {
			t.Fatalf("argument %s changed: result=%+v error=%v", expression, result, err)
		}
	}
}

func TestExposeFrameResponsesAndQuotedNames(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><iframe srcdoc="<!doctype html><p>Child</p>"></iframe>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()
	const name = `quoted"name`
	stop, err := p.Expose(name, func(value jsonvalue.Value) (any, error) {
		if value.Str() == "reject" {
			return nil, errors.New("callback rejected")
		}
		return value.Str(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { g.E(stop()) }()
	p.MustReload().MustWaitLoad()
	result, err := p.Eval(`async name => {
		const values = await Promise.all([window[name]("main"), frames[0][name]("child")]);
		try { await frames[0][name]("reject") } catch (error) { values.push(String(error)) }
		return values;
	}`, name)
	if err != nil {
		t.Fatal(err)
	}
	g.Eq(result.Value.Join(","), "main,child,callback rejected")
	g.E(stop())
}

func TestExposeSetupRollback(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	injected := errors.New("reload script registration failed")
	var binding, removed string
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		if method == "Runtime.addBinding" || method == "Runtime.removeBinding" {
			data, err := json.Marshal(params)
			if err != nil {
				return nil, err
			}
			var request struct{ Name string }
			if err := json.Unmarshal(data, &request); err != nil {
				return nil, err
			}
			if method == "Runtime.addBinding" {
				binding = request.Name
			} else {
				removed = request.Name
			}
		}
		if method == "Page.addScriptToEvaluateOnNewDocument" {
			return nil, injected
		}
		return g.mc.principal.Call(ctx, session, method, params)
	})
	defer g.mc.resetCall()
	stop, err := p.Expose("rollback", func(jsonvalue.Value) (any, error) { return nil, nil })
	if !errors.Is(err, injected) || stop != nil || binding == "" || removed != binding {
		t.Fatalf("setup rollback: binding=%q removed=%q stop=%v error=%v", binding, removed, stop != nil, err)
	}
}

func TestExposeRuntimeSetupFailure(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	injected := errors.New("runtime subscription setup failed")
	g.mc.stub(1, proto.RuntimeEnable{}, func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(nil), injected
	})
	stop, err := p.Expose("unavailable", func(jsonvalue.Value) (any, error) { return nil, nil })
	if !errors.Is(err, injected) || stop != nil {
		t.Fatalf("runtime setup failure: stop=%v error=%v", stop != nil, err)
	}
}

func TestFrameEvaluationUsesCurrentContext(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><iframe srcdoc="<!doctype html><p>Frame content</p>"></iframe>`)).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()
	ctx, cancel := context.WithCancel(p.GetContext())
	defer cancel()
	frame := p.MustElement("iframe").Context(ctx).MustFrame()
	cancel()
	value, err := frame.Context(p.GetContext()).Eval(`() => document.querySelector('p').textContent`)
	if err != nil || value.Value.Str() != "Frame content" {
		t.Fatalf("frame evaluation reused the old element context: value=%+v error=%v", value, err)
	}
}

func TestJSHelperCacheConcurrentQueryNavigation(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><p>Old document</p>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	helperCreated := make(chan struct{})
	resumeHelper := make(chan struct{})
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(resumeHelper) }) }
	var paused atomic.Bool
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		response, err := g.mc.principal.Call(ctx, session, method, params)
		if request, ok := params.(proto.RuntimeCallFunctionOn); ok &&
			session == string(p.SessionID) && request.FunctionDeclaration == js.Functions.Definition &&
			err == nil && paused.CompareAndSwap(false, true) {
			// Hold the valid old-document response until a concurrent operation
			// replaces the document and initializes the new execution context.
			close(helperCreated)
			select {
			case <-resumeHelper:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return response, err
	})
	type queryResult struct {
		element *rod.Element
		err     error
	}
	result := make(chan queryResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		element, err := p.Element("p")
		result <- queryResult{element, err}
	}()
	defer func() {
		resume()
		p.CancelTimeout()
		<-done
		g.mc.resetCall()
	}()
	select {
	case <-helperCreated:
	case <-p.GetContext().Done():
		t.Fatal("query did not reach helper creation")
	}
	g.E(p.Navigate(g.html(`<!doctype html><p>New document</p>`)))
	_, err := p.Eval(`() => location.href`)
	g.E(err)
	resume()
	select {
	case query := <-result:
		if query.err != nil {
			t.Fatal(query.err)
		}
		g.Eq(query.element.MustText(), "New document")
	case <-p.GetContext().Done():
		t.Fatal("query did not recover after navigation")
	}
}

func TestSearchFailureReleasesDomain(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	_, err := p.Sleeper(rod.NotFoundSleeper).Search("#absent")
	if !errors.Is(err, &rod.ElementNotFoundError{}) {
		t.Fatalf("missing search: %v", err)
	}
	if p.LoadState(&proto.DOMEnable{}) {
		t.Fatal("failed Search retained DOM ownership")
	}
}

func TestSearchReleaseSurvivesCancellation(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><p>Result</p>`))
	ctx, cancel := context.WithCancel(p.GetContext())
	result, err := p.Context(ctx).Search("p")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	g.E(result.Release())
	g.E(result.Release())
	if p.LoadState(&proto.DOMEnable{}) {
		t.Fatal("Release retained DOM after search context expired")
	}
}

func TestElementsReportsReleaseFailure(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	injected := errors.New("array release failed")
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		if method == "Runtime.releaseObject" {
			return nil, injected
		}
		return g.mc.principal.Call(ctx, session, method, params)
	})
	defer g.mc.resetCall()
	if _, err := p.Elements("body"); !errors.Is(err, injected) {
		t.Fatalf("Elements lost cleanup error: %v", err)
	}
}

func TestQueryEmptyRegexAndQuotedXPath(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><main><button></button><button>Other</button><p>Selected</p></main>`)).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	for _, find := range []func() (*rod.Element, error){
		func() (*rod.Element, error) { return p.ElementR("button", "") },
		func() (*rod.Element, error) { return p.MustElement("main").ElementR("button", "") },
		func() (*rod.Element, error) { return p.Race().ElementR("button", "").Do() },
	} {
		element, err := find()
		if err != nil || element.MustText() != "" {
			t.Fatalf("empty regex did not select first empty button: %v", err)
		}
	}
	if has, _, err := p.HasR("missing", ""); err != nil || has {
		t.Fatalf("empty regex missing selector: has=%v error=%v", has, err)
	}
	for _, id := range []string{`item's`, `item"s`, `item'"s`} {
		element := p.MustElement("p")
		element.MustEval(`id => this.id = id`, id)
		xpath := element.MustGetXPath(true)
		found, err := p.Sleeper(rod.NotFoundSleeper).ElementX(xpath)
		if err != nil || found.MustText() != "Selected" {
			t.Fatalf("XPath %q did not select its source: %v", xpath, err)
		}
	}
}

func TestReflectedElementIsInteractable(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><button style="position:absolute;left:80px;top:80px;width:120px;height:40px;transform:scaleX(-1)">Click</button>`)).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	button := p.MustElement("button")
	button.MustEval(`() => this.addEventListener("click", () => this.textContent = "Clicked")`)
	g.E(button.Click(proto.InputMouseButtonLeft, 1))
	g.Eq(button.MustText(), "Clicked")
}

func TestNumericKeypadTypesCompleteSequence(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><textarea></textarea>`)).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	text := p.MustElement("textarea")
	g.E(text.Type(input.NumpadAdd, input.Numpad2, input.Numpad1, input.Numpad2,
		input.Numpad6, input.Numpad3, input.Numpad5, input.Numpad4, input.Numpad1,
		input.Numpad3, input.Numpad2, input.Numpad1, input.Numpad6,
		input.NumpadDecimal, input.Numpad0, input.Numpad7, input.Numpad8, input.Numpad9))
	g.Eq(text.MustText(), "+212635413216.0789")
}

func TestElementWaitLoadDoesNotSerializeEvent(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><img>`)).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	img := p.MustElement("img")
	img.MustEval(`() => {
		Object.defineProperty(this, "complete", {value: false});
		const add = this.addEventListener;
		this.addEventListener = function(type, listener, options) {
			add.call(this, type, listener, options);
			if (type === "load") window.loadListenerReady = true;
		};
	}`)
	result := make(chan error, 1)
	go func() { result <- img.WaitLoad() }()
	g.E(p.Wait(rod.Eval(`() => window.loadListenerReady === true`)))
	img.MustEval(`() => {
		const event = new Event("load");
		event.circular = event;
		this.dispatchEvent(event);
	}`)
	if err := <-result; err != nil {
		t.Fatalf("WaitLoad serialized an unused event: %v", err)
	}
}

func TestExposeUnsupportedResultRejects(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	var calls atomic.Int32
	stop, err := p.Expose("unsupported", func(jsonvalue.Value) (any, error) {
		calls.Add(1)
		return func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { g.E(stop()) }()
	result, err := p.Eval(`async () => { try { await unsupported() } catch(error) { return String(error) } }`)
	if err != nil || !strings.Contains(result.Value.Str(), "encode exposed function response") || calls.Load() != 1 {
		t.Fatalf("unsupported response: result=%v calls=%d error=%v", result, calls.Load(), err)
	}
}
