package rod

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

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
	for _, mode := range []string{
		"success", "evaluation failure", "covered", "wait", "must",
		"pseudo success", "pseudo covered", "pseudo diagnostic failure", "pseudo diagnostic non-string", "pseudo wait", "pseudo must",
	} {
		t.Run(mode, func(t *testing.T) {
			pseudo := strings.HasPrefix(mode, "pseudo ")
			mode = strings.TrimPrefix(mode, "pseudo ")
			failed := errors.New("contains failed")
			var released []proto.RuntimeRemoteObjectID
			attempts, calls, diagnostics := 0, 0, 0
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
						if mode == "covered" || strings.HasPrefix(mode, "diagnostic ") || mode == "must" || (mode == "wait" && attempts < 3) {
							return []byte(`{"result":{"type":"boolean","value":false}}`), nil
						}
					case strings.Contains(req.FunctionDeclaration, "this.element"):
						diagnostics++
						if req.ObjectID != "temporary-element" || req.ReturnByValue == nil || !*req.ReturnByValue {
							t.Fatal("pseudo-element description must use its existing handle and return by value")
						}
						if mode == "diagnostic failure" {
							return nil, errors.New("description failed")
						}
						if mode == "diagnostic non-string" {
							return []byte(`{"result":{"type":"boolean","value":true}}`), nil
						}
						return []byte(`{"result":{"type":"string","value":"div#cover::after"}}`), nil
					}
					return []byte(`{"result":{"type":"boolean","value":true}}`), nil
				case "DOM.getContentQuads":
					return []byte(`{"quads":[[0,0,10,0,10,10,0,10]]}`), nil
				case "DOM.getNodeForLocation":
					return []byte(`{"backendNodeId":1,"frameId":""}`), nil
				case "DOM.resolveNode":
					if pseudo {
						return []byte(`{"object":{"objectId":"temporary-element","type":"object","className":"CSSPseudoElement","description":"CSSPseudoElement"}}`), nil
					}
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
			wantDiagnostics := 0
			if pseudo && mode != "success" {
				wantDiagnostics = attempts
				if mode == "wait" {
					wantDiagnostics--
				}
			}
			if diagnostics != wantDiagnostics {
				t.Fatalf("pseudo-element descriptions = %d, want %d", diagnostics, wantDiagnostics)
			}
			switch mode {
			case "evaluation failure":
				if !errors.Is(err, failed) {
					t.Fatalf("lost evaluation failure: %v", err)
				}
			case "covered", "diagnostic failure", "diagnostic non-string":
				covered, ok := errors.AsType[*CoveredError](err)
				if !ok || covered.Object.ObjectID != "temporary-element" || slices.Contains(released, "temporary-element") {
					t.Fatalf("covered error must transfer its element to the caller: %v, %v", err, released)
				}
				if pseudo {
					wantDescription := "div#cover::after"
					if strings.HasPrefix(mode, "diagnostic ") {
						wantDescription = "CSSPseudoElement"
					}
					if covered.Object.Description != wantDescription {
						t.Fatalf("pseudo-element description = %q, want %q", covered.Object.Description, wantDescription)
					}
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

func TestWaitInteractableRetriesMovingElement(t *testing.T) {
	// The scripted viewport is 844 px high. Each try reads the shape twice for
	// the stability wait and once for the hit test. A try without a point in
	// the viewport reads it once more, and so does a try that finds a point
	// after such a try. A sleep takes 100 ms, and so does a slow answer to a
	// shape request; nothing else takes time.
	const (
		below    = `{"quads":[[61,843.5,354,843.5,354,875.5,61,875.5]]}`
		moved    = `{"quads":[[61,841.5,354,841.5,354,873.5,61,873.5]]}`
		nearer   = `{"quads":[[61,830,354,830,354,862,61,862]]}`
		entering = `{"quads":[[61,800,354,800,354,832,61,832]]}`
		inside   = `{"quads":[[61,400,354,400,354,432,61,432]]}`
		none     = `{"quads":[]}`
		// These fail the request; canceled also cancels the context.
		failed   = ""
		canceled = "canceled"
		// A shape with this prefix is a slow answer.
		slow = "slow"
	)
	shapeFailed := errors.New("shape failed")
	sleepFailed := errors.New("sleep failed")
	for _, test := range []struct {
		name string
		// shapes are the DOM.getContentQuads results in call order.
		shapes []string
		// covered is the try whose hit test finds another element.
		covered int
		// ends is the sleep that ends the wait, with the error end. For
		// context.Canceled, it cancels the context of the element first.
		ends          int
		end           error
		tries, sleeps int
		// outOfView expects an InvisibleShapeError and with another error.
		// A point is expected without them; with outOfView only, the error
		// is the InvisibleShapeError itself.
		outOfView bool
		with      error
	}{
		{"in view", []string{inside, inside, inside}, 0, 0, nil, 1, 0, false, nil},
		// The point is returned by the first try that has read the same shape
		// in view for 300 ms.
		{"moving into view", slices.Concat(
			[]string{below, below, moved, moved}, slices.Repeat([]string{nearer}, 4),
			slices.Repeat([]string{inside}, 16),
		), 0, 0, nil, 6, 5, false, nil},
		// The target rests in view between two steps.
		{"stepping into view", slices.Concat(
			slices.Repeat([]string{below}, 4), slices.Repeat([]string{entering}, 4),
			slices.Repeat([]string{inside}, 16),
		), 0, 0, nil, 6, 5, false, nil},
		// A try that finds the target covered starts the 300 ms again.
		{"covered on the way", slices.Concat(
			slices.Repeat([]string{below}, 4), slices.Repeat([]string{inside}, 4),
			slices.Repeat([]string{inside}, 3), slices.Repeat([]string{inside}, 16),
		), 3, 0, nil, 7, 6, false, nil},
		// So does a try that finds the target out of view again.
		{"out of view again", slices.Concat(
			slices.Repeat([]string{below}, 4), slices.Repeat([]string{inside}, 4),
			slices.Repeat([]string{moved}, 4), slices.Repeat([]string{inside}, 16),
		), 0, 0, nil, 7, 6, false, nil},
		// The time that an answer takes to arrive is not time at rest.
		{"slow answer", slices.Concat(
			slices.Repeat([]string{below}, 4), slices.Repeat([]string{inside}, 11),
			[]string{slow + inside}, slices.Repeat([]string{inside}, 4),
		), 0, 0, nil, 5, 4, false, nil},
		{"out of view", slices.Repeat([]string{below}, 8), 0, 0, nil, 2, 1, true, nil},
		// The target moves between the first two tries and then stays in place.
		{"moved out of view", slices.Concat(
			slices.Repeat([]string{below}, 4), slices.Repeat([]string{moved}, 8),
		), 0, 0, nil, 3, 2, true, nil},
		{"no shape", slices.Repeat([]string{none}, 8), 0, 0, nil, 2, 1, true, nil},
		// An error that is not from the context is returned as it is.
		{"shape error", []string{below, below, below, failed}, 0, 0, nil, 1, 0, false, shapeFailed},
		{"shape error in the next try", []string{below, below, below, below, failed}, 0, 0, nil, 1, 1, false, shapeFailed},
		{"shape error in view", []string{below, below, below, below, inside, inside, inside, failed}, 0, 0, nil, 2, 1, false, shapeFailed},
		{"sleeper error", slices.Repeat([]string{below}, 4), 0, 1, sleepFailed, 1, 1, true, nil},
		// A sleeper that gives up can return the error of a context of its own.
		{"sleeper error of its own context", slices.Repeat([]string{below}, 4), 0, 1, context.DeadlineExceeded, 1, 1, true, nil},
		{"sleeper error in view", slices.Concat(
			slices.Repeat([]string{below}, 4), slices.Repeat([]string{inside}, 4),
		), 0, 2, sleepFailed, 2, 2, true, nil},
		// A covered element ends with the error of the sleeper.
		{"sleeper error when covered", slices.Concat(
			slices.Repeat([]string{below}, 4), slices.Repeat([]string{inside}, 3),
		), 2, 2, sleepFailed, 2, 2, false, sleepFailed},
		{"canceled", slices.Repeat([]string{below}, 4), 0, 1, context.Canceled, 1, 1, true, context.Canceled},
		{"canceled in the first try", []string{below, below, below, canceled}, 0, 0, nil, 1, 0, true, context.Canceled},
		{"canceled in the next try", []string{below, below, below, below, canceled}, 0, 0, nil, 1, 1, true, context.Canceled},
		{"canceled in view", slices.Concat(
			slices.Repeat([]string{below}, 4), slices.Repeat([]string{inside}, 4),
		), 0, 2, context.Canceled, 2, 2, true, context.Canceled},
		// The context ends the wait after an earlier try found no point in the
		// viewport.
		{"canceled when covered", slices.Concat(
			slices.Repeat([]string{below}, 4), slices.Repeat([]string{inside}, 3),
		), 2, 2, context.Canceled, 2, 2, true, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				shapes, tries, sleeps, resolved := 0, 0, 0, 0
				var released []proto.RuntimeRemoteObjectID
				client := &lifecycleRegressionClient{call: func(_ context.Context, method string, params any) ([]byte, error) {
					switch method {
					case "Runtime.callFunctionOn":
						req := params.(proto.RuntimeCallFunctionOn)
						switch {
						case strings.Contains(req.FunctionDeclaration, "pointerEvents"):
							return []byte(`{"result":{"type":"boolean","value":false}}`), nil
						case strings.Contains(req.FunctionDeclaration, "window.scrollX"):
							return []byte(`{"result":{"type":"object","value":{"x":0,"y":0}}}`), nil
						case strings.Contains(req.FunctionDeclaration, "/* containsElement */") && tries == test.covered:
							return []byte(`{"result":{"type":"boolean","value":false}}`), nil
						}
						// Visibility, animation frame, and containment.
						return []byte(`{"result":{"type":"boolean","value":true}}`), nil
					case "DOM.getContentQuads":
						if shapes == len(test.shapes) {
							t.Fatal("unexpected shape request")
						}
						shape := test.shapes[shapes]
						shapes++
						if answer, ok := strings.CutPrefix(shape, slow); ok {
							time.Sleep(100 * time.Millisecond)
							shape = answer
						}
						switch shape {
						case failed:
							return nil, shapeFailed
						case canceled:
							cancel()
							return nil, ctx.Err()
						}
						return []byte(shape), nil
					case "DOM.scrollIntoViewIfNeeded":
						tries++
						return []byte(`{}`), nil
					case "DOM.getNodeForLocation":
						if params.(proto.DOMGetNodeForLocation).Y >= 844 {
							return nil, cdp.ErrNodeNotFoundAtPos
						}
						return []byte(`{"backendNodeId":1,"frameId":""}`), nil
					case "DOM.resolveNode":
						resolved++
						return []byte(`{"object":{"objectId":"temporary-element","type":""}}`), nil
					case "Runtime.releaseObject":
						released = append(released, params.(proto.RuntimeReleaseObject).ObjectID)
						return []byte(`{}`), nil
					default:
						t.Fatalf("unexpected request: %s", method)
						return nil, nil
					}
				}}
				browser := New().Context(ctx).Client(client)
				page := browser.PageFromSession("session")
				*page.jsCtxID = "window"
				page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{
					"window": {"functions": "functions", "containsElement": "containsElement", "tag": "tag", "visible": "visible"},
				}
				element := &Element{
					e: browser.e, ctx: ctx, page: page, Object: &proto.RuntimeRemoteObject{ObjectID: "target"},
					sleeper: func() utils.Sleeper {
						return func(ctx context.Context) error {
							sleeps++
							if sleeps != test.ends {
								time.Sleep(100 * time.Millisecond)
								return nil
							}
							if test.end == context.Canceled {
								cancel()
								return ctx.Err()
							}
							return test.end
						}
					},
				}
				point, err := element.WaitInteractable()
				_, alone := err.(*InvisibleShapeError)
				switch {
				case !test.outOfView && test.with == nil:
					if err != nil || point == nil || *point != proto.NewPoint(207.5, 416) {
						t.Fatalf("WaitInteractable = %v, %v", point, err)
					}
				case errors.Is(err, &InvisibleShapeError{}) != test.outOfView, alone != (test.with == nil),
					test.with != nil && !errors.Is(err, test.with):
					t.Fatalf("WaitInteractable error = %T %v; want InvisibleShapeError: %v, with %v", err, err, test.outOfView, test.with)
				}
				if !slices.Equal(released, slices.Repeat([]proto.RuntimeRemoteObjectID{"temporary-element"}, resolved)) {
					t.Fatalf("released %v of %d resolved nodes", released, resolved)
				}
				if shapes != len(test.shapes) || tries != test.tries || sleeps != test.sleeps {
					t.Fatalf("shape requests = %d, tries = %d, sleeps = %d; want %d, %d, %d",
						shapes, tries, sleeps, len(test.shapes), test.tries, test.sleeps)
				}
			})
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

// Page.HTML reads the html element through the DOM domain and releases the
// handle of every query, also when the read fails or the context ends. It
// sleeps with the page's sleeper while the document has no html element, and
// queries again at once when the document is replaced during the query or
// before the read.
func TestPageHTMLReleasesTemporaryObjects(t *testing.T) {
	const missing = `{"result":{"type":"object","subtype":"null","value":null}}`
	// destroyed and navigated stand for queries that end with the errors of a
	// document that is replaced during them.
	const destroyed, navigated = "destroyed", "navigated"
	element := func(id string) string {
		return `{"result":{"type":"object","subtype":"node","objectId":"` + id + `"}}`
	}
	ids := func(ids ...proto.RuntimeRemoteObjectID) []proto.RuntimeRemoteObjectID { return ids }
	failed := errors.New("request failed")
	releaseFailed := errors.New("release failed")
	queryErrors := map[string]error{
		"":        failed,
		destroyed: cdp.ErrCtxDestroyed,
		navigated: &cdp.Error{Code: -32000, Message: "Inspected target navigated or closed"},
	}
	for _, test := range []struct {
		name string
		// queries are the results of the queries for the html element in call
		// order; an empty entry, destroyed, and navigated fail the call.
		queries []string
		// reads are the errors of the DOM.getOuterHTML calls in call order; nil
		// returns the markup.
		reads []error
		// cancel ends the page's context during the last read, and closed ends
		// its session. In a case without reads, they do so during the last
		// query.
		cancel, closed bool
		release        error
		sleeper        func() utils.Sleeper
		want           string
		wantErr        error
		released       []proto.RuntimeRemoteObjectID
		slept          int
	}{
		{name: "success", queries: []string{element("a")}, reads: []error{nil}, want: "<html></html>", released: ids("a")},
		{name: "element missing", queries: []string{missing, missing, element("a")}, reads: []error{nil},
			want: "<html></html>", released: ids("a"), slept: 2},
		{name: "sleeper error", queries: []string{missing}, sleeper: NotFoundSleeper, wantErr: &ElementNotFoundError{}},
		{name: "failed query", queries: []string{""}, wantErr: failed},
		{name: "not an element", queries: []string{`{"result":{"type":"object","objectId":"plain"}}`},
			wantErr: &ExpectElementError{}, released: ids("plain")},
		{name: "not an object", queries: []string{`{"result":{"type":"undefined"}}`}, wantErr: &ExpectElementError{}},
		{name: "failed read", queries: []string{element("a")}, reads: []error{failed}, wantErr: failed, released: ids("a")},
		{name: "canceled read", queries: []string{element("a")}, reads: []error{failed}, cancel: true,
			wantErr: failed, released: ids("a")},
		{name: "failed release", queries: []string{element("a")}, reads: []error{nil}, release: releaseFailed,
			want: "<html></html>", wantErr: releaseFailed, released: ids("a")},
		{name: "replaced during the query", queries: []string{destroyed, navigated, element("a")}, reads: []error{nil},
			want: "<html></html>", released: ids("a")},
		{name: "replaced during the query until the context ends", queries: []string{destroyed, navigated}, cancel: true,
			wantErr: context.Canceled},
		{name: "replaced during the query until the page closes", queries: []string{navigated, destroyed}, closed: true,
			wantErr: context.Canceled},
		{name: "replaced document", queries: []string{element("a"), element("b"), element("c"), element("d")},
			reads: []error{cdp.ErrCtxNotFound, cdp.ErrCtxDestroyed, cdp.ErrObjNotFound, nil},
			want:  "<html></html>", released: ids("a", "b", "c", "d")},
		{name: "replaced document and failed release", queries: []string{element("a")}, reads: []error{cdp.ErrCtxNotFound},
			release: releaseFailed, wantErr: releaseFailed, released: ids("a")},
		{name: "replaced until the context ends", queries: []string{element("a"), element("b"), element("c")},
			reads: []error{cdp.ErrCtxNotFound, cdp.ErrCtxNotFound, cdp.ErrCtxNotFound}, cancel: true,
			wantErr: context.Canceled, released: ids("a", "b", "c")},
		// The handles of a closed page end with its session.
		{name: "replaced until the page closes", queries: []string{element("a"), element("b")},
			reads: []error{cdp.ErrCtxNotFound, cdp.ErrCtxDestroyed}, closed: true,
			wantErr: context.Canceled, released: ids("a")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sessionCtx, closeSession := context.WithCancel(t.Context())
			defer closeSession()
			queries, reads, slept := 0, 0, 0
			var released []proto.RuntimeRemoteObjectID
			end := func() {
				if test.cancel {
					cancel()
				}
				if test.closed {
					closeSession()
				}
			}
			client := &lifecycleRegressionClient{call: func(ctx context.Context, method string, params any) ([]byte, error) {
				switch method {
				case "Runtime.callFunctionOn":
					if queries == len(test.queries) {
						t.Fatal("unexpected query")
					}
					query := test.queries[queries]
					queries++
					if queries == len(test.queries) && len(test.reads) == 0 {
						end()
					}
					if err, fails := queryErrors[query]; fails {
						return nil, err
					}
					return []byte(query), nil
				case "DOM.getOuterHTML":
					if reads == len(test.reads) {
						t.Fatal("unexpected read")
					}
					err := test.reads[reads]
					reads++
					if reads == len(test.reads) {
						end()
					}
					if err != nil {
						return nil, err
					}
					return []byte(`{"outerHTML":"<html></html>"}`), nil
				case "Runtime.releaseObject":
					if err := ctx.Err(); err != nil {
						t.Fatalf("cleanup reused expired operation context: %v", err)
					}
					released = append(released, params.(proto.RuntimeReleaseObject).ObjectID)
					if test.release != nil {
						return nil, test.release
					}
					return []byte(`{}`), nil
				default:
					t.Fatalf("unexpected request: %s", method)
				}
				return nil, nil
			}}
			page := New().Context(ctx).Client(client).PageFromSession("session")
			page.sessionCtx = sessionCtx
			*page.jsCtxID = "window"
			sleeper := test.sleeper
			if sleeper == nil {
				sleeper = func() utils.Sleeper {
					return func(context.Context) error { slept++; return nil }
				}
			}
			result, err := page.Sleeper(sleeper).HTML()
			if result != test.want || !errors.Is(err, test.wantErr) {
				t.Fatalf("HTML = %q, %v", result, err)
			}
			if queries != len(test.queries) || reads != len(test.reads) || slept != test.slept {
				t.Fatalf("queries = %d, reads = %d, sleeps = %d", queries, reads, slept)
			}
			if !slices.Equal(released, test.released) {
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
