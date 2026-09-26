package rod

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/js"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

func TestJSContextLookupOrder(t *testing.T) {
	var checked []proto.RuntimeRemoteObjectID
	client := &resolutionClient{respond: func(_ string, params any) ([]byte, error) {
		req, ok := isContextCheck(params)
		if !ok {
			t.Fatalf("unexpected request: %#v", params)
		}
		checked = append(checked, req.ObjectID)
		if req.ObjectID != "c" {
			return nil, errOtherContextForTest
		}
		return []byte(`{"result":{"type":"undefined"}}`), nil
	}}
	page := resolutionPage(t, client, "d", "b", "a", "c", "page", "recent")
	page.helpers.last = "recent"
	for range 3 {
		checked = nil
		id, err := page.jsCtxIDByObjectID("element", "page")
		if err != nil || id != "c" {
			t.Fatalf("context = %q, %v", id, err)
		}
		if want := []proto.RuntimeRemoteObjectID{"page", "recent", "a", "b", "c"}; !slices.Equal(checked, want) {
			t.Fatalf("checked %v, want %v", checked, want)
		}
		if page.helpers.last != "c" {
			t.Fatalf("last match = %q", page.helpers.last)
		}
		page.helpers.last = "recent"
	}
	if _, err := page.jsCtxIDByObjectID("element", "page"); err != nil {
		t.Fatal(err)
	}
	checked = nil
	if id, err := page.jsCtxIDByObjectID("element", ""); err != nil || id != "c" || !slices.Equal(checked, []proto.RuntimeRemoteObjectID{"c"}) {
		t.Fatalf("last match was not checked first: %q, %v, %v", id, err, checked)
	}
	if len(page.helpers.contexts) != 6 {
		t.Fatalf("lookup changed the cached contexts: %v", page.helpers.contexts)
	}
}

func TestJSContextLookupFailures(t *testing.T) {
	transport := errors.New("transport failed")
	invalid := &cdp.Error{Code: -32000, Message: "Invalid remote object id"}
	for _, test := range []struct {
		name string
		// check answers the context check on each cached window.
		check map[proto.RuntimeRemoteObjectID]error
		// window answers the creation of the object's own window.
		window error
		// windowResult replaces the successful window response.
		windowResult string
		want         proto.RuntimeRemoteObjectID
		wantErr      error
		remaining    []proto.RuntimeRemoteObjectID
	}{
		{
			name:      "destroyed context is uncached",
			check:     map[proto.RuntimeRemoteObjectID]error{"page": cdp.ErrCtxNotFound, "frame": nil},
			want:      "frame",
			remaining: []proto.RuntimeRemoteObjectID{"frame"},
		},
		{
			name:      "released window is skipped",
			check:     map[proto.RuntimeRemoteObjectID]error{"page": cdp.ErrObjNotFound, "frame": errOtherContextForTest},
			want:      "new-window",
			remaining: []proto.RuntimeRemoteObjectID{"frame", "new-window", "page"},
		},
		{
			name:      "released object is reported",
			check:     map[proto.RuntimeRemoteObjectID]error{"page": cdp.ErrObjNotFound, "frame": errOtherContextForTest},
			window:    cdp.ErrObjNotFound,
			wantErr:   cdp.ErrObjNotFound,
			remaining: []proto.RuntimeRemoteObjectID{"frame", "page"},
		},
		{
			name:         "window response without a result",
			check:        map[proto.RuntimeRemoteObjectID]error{"page": errOtherContextForTest, "frame": errOtherContextForTest},
			windowResult: `{}`,
			wantErr:      errMalformedWindowForTest,
			remaining:    []proto.RuntimeRemoteObjectID{"frame", "page"},
		},
		{
			name:         "window response without a handle",
			check:        map[proto.RuntimeRemoteObjectID]error{"page": errOtherContextForTest, "frame": errOtherContextForTest},
			windowResult: `{"result":{"type":"undefined"}}`,
			wantErr:      errMalformedWindowForTest,
			remaining:    []proto.RuntimeRemoteObjectID{"frame", "page"},
		},
		{
			name:      "unrelated protocol error",
			check:     map[proto.RuntimeRemoteObjectID]error{"page": invalid},
			wantErr:   invalid,
			remaining: []proto.RuntimeRemoteObjectID{"frame", "page"},
		},
		{
			name:      "transport error",
			check:     map[proto.RuntimeRemoteObjectID]error{"page": transport},
			wantErr:   transport,
			remaining: []proto.RuntimeRemoteObjectID{"frame", "page"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var released []proto.RuntimeRemoteObjectID
			windows := 0
			client := &resolutionClient{respond: func(method string, params any) ([]byte, error) {
				if method == "Runtime.releaseObject" {
					released = append(released, params.(proto.RuntimeReleaseObject).ObjectID)
					return []byte(`{}`), nil
				}
				if req, ok := isContextCheck(params); ok {
					err, ok := test.check[req.ObjectID]
					if !ok {
						t.Fatalf("checked %q after a result was known", req.ObjectID)
					}
					if err != nil {
						return nil, err
					}
					return []byte(`{"result":{"type":"undefined"}}`), nil
				}
				if req, ok := params.(proto.RuntimeCallFunctionOn); ok && req.FunctionDeclaration == `() => window` {
					if windows++; windows > 1 {
						t.Fatal("created the object's window again")
					}
					if test.window != nil {
						return nil, test.window
					}
					if test.windowResult != "" {
						return []byte(test.windowResult), nil
					}
					return []byte(`{"result":{"type":"object","objectId":"new-window"}}`), nil
				}
				t.Fatalf("unexpected request: %s", method)
				return nil, nil
			}}
			page := resolutionPage(t, client, "page", "frame")
			id, err := page.jsCtxIDByObjectID("element", "page")
			if test.wantErr == errMalformedWindowForTest {
				if err == nil || id != "" || errors.Is(err, cdp.ErrCtxNotFound) || errors.Is(err, cdp.ErrObjNotFound) {
					t.Fatalf("lookup = %q, %v; want an error for the malformed window", id, err)
				}
			} else if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) || id != "" {
					t.Fatalf("lookup = %q, %v; want error %v", id, err, test.wantErr)
				}
			} else if err != nil || id != test.want {
				t.Fatalf("lookup = %q, %v; want %q", id, err, test.want)
			}
			remaining := slices.Sorted(func(yield func(proto.RuntimeRemoteObjectID) bool) {
				for id := range page.helpers.contexts {
					if !yield(id) {
						return
					}
				}
			})
			if !slices.Equal(remaining, test.remaining) {
				t.Fatalf("cached contexts = %v, want %v", remaining, test.remaining)
			}
			if len(released) != 0 {
				t.Fatalf("released cached or missing handles: %v", released)
			}
		})
	}
}

