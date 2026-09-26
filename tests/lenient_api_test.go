package rod_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"runtime/debug"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

// lenientClient is an endpoint that answers each command with the response or
// error of answer, or else with the JSON in responses or with {}, and sends the
// events queued in triggers when their command is called. It records the
// parameters of each command.
type lenientClient struct {
	events chan *cdp.Event

	mu        sync.Mutex
	answer    func(method string, params any) (string, error)
	responses map[string]string
	triggers  map[string][]*cdp.Event
	calls     map[string][]any
}

func (c *lenientClient) Event() <-chan *cdp.Event { return c.events }

func (c *lenientClient) Call(_ context.Context, _, method string, params any) ([]byte, error) {
	c.mu.Lock()
	c.calls[method] = append(c.calls[method], params)
	answer := c.answer
	response, ok := c.responses[method]
	events := c.triggers[method]
	delete(c.triggers, method)
	c.mu.Unlock()
	for _, event := range events {
		c.events <- event
	}
	if answer != nil {
		if answered, err := answer(method, params); err != nil {
			return nil, err
		} else if answered != "" {
			return []byte(answered), nil
		}
	}
	if !ok {
		response = `{}`
	}
	return []byte(response), nil
}

// params returns the parameters of the calls of method.
func (c *lenientClient) params(method string) []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[method]
}

// lenientSetup answers the commands that attach the test page and resolve
// its window with complete data.
var lenientSetup = map[string]string{
	"Target.attachToTarget":  `{"sessionId":"session"}`,
	"Runtime.evaluate":       `{"result":{"type":"object","objectId":"window"}}`,
	"Runtime.callFunctionOn": `{"result":{"type":"undefined"}}`,
}

// lenientEnv is a browser connected to a lenientClient, an attached page of
// session "session", and an element of that page.
type lenientEnv struct {
	client  *lenientClient
	browser *rod.Browser
	page    *rod.Page
	element *rod.Element
}

func newLenientEnv(t *testing.T, ctx context.Context, mode proto.Decoding, test lenientCase) *lenientEnv {
	t.Helper()
	client := &lenientClient{
		events:    make(chan *cdp.Event, 16),
		responses: maps.Clone(lenientSetup),
		triggers:  map[string][]*cdp.Event{},
		calls:     map[string][]any{},
	}
	browser := rod.New().Context(ctx).Monitor("").Trace(false).SlowMotion(0).NoDefaultDevice().Client(client).Decoding(mode)
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	page, err := browser.PageFromTarget("page")
	if err != nil {
		t.Fatal(err)
	}
	element, err := page.ElementFromObject(&proto.RuntimeRemoteObject{
		Type: proto.RuntimeRemoteObjectTypeObject, Subtype: proto.RuntimeRemoteObjectSubtypeNode, ObjectID: "element",
	})
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	maps.Copy(client.responses, test.responses)
	client.answer = test.answer
	client.calls = map[string][]any{}
	client.mu.Unlock()
	return &lenientEnv{client: client, browser: browser, page: page, element: element}
}

// emit sends an event of the session, "" for the browser.
func (env *lenientEnv) emit(session, method, params string) {
	env.client.events <- &cdp.Event{SessionID: session, Method: method, Params: json.RawMessage(params)}
}

// respond answers the next calls of command with response.
func (env *lenientEnv) respond(command, response string) {
	env.client.mu.Lock()
	defer env.client.mu.Unlock()
	env.client.responses[command] = response
}

// after sends an event of the session when command is next called.
func (env *lenientEnv) after(command, session, method, params string) {
	env.client.mu.Lock()
	defer env.client.mu.Unlock()
	env.client.triggers[command] = append(env.client.triggers[command],
		&cdp.Event{SessionID: session, Method: method, Params: json.RawMessage(params)})
}

// lenientDone is the result of a method that returns only an error.
func lenientDone(err error) (any, error) { return struct{}{}, err }

