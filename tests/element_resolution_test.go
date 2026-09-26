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
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

const elementResolutionHTML = `<!doctype html>
<script>var marker = 'main'</script>
<button>ok</button><div><span id="main">Main</span></div>
<iframe srcdoc="<script>var marker = 'frame'</script><p><span id=child>Child</span></p>"></iframe>`

func TestElementQueryRoundTrips(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(elementResolutionHTML)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()
	frame := p.MustElement("iframe").MustFrame()
	div := p.MustElement("div")
	span := p.MustElement("#main")
	button := p.MustElement("button")
	// Install the query and containment helpers in both contexts.
	frame.MustElement("#child").MustRelease()
	g.True(button.MustInteractable())

	var calls atomic.Int64
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		calls.Add(1)
		return g.mc.principal.Call(ctx, session, method, params)
	})
	defer g.mc.resetCall()
	for _, test := range []struct {
		name  string
		calls int64
		op    func() *rod.Element
	}{
		{"Page.Element", 1, func() *rod.Element { return p.MustElement("button") }},
		{"Element.Parent", 1, func() *rod.Element { return span.MustParent() }},
		{"Element.Element", 1, func() *rod.Element { return div.MustElement("span") }},
		{"iframe Page.Element", 1, func() *rod.Element { return frame.MustElement("#child") }},
		// Style, shape, scroll, hit test, resolve, containment, and release.
		{"Element.Interactable", 7, func() *rod.Element {
			_, err := button.Interactable()
			g.E(err)
			return nil
		}},
	} {
		before := calls.Load()
		el := test.op()
		if got := calls.Load() - before; got != test.calls {
			t.Errorf("%s made %d CDP calls, want %d", test.name, got, test.calls)
		}
		if el != nil {
			g.E(el.Release())
		}
	}
}

func TestElementFromObjectContexts(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(elementResolutionHTML)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()
	frame := p.MustElement("iframe").MustFrame()
	child, err := frame.Evaluate(rod.Eval(`() => document.querySelector('#child')`).ByObject())
	g.E(err)
	window, err := p.Evaluate(rod.Eval(`() => window`).ByObject())
	g.E(err)
	defer func() { g.E(p.Release(window)) }()

	// Context lookup relies on Chrome rejecting an argument owned by another
	// execution context before the function runs.
	_, err = proto.RuntimeCallFunctionOn{
		ObjectID:            window.ObjectID,
		FunctionDeclaration: `function() {}`,
		Arguments:           []*proto.RuntimeCallArgument{{ObjectID: child.ObjectID}},
	}.Call(p)
	if protocolErr, ok := errors.AsType[*cdp.Error](err); !ok || protocolErr.Code != -32000 ||
		protocolErr.Message != "Argument should belong to the same JavaScript world as target object" {
		t.Fatalf("cross-context argument error = %v", err)
	}

	var windows atomic.Int64
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		if request, ok := params.(proto.RuntimeCallFunctionOn); ok && request.FunctionDeclaration == `() => window` {
			windows.Add(1)
		}
		return g.mc.principal.Call(ctx, session, method, params)
	})
	defer g.mc.resetCall()

	// A frame handle converted through the parent page runs in the frame.
	el, err := p.ElementFromObject(child)
	g.E(err)
	g.Eq(el.MustEval(`() => marker`).Str(), "frame")
	g.Eq(el.MustText(), "Child")
	g.Eq(el.MustParent().MustEval(`() => marker`).Str(), "frame")
	g.Eq(windows.Load(), int64(0))

	// An isolated world gets its own context and helper cache.
	tree, err := proto.PageGetFrameTree{}.Call(p)
	g.E(err)
	world, err := proto.PageCreateIsolatedWorld{FrameID: tree.FrameTree.Frame.ID, WorldName: "rod-test"}.Call(p)
	g.E(err)
	for i, selector := range []string{"#main", "button"} {
		res, err := proto.RuntimeEvaluate{
			Expression: "document.querySelector('" + selector + "')",
			ContextID:  world.ExecutionContextID,
		}.Call(p)
		g.E(err)
		isolated, err := p.ElementFromObject(res.Result)
		g.E(err)
		g.Eq(isolated.MustEval(`() => typeof marker`).Str(), "undefined")
		g.Eq(isolated.MustParent().MustEval(`() => typeof marker`).Str(), "undefined")
		if i == 0 {
			g.Eq(isolated.MustText(), "Main")
		}
		// Only the first handle from the new context caches a window.
		g.Eq(windows.Load(), int64(1))
	}
	g.Eq(p.MustElement("#main").MustEval(`() => marker`).Str(), "main")

	// A released handle reports the missing object rather than a context.
	g.E(frame.Release(child))
	_, err = p.ElementFromObject(child)
	g.Is(err, cdp.ErrObjNotFound)
}