func TestJSContextFrameWindowHint(t *testing.T) {
	var checked []proto.RuntimeRemoteObjectID
	windows := 0
	client := &resolutionClient{respond: func(method string, params any) ([]byte, error) {
		if req, ok := isContextCheck(params); ok {
			checked = append(checked, req.ObjectID)
			if req.ObjectID != "c-frame" && req.ObjectID != "new-frame" {
				return nil, errOtherContextForTest
			}
			return []byte(`{"result":{"type":"undefined"}}`), nil
		}
		switch method {
		case "DOM.describeNode":
			return []byte(`{"node":{"nodeName":"IFRAME","frameId":"frame","contentDocument":{"backendNodeId":7,"nodeId":0,"nodeType":0,"nodeName":"","localName":"","nodeValue":""},"nodeId":0,"backendNodeId":0,"nodeType":0,"localName":"","nodeValue":""}}`), nil
		case "DOM.resolveNode":
			return []byte(`{"object":{"type":"object","objectId":"document"}}`), nil
		case "Runtime.callFunctionOn":
			if params.(proto.RuntimeCallFunctionOn).FunctionDeclaration == `() => window` {
				windows++
				return []byte(`{"result":{"type":"object","objectId":"new-frame"}}`), nil
			}
		case "Runtime.evaluate":
			return []byte(`{"result":{"type":"object","objectId":"new-window"}}`), nil
		case "Runtime.releaseObject":
			return []byte(`{}`), nil
		}
		t.Fatalf("unexpected request: %s", method)
		return nil, nil
	}}
	page := resolutionPage(t, client, "window", "a-frame", "b-frame", "c-frame")
	*page.jsCtxID = "window"
	iframe := page.elementInJSCtx(&proto.RuntimeRemoteObject{ObjectID: "iframe"}, "window", "window")
	resolve := func() proto.RuntimeRemoteObjectID {
		t.Helper()
		checked = nil
		view, err := iframe.Frame()
		if err != nil {
			t.Fatal(err)
		}
		id, err := view.getJSCtxID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	if id := resolve(); id != "c-frame" || !slices.Equal(checked, []proto.RuntimeRemoteObjectID{"a-frame", "b-frame", "c-frame"}) {
		t.Fatalf("first view resolved %q after checking %v", id, checked)
	}
	// A new view of the frame checks the frame's last window first.
	page.helpers.last = "a-frame"
	if id := resolve(); id != "c-frame" || !slices.Equal(checked, []proto.RuntimeRemoteObjectID{"c-frame"}) {
		t.Fatalf("new view resolved %q after checking %v", id, checked)
	}

	// Uncaching the window forgets the frame's hint.
	view, err := iframe.Frame()
	if err != nil {
		t.Fatal(err)
	}
	*view.jsCtxID = "c-frame"
	view.unsetJSCtxID()
	if hint := page.helpers.frameWindow("frame"); hint != "" {
		t.Fatalf("uncached window is still the frame hint: %q", hint)
	}
	if id := resolve(); id != "new-frame" || windows != 1 || page.helpers.frameWindow("frame") != "new-frame" {
		t.Fatalf("view after reset resolved %q with %d windows", id, windows)
	}

	// A new page window replaces every cached context and hint.
	page.unsetJSCtxID()
	if _, err := page.getJSCtxID(); err != nil {
		t.Fatal(err)
	}
	if page.helpers.frames != nil {
		t.Fatalf("page reset kept frame hints: %v", page.helpers.frames)
	}
}

// A window, frame node, frame document or helper response without a handle
// fails the operation and leaves the cache unchanged.
func TestJSContextMalformedResponses(t *testing.T) {
	thrown := `{"result":{"type":"object","subtype":"error","objectId":"thrown"},` +
		`"exceptionDetails":{"exceptionId":1,"text":"Uncaught","lineNumber":0,"columnNumber":0}}`
	for _, test := range []struct {
		name string
		// step is the request answered with body: the page window, the frame
		// node, the frame document, the functions object or a helper install.
		step, body string
		wantErr    error
	}{
		{name: "window without result", step: "window", body: `{}`},
		{name: "window without handle", step: "window", body: `{"result":{"type":"object"}}`},
		{name: "frame node without node", step: "frame node", body: `{}`},
		{name: "frame document without object", step: "document", body: `{}`},
		{name: "frame document without handle", step: "document", body: `{"object":{"type":"object"}}`},
		{name: "functions object without result", step: "functions", body: `{}`},
		{name: "functions object without handle", step: "functions", body: `{"result":{"type":"object"}}`},
		{name: "helper without result", step: "helper", body: `{}`},
		{name: "helper without handle", step: "helper", body: `{"result":{"type":"function"}}`},
		{name: "helper install throws", step: "helper", body: thrown, wantErr: &EvalError{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			// framed is set once the frame view exists; describing its node
			// then resolves the view's window.
			framed := false
			answer := func(step, body string) ([]byte, error) {
				if step == test.step {
					requests++
					body = test.body
				}
				return []byte(body), nil
			}
			client := &resolutionClient{respond: func(method string, params any) ([]byte, error) {
				switch method {
				case "Runtime.evaluate":
					return answer("window", `{"result":{"type":"object","objectId":"window"}}`)
				case "DOM.describeNode":
					iframe := `{"node":{"nodeId":0,"backendNodeId":6,"nodeType":1,"nodeName":"IFRAME","localName":"iframe","nodeValue":"","frameId":"frame",` +
						`"contentDocument":{"nodeId":0,"backendNodeId":7,"nodeType":9,"nodeName":"#document","localName":"","nodeValue":""}}}`
					if !framed {
						return []byte(iframe), nil
					}
					return answer("frame node", iframe)
				case "DOM.resolveNode":
					return answer("document", `{"object":{"type":"object","objectId":"document"}}`)
				case "Runtime.callFunctionOn":
				default:
					t.Fatalf("unexpected request: %s", method)
				}
				req := params.(proto.RuntimeCallFunctionOn)
				switch {
				case req.ObjectID == "":
					t.Fatalf("call without a target: %s", req.FunctionDeclaration)
				case req.FunctionDeclaration == js.Functions.Definition:
					return answer("functions", `{"result":{"type":"object","objectId":"functions"}}`)
				case strings.HasPrefix(req.FunctionDeclaration, "functions => "):
					if req.Arguments[0].ObjectID == "" {
						t.Fatal("helper installed without a functions object")
					}
					return answer("helper", `{"result":{"type":"function","objectId":"helper"}}`)
				}
				return []byte(`{"result":{"type":"object","subtype":"node","objectId":"node"}}`), nil
			}}
			page := resolutionPage(t, client)
			view := page
			switch test.step {
			case "frame node", "document":
				*page.jsCtxID = "window"
				page.helpers.contexts["window"] = map[string]proto.RuntimeRemoteObjectID{}
				var err error
				view, err = page.elementInJSCtx(&proto.RuntimeRemoteObject{ObjectID: "iframe"}, "window", "window").Frame()
				if err != nil {
					t.Fatal(err)
				}
				framed = true
			case "functions", "helper":
				*page.jsCtxID = "window"
				page.helpers.contexts["window"] = map[string]proto.RuntimeRemoteObjectID{}
				if test.step == "helper" {
					page.helpers.contexts["window"][js.Functions.Name] = "functions"
				}
			}
			cached := maps.Clone(page.helpers.contexts)
			for id, helpers := range cached {
				cached[id] = maps.Clone(helpers)
			}
			window := *page.jsCtxID

			ops := 0
			if test.step == "window" || test.step == "frame node" || test.step == "document" {
				ops++
				if res, err := view.Eval(`() => 1`); res != nil || err == nil {
					t.Fatalf("eval = %v, %v", res, err)
				}
			}
			for range 2 {
				ops++
				el, err := view.Element("#who")
				if el != nil || err == nil || (test.wantErr != nil && !errors.Is(err, test.wantErr)) {
					t.Fatalf("element = %v, %v", el, err)
				}
			}
			if requests != ops {
				t.Fatalf("%s requested %d times in %d operations", test.step, requests, ops)
			}
			if !maps.EqualFunc(page.helpers.contexts, cached, maps.Equal) || *page.jsCtxID != window ||
				len(page.helpers.frames) != 0 || (view != page && *view.jsCtxID != "") {
				t.Fatalf("cache changed: contexts %v, window %q, frames %v, view window %q",
					page.helpers.contexts, *page.jsCtxID, page.helpers.frames, *view.jsCtxID)
			}
		})
	}
}

func TestElementContextReuse(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	browser := New().Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := browser.Close(); err != nil {
			t.Error(err)
		}
	}()
	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		element, err := page.Element("html")
		if err != nil {
			t.Fatal(err)
		}
		if err := element.Release(); err != nil {
			t.Fatal(err)
		}
	}
	page.helpers.Lock()
	contexts := len(page.helpers.contexts)
	page.helpers.Unlock()
	if got := contexts; got != 1 {
		t.Fatalf("ten queries retained %d helper contexts for one execution context", got)
	}
	if err := page.Navigate("data:text/html," + url.PathEscape(`<iframe srcdoc="<p>frame</p>"></iframe>`)); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		iframe, err := page.Element("iframe")
		if err != nil {
			t.Fatal(err)
		}
		frame, err := iframe.Frame()
		if err != nil {
			t.Fatal(err)
		}
		element, err := frame.Element("p")
		if err != nil {
			t.Fatal(err)
		}
		if text, err := element.Text(); err != nil || text != "frame" {
			t.Fatalf("iframe text = %q, %v", text, err)
		}
		if err := errors.Join(element.Release(), iframe.Release()); err != nil {
			t.Fatal(err)
		}
	}
	page.helpers.Lock()
	contexts = len(page.helpers.contexts)
	page.helpers.Unlock()
	if got := contexts; got != 2 {
		t.Fatalf("ten iframe queries retained %d helper contexts for two execution contexts", got)
	}
}

func TestJSContextLookupDoesNotBlockCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := make(chan struct{})
		client := &lifecycleRegressionClient{call: func(ctx context.Context, method string, params any) ([]byte, error) {
			if method == "Runtime.releaseObject" {
				return []byte(`{}`), nil
			}
			if params.(proto.RuntimeCallFunctionOn).FunctionDeclaration == `() => window` {
				return []byte(`{"result":{"type":"object","objectId":"temporary-window"}}`), nil
			}
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		page := New().Context(ctx).Client(client).PageFromSession("session")
		page.sessionCtx = t.Context()
		page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{
			"window": {"element": "helper"},
		}
		done := make(chan error, 1)
		go func() {
			_, err := page.jsCtxIDByObjectID("element", "")
			done <- err
		}()
		<-started
		if id, ok := page.getHelper("window", "element"); !ok || id != "helper" {
			t.Fatalf("cached helper unavailable during slow context lookup: %q, %t", id, ok)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("slow context lookup ignored cancellation: %v", err)
		}
		if len(page.helpers.contexts) != 1 {
			t.Fatal("failed context lookup retained a temporary window")
		}
	})
}

func TestJSContextConcurrentLookupReusesContext(t *testing.T) {
	var lookups, releases atomic.Int32
	ready := make(chan struct{})
	client := &lifecycleRegressionClient{call: func(_ context.Context, method string, params any) ([]byte, error) {
		if method == "Runtime.releaseObject" {
			releases.Add(1)
			return []byte(`{}`), nil
		}
		req := params.(proto.RuntimeCallFunctionOn)
		if req.FunctionDeclaration == `() => window` {
			if lookups.Add(1) == 2 {
				close(ready)
			}
			<-ready
			return json.Marshal(proto.RuntimeCallFunctionOnResult{Result: &proto.RuntimeRemoteObject{ObjectID: "window-" + req.ObjectID}})
		}
		if req.ObjectID == "parent-window" {
			return nil, &cdp.Error{Code: -32000, Message: "Argument should belong to the same JavaScript world as target object"}
		}
		return []byte(`{"result":{"type":"boolean","value":true}}`), nil
	}}
	page := New().Context(t.Context()).Client(client).PageFromSession("session")
	page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{"parent-window": {}}
	contexts := make(chan proto.RuntimeRemoteObjectID, 2)
	var wg sync.WaitGroup
	for _, id := range []proto.RuntimeRemoteObjectID{"one", "two"} {
		wg.Go(func() {
			context, err := page.jsCtxIDByObjectID(id, "")
			if err != nil {
				t.Error(err)
			}
			contexts <- context
		})
	}
	wg.Wait()
	first, second := <-contexts, <-contexts
	if first == "" || first != second || len(page.helpers.contexts) != 2 || releases.Load() != 1 {
		t.Fatalf("concurrent lookup retained duplicate contexts: %q, %q, contexts=%d, releases=%d", first, second, len(page.helpers.contexts), releases.Load())
	}
}

