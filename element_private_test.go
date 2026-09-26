package rod

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

func TestElementByJSNonElementCleanup(t *testing.T) {
	failed := errors.New("release failed")
	for _, test := range []struct {
		name     string
		result   string
		release  error
		released []proto.RuntimeRemoteObjectID
	}{
		{"object", `{"result":{"type":"object","objectId":"plain"}}`, nil, []proto.RuntimeRemoteObjectID{"plain"}},
		{"failed release", `{"result":{"type":"function","objectId":"plain"}}`, failed, []proto.RuntimeRemoteObjectID{"plain"}},
		{"primitive", `{"result":{"type":"number","value":1}}`, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var released []proto.RuntimeRemoteObjectID
			client := &resolutionClient{respond: func(method string, params any) ([]byte, error) {
				switch method {
				case "Runtime.callFunctionOn":
					return []byte(test.result), nil
				case "Runtime.releaseObject":
					released = append(released, params.(proto.RuntimeReleaseObject).ObjectID)
					return []byte(`{}`), test.release
				}
				t.Fatalf("unexpected request: %s", method)
				return nil, nil
			}}
			page := resolutionPage(t, client, "window")
			*page.jsCtxID = "window"
			el, err := page.ElementByJS(Eval(`() => value`))
			if el != nil || !errors.Is(err, &ExpectElementError{}) {
				t.Fatalf("query = %v, %v", el, err)
			}
			if test.release != nil && !errors.Is(err, test.release) {
				t.Fatalf("lost release failure: %v", err)
			}
			if test.release == nil {
				if _, ok := err.(*ExpectElementError); !ok {
					t.Fatalf("successful cleanup wrapped the error: %T", err)
				}
			}
			if !slices.Equal(released, test.released) {
				t.Fatalf("released %v, want %v", released, test.released)
			}
		})
	}
}

func TestElementByJSLookupFailureCleanup(t *testing.T) {
	injected := errors.New("context check failed")
	var released []proto.RuntimeRemoteObjectID
	client := &resolutionClient{respond: func(method string, params any) ([]byte, error) {
		if _, ok := isContextCheck(params); ok {
			return nil, injected
		}
		switch method {
		case "Runtime.callFunctionOn":
			return []byte(`{"result":{"type":"object","subtype":"node","objectId":"node"}}`), nil
		case "Runtime.releaseObject":
			released = append(released, params.(proto.RuntimeReleaseObject).ObjectID)
			return []byte(`{}`), nil
		}
		t.Fatalf("unexpected request: %s", method)
		return nil, nil
	}}
	page := resolutionPage(t, client, "window")
	*page.jsCtxID = "window"
	// A call on an object of unknown context must look the result's context up.
	el, err := page.ElementByJS(Eval(`() => this`).This(&proto.RuntimeRemoteObject{ObjectID: "other"}))
	if el != nil || !errors.Is(err, injected) || !slices.Equal(released, []proto.RuntimeRemoteObjectID{"node"}) {
		t.Fatalf("query = %v, %v; released %v", el, err, released)
	}
}