// lenientCase calls one API of a browser whose endpoint answers commands with
// {} unless answer or responses has another answer.
type lenientCase struct {
	name string
	// responses answer these commands instead of {}.
	responses map[string]string
	// answer, when it is set, answers the calls for which it returns a
	// response or an error instead of responses.
	answer func(method string, params any) (string, error)
	run    func(*lenientEnv) (any, error)
	// missing is the "Type.path" that the MissingFieldError of the call
	// names with lenient decoding. With strict decoding, the call returns
	// an error matching proto.ErrMissingField, for this or an earlier field.
	missing string
	// wantErr, when missing is empty, is the error the call returns;
	// otherwise the call succeeds with a non-nil result.
	wantErr error
	// lenientOnly skips strict decoding, which rejects the endpoint's data
	// in another way.
	lenientOnly bool
	check       func(*testing.T, *lenientEnv)
}

const (
	lenientArrayResult   = `{"result":{"type":"object","subtype":"array","objectId":"list"}}`
	lenientLayoutMetrics = `{"cssContentSize":{"x":0,"y":0,"width":10,"height":10},` +
		`"cssVisualViewport":{"offsetX":0,"offsetY":0,"pageX":0,"pageY":0,"clientWidth":10,"clientHeight":10,"scale":1}}`
	lenientNode   = `{"node":{"nodeId":1,"backendNodeId":1,"nodeType":1,"nodeName":"DIV","localName":"div","nodeValue":""}}`
	lenientTarget = `{"targetId":"top","type":"page","title":"","url":"","attached":false}`
)

// lenientNoResult makes JavaScript evaluation return no result object.
var lenientNoResult = map[string]string{"Runtime.callFunctionOn": `{}`}

