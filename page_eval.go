// This file serves for the Page.Evaluate.

package rod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/js"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// ErrFrameContextChanged means an iframe document or renderer is no longer
// available to this page view. Acquire a new view with [Element.Frame].
var ErrFrameContextChanged = errors.New("iframe context changed; acquire a new view with Element.Frame")

// jsHelperCache is shared by all Page views attached to the same session.
type jsHelperCache struct {
	sync.Mutex
	contexts map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID
}

func (cache *jsHelperCache) addContext(id proto.RuntimeRemoteObjectID) {
	cache.Lock()
	defer cache.Unlock()
	if cache.contexts == nil {
		cache.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{}
	}
	if cache.contexts[id] == nil {
		cache.contexts[id] = map[string]proto.RuntimeRemoteObjectID{}
	}
}

// EvalOptions for Page.Evaluate.
type EvalOptions struct {
	// If enabled the eval result will be a plain JSON value.
	// If disabled the eval result will be a reference of a remote js object.
	ByValue bool

	AwaitPromise bool

	// ThisObj represents the "this" object in the JS
	ThisObj *proto.RuntimeRemoteObject

	// JS function definition to execute.
	JS string

	// JSArgs represents the arguments that will be passed to JS.
	// If an argument is [*proto.RuntimeRemoteObject] type, the corresponding remote object will be used.
	// Or it will be passed as a plain JSON value.
	// When an arg in the args is a *js.Function, the arg will be cached on the page's js context.
	// When the arg.Name exists in the page's cache, it reuse the cache without sending
	// the definition to the browser again.
	// Useful when you need to eval a huge js expression many times.
	JSArgs []any

	// Whether execution should be treated as initiated by user in the UI.
	UserGesture bool
}

// Eval creates a [EvalOptions] with ByValue set to true.
func Eval(js string, args ...any) *EvalOptions {
	return &EvalOptions{
		ByValue:      true,
		AwaitPromise: false,
		ThisObj:      nil,
		JS:           js,
		JSArgs:       args,
		UserGesture:  false,
	}
}

func evalHelper(fn *js.Function, args ...any) *EvalOptions {
	return &EvalOptions{
		ByValue: true,
		JSArgs:  append([]any{fn}, args...),
		JS:      fmt.Sprintf(`function (f /* %s */, ...args) { return f.apply(this, args) }`, fn.Name),
	}
}

// String interface.
func (e *EvalOptions) String() string {
	fn := e.JS
	args := e.JSArgs

	paramsStr := ""
	thisStr := ""

	if e.ThisObj != nil {
		thisStr = e.ThisObj.Description
	}
	if len(args) > 0 {
		if f, ok := args[0].(*js.Function); ok {
			fn = "rod." + f.Name
			args = e.JSArgs[1:]
		}

		paramsStr = strings.Trim(mustToJSONForDev(args), "[]\r\n")
	}

	return fmt.Sprintf("%s(%s) %s", fn, paramsStr, thisStr)
}

// This set the obj as ThisObj.
func (e *EvalOptions) This(obj *proto.RuntimeRemoteObject) *EvalOptions {
	e.ThisObj = obj
	return e
}

// ByObject disables ByValue.
func (e *EvalOptions) ByObject() *EvalOptions {
	e.ByValue = false
	return e
}

// ByUser enables UserGesture.
func (e *EvalOptions) ByUser() *EvalOptions {
	e.UserGesture = true
	return e
}

// ByPromise enables AwaitPromise.
func (e *EvalOptions) ByPromise() *EvalOptions {
	e.AwaitPromise = true
	return e
}

func (e *EvalOptions) formatToJSFunc() string {
	js := strings.Trim(e.JS, "\t\n\v\f\r ;")
	return `function() { return (` + js + `).apply(this, arguments) }`
}

// Eval is a shortcut for [Page.Evaluate] with AwaitPromise, ByValue set to true.
func (p *Page) Eval(js string, args ...any) (*proto.RuntimeRemoteObject, error) {
	return p.Evaluate(Eval(js, args...).ByPromise())
}

// Evaluate js on the page.
func (p *Page) Evaluate(opts *EvalOptions) (res *proto.RuntimeRemoteObject, err error) {
	var backoff utils.Sleeper

	// js context will be invalid if a frame is reloaded or not ready, then the isNilContextErr
	// will be true, then we retry the eval again.
	for {
		res, err = p.evaluate(opts)
		if err != nil && errors.Is(err, cdp.ErrCtxNotFound) {
			if opts.ThisObj != nil {
				return nil, &ObjectNotFoundError{opts.ThisObj}
			}

			if backoff == nil {
				backoff = utils.BackoffSleeper(30*time.Millisecond, 3*time.Second, nil)
			} else {
				if err := backoff(p.ctx); err != nil {
					return nil, err
				}
			}

			p.unsetJSCtxID()

			continue
		}
		return
	}
}