func TestElementQueryKnownContext(t *testing.T) {
	node := []byte(`{"result":{"type":"object","subtype":"node","objectId":"node"}}`)

	t.Run("page window unchanged", func(t *testing.T) {
		client := &resolutionClient{respond: func(method string, params any) ([]byte, error) {
			if _, ok := isContextCheck(params); ok || method != "Runtime.callFunctionOn" {
				t.Fatalf("unexpected request: %s %#v", method, params)
			}
			return node, nil
		}}
		page := resolutionPage(t, client, "window")
		*page.jsCtxID = "window"
		el, err := page.ElementByJS(Eval(`() => document.body`))
		if err != nil || el.jsCtxID != "window" || el.page.jsCtxID != page.jsCtxID || len(client.calls) != 1 {
			t.Fatalf("element = %+v, %v; calls %v", el, err, client.calls)
		}
	})

	t.Run("page window resolved by the query", func(t *testing.T) {
		client := &resolutionClient{respond: func(method string, params any) ([]byte, error) {
			switch method {
			case "Runtime.evaluate":
				return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
			case "Runtime.callFunctionOn":
				if _, ok := isContextCheck(params); !ok {
					return node, nil
				}
			}
			t.Fatalf("unexpected request: %s %#v", method, params)
			return nil, nil
		}}
		page := resolutionPage(t, client)
		el, err := page.ElementByJS(Eval(`() => document.body`))
		if err != nil || el.jsCtxID != "window" || !slices.Equal(client.calls, []string{"Runtime.evaluate", "Runtime.callFunctionOn"}) {
			t.Fatalf("element = %+v, %v; calls %v", el, err, client.calls)
		}
	})

	t.Run("page collection", func(t *testing.T) {
		client := &resolutionClient{respond: func(method string, params any) ([]byte, error) {
			switch method {
			case "Runtime.callFunctionOn":
				if _, ok := isContextCheck(params); ok {
					t.Fatal("known collection context was looked up")
				}
				return []byte(`{"result":{"type":"object","subtype":"array","objectId":"array"}}`), nil
			case "Runtime.getProperties":
				return []byte(`{"result":[{"name":"0","value":{"type":"object","subtype":"node","objectId":"node"},"configurable":false,"enumerable":false}]}`), nil
			case "Runtime.releaseObject":
				return []byte(`{}`), nil
			}
			t.Fatalf("unexpected request: %s", method)
			return nil, nil
		}}
		page := resolutionPage(t, client, "window")
		*page.jsCtxID = "window"
		list, err := page.ElementsByJS(Eval(`() => [document.body]`))
		if err != nil || len(list) != 1 || list[0].jsCtxID != "window" {
			t.Fatalf("elements = %v, %v", list, err)
		}
	})

	t.Run("page window replaced during the call", func(t *testing.T) {
		var page *Page
		var checked []proto.RuntimeRemoteObjectID
		client := &resolutionClient{respond: func(_ string, params any) ([]byte, error) {
			if req, ok := isContextCheck(params); ok {
				checked = append(checked, req.ObjectID)
				return []byte(`{"result":{"type":"undefined"}}`), nil
			}
			// A concurrent operation replaces the window after the query was sent.
			page.unsetJSCtxID()
			page.jsCtxLock.Lock()
			*page.jsCtxID = "new-window"
			page.jsCtxLock.Unlock()
			page.helpers.Lock()
			page.helpers.contexts["new-window"] = map[string]proto.RuntimeRemoteObjectID{}
			page.helpers.Unlock()
			return node, nil
		}}
		page = resolutionPage(t, client, "window")
		*page.jsCtxID = "window"
		el, err := page.ElementByJS(Eval(`() => document.body`))
		if err != nil || el.jsCtxID != "new-window" || !slices.Equal(checked, []proto.RuntimeRemoteObjectID{"new-window"}) {
			t.Fatalf("element = %+v, %v; checked %v", el, err, checked)
		}
	})

	for _, cached := range []bool{true, false} {
		t.Run(map[bool]string{true: "element context cached", false: "element context uncached"}[cached], func(t *testing.T) {
			var checked []proto.RuntimeRemoteObjectID
			client := &resolutionClient{respond: func(_ string, params any) ([]byte, error) {
				if req, ok := isContextCheck(params); ok {
					checked = append(checked, req.ObjectID)
					if req.ObjectID != "frame" {
						return nil, errOtherContextForTest
					}
					return []byte(`{"result":{"type":"undefined"}}`), nil
				}
				return node, nil
			}}
			page := resolutionPage(t, client, "window", "frame")
			*page.jsCtxID = "window"
			parent := page.elementInJSCtx(&proto.RuntimeRemoteObject{ObjectID: "parent"}, "window", "frame")
			if !cached {
				parent.jsCtxID = "gone"
			}
			el, err := parent.Parent()
			if err != nil || el.jsCtxID != "frame" || *el.page.jsCtxID != "frame" {
				t.Fatalf("element = %+v, %v", el, err)
			}
			var want []proto.RuntimeRemoteObjectID
			if !cached {
				// The element's view prefers its frame window.
				want = []proto.RuntimeRemoteObjectID{"frame"}
			}
			if !slices.Equal(checked, want) {
				t.Fatalf("checked %v, want %v", checked, want)
			}
		})
	}
}