var lenientCases = []lenientCase{
	// JavaScript evaluation and queries.
	{name: "Page.Eval", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.page.Eval(`() => 1`) }},
	{name: "Page.Eval/window", responses: map[string]string{"Runtime.evaluate": `{}`}, missing: "RuntimeEvaluateResult.result",
		run: func(env *lenientEnv) (any, error) { return env.browser.PageFromSession("other").Eval(`() => 1`) }},
	{name: "Page.Evaluate", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.page.Evaluate(rod.Eval(`() => document`).ByObject()) }},
	{name: "Page.EvalJSON", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) {
			var value int
			return lenientDone(env.page.EvalJSON(&value, `() => 1`))
		}},
	{name: "Page.ObjectToJSON", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) {
			return env.page.ObjectToJSON(&proto.RuntimeRemoteObject{Type: proto.RuntimeRemoteObjectTypeObject, ObjectID: "object"})
		}},
	{name: "Page.Wait", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.page.Wait(rod.Eval(`() => true`))) }},
	{name: "Page.WaitLoad", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.page.WaitLoad()) }},
	{name: "Page.WaitDOMStable", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.page.WaitDOMStable(time.Second, 0)) }},
	{name: "Page.NavigateBack", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.page.NavigateBack()) }},
	{name: "Page.Element", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.page.Element("a") }},
	{name: "Page.Elements", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.page.Elements("a") }},
	{name: "Page.Has", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) {
			_, el, err := env.page.Has("a")
			return el, err
		}},
	{name: "Page.HTML", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.page.HTML() }},
	{name: "Page.Race", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.page.Race().Element("a").Do() }},
	{name: "Page.ElementsByJS/null entry", missing: "RuntimeGetPropertiesResult.result[0]",
		responses: map[string]string{"Runtime.callFunctionOn": lenientArrayResult, "Runtime.getProperties": `{"result":[null]}`},
		run:       func(env *lenientEnv) (any, error) { return env.page.ElementsByJS(rod.Eval(`() => []`)) }},
	{name: "Page.ElementsByJS/no list", missing: "RuntimeGetPropertiesResult.result",
		responses: map[string]string{"Runtime.callFunctionOn": lenientArrayResult},
		run:       func(env *lenientEnv) (any, error) { return env.page.ElementsByJS(rod.Eval(`() => []`)) }},
	{name: "Page.Search", missing: "DOMGetSearchResultsResult.nodeIds",
		responses: map[string]string{"DOM.performSearch": `{"searchId":"search","resultCount":1}`},
		run:       func(env *lenientEnv) (any, error) { return env.page.Search("a") }},
	{name: "Page.Search/no search ID", missing: "DOMPerformSearchResult.searchId",
		responses: map[string]string{"DOM.performSearch": `{"resultCount":1}`},
		answer: func(_ string, params any) (string, error) {
			if search, ok := params.(proto.DOMGetSearchResults); ok && search.SearchID == "" {
				return "", cdp.ErrSearchSessionNotFound
			}
			return "", nil
		},
		run: func(env *lenientEnv) (any, error) { return env.page.Search("a") }},
	{name: "SearchResult.Get", missing: "DOMGetSearchResultsResult.nodeIds",
		responses: map[string]string{
			"DOM.performSearch":    `{"searchId":"search","resultCount":1}`,
			"DOM.getSearchResults": `{"nodeIds":[1]}`,
			"DOM.resolveNode":      `{"object":{"type":"object","subtype":"node","objectId":"found"}}`,
			"DOM.describeNode":     lenientNode,
		},
		run: func(env *lenientEnv) (any, error) {
			result, err := env.page.Search("a")
			if err != nil {
				return nil, err
			}
			env.respond("DOM.getSearchResults", `{}`)
			list, err := result.Get(0, 1)
			return list, errors.Join(err, result.Release())
		}},
	{name: "Page.ElementFromObject/other world", missing: "RuntimeCallFunctionOnResult.result",
		answer: func(_ string, params any) (string, error) {
			call, _ := params.(proto.RuntimeCallFunctionOn)
			switch call.FunctionDeclaration {
			case `function() {}`: // the object is not in the cached window's world
				return "", &cdp.Error{Code: -32000, Message: "Argument should belong to the same JavaScript world as target object"}
			case `() => window`:
				return `{}`, nil
			}
			return "", nil
		},
		run: func(env *lenientEnv) (any, error) {
			return env.page.ElementFromObject(&proto.RuntimeRemoteObject{
				Type: proto.RuntimeRemoteObjectTypeObject, Subtype: proto.RuntimeRemoteObjectSubtypeNode, ObjectID: "other",
			})
		}},
	{name: "Page.WaitDOMStable/no start result", missing: "RuntimeCallFunctionOnResult.result",
		answer: func(_ string, params any) (string, error) {
			if call, ok := params.(proto.RuntimeCallFunctionOn); ok {
				if call.FunctionDeclaration == `function() { return this.start() }` {
					return `{}`, nil
				}
				return `{"result":{"type":"object","objectId":"waiter"}}`, nil
			}
			return "", nil
		},
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.page.WaitDOMStable(time.Second, 0)) }},
	{name: "Page.ElementFromNode", missing: "DOMResolveNodeResult.object",
		run: func(env *lenientEnv) (any, error) { return env.page.ElementFromNode(&proto.DOMNode{BackendNodeID: 1}) }},
	{name: "Page.ElementFromPoint", missing: "DOMResolveNodeResult.object",
		responses: map[string]string{"DOM.getNodeForLocation": `{"backendNodeId":1,"frameId":"frame"}`},
		run:       func(env *lenientEnv) (any, error) { return env.page.ElementFromPoint(1, 1) }},

	// Elements.
	{name: "Element.Describe", missing: "DOMDescribeNodeResult.node",
		run: func(env *lenientEnv) (any, error) { return env.element.Describe(1, false) }},
	{name: "Element.ShadowRoot/no node", missing: "DOMDescribeNodeResult.node",
		run: func(env *lenientEnv) (any, error) { return env.element.ShadowRoot() }},
	{name: "Element.ShadowRoot/null root", missing: "DOMDescribeNodeResult.node.shadowRoots[0]",
		responses: map[string]string{"DOM.describeNode": `{"node":{"shadowRoots":[null]}}`},
		run:       func(env *lenientEnv) (any, error) { return env.element.ShadowRoot() }},
	{name: "Element.ShadowRoot/no object", missing: "DOMResolveNodeResult.object",
		responses: map[string]string{"DOM.describeNode": `{"node":{"shadowRoots":[{"backendNodeId":5}]}}`},
		run:       func(env *lenientEnv) (any, error) { return env.element.ShadowRoot() }},
	{name: "Element.Frame", missing: "DOMDescribeNodeResult.node",
		run: func(env *lenientEnv) (any, error) { return env.element.Frame() }},
	{name: "Element.Interactable", missing: "DOMResolveNodeResult.object",
		responses: map[string]string{
			"Runtime.callFunctionOn": `{"result":{"type":"object","value":{"x":0,"y":0}}}`,
			"DOM.getContentQuads":    `{"quads":[[0,0,10,0,10,10,0,10]]}`,
			"DOM.getNodeForLocation": `{"backendNodeId":1,"frameId":"frame"}`,
		},
		run: func(env *lenientEnv) (any, error) { return env.element.Interactable() }},
	{name: "Element.Text", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.element.Text() }},
	{name: "Element.Click", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) {
			return lenientDone(env.element.Click(proto.InputMouseButtonLeft, 1))
		}},
	{name: "Element.Input", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.element.Input("text")) }},
	{name: "Element.Screenshot", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) {
			return env.element.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
		}},
	{name: "Element.Screenshot/no viewport", missing: "PageGetLayoutMetricsResult.cssVisualViewport",
		responses: map[string]string{
			"Runtime.callFunctionOn": `{"result":{"type":"object","objectId":"helper","value":true}}`,
			"DOM.getContentQuads":    `{"quads":[[0,0,10,0,10,10,0,10]]}`,
		},
		run: func(env *lenientEnv) (any, error) {
			return env.element.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
		}},
	{name: "Element.Element", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.element.Element("a") }},
	{name: "Element.Parent", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.element.Parent() }},
	{name: "Element.Equal", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.element.Equal(env.element) }},
	{name: "Element.GetXPath", responses: lenientNoResult, missing: "RuntimeCallFunctionOnResult.result",
		run: func(env *lenientEnv) (any, error) { return env.element.GetXPath(true) }},
	{name: "Element.HTML", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return env.element.HTML() }},
	{name: "Element.Shape", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return env.element.Shape() }},
	{name: "Element.SetFiles", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.element.SetFiles([]string{"file"})) }},

	// Page data and navigation.
	{name: "Page.Info", missing: "TargetGetTargetInfoResult.targetInfo",
		run: func(env *lenientEnv) (any, error) { return env.page.Info() }},
	{name: "Page.Cookies/no list", missing: "NetworkGetCookiesResult.cookies",
		run: func(env *lenientEnv) (any, error) { return env.page.Cookies([]string{"https://example.test"}) }},
	{name: "Page.Cookies/null entry", missing: "NetworkGetCookiesResult.cookies[0]",
		responses: map[string]string{"Network.getCookies": `{"cookies":[null]}`},
		run:       func(env *lenientEnv) (any, error) { return env.page.Cookies([]string{"https://example.test"}) }},
	{name: "Page.GetWindow", missing: "BrowserGetWindowBoundsResult.bounds",
		run: func(env *lenientEnv) (any, error) { return env.page.GetWindow() }},
	{name: "Page.Screenshot", missing: "PageCaptureScreenshotResult.data",
		run: func(env *lenientEnv) (any, error) { return env.page.Screenshot(false, nil) }},
	{name: "Page.Screenshot/full page", missing: "PageGetLayoutMetricsResult.cssContentSize",
		run: func(env *lenientEnv) (any, error) { return env.page.Screenshot(true, nil) }},
	{name: "Page.ScrollScreenshot/no metrics", missing: "PageGetLayoutMetricsResult.cssContentSize",
		run: func(env *lenientEnv) (any, error) { return env.page.ScrollScreenshot(nil) }},
	{name: "Page.ScrollScreenshot/no viewport", missing: "PageGetLayoutMetricsResult.cssVisualViewport",
		responses: map[string]string{"Page.getLayoutMetrics": `{"cssContentSize":{"x":0,"y":0,"width":10,"height":10}}`},
		run:       func(env *lenientEnv) (any, error) { return env.page.ScrollScreenshot(nil) }},
	{name: "Page.ScrollScreenshot/no data", missing: "PageCaptureScreenshotResult.data",
		responses: map[string]string{"Page.getLayoutMetrics": lenientLayoutMetrics},
		run:       func(env *lenientEnv) (any, error) { return env.page.ScrollScreenshot(nil) }},
	{name: "Page.PDF", missing: "IOReadResult.eof",
		run: func(env *lenientEnv) (any, error) {
			reader, err := env.page.PDF(&proto.PagePrintToPDF{})
			if err != nil {
				return nil, err
			}
			return io.ReadAll(reader)
		}},
	{name: "Page.GetResource", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return env.page.GetResource("https://example.test/") }},
	{name: "Page.Navigate", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.page.Navigate("https://example.test/")) }},
	{name: "Page.Activate", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return env.page.Activate() }},
	{name: "Page.SetCookies", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.page.SetCookies(nil)) }},
	{name: "Page.Close/target destroyed without ID", lenientOnly: true, wantErr: context.DeadlineExceeded,
		run: func(env *lenientEnv) (any, error) {
			env.after("Page.close", "", "Target.targetDestroyed", `{}`)
			return lenientDone(env.browser.PageFromSession("other").Timeout(time.Minute).Close())
		}},
	{name: "Page.Reload", missing: "PageFrameNavigated.frame",
		run: func(env *lenientEnv) (any, error) {
			env.after("Runtime.callFunctionOn", "session", "Page.frameNavigated", `{"type":"Navigation"}`)
			return lenientDone(env.page.Reload())
		}},
	{name: "Page.WaitOpen", missing: "TargetTargetCreated.targetInfo",
		run: func(env *lenientEnv) (any, error) {
			wait := env.page.WaitOpen()
			env.emit("", "Target.targetCreated", `{}`)
			return wait()
		}},
	{name: "Page.WaitOpen/no target ID", missing: "TargetTargetCreated.targetInfo.targetId",
		run: func(env *lenientEnv) (any, error) {
			wait := env.page.WaitOpen()
			env.emit("", "Target.targetCreated", `{"targetInfo":{"openerId":"other"}}`)
			env.emit("", "Target.targetCreated", `{"targetInfo":{"openerId":"page"}}`)
			return wait()
		}},
	{name: "Page.EvalOnNewDocument", missing: "PageAddScriptToEvaluateOnNewDocumentResult.identifier",
		run: func(env *lenientEnv) (any, error) { return env.page.EvalOnNewDocument(`1`) }},
	{name: "Page.WaitRequestIdle", missing: "NetworkRequestWillBeSent.request",
		run: func(env *lenientEnv) (any, error) {
			wait := env.page.WaitRequestIdle(time.Second, nil, nil, nil)
			env.emit("session", "Network.requestWillBeSent", `{"requestId":"request"}`)
			return lenientDone(wait())
		}},

	// Input.
	{name: "Keyboard.Type", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.page.Keyboard.Type(input.KeyA)) }},
	{name: "Mouse.Click", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) {
			return lenientDone(env.page.Mouse.Click(proto.InputMouseButtonLeft, 1))
		}},
	{name: "Page.InsertText", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return lenientDone(env.page.InsertText("text")) }},

	// Browser.
	{name: "Browser.Pages/no list", missing: "TargetGetTargetsResult.targetInfos",
		run: func(env *lenientEnv) (any, error) { return env.browser.Pages() }},
	{name: "Browser.Pages/null entry", missing: "TargetGetTargetsResult.targetInfos[1]",
		responses: map[string]string{"Target.getTargets": `{"targetInfos":[{"targetId":"worker","type":"worker","title":"","url":"","attached":false},null]}`},
		run:       func(env *lenientEnv) (any, error) { return env.browser.Pages() }},
	{name: "Browser.Pages/page without ID", missing: "TargetGetTargetsResult.targetInfos[1].targetId",
		responses: map[string]string{"Target.getTargets": `{"targetInfos":[{"type":"worker"},{"type":"page"}]}`},
		run:       func(env *lenientEnv) (any, error) { return env.browser.Pages() }},
	{name: "Browser.GetCookies/no list", missing: "StorageGetCookiesResult.cookies",
		run: func(env *lenientEnv) (any, error) { return env.browser.GetCookies() }},
	{name: "Browser.GetCookies/null entry", missing: "StorageGetCookiesResult.cookies[0]",
		responses: map[string]string{"Storage.getCookies": `{"cookies":[null]}`},
		run:       func(env *lenientEnv) (any, error) { return env.browser.GetCookies() }},
	{name: "Browser.Incognito", missing: "TargetCreateBrowserContextResult.browserContextId",
		run: func(env *lenientEnv) (any, error) { return env.browser.Incognito() }},
	{name: "Browser.Page", missing: "TargetCreateTargetResult.targetId",
		run: func(env *lenientEnv) (any, error) { return env.browser.Page(proto.TargetCreateTarget{}) }},
	{name: "Browser.PageFromTarget", missing: "TargetAttachToTargetResult.sessionId",
		responses: map[string]string{"Target.attachToTarget": `{}`},
		run:       func(env *lenientEnv) (any, error) { return env.browser.PageFromTarget("other") }},
	{name: "Browser.Version", lenientOnly: true,
		run: func(env *lenientEnv) (any, error) { return env.browser.Version() }},
	{name: "Browser.WaitDownload", lenientOnly: true, wantErr: rod.ErrDownloadGUID,
		responses: map[string]string{"Target.getTargets": `{"targetInfos":[{"targetId":"frame"}]}`},
		run: func(env *lenientEnv) (any, error) {
			wait, err := env.browser.WaitDownload("downloads")
			if err != nil {
				return nil, err
			}
			env.emit("", "Target.targetCreated", `{}`)
			env.emit("", "Browser.downloadWillBegin", `{"frameId":"frame"}`)
			return wait()
		}},
	{name: "Browser.WaitDownload/child frame without session", missing: "TargetAttachToTargetResult.sessionId",
		responses: map[string]string{
			"Target.getTargets":         `{"targetInfos":[` + lenientTarget + `]}`,
			"Target.getBrowserContexts": `{"browserContextIds":[]}`,
			"Target.attachToTarget":     `{}`,
		},
		run: func(env *lenientEnv) (any, error) {
			wait, err := env.browser.WaitDownload("downloads")
			if err != nil {
				return nil, err
			}
			env.emit("", "Browser.downloadWillBegin",
				`{"frameId":"child","guid":"download","url":"https://example.test/","suggestedFilename":"file"}`)
			return wait()
		}},
	{name: "Browser.WaitDownload/without IDs", lenientOnly: true,
		responses: map[string]string{
			"Target.getTargets":       `{"targetInfos":[{"type":"page"},` + lenientTarget + `]}`,
			"Page.getFrameTree":       `{"frameTree":{"frame":{"id":"top"},"childFrames":[{"frame":{"id":"child"}}]}}`,
			"Target.detachFromTarget": `{}`,
		},
		run: func(env *lenientEnv) (any, error) {
			wait, err := env.browser.WaitDownload("downloads")
			if err != nil {
				return nil, err
			}
			// Neither the download without a frame nor the page without an ID
			// can be attributed to a browser context.
			env.emit("", "Browser.downloadWillBegin", `{"guid":"unattributed"}`)
			env.emit("", "Browser.downloadWillBegin", `{"frameId":"child","guid":"download"}`)
			env.emit("", "Browser.downloadProgress", `{"guid":"unattributed","state":"completed"}`)
			env.emit("", "Browser.downloadProgress", `{"guid":"download","state":"completed"}`)
			info, err := wait()
			if err == nil && info.GUID != "download" {
				err = fmt.Errorf("waited for download %q", info.GUID)
			}
			return info, err
		},
		check: func(t *testing.T, env *lenientEnv) {
			for _, params := range env.client.params("Target.attachToTarget") {
				if target := params.(proto.TargetAttachToTarget).TargetID; target != "top" {
					t.Fatalf("attached to target %q", target)
				}
			}
		}},
	{name: "Browser.HandleAuth", lenientOnly: true, wantErr: context.DeadlineExceeded,
		run: func(env *lenientEnv) (any, error) {
			wait := env.browser.Timeout(time.Minute).HandleAuth(rod.AuthCredentials{
				Source: proto.FetchAuthChallengeSourceServer, AnyOrigin: true, Username: "user", Password: "secret",
			})
			env.emit("", "Fetch.authRequired", `{"requestId":"challenge"}`)
			return lenientDone(wait())
		},
		check: func(t *testing.T, env *lenientEnv) {
			answers := env.client.params("Fetch.continueWithAuth")
			if len(answers) != 1 {
				t.Fatalf("answers = %v", answers)
			}
			if answer := answers[0].(proto.FetchContinueWithAuth).AuthChallengeResponse; answer.Response != proto.FetchAuthChallengeResponseResponseDefault || answer.Username != "" {
				t.Fatalf("a challenge without details was answered with %+v", answer)
			}
		}},
	{name: "HijackRouter", lenientOnly: true, missing: "FetchRequestPaused.request",
		run: func(env *lenientEnv) (any, error) {
			router := env.page.HijackRequests()
			reported := make(chan error, 1)
			router.OnError(func(err error) { reported <- err })
			if err := router.Add("*", "", func(*rod.Hijack) { panic("handler ran for a request without details") }); err != nil {
				return nil, err
			}
			running := make(chan error, 1)
			go func() { running <- router.Run() }()
			env.emit("session", "Fetch.requestPaused", `{"requestId":"paused"}`)
			err := <-reported
			return nil, errors.Join(err, router.Stop(), <-running)
		},
		check: func(t *testing.T, env *lenientEnv) {
			failed := env.client.params("Fetch.failRequest")
			if len(failed) != 1 || failed[0].(proto.FetchFailRequest).RequestID != "paused" {
				t.Fatalf("failed requests = %v", failed)
			}
		}},

	// Exposed functions.
	{name: "Page.Expose/no identifier", missing: "PageAddScriptToEvaluateOnNewDocumentResult.identifier",
		run: func(env *lenientEnv) (any, error) {
			return env.page.Expose("fn", func(jsonvalue.Value) (any, error) { return nil, nil }, nil)
		}},
	{name: "Page.Expose/call without context", lenientOnly: true,
		responses: map[string]string{"Page.addScriptToEvaluateOnNewDocument": `{"identifier":"script"}`},
		run: func(env *lenientEnv) (any, error) {
			requests := make(chan jsonvalue.Value, 2)
			stop, err := env.page.Expose("fn", func(request jsonvalue.Value) (any, error) {
				requests <- request
				return nil, nil
			}, nil)
			if err != nil {
				return nil, err
			}
			bind := env.client.params("Runtime.addBinding")[0].(proto.RuntimeAddBinding).Name
			env.emit("session", "Runtime.bindingCalled", lenientBindingCall(bind, 0, 1))
			env.emit("session", "Runtime.bindingCalled", lenientBindingCall(bind, 5, 2))
			request := <-requests
			if err := stop(); err != nil {
				return nil, err
			}
			if request.Int() != 2 {
				return nil, fmt.Errorf("handled request %v, want the call with a context", request)
			}
			return request, nil
		}},

	// Diagnostics.
	{name: "PageDiagnostics.Stop", missing: "PageCreateIsolatedWorldResult.executionContextId",
		run: func(env *lenientEnv) (any, error) {
			diagnostics, err := env.page.StartDiagnostics(rod.DiagnosticsOptions{StopTimeout: time.Second})
			if err != nil {
				return nil, err
			}
			return diagnostics.Stop()
		},
		check: func(t *testing.T, env *lenientEnv) {
			if calls := env.client.params("Runtime.evaluate"); len(calls) != 0 {
				t.Fatalf("evaluated the boundary without its world: %v", calls)
			}
		}},
	{name: "PageDiagnostics/exception", missing: "RuntimeExceptionThrown.exceptionDetails",
		responses: lenientDiagnosticsWorld,
		run:       lenientDiagnostics("Runtime.exceptionThrown", `{"timestamp":1}`)},
	{name: "PageDiagnostics/request", missing: "NetworkRequestWillBeSent.request",
		responses: lenientDiagnosticsWorld,
		run:       lenientDiagnostics("Network.requestWillBeSent", `{"requestId":"request"}`)},
	{name: "PageDiagnostics/response", missing: "NetworkResponseReceived.response",
		responses: lenientDiagnosticsWorld,
		run:       lenientDiagnostics("Network.responseReceived", `{"requestId":"request"}`)},
}