func (p *Page) evaluate(opts *EvalOptions) (*proto.RuntimeRemoteObject, error) {
	args, err := p.formatArgs(opts)
	if err != nil {
		return nil, err
	}

	req := proto.RuntimeCallFunctionOn{
		AwaitPromise:        new(opts.AwaitPromise),
		ReturnByValue:       new(opts.ByValue),
		UserGesture:         new(opts.UserGesture),
		FunctionDeclaration: opts.formatToJSFunc(),
		Arguments:           args,
	}

	if opts.ThisObj == nil {
		req.ObjectID, err = p.getJSCtxID()
		if err != nil {
			return nil, err
		}
	} else {
		req.ObjectID = opts.ThisObj.ObjectID
	}

	res, err := req.Call(p)
	if err != nil {
		return nil, err
	}

	if res.ExceptionDetails != nil {
		return nil, &EvalError{res.ExceptionDetails}
	}

	return res.Result, nil
}

// Expose fn to the page's window object with the name. The exposure survives reloads.
// Go errors and results that cannot be JSON-encoded reject the JavaScript promise
// with an error message. Replies are delivered in the frame that called the function.
// Call the idempotent stop function to remove the binding and reload script.
// Cleanup has its own bounded context. A Go callback already running is not interrupted.
func (p *Page) Expose(name string, fn func(jsonvalue.Value) (any, error)) (stop func() error, err error) {
	releaseRuntime, err := p.browser.Context(p.ctx).acquireDomain(p.SessionID, proto.RuntimeEnable{})
	if err != nil {
		return nil, err
	}
	bind := "_" + utils.RandString(8)
	events, cancel := p.WithCancel()
	messages := events.Event()
	restoreRuntime := func() error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), 5*time.Second)
		defer cancel()
		return releaseRuntime(ctx)
	}
	stopRestore := context.AfterFunc(events.ctx, func() { _ = restoreRuntime() })
	var scriptID proto.PageScriptIdentifier
	var bindingAdded bool
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() {
			cancel()
			ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), 5*time.Second)
			defer cancel()
			page := p.Context(ctx)
			if scriptID != "" {
				cleanupErr = proto.PageRemoveScriptToEvaluateOnNewDocument{Identifier: scriptID}.Call(page)
			}
			if bindingAdded {
				cleanupErr = errors.Join(cleanupErr, proto.RuntimeRemoveBinding{Name: bind}.Call(page))
			}
			stopRestore()
			cleanupErr = errors.Join(cleanupErr, releaseRuntime(ctx))
		})
		return cleanupErr
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup())
		}
	}()

	// Subscribe before installing a callable function in the document.
	go func() {
		defer cancel()
		for message := range messages {
			var e proto.RuntimeBindingCalled
			if !message.Load(&e) || e.Name != bind {
				continue
			}
			var payload struct {
				Request  jsonvalue.Value `json:"req"`
				Callback string          `json:"cb"`
			}
			if json.Unmarshal([]byte(e.Payload), &payload) != nil || !strings.HasPrefix(payload.Callback, bind+"_cb") {
				continue
			}
			result, callErr := fn(payload.Request)
			data, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				data = []byte("null")
				callErr = errors.Join(callErr, fmt.Errorf("encode exposed function response: %w", marshalErr))
			}
			var errorMessage *string
			if callErr != nil {
				errorMessage = new(callErr.Error())
			}
			_, _ = proto.RuntimeCallFunctionOn{
				ExecutionContextID: e.ExecutionContextID,
				FunctionDeclaration: `function(callback, result, error) {
				const resolve = globalThis[callback];
				if (typeof resolve === "function") resolve(result, error);
			}`,
				Arguments: []*proto.RuntimeCallArgument{
					{Value: jsonvalue.New(payload.Callback)},
					{Value: jsonvalue.New(data)},
					{Value: jsonvalue.New(errorMessage)},
				},
			}.Call(events)
		}
	}()

	bindingAdded = true
	if err = (proto.RuntimeAddBinding{Name: bind}).Call(p); err != nil {
		return nil, err
	}
	if _, err = p.Evaluate(Eval(js.ExposeFunc.Definition, name, bind)); err != nil {
		return nil, err
	}
	code := fmt.Sprintf(`(%s)(%s, %s)`, js.ExposeFunc.Definition, utils.MustToJSON(name), utils.MustToJSON(bind))
	script, err := (proto.PageAddScriptToEvaluateOnNewDocument{Source: code}).Call(p)
	if err != nil {
		return nil, err
	}
	scriptID = script.Identifier
	return cleanup, nil
}

func (p *Page) formatArgs(opts *EvalOptions) ([]*proto.RuntimeCallArgument, error) {
	formatted := []*proto.RuntimeCallArgument{}
	for _, arg := range opts.JSArgs {
		if obj, ok := arg.(*proto.RuntimeRemoteObject); ok { // remote object
			if obj == nil {
				formatted = append(formatted, &proto.RuntimeCallArgument{})
			} else {
				formatted = append(formatted, &proto.RuntimeCallArgument{
					ObjectID: obj.ObjectID, Value: obj.Value, UnserializableValue: obj.UnserializableValue,
				})
			}
		} else if obj, ok := arg.(*js.Function); ok { // js helper
			id, err := p.ensureJSHelper(obj)
			if err != nil {
				return nil, err
			}
			formatted = append(formatted, &proto.RuntimeCallArgument{ObjectID: id})
		} else { // plain json data
			formatted = append(formatted, &proto.RuntimeCallArgument{Value: jsonvalue.New(arg)})
		}
	}

	return formatted, nil
}