func TestFrameContextReleasesDocumentObject(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failed context lookup"}[fail], func(t *testing.T) {
			failed := errors.New("context lookup failed")
			var released []proto.RuntimeRemoteObjectID
			browser := New().Context(t.Context()).Client(&lifecycleRegressionClient{call: func(_ context.Context, method string, params any) ([]byte, error) {
				switch method {
				case "DOM.describeNode":
					return []byte(`{"node":{"contentDocument":{"backendNodeId":2,"nodeId":0,"nodeType":0,"nodeName":"","localName":"","nodeValue":""},"nodeId":0,"backendNodeId":0,"nodeType":0,"nodeName":"","localName":"","nodeValue":""}}`), nil
				case "DOM.resolveNode":
					return []byte(`{"object":{"objectId":"document","type":""}}`), nil
				case "Runtime.callFunctionOn":
					if fail {
						return nil, failed
					}
					return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
				case "Runtime.releaseObject":
					released = append(released, params.(proto.RuntimeReleaseObject).ObjectID)
					return []byte(`{}`), nil
				default:
					t.Fatalf("unexpected request: %s", method)
					return nil, nil
				}
			}})
			parent := browser.PageFromSession("session")
			frame := &Page{
				browser: browser, ctx: t.Context(), SessionID: "session", FrameID: "child",
				jsCtxLock: new(sync.Mutex), jsCtxID: new(proto.RuntimeRemoteObjectID), helpers: &jsHelperCache{},
				element: &Element{page: parent, ctx: t.Context(), Object: &proto.RuntimeRemoteObject{ObjectID: "iframe"}},
			}
			id, err := frame.getJSCtxID()
			if fail && !errors.Is(err, failed) {
				t.Fatalf("context lookup lost failure: %v", err)
			}
			if !fail && (err != nil || id != "window") {
				t.Fatalf("context = %q, %v", id, err)
			}
			if !slices.Equal(released, []proto.RuntimeRemoteObjectID{"document"}) {
				t.Fatalf("released temporary document = %v", released)
			}
		})
	}
}