func TestElementFromNodeAfterScriptNavigation(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><p>Old document</p>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustElement("p").MustRelease()
	next := g.html(`<!doctype html><p id="next">New document</p>`)
	// Navigate without Rod noticing, so the page still refers to the old window.
	p.MustEval(`url => { location.href = url }`, next)
	var node *proto.DOMNode
	g.E(utils.Retry(p.GetContext(), rod.DefaultSleeper(), func() (bool, error) {
		doc, err := proto.DOMGetDocument{}.Call(p)
		if err != nil {
			return false, nil
		}
		found, err := proto.DOMQuerySelector{NodeID: doc.Root.NodeID, Selector: "#next"}.Call(p)
		if err != nil || found.NodeID == 0 {
			return false, nil
		}
		node = &proto.DOMNode{NodeID: found.NodeID}
		return true, nil
	}))
	el := p.MustElementFromNode(node)
	g.Eq(el.MustText(), "New document")
	g.Eq(p.MustElement("#next").MustText(), "New document")
}

func TestElementByJSReleasesNonElement(t *testing.T) {
	g := setup(t)
	p := g.page.MustNavigate(g.blank())
	_, err := p.ElementByJS(rod.Eval(`() => ({ answer: 42 })`))
	expected, ok := errors.AsType[*rod.ExpectElementError](err)
	if !ok || expected.ObjectID == "" {
		t.Fatalf("query error = %v", err)
	}
	_, err = p.ObjectToJSON(expected.RuntimeRemoteObject)
	g.Is(err, cdp.ErrObjNotFound)
}