func TestElementsByJSReleasesUnreturnedHandles(t *testing.T) {
	array := `{"result":{"type":"object","subtype":"array","objectId":"array"}}`
	prototype := `"internalProperties":[{"name":"[[Prototype]]","value":{"type":"object","objectId":"proto"}}]`
	lookupFailed := errors.New("context check failed")
	for _, test := range []struct {
		name   string
		result string
		props  string
		// this runs the query on an object of unknown context, whose check fails.
		this     bool
		wantErr  error
		elements []proto.RuntimeRemoteObjectID
		released []proto.RuntimeRemoteObjectID
	}{
		{
			name:   "members become elements",
			result: array,
			props: `{"result":[
				{"name":"0","configurable":true,"enumerable":true,"value":{"type":"object","subtype":"node","objectId":"n0"}},
				{"name":"1","configurable":true,"enumerable":true,"value":{"type":"object","subtype":"node","objectId":"n1"}},
				{"name":"length","configurable":true,"enumerable":true,"value":{"type":"number","value":2}},
				{"name":"__proto__","configurable":true,"enumerable":true,"value":{"type":"object","objectId":"own-proto"}}
			],` + prototype + `,"privateProperties":[{"name":"#x","value":{"type":"object","objectId":"private"}}]}`,
			elements: []proto.RuntimeRemoteObjectID{"n0", "n1"},
			released: []proto.RuntimeRemoteObjectID{"array", "own-proto", "private", "proto"},
		},
		{
			name:     "non-array result",
			result:   `{"result":{"type":"object","objectId":"plain"}}`,
			wantErr:  &ExpectElementsError{},
			released: []proto.RuntimeRemoteObjectID{"plain"},
		},
		{
			name:   "non-node member",
			result: array,
			props: `{"result":[
				{"name":"0","configurable":true,"enumerable":true,"value":{"type":"object","subtype":"node","objectId":"n0"}},
				{"name":"1","configurable":true,"enumerable":true,"value":{"type":"object","objectId":"plain"}},
				{"name":"2","configurable":true,"enumerable":true,"value":{"type":"object","subtype":"node","objectId":"n2"}}
			],` + prototype + `}`,
			wantErr:  &ExpectElementsError{},
			released: []proto.RuntimeRemoteObjectID{"array", "n0", "n2", "plain", "proto"},
		},
		{
			name:   "accessor member",
			result: array,
			props: `{"result":[
				{"name":"0","configurable":true,"enumerable":true,"get":{"type":"function","objectId":"getter"},"set":{"type":"undefined"}}
			],` + prototype + `}`,
			wantErr:  &ExpectElementsError{},
			released: []proto.RuntimeRemoteObjectID{"array", "getter", "proto"},
		},
		{
			name:     "member without value",
			result:   array,
			props:    `{"result":[{"name":"0","configurable":true,"enumerable":true}]}`,
			wantErr:  &ExpectElementsError{},
			released: []proto.RuntimeRemoteObjectID{"array"},
		},
		{
			name:     "null descriptors",
			result:   array,
			props:    `{"result":[null,{"name":"0","configurable":true,"enumerable":true}],"internalProperties":[null],"privateProperties":[null]}`,
			wantErr:  proto.ErrMissingField,
			released: []proto.RuntimeRemoteObjectID{"array"},
		},
		{
			name:   "first member lookup failure",
			result: array,
			props: `{"result":[
				{"name":"0","configurable":true,"enumerable":true,"value":{"type":"object","subtype":"node","objectId":"n0"}},
				{"name":"1","configurable":true,"enumerable":true,"value":{"type":"object","subtype":"node","objectId":"n1"}}
			],` + prototype + `}`,
			this:     true,
			wantErr:  lookupFailed,
			released: []proto.RuntimeRemoteObjectID{"array", "n0", "n1", "proto"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var lock sync.Mutex
			var released []proto.RuntimeRemoteObjectID
			client := &resolutionClient{respond: func(method string, params any) ([]byte, error) {
				if _, ok := isContextCheck(params); ok {
					return nil, lookupFailed
				}
				switch method {
				case "Runtime.callFunctionOn":
					return []byte(test.result), nil
				case "Runtime.getProperties":
					return []byte(test.props), nil
				case "Runtime.releaseObject":
					lock.Lock()
					released = append(released, params.(proto.RuntimeReleaseObject).ObjectID)
					lock.Unlock()
					return []byte(`{}`), nil
				}
				t.Errorf("unexpected request: %s", method)
				return nil, errors.New("unexpected request")
			}}
			page := resolutionPage(t, client, "window")
			*page.jsCtxID = "window"
			opts := Eval(`() => value`)
			if test.this {
				opts = opts.This(&proto.RuntimeRemoteObject{ObjectID: "other"})
			}
			list, err := page.ElementsByJS(opts)
			if test.wantErr != nil {
				if list != nil || !errors.Is(err, test.wantErr) {
					t.Fatalf("query = %v, %v; want %v", list, err, test.wantErr)
				}
				if _, ok := test.wantErr.(*ExpectElementsError); ok {
					if _, bare := err.(*ExpectElementsError); !bare {
						t.Fatalf("successful cleanup wrapped the error: %T", err)
					}
					_ = err.Error()
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var elements []proto.RuntimeRemoteObjectID
			for _, el := range list {
				elements = append(elements, el.Object.ObjectID)
			}
			if !slices.Equal(elements, test.elements) {
				t.Fatalf("elements %v, want %v", elements, test.elements)
			}
			slices.Sort(released)
			if !slices.Equal(released, test.released) {
				t.Fatalf("released %v, want %v", released, test.released)
			}
		})
	}
}

func TestInteractableReleasesTemporaryObjects(t *testing.T) {
	for _, mode := range []string{"success", "evaluation failure", "covered", "wait", "must"} {
		t.Run(mode, func(t *testing.T) {
			failed := errors.New("contains failed")
			var released []proto.RuntimeRemoteObjectID
			attempts, calls := 0, 0
			client := &lifecycleRegressionClient{call: func(_ context.Context, method string, params any) ([]byte, error) {
				calls++
				switch method {
				case "Runtime.callFunctionOn":
					req := params.(proto.RuntimeCallFunctionOn)
					switch {
					case req.FunctionDeclaration == `() => window`:
						t.Fatal("the pointer check created a temporary window")
					case strings.Contains(req.FunctionDeclaration, "pointerEvents"):
						return []byte(`{"result":{"type":"boolean","value":false}}`), nil
					case strings.Contains(req.FunctionDeclaration, "window.scrollX"):
						return []byte(`{"result":{"type":"object","value":{"x":0,"y":0}}}`), nil
					case strings.Contains(req.FunctionDeclaration, "/* containsElement */"):
						attempts++
						if mode == "evaluation failure" {
							return nil, failed
						}
						if mode == "covered" || mode == "must" || (mode == "wait" && attempts < 3) {
							return []byte(`{"result":{"type":"boolean","value":false}}`), nil
						}
					}
					return []byte(`{"result":{"type":"boolean","value":true}}`), nil
				case "DOM.getContentQuads":
					return []byte(`{"quads":[[0,0,10,0,10,10,0,10]]}`), nil
				case "DOM.getNodeForLocation":
					return []byte(`{"backendNodeId":1,"frameId":""}`), nil
				case "DOM.resolveNode":
					return []byte(`{"object":{"objectId":"temporary-element","type":""}}`), nil
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
			// WaitInteractable also runs the visibility helper.
			page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{
				"window": {"functions": "functions", "containsElement": "containsElement", "tag": "tag", "visible": "visible"},
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
				covered, ok := errors.AsType[*CoveredError](err)
				if !ok || covered.Object.ObjectID != "temporary-element" || slices.Contains(released, "temporary-element") {
					t.Fatalf("covered error must transfer its element to the caller: %v, %v", err, released)
				}
				return
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(released, slices.Repeat([]proto.RuntimeRemoteObjectID{"temporary-element"}, attempts)) {
				t.Fatalf("released %v after %d attempts", released, attempts)
			}
			// Style, shape, scroll, hit test, resolve, containment, and release.
			if mode == "success" && calls != 7 {
				t.Fatalf("pointer check made %d CDP calls, want 7", calls)
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
						t.Fatal("the query created a temporary window")
					}
					if req.ReturnByValue != nil && *req.ReturnByValue {
						return []byte(`{"result":{"type":"boolean","value":true}}`), nil
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
			if !slices.Equal(released, []proto.RuntimeRemoteObjectID{"temporary-element"}) {
				t.Fatalf("released temporary objects = %v", released)
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
			return []byte(`{"node":{"nodeName":"#text","nodeId":0,"backendNodeId":0,"nodeType":0,"localName":"","nodeValue":""}}`), nil
		case "Runtime.callFunctionOn":
			req := params.(proto.RuntimeCallFunctionOn)
			if req.FunctionDeclaration == `() => window` {
				t.Fatal("the conversion created a temporary window")
			}
			if req.ReturnByValue != nil && *req.ReturnByValue {
				return []byte(`{"result":{"type":"boolean","value":true}}`), nil
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
	if !slices.Equal(released, []proto.RuntimeRemoteObjectID{"text", "parent"}) {
		t.Fatalf("cleanup left an unreturned parent handle: %v", released)
	}
}

func TestElementEqualReturnsEvaluationError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	browser := New().Context(ctx).Client(&lifecycleRegressionClient{call: func(ctx context.Context, _ string, _ any) ([]byte, error) {
		return nil, ctx.Err()
	}})
	page := &Page{browser: browser, ctx: ctx, helpers: &jsHelperCache{}}
	element := &Element{ctx: ctx, page: page, Object: &proto.RuntimeRemoteObject{ObjectID: "element"}}
	if equal, err := element.Equal(element); equal || !errors.Is(err, context.Canceled) {
		t.Fatalf("Equal on canceled context = %v, %v", equal, err)
	}
}