// Check the doc of EvalHelper.
func (p *Page) ensureJSHelper(fn *js.Function) (proto.RuntimeRemoteObjectID, error) {
	jsCtxID, err := p.getJSCtxID()
	if err != nil {
		return "", err
	}

	fnID, has := p.getHelper(jsCtxID, js.Functions.Name)
	if !has {
		res, err := proto.RuntimeCallFunctionOn{
			ObjectID:            jsCtxID,
			FunctionDeclaration: js.Functions.Definition,
		}.Call(p)
		if err != nil {
			return "", err
		}
		fnID = res.Result.ObjectID
		if !p.setHelper(jsCtxID, js.Functions.Name, fnID) {
			return "", cdp.ErrCtxNotFound
		}
	}

	id, has := p.getHelper(jsCtxID, fn.Name)
	if !has {
		for _, dep := range fn.Dependencies {
			_, err := p.ensureJSHelper(dep)
			if err != nil {
				return "", err
			}
		}

		res, err := proto.RuntimeCallFunctionOn{
			ObjectID:  jsCtxID,
			Arguments: []*proto.RuntimeCallArgument{{ObjectID: fnID}},

			FunctionDeclaration: fmt.Sprintf(
				// we only need the object id, but the cdp will return the whole function string.
				// So we override the toString to reduce the overhead.
				"functions => { const f = functions.%s = %s; f.toString = () => 'fn'; return f }",
				fn.Name, fn.Definition,
			),
		}.Call(p)
		if err != nil {
			return "", err
		}

		id = res.Result.ObjectID
		if !p.setHelper(jsCtxID, fn.Name, id) {
			return "", cdp.ErrCtxNotFound
		}
	}

	return id, nil
}

func (p *Page) getHelper(jsCtxID proto.RuntimeRemoteObjectID, name string) (proto.RuntimeRemoteObjectID, bool) {
	p.helpers.Lock()
	defer p.helpers.Unlock()

	id, ok := p.helpers.contexts[jsCtxID][name]
	return id, ok
}

func (p *Page) setHelper(jsCtxID proto.RuntimeRemoteObjectID, name string, fnID proto.RuntimeRemoteObjectID) bool {
	p.helpers.Lock()
	defer p.helpers.Unlock()
	if list := p.helpers.contexts[jsCtxID]; list != nil {
		list[name] = fnID
		return true
	}
	return false
}

// Returns the page's window object, the page can be an iframe.
func (p *Page) getJSCtxID() (proto.RuntimeRemoteObjectID, error) {
	p.jsCtxLock.Lock()
	defer p.jsCtxLock.Unlock()

	if *p.jsCtxID != "" {
		return *p.jsCtxID, nil
	}

	if !p.IsIframe() || p.SessionID != p.element.page.SessionID {
		obj, err := proto.RuntimeEvaluate{Expression: "window"}.Call(p)
		if err != nil {
			return "", err
		}

		*p.jsCtxID = obj.Result.ObjectID
		p.helpers.Lock()
		p.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{
			*p.jsCtxID: {},
		}
		p.helpers.Unlock()
		return *p.jsCtxID, nil
	}

	node, err := p.element.Context(p.ctx).Describe(1, true)
	if err != nil {
		return "", p.frameContextError(err)
	}
	if node.ContentDocument == nil {
		return "", fmt.Errorf("%w: %s", ErrFrameContextChanged, p.FrameID)
	}

	obj, err := proto.DOMResolveNode{BackendNodeID: node.ContentDocument.BackendNodeID}.Call(p)
	if err != nil {
		return "", p.frameContextError(err)
	}

	id, err := p.jsCtxIDByObjectID(obj.Object.ObjectID)
	if err != nil {
		return "", err
	}
	*p.jsCtxID = id
	p.helpers.addContext(id)
	return id, nil
}

func (p *Page) frameContextError(err error) error {
	if protocolErr, ok := errors.AsType[*cdp.Error](err); ok && protocolErr.Code == -32000 &&
		protocolErr.Message == "Node with given id does not belong to the document" {
		return fmt.Errorf("%w: %s: %w", ErrFrameContextChanged, p.FrameID, err)
	}
	return err
}

func (p *Page) unsetJSCtxID() {
	p.jsCtxLock.Lock()
	defer p.jsCtxLock.Unlock()

	p.helpers.Lock()
	delete(p.helpers.contexts, *p.jsCtxID)
	p.helpers.Unlock()
	*p.jsCtxID = ""
}

func (p *Page) jsCtxIDByObjectID(id proto.RuntimeRemoteObjectID) (proto.RuntimeRemoteObjectID, error) {
	res, err := proto.RuntimeCallFunctionOn{
		ObjectID:            id,
		FunctionDeclaration: `() => window`,
	}.Call(p)
	if err != nil {
		return "", err
	}

	return res.Result.ObjectID, nil
}
