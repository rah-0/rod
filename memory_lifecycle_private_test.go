package rod

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

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

func TestInteractableReleasesTemporaryObjects(t *testing.T) {
	for _, mode := range []string{"success", "evaluation failure", "covered", "wait", "must"} {
		t.Run(mode, func(t *testing.T) {
			failed := errors.New("contains failed")
			var released []proto.RuntimeRemoteObjectID
			attempts := 0
			client := &lifecycleRegressionClient{call: func(_ context.Context, method string, params any) ([]byte, error) {
				switch method {
				case "Runtime.callFunctionOn":
					req := params.(proto.RuntimeCallFunctionOn)
					switch {
					case req.FunctionDeclaration == `() => window`:
						return []byte(`{"result":{"objectId":"temporary-window"}}`), nil
					case strings.Contains(req.FunctionDeclaration, "pointerEvents"):
						return []byte(`{"result":{"value":false}}`), nil
					case strings.Contains(req.FunctionDeclaration, "window.scrollX"):
						return []byte(`{"result":{"value":{"x":0,"y":0}}}`), nil
					case strings.Contains(req.FunctionDeclaration, "/* containsElement */"):
						attempts++
						if mode == "evaluation failure" {
							return nil, failed
						}
						if mode == "covered" || mode == "must" || (mode == "wait" && attempts < 3) {
							return []byte(`{"result":{"value":false}}`), nil
						}
					}
					return []byte(`{"result":{"value":true}}`), nil
				case "DOM.getContentQuads":
					return []byte(`{"quads":[[0,0,10,0,10,10,0,10]]}`), nil
				case "DOM.getNodeForLocation":
					return []byte(`{"backendNodeId":1}`), nil
				case "DOM.resolveNode":
					return []byte(`{"object":{"objectId":"temporary-element"}}`), nil
				case "DOM.describeNode":
					return []byte(`{"node":{"nodeName":"DIV"}}`), nil
				case "DOM.scrollIntoViewIfNeeded":
					return []byte(`{}`), nil
				case "Runtime.releaseObject":
					released = append(released, params.(proto.RuntimeReleaseObject).ObjectID)
					return []byte(`{}`), nil
				default:
					t.Fatalf("unexpected request: %s", method)
					return nil, nil
				}
			}}
			browser := New().Context(t.Context()).Client(client)
			page := browser.PageFromSession("session")
			*page.jsCtxID = "window"
			page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{
				"window": {"functions": "functions", "containsElement": "containsElement"},
			}
			element := &Element{
				e: browser.e, ctx: t.Context(), page: page, Object: &proto.RuntimeRemoteObject{ObjectID: "target"},
				sleeper: func() utils.Sleeper { return func(context.Context) error { return nil } },
			}
			var err error
			switch mode {
			case "wait":
				_, err = element.WaitInteractable()
				if attempts != 3 {
					t.Fatalf("wait attempts = %d", attempts)
				}
			case "must":
				if element.MustInteractable() {
					t.Fatal("covered element is interactable")
				}
			default:
				_, err = element.Interactable()
			}
			switch mode {
			case "evaluation failure":
				if !errors.Is(err, failed) {
					t.Fatalf("lost evaluation failure: %v", err)
				}
			case "covered":
				if !errors.Is(err, &CoveredError{}) || slices.Contains(released, "temporary-element") {
					t.Fatalf("covered error must transfer its element to the caller: %v, %v", err, released)
				}
				return
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(released) != attempts*2 {
				t.Fatalf("released %v after %d attempts", released, attempts)
			}
		})
	}
}

func TestTemporaryObjectAlreadyReleased(t *testing.T) {
	for _, cause := range []error{cdp.ErrObjNotFound, cdp.ErrCtxNotFound, cdp.ErrCtxDestroyed, errors.New("transport failed")} {
		t.Run(cause.Error(), func(t *testing.T) {
			browser := New().Context(t.Context()).Client(&lifecycleRegressionClient{call: func(context.Context, string, any) ([]byte, error) {
				return nil, cause
			}})
			err := browser.PageFromSession("session").releaseObject(&proto.RuntimeRemoteObject{ObjectID: "temporary"})
			if _, reclaimed := cause.(*cdp.Error); reclaimed {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, cause) {
				t.Fatalf("lost cleanup error: %v", err)
			}
		})
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
				return []byte(`{"result":{"objectId":"temporary-window"}}`), nil
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
			_, err := page.jsCtxIDByObjectID("element")
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
		return []byte(`{"result":{"value":true}}`), nil
	}}
	page := New().Context(t.Context()).Client(client).PageFromSession("session")
	page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{"parent-window": {}}
	contexts := make(chan proto.RuntimeRemoteObjectID, 2)
	var wg sync.WaitGroup
	for _, id := range []proto.RuntimeRemoteObjectID{"one", "two"} {
		wg.Go(func() {
			context, err := page.jsCtxIDByObjectID(id)
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

func TestPageHTMLReleasesTemporaryObjects(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "canceled read"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failed := errors.New("read failed")
			var released []proto.RuntimeRemoteObjectID
			client := &lifecycleRegressionClient{call: func(ctx context.Context, method string, params any) ([]byte, error) {
				switch method {
				case "Runtime.callFunctionOn":
					req := params.(proto.RuntimeCallFunctionOn)
					if req.FunctionDeclaration == `() => window` {
						return []byte(`{"result":{"objectId":"temporary-window"}}`), nil
					}
					if req.ReturnByValue != nil && *req.ReturnByValue {
						return []byte(`{"result":{"value":true}}`), nil
					}
					return []byte(`{"result":{"type":"object","subtype":"node","objectId":"temporary-element"}}`), nil
				case "DOM.getOuterHTML":
					if fail {
						cancel()
						return nil, failed
					}
					return []byte(`{"outerHTML":"<html></html>"}`), nil
				case "Runtime.releaseObject":
					if err := ctx.Err(); err != nil {
						t.Fatalf("cleanup reused expired operation context: %v", err)
					}
					released = append(released, params.(proto.RuntimeReleaseObject).ObjectID)
					return []byte(`{}`), nil
				default:
					t.Fatalf("unexpected request: %s", method)
					return nil, nil
				}
			}}
			browser := New().Context(ctx).Client(client)
			page := browser.PageFromSession("session")
			page.sessionCtx = t.Context()
			*page.jsCtxID = "window"
			page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{
				"window": {"functions": "functions", "element": "element"},
			}
			result, err := page.HTML()
			if fail && !errors.Is(err, failed) {
				t.Fatalf("HTML lost read failure: %v", err)
			}
			if !fail && (err != nil || result != "<html></html>") {
				t.Fatalf("HTML = %q, %v", result, err)
			}
			if !slices.Equal(released, []proto.RuntimeRemoteObjectID{"temporary-window", "temporary-element"}) {
				t.Fatalf("released temporary objects = %v", released)
			}
		})
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
					return []byte(`{"node":{"contentDocument":{"backendNodeId":2}}}`), nil
				case "DOM.resolveNode":
					return []byte(`{"object":{"objectId":"document"}}`), nil
				case "Runtime.callFunctionOn":
					if fail {
						return nil, failed
					}
					return []byte(`{"result":{"objectId":"window"}}`), nil
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

func TestElementFromNodeReleasesUnreturnedParent(t *testing.T) {
	failed := errors.New("release text failed")
	var released []proto.RuntimeRemoteObjectID
	client := &lifecycleRegressionClient{call: func(_ context.Context, method string, params any) ([]byte, error) {
		switch method {
		case "DOM.resolveNode":
			return []byte(`{"object":{"type":"object","subtype":"node","objectId":"text"}}`), nil
		case "DOM.describeNode":
			return []byte(`{"node":{"nodeName":"#text"}}`), nil
		case "Runtime.callFunctionOn":
			req := params.(proto.RuntimeCallFunctionOn)
			if req.FunctionDeclaration == `() => window` {
				return []byte(`{"result":{"objectId":"temporary-window"}}`), nil
			}
			if req.ReturnByValue != nil && *req.ReturnByValue {
				return []byte(`{"result":{"value":true}}`), nil
			}
			return []byte(`{"result":{"type":"object","subtype":"node","objectId":"parent"}}`), nil
		case "Runtime.releaseObject":
			id := params.(proto.RuntimeReleaseObject).ObjectID
			released = append(released, id)
			if id == "text" {
				return nil, failed
			}
			return []byte(`{}`), nil
		default:
			t.Fatalf("unexpected request: %s", method)
			return nil, nil
		}
	}}
	page := New().Context(t.Context()).Client(client).PageFromSession("session")
	*page.jsCtxID = "window"
	page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{"window": {}}
	element, err := page.ElementFromNode(&proto.DOMNode{BackendNodeID: 1})
	if element != nil || !errors.Is(err, failed) {
		t.Fatalf("conversion returned an element after failed temporary cleanup: %v, %v", element, err)
	}
	if !slices.Equal(released, []proto.RuntimeRemoteObjectID{"temporary-window", "temporary-window", "text", "parent"}) {
		t.Fatalf("cleanup left an unreturned parent handle: %v", released)
	}
}