// lenientDiagnosticsWorld answers the command that creates the isolated world
// of diagnostics.
var lenientDiagnosticsWorld = map[string]string{"Page.createIsolatedWorld": `{"executionContextId":7}`}

// lenientDiagnostics runs page diagnostics that receive an event of method
// with params, and requires that Stop reports them incomplete.
func lenientDiagnostics(method, params string) func(*lenientEnv) (any, error) {
	return func(env *lenientEnv) (any, error) {
		diagnostics, err := env.page.StartDiagnostics(rod.DiagnosticsOptions{StopTimeout: time.Second})
		if err != nil {
			return nil, err
		}
		env.emit("session", method, params)
		synctest.Wait()
		snapshot, err := diagnostics.Stop()
		if !errors.Is(err, rod.ErrDiagnosticsIncomplete) {
			return nil, fmt.Errorf("stop error %w does not match ErrDiagnosticsIncomplete", err)
		}
		return snapshot, err
	}
}

// lenientBindingCall returns a Runtime.bindingCalled event of the binding with the
// request, from the execution context, or without one when contextID is 0.
func lenientBindingCall(binding string, contextID, request int) string {
	payload, _ := json.Marshal(map[string]any{"req": request, "cb": fmt.Sprintf("%s_cb%d", binding, request)})
	event := map[string]any{"name": binding, "payload": string(payload)}
	if contextID != 0 {
		event["executionContextId"] = contextID
	}
	data, _ := json.Marshal(event)
	return string(data)
}