func TestElementsByJSReleasesUnreturnedHandles(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><p>one</p><p>two</p>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()

	// Record every handle that Runtime.getProperties creates.
	var lock sync.Mutex
	var created []proto.RuntimeRemoteObjectID
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		res, err := g.mc.principal.Call(ctx, session, method, params)
		if method == "Runtime.getProperties" && err == nil {
			var list proto.RuntimeGetPropertiesResult
			g.E(json.Unmarshal(res, &list))
			lock.Lock()
			for _, prop := range list.Result {
				for _, obj := range []*proto.RuntimeRemoteObject{prop.Value, prop.Get, prop.Set, prop.Symbol} {
					if obj != nil && obj.ObjectID != "" {
						created = append(created, obj.ObjectID)
					}
				}
			}
			for _, prop := range list.InternalProperties {
				if prop.Value != nil && prop.Value.ObjectID != "" {
					created = append(created, prop.Value.ObjectID)
				}
			}
			lock.Unlock()
		}
		return res, err
	})
	defer g.mc.resetCall()
	alive := func(id proto.RuntimeRemoteObjectID) bool {
		t.Helper()
		_, err := proto.RuntimeCallFunctionOn{ObjectID: id, FunctionDeclaration: `function() {}`}.Call(p)
		if err != nil && !errors.Is(err, cdp.ErrObjNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}
	query := func(js string) (rod.Elements, []proto.RuntimeRemoteObjectID, error) {
		lock.Lock()
		created = nil
		lock.Unlock()
		list, err := p.ElementsByJS(rod.Eval(js))
		lock.Lock()
		defer lock.Unlock()
		return list, created, err
	}

	// Only the members returned as elements stay alive, not the prototype.
	list, handles, err := query(`() => document.querySelectorAll('p')`)
	g.E(err)
	g.Len(list, 2)
	kept := 0
	for _, id := range handles {
		member := list[0].Object.ObjectID == id || list[1].Object.ObjectID == id
		if alive(id) != member {
			t.Fatalf("handle %s alive = %t, returned member = %t", id, !member, member)
		}
		if member {
			kept++
		}
	}
	g.Eq(kept, 2)
	g.Gt(len(handles), 2)
	g.E(list[0].Release())
	g.E(list[1].Release())

	for _, test := range []struct{ name, js string }{
		{"non-array result", `() => ({ answer: 42 })`},
		{"non-node member", `() => [document.body, {}, document.head]`},
		{"accessor member", `() => {
			const list = []
			Object.defineProperty(list, '0', { get() { return document.body }, enumerable: true })
			return list
		}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			list, handles, err := query(test.js)
			expected, ok := errors.AsType[*rod.ExpectElementsError](err)
			if list != nil || !ok {
				t.Fatalf("query = %v, %v", list, err)
			}
			if expected.RuntimeRemoteObject != nil && expected.ObjectID != "" {
				handles = append(handles, expected.ObjectID)
			}
			if len(handles) == 0 {
				t.Fatal("the query created no handles to check")
			}
			for _, id := range handles {
				if alive(id) {
					t.Fatalf("handle %s outlived the failed query: %v", id, err)
				}
			}
			if strings.Contains(test.name, "accessor") && expected.Type != proto.RuntimeRemoteObjectTypeFunction {
				t.Fatalf("accessor member described as %v", expected.RuntimeRemoteObject)
			}
		})
	}
}

func TestFrameViewContextLookup(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html>` + strings.Repeat(`<iframe srcdoc="<p>frame</p>"></iframe>`, 3))).
		Timeout(10 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()
	frame := func(i int) *rod.Page {
		iframes := p.MustElements("iframe")
		for j, iframe := range iframes {
			if j != i {
				iframe.MustRelease()
			}
		}
		return iframes[i].MustFrame()
	}
	// Cache the contexts of all frames.
	for i := range 3 {
		frame(i).MustElement("p").MustRelease()
	}

	var windows, checks atomic.Int64
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		if request, ok := params.(proto.RuntimeCallFunctionOn); ok {
			switch {
			case request.FunctionDeclaration == `() => window`:
				windows.Add(1)
			case request.FunctionDeclaration == `function() {}` && len(request.Arguments) == 1:
				checks.Add(1)
			}
		}
		return g.mc.principal.Call(ctx, session, method, params)
	})
	defer g.mc.resetCall()
	// A new view of a known frame checks that frame's window first.
	for _, i := range []int{0, 2, 1, 0, 1, 2} {
		checksBefore := checks.Load()
		frame(i).MustElement("p").MustRelease()
		if count := checks.Load() - checksBefore; count != 1 {
			t.Errorf("new view of frame %d made %d context checks, want 1", i, count)
		}
	}
	g.Eq(windows.Load(), int64(0))
}

func TestElementFromObjectRequiresHandle(t *testing.T) {
	client := &sessionTestClient{call: func(_ context.Context, _, method string, _ any) ([]byte, error) {
		t.Fatalf("unexpected request: %s", method)
		return nil, nil
	}}
	page := rod.New().Context(t.Context()).Client(client).PageFromSession("session")
	for _, obj := range []*proto.RuntimeRemoteObject{nil, {Type: proto.RuntimeRemoteObjectTypeNumber}} {
		if el, err := page.ElementFromObject(obj); el != nil || !errors.Is(err, &rod.ExpectElementError{}) {
			t.Fatalf("element from %#v = %v, %v", obj, el, err)
		}
	}
}