func TestFrameContextErrorPreservesCause(t *testing.T) {
	for _, method := range []string{"DOM.describeNode", "DOM.resolveNode"} {
		for _, failure := range []struct {
			name    string
			err     error
			changed bool
		}{
			{"stale document", &cdp.Error{Code: -32000, Message: "Node with given id does not belong to the document"}, true},
			{"other protocol error", &cdp.Error{Code: -32000, Message: "unrelated protocol failure"}, false},
			{"cancellation", context.Canceled, false},
		} {
			t.Run(method+"/"+failure.name, func(t *testing.T) {
				browser := New().Context(t.Context()).Client(&lifecycleRegressionClient{call: func(_ context.Context, called string, _ any) ([]byte, error) {
					if called == method {
						return nil, failure.err
					}
					if called == "DOM.describeNode" {
						return []byte(`{"node":{"contentDocument":{"backendNodeId":2,"nodeId":0,"nodeType":0,"nodeName":"","localName":"","nodeValue":""},"nodeId":0,"backendNodeId":0,"nodeType":0,"nodeName":"","localName":"","nodeValue":""}}`), nil
					}
					t.Fatalf("unexpected request: %s", called)
					return nil, nil
				}})
				parent := &Page{browser: browser, ctx: t.Context(), SessionID: "session"}
				frame := &Page{
					browser: browser, ctx: t.Context(), SessionID: "session", FrameID: "child",
					jsCtxLock: new(sync.Mutex), jsCtxID: new(proto.RuntimeRemoteObjectID), helpers: &jsHelperCache{},
					element: &Element{page: parent, ctx: t.Context(), Object: &proto.RuntimeRemoteObject{ObjectID: "iframe"}},
				}
				_, err := frame.getJSCtxID()
				if !errors.Is(err, failure.err) {
					t.Fatalf("lost original error: %v", err)
				}
				if changed := errors.Is(err, ErrFrameContextChanged); changed != failure.changed {
					t.Fatalf("context changed classification = %v, want %v: %v", changed, failure.changed, err)
				}
				if !failure.changed && err != failure.err {
					t.Fatalf("unrelated error was wrapped: %v", err)
				}
			})
		}
	}
}