// Rod's non-Must APIs do not panic or return nil results when a browser
// decodes leniently and the endpoint omits required fields. A missing field
// that a method uses returns the *proto.MissingFieldError that strict decoding
// returns, so errors.Is(err, proto.ErrMissingField) holds in both modes.
func TestLenientDecodingAPIs(t *testing.T) {
	for _, test := range lenientCases {
		for _, mode := range []proto.Decoding{proto.DecodeLenient, proto.DecodeStrict} {
			name := test.name + "/lenient"
			if mode == proto.DecodeStrict {
				if test.missing == "" || test.lenientOnly {
					continue
				}
				name = test.name + "/strict"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
					defer cancel()
					env := newLenientEnv(t, ctx, mode, test)
					result, err := lenientCall(t, env, test.run)
					switch {
					case test.missing != "" && !errors.Is(err, proto.ErrMissingField):
						t.Fatalf("error = %v, want one matching proto.ErrMissingField", err)
					case test.missing != "" && mode == proto.DecodeLenient:
						missing, ok := errors.AsType[*proto.MissingFieldError](err)
						if !ok || missing.Type+"."+missing.Path != test.missing {
							t.Fatalf("error = %v, want %s missing", err, test.missing)
						}
					case test.wantErr != nil:
						if !errors.Is(err, test.wantErr) {
							t.Fatalf("error = %v, want %v", err, test.wantErr)
						}
					case test.missing == "":
						if err != nil || lenientNil(result) {
							t.Fatalf("result = %#v, %v", result, err)
						}
					}
					if test.check != nil {
						test.check(t, env)
					}
				})
			})
		}
	}
}

// lenientCall runs a case and fails the test if it panics.
func lenientCall(t *testing.T, env *lenientEnv, run func(*lenientEnv) (any, error)) (result any, err error) {
	t.Helper()
	defer func() {
		if value := recover(); value != nil {
			t.Fatalf("panic: %v\n%s", value, debug.Stack())
		}
	}()
	return run(env)
}

// lenientNil reports whether value is nil or a nil pointer, slice or map.
func lenientNil(value any) bool {
	if value == nil {
		return true
	}
	switch v := reflect.ValueOf(value); v.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}