func TestJSHelperCacheInvalidationAcrossViews(t *testing.T) {
	for _, clearAll := range []bool{false, true} {
		page := &Page{
			ctx: t.Context(), jsCtxLock: &sync.Mutex{}, jsCtxID: new(proto.RuntimeRemoteObjectID("old")),
			helpers: &jsHelperCache{contexts: map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{
				"old": {"element": "old-function"},
			}},
		}
		view := page.Context(t.Context())
		if id, ok := view.getHelper("old", "element"); !ok || id != "old-function" {
			t.Fatal("view did not share the original helper")
		}
		page.helpers.Lock()
		if clearAll {
			page.helpers.contexts = nil
		} else {
			delete(page.helpers.contexts, "old")
		}
		page.helpers.Unlock()
		if view.setHelper("old", "element", "stale-function") {
			t.Fatal("stale helper was accepted after invalidation")
		}
		if _, ok := view.getHelper("old", "element"); ok {
			t.Fatal("view retained a helper after invalidation")
		}
		if len(page.helpers.contexts) != 0 {
			t.Fatal("lookup recreated an invalidated context")
		}
		page.helpers.Lock()
		page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{"current": {}}
		page.helpers.Unlock()
		if !view.setHelper("current", "element", "current-function") {
			t.Fatal("current helper was not cached")
		}
		if id, ok := page.getHelper("current", "element"); !ok || id != "current-function" {
			t.Fatal("new cache was not visible through the original page")
		}
	}
}

func TestJSHelperCacheUnsetInvalidatesSharedContext(t *testing.T) {
	page := &Page{
		ctx: t.Context(), jsCtxLock: &sync.Mutex{}, jsCtxID: new(proto.RuntimeRemoteObjectID("old")),
		helpers: &jsHelperCache{contexts: map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{"old": {}}},
	}
	view := page.Context(t.Context())
	view.unsetJSCtxID()
	if *page.jsCtxID != "" || page.setHelper("old", "element", "stale") {
		t.Fatal("reset did not invalidate the context and its helpers in every view")
	}
}
