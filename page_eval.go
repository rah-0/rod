// This file serves for the Page.Evaluate.

package rod

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
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

	// last is the context most recently matched by an object lookup.
	last proto.RuntimeRemoteObjectID

	// frames maps each same-process frame to the window its views last resolved.
	frames map[proto.PageFrameID]proto.RuntimeRemoteObjectID

	// installs maps a context to a channel that closes when the helper install
	// running in it ends.
	installs map[proto.RuntimeRemoteObjectID]chan struct{}
}

// lockInstall waits until no helper install runs in the context, then starts
// one. The caller calls unlock when the install ends.
func (c *jsHelperCache) lockInstall(ctx context.Context, id proto.RuntimeRemoteObjectID) (unlock func(), err error) {
	for {
		c.Lock()
		running, busy := c.installs[id]
		if !busy {
			done := make(chan struct{})
			if c.installs == nil {
				c.installs = map[proto.RuntimeRemoteObjectID]chan struct{}{}
			}
			c.installs[id] = done
			c.Unlock()
			return func() {
				c.Lock()
				delete(c.installs, id)
				c.Unlock()
				close(done)
			}, nil
		}
		c.Unlock()
		select {
		case <-running:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// candidates lists the cached contexts not yet checked: preferred first, then
// the last match, then the rest by ID. The caller holds the lock.
func (c *jsHelperCache) candidates(checked map[proto.RuntimeRemoteObjectID]bool, preferred proto.RuntimeRemoteObjectID) []proto.RuntimeRemoteObjectID {
	rank := func(id proto.RuntimeRemoteObjectID) int {
		switch id {
		case preferred:
			return 0
		case c.last:
			return 1
		}
		return 2
	}
	var list []proto.RuntimeRemoteObjectID
	for id := range c.contexts {
		if !checked[id] {
			list = append(list, id)
		}
	}
	slices.SortFunc(list, func(a, b proto.RuntimeRemoteObjectID) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a, b))
	})
	return list
}

// use records a match and reports whether the context is still cached.
func (c *jsHelperCache) use(id proto.RuntimeRemoteObjectID) bool {
	c.Lock()
	defer c.Unlock()
	if _, ok := c.contexts[id]; !ok {
		return false
	}
	c.last = id
	return true
}

// has reports whether the context is cached.
func (c *jsHelperCache) has(id proto.RuntimeRemoteObjectID) bool {
	c.Lock()
	defer c.Unlock()
	_, ok := c.contexts[id]
	return ok
}

// remove uncaches a context and the frames that resolved to it. The caller
// holds the lock.
func (c *jsHelperCache) remove(id proto.RuntimeRemoteObjectID) {
	delete(c.contexts, id)
	maps.DeleteFunc(c.frames, func(_ proto.PageFrameID, window proto.RuntimeRemoteObjectID) bool {
		return window == id
	})
}

// frameWindow returns the window the frame's views last resolved; empty when unknown.
func (c *jsHelperCache) frameWindow(frameID proto.PageFrameID) proto.RuntimeRemoteObjectID {
	c.Lock()
	defer c.Unlock()
	return c.frames[frameID]
}

// setFrameWindow records the window of the frame while that context is cached.
func (c *jsHelperCache) setFrameWindow(frameID proto.PageFrameID, id proto.RuntimeRemoteObjectID) {
	c.Lock()
	defer c.Unlock()
	if _, ok := c.contexts[id]; !ok {
		return
	}
	if c.frames == nil {
		c.frames = map[proto.PageFrameID]proto.RuntimeRemoteObjectID{}
	}
	c.frames[frameID] = id
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
	// The helper arguments and a call on the page window use one resolution of
	// the window, so a concurrent reset cannot place them in different contexts.
	var jsCtxID proto.RuntimeRemoteObjectID
	if opts.ThisObj == nil || slices.ContainsFunc(opts.JSArgs, isJSHelper) {
		var err error
		jsCtxID, err = p.getJSCtxID()
		if err != nil {
			return nil, err
		}
	}
	args, err := p.formatArgs(opts, jsCtxID)
	if err != nil {
		return nil, err
	}

	req := proto.RuntimeCallFunctionOn{
		ObjectID:            jsCtxID,
		AwaitPromise:        new(opts.AwaitPromise),
		ReturnByValue:       new(opts.ByValue),
		UserGesture:         new(opts.UserGesture),
		FunctionDeclaration: opts.formatToJSFunc(),
		Arguments:           args,
	}

	if opts.ThisObj != nil {
		req.ObjectID = opts.ThisObj.ObjectID
	}

	res, err := req.Call(p)
	if err != nil {
		return nil, err
	}

	if res.ExceptionDetails != nil {
		return nil, &EvalError{res.ExceptionDetails}
	}
	if res.Result == nil {
		return nil, missingField("RuntimeCallFunctionOnResult", "result")
	}

	return res.Result, nil
}

// Expose fn to the page's window object with the name. The exposure survives reloads.
// Go errors and results that cannot be JSON-encoded reject the JavaScript promise
// with an error message. Replies are delivered in the frame that called the function.
// If fn or the encoding of its result panics, or fn ends its goroutine with
// runtime.Goexit, such as through a failing Must helper, the promise rejects with
// the message "exposed function failed" and later calls are still handled. The
// page does not receive the panic value.
// A non-nil onError receives the errors of calls that fn did not return: a
// *TryError holding the panic value, ErrExposedFunctionExited for runtime.Goexit,
// and an error wrapping the encoding error of a result that cannot be JSON-encoded.
// onError runs on the goroutine that calls fn, after the call's reply is queued,
// so later calls wait for it. A panic in onError is not recovered.
// Calls run fn one at a time in the order the page made them, and replies reach
// the page in that order. fn can start the next call before the page receives the
// previous reply. Replies that wait for the page are delivered in batches, so
// several promises can resolve in the same task. Calls that fn has not handled
// and replies that the page has not received, including fn's encoded results,
// stay in this process's memory until then, so a page that keeps making calls
// faster than it receives the replies increases memory use.
// Call the idempotent stop function to remove the binding and reload script.
// Cleanup has its own bounded context. A Go callback already running is not interrupted.
func (p *Page) Expose(name string, fn func(jsonvalue.Value) (any, error), onError func(error)) (stop func() error, err error) {
	releaseRuntime, err := p.browser.Context(p.ctx).acquireDomain(p.SessionID, proto.RuntimeEnable{})
	if err != nil {
		return nil, err
	}
	bind := "_" + utils.RandString(8)
	events, cancel := p.WithCancel()
	bindingMethod := (proto.RuntimeBindingCalled{}).ProtoEvent()
	messages := p.event.SubscribeFilter(events.ctx, func(msg *Message) bool { return msg.Method == bindingMethod })
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

	// A separate sender delivers replies in call order, so a slow reply round
	// trip does not hold later calls. Replies that queue meanwhile share the
	// next round trip.
	replies := &exposeReplies{wake: make(chan struct{}, 1)}
	go replies.send(events)

	// Subscribe before installing a callable function in the document.
	handler := &exposeHandler{bind: bind, fn: fn, onError: onError, messages: messages, replies: replies, cancel: cancel, decoding: p.GetDecoding()}
	go handler.serve(nil)

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
	if lenientMissing(p.GetDecoding(), script.Identifier) {
		// Without the identifier, stop could not remove the script.
		return nil, missingField("PageAddScriptToEvaluateOnNewDocumentResult", "identifier")
	}
	scriptID = script.Identifier
	return cleanup, nil
}

// ErrExposedFunctionExited means an exposed function ended its goroutine with
// runtime.Goexit, such as through a WithPanic fail function, instead of returning.
var ErrExposedFunctionExited = errors.New("rod: exposed function exited without returning")

// exposeFailure rejects a call whose function did not return. The page gets no
// details of the failure.
const exposeFailure = "exposed function failed"

// exposeHandler runs an exposed function for each binding call, one at a time
// in the order the page made the calls, and queues the replies.
type exposeHandler struct {
	bind     string
	fn       func(jsonvalue.Value) (any, error)
	onError  func(error)
	messages <-chan *Message
	replies  *exposeReplies
	cancel   context.CancelFunc
	decoding proto.Decoding
}

// serve reports exited, when it is not nil, and then handles binding calls
// until the subscription ends. If fn or onError ends the goroutine with
// runtime.Goexit, serve rejects the call being handled and continues on a new
// goroutine.
func (h *exposeHandler) serve(exited error) {
	var contextID proto.RuntimeExecutionContextID
	var callback string
	handling, returned := false, false
	defer func() {
		if returned {
			h.cancel()
			return
		}
		// runtime.Goexit skips Try's recover but still runs deferred calls.
		var err error
		if handling {
			h.reply(contextID, callback, nil, new(exposeFailure))
			err = ErrExposedFunctionExited
		}
		go h.serve(err)
	}()
	h.report(exited)
	for message := range h.messages {
		// An undecodable binding call names no callback to answer, and one
		// without an execution context, which lenient decoding leaves zero,
		// names no context to answer in; skip them.
		var e proto.RuntimeBindingCalled
		if ok, _ := message.Load(&e); !ok || e.Name != h.bind || lenientMissing(h.decoding, e.ExecutionContextID) {
			continue
		}
		var payload struct {
			Request  jsonvalue.Value `json:"req"`
			Callback string          `json:"cb"`
		}
		if json.Unmarshal([]byte(e.Payload), &payload) != nil || !exposeCallback(h.bind, payload.Callback) {
			continue
		}
		contextID, callback, handling = e.ExecutionContextID, payload.Callback, true
		result, errorMessage, err := exposeCall(h.fn, payload.Request)
		handling = false
		h.reply(contextID, callback, result, errorMessage)
		h.report(err)
	}
	returned = true
}

// reply queues the [callback, result, error] reply of a call. A nil result is
// null.
func (h *exposeHandler) reply(contextID proto.RuntimeExecutionContextID, callback string, result json.RawMessage, errorMessage *string) {
	data, err := json.Marshal([]any{callback, result, errorMessage})
	if err != nil {
		return // Unreachable: every part is a string, null or encoded JSON.
	}
	h.replies.push(exposeReply{contextID: contextID, data: data})
}

func (h *exposeHandler) report(err error) {
	if err != nil && h.onError != nil {
		h.onError(err)
	}
}

// exposeCall runs fn for one call and returns its encoded result and the error
// message for the page. A result that cannot be encoded rejects the call and
// returns the encoding error. A panic in fn, or while encoding its result or
// error, rejects the call with exposeFailure and returns a *TryError.
func exposeCall(fn func(jsonvalue.Value) (any, error), request jsonvalue.Value) (result json.RawMessage, errorMessage *string, err error) {
	panicErr := Try(func() {
		value, callErr := fn(request)
		var encodeErr error
		if result, encodeErr = json.Marshal(value); encodeErr != nil {
			result = nil
			err = fmt.Errorf("encode exposed function response: %w", encodeErr)
			callErr = errors.Join(callErr, err)
		}
		if callErr != nil {
			errorMessage = new(callErr.Error())
		}
	})
	if panicErr != nil {
		return nil, new(exposeFailure), panicErr
	}
	return result, errorMessage, err
}

// exposeReplyBatchBytes bounds the encoded replies in one Runtime.callFunctionOn,
// well below the browser's limit for a protocol message. A larger reply is sent
// alone.
const exposeReplyBatchBytes = 32 << 20

// exposeReplyJS resolves a batch of [callback, result, error] replies in order.
// A page callback that throws does not prevent the later replies.
const exposeReplyJS = `function(replies) {
	for (let i = 0; i < replies.length; i++) {
		try {
			const resolve = globalThis[replies[i][0]];
			if (typeof resolve === "function") resolve(replies[i][1], replies[i][2]);
		} catch {}
	}
}`

// exposeCallback reports whether callback has the form that the page-side
// function of binding creates: the binding, "_cb" and a decimal counter.
func exposeCallback(binding, callback string) bool {
	counter, ok := strings.CutPrefix(callback, binding+"_cb")
	if !ok || counter == "" || len(counter) > 20 {
		return false
	}
	for _, c := range []byte(counter) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// exposeReply is one encoded [callback, result, error] reply of an exposed
// function.
type exposeReply struct {
	contextID proto.RuntimeExecutionContextID
	data      json.RawMessage
}

// exposeReplies is the ordered, lossless queue between an exposed function and
// the goroutine that delivers its replies.
type exposeReplies struct {
	mu    sync.Mutex
	queue []exposeReply
	wake  chan struct{}
}

func (r *exposeReplies) push(reply exposeReply) {
	r.mu.Lock()
	r.queue = append(r.queue, reply)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// send delivers the queued replies until p's context ends. Replies that queue
// while a delivery is in flight go out together in the next one.
func (r *exposeReplies) send(p *Page) {
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-r.wake:
		}
		r.mu.Lock()
		queue := r.queue
		r.queue = nil
		r.mu.Unlock()
		for len(queue) > 0 && p.ctx.Err() == nil {
			n := exposeReplyBatchLen(queue)
			batch := make([]json.RawMessage, n)
			for i, reply := range queue[:n] {
				batch[i] = reply.data
			}
			_, _ = proto.RuntimeCallFunctionOn{
				ExecutionContextID:  queue[0].contextID,
				FunctionDeclaration: exposeReplyJS,
				Arguments:           []*proto.RuntimeCallArgument{{Value: jsonvalue.New(batch)}},
			}.Call(p)
			// Release the delivered replies before the next round trip.
			clear(queue[:n])
			queue = queue[n:]
		}
	}
}

// exposeReplyBatchLen returns how many leading replies go in one delivery: the
// first, and then those for the same execution context that keep the batch
// within exposeReplyBatchBytes.
func exposeReplyBatchLen(queue []exposeReply) int {
	size := len(queue[0].data)
	n := 1
	for ; n < len(queue) && queue[n].contextID == queue[0].contextID; n++ {
		size += len(queue[n].data) + 1 // One comma separates each reply.
		if size > exposeReplyBatchBytes {
			break
		}
	}
	return n
}

// formatArgs converts the arguments of opts. Helpers are installed in the
// context of the window jsCtxID.
func (p *Page) formatArgs(opts *EvalOptions, jsCtxID proto.RuntimeRemoteObjectID) ([]*proto.RuntimeCallArgument, error) {
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
			id, err := p.ensureJSHelper(jsCtxID, obj)
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

// isJSHelper reports whether an argument is a helper that formatArgs installs.
func isJSHelper(arg any) bool {
	_, ok := arg.(*js.Function)
	return ok
}

// ensureJSHelper returns the handle of fn in the context of the window jsCtxID.
// Missing helpers are installed with their dependencies into the functions
// object of that context. Installs in one context run one at a time, so every
// helper of a context uses the same functions object.
func (p *Page) ensureJSHelper(jsCtxID proto.RuntimeRemoteObjectID, fn *js.Function) (proto.RuntimeRemoteObjectID, error) {
	if id, has := p.getHelper(jsCtxID, fn.Name); has {
		return id, nil
	}

	unlock, err := p.helpers.lockInstall(p.ctx, jsCtxID)
	if err != nil {
		return "", err
	}
	defer unlock()

	functions, has := p.getHelper(jsCtxID, js.Functions.Name)
	if !has {
		functions, err = p.createHelper(jsCtxID, js.Functions.Name, proto.RuntimeCallFunctionOn{
			ObjectID:            jsCtxID,
			FunctionDeclaration: js.Functions.Definition,
		})
		if err != nil {
			return "", err
		}
	}
	return p.installJSHelper(jsCtxID, functions, fn)
}

// installJSHelper installs fn and its dependencies into the functions object
// of the context unless they are cached. The caller holds the install lock.
func (p *Page) installJSHelper(jsCtxID, functions proto.RuntimeRemoteObjectID, fn *js.Function) (proto.RuntimeRemoteObjectID, error) {
	if id, has := p.getHelper(jsCtxID, fn.Name); has {
		return id, nil
	}
	for _, dep := range fn.Dependencies {
		if _, err := p.installJSHelper(jsCtxID, functions, dep); err != nil {
			return "", err
		}
	}
	return p.createHelper(jsCtxID, fn.Name, proto.RuntimeCallFunctionOn{
		ObjectID:  jsCtxID,
		Arguments: []*proto.RuntimeCallArgument{{ObjectID: functions}},

		FunctionDeclaration: fmt.Sprintf(
			// we only need the object id, but the cdp will return the whole function string.
			// So we override the toString to reduce the overhead.
			"functions => { const f = functions.%s = %s; f.toString = () => 'fn'; return f }",
			fn.Name, fn.Definition,
		),
	})
}

// createHelper runs req, which returns a helper object, and caches the handle
// under name while the context is cached.
func (p *Page) createHelper(jsCtxID proto.RuntimeRemoteObjectID, name string, req proto.RuntimeCallFunctionOn) (proto.RuntimeRemoteObjectID, error) {
	res, err := req.Call(p)
	if err != nil {
		return "", err
	}
	if res.ExceptionDetails != nil {
		return "", &EvalError{res.ExceptionDetails}
	}
	if res.Result == nil {
		return "", missingField("RuntimeCallFunctionOnResult", "result")
	}
	if res.Result.ObjectID == "" {
		return "", fmt.Errorf("failed to create the js helper %s", name)
	}
	if !p.setHelper(jsCtxID, name, res.Result.ObjectID) {
		return "", cdp.ErrCtxNotFound
	}
	return res.Result.ObjectID, nil
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
		if obj.Result == nil {
			return "", missingField("RuntimeEvaluateResult", "result")
		}
		if obj.Result.ObjectID == "" {
			return "", errors.New("failed to get the window of the page")
		}

		*p.jsCtxID = obj.Result.ObjectID
		p.helpers.Lock()
		p.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{
			*p.jsCtxID: {},
		}
		p.helpers.frames = nil
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

	obj, err := resolveNode(p, proto.DOMResolveNode{BackendNodeID: node.ContentDocument.BackendNodeID})
	if err != nil {
		return "", p.frameContextError(err)
	}
	if obj.ObjectID == "" {
		return "", fmt.Errorf("failed to resolve the document of frame %s", p.FrameID)
	}

	id, err := p.jsCtxIDByObjectID(obj.ObjectID, p.helpers.frameWindow(p.FrameID))
	err = errors.Join(err, p.releaseObject(obj))
	if err != nil {
		return "", err
	}
	p.helpers.setFrameWindow(p.FrameID, id)
	*p.jsCtxID = id
	return id, nil
}

func (p *Page) frameContextError(err error) error {
	if protocolErr, ok := errors.AsType[*cdp.Error](err); ok && protocolErr.Code == -32000 &&
		protocolErr.Message == "Node with given id does not belong to the document" {
		return fmt.Errorf("%w: %s: %w", ErrFrameContextChanged, p.FrameID, err)
	}
	return err
}

// currentJSCtxID returns the page's window without resolving it; empty when unset.
// A reset uncaches the window, and resolution returns only cached or new windows,
// so two equal reads mean the page used that window throughout.
func (p *Page) currentJSCtxID() proto.RuntimeRemoteObjectID {
	p.jsCtxLock.Lock()
	defer p.jsCtxLock.Unlock()
	return *p.jsCtxID
}

func (p *Page) unsetJSCtxID() {
	p.jsCtxLock.Lock()
	defer p.jsCtxLock.Unlock()

	p.helpers.Lock()
	p.helpers.remove(*p.jsCtxID)
	p.helpers.Unlock()
	*p.jsCtxID = ""
}

// isOtherJSContext reports Chrome's rejection of a call argument whose handle
// belongs to an execution context other than the call target's.
func isOtherJSContext(err error) bool {
	protocolErr, ok := errors.AsType[*cdp.Error](err)
	return ok && protocolErr.Code == -32000 &&
		protocolErr.Message == "Argument should belong to the same JavaScript world as target object"
}

// jsCtxIDByObjectID returns the cached window of the execution context that owns
// the object handle, checking the preferred context first. Chrome resolves a call
// argument only within the target's own context, so each cached window costs one
// call and no handle. An unknown context caches a window of its own.
func (p *Page) jsCtxIDByObjectID(objID, preferred proto.RuntimeRemoteObjectID) (_ proto.RuntimeRemoteObjectID, err error) {
	var window *proto.RuntimeRemoteObject
	defer func() {
		if window != nil {
			err = errors.Join(err, p.releaseObject(window))
		}
	}()
	checked := map[proto.RuntimeRemoteObjectID]bool{}
	for {
		p.helpers.Lock()
		candidates := p.helpers.candidates(checked, preferred)
		if len(candidates) == 0 && window != nil {
			// No other lookup cached this context while the window was created.
			if p.helpers.contexts == nil {
				p.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{}
			}
			id := window.ObjectID
			p.helpers.contexts[id] = map[string]proto.RuntimeRemoteObjectID{}
			p.helpers.last = id
			p.helpers.Unlock()
			window = nil
			return id, nil
		}
		p.helpers.Unlock()
		if len(candidates) == 0 {
			res, err := proto.RuntimeCallFunctionOn{
				ObjectID:            objID,
				FunctionDeclaration: `() => window`,
			}.Call(p)
			if err != nil {
				return "", err
			}
			if res.Result == nil {
				return "", missingField("RuntimeCallFunctionOnResult", "result")
			}
			if res.Result.ObjectID == "" {
				return "", fmt.Errorf("failed to get the window of remote object %s", objID)
			}
			window = res.Result
			continue
		}
		for _, cached := range candidates {
			checked[cached] = true
			_, err := proto.RuntimeCallFunctionOn{
				ObjectID:            cached,
				FunctionDeclaration: `function() {}`,
				Arguments:           []*proto.RuntimeCallArgument{{ObjectID: objID}},
				ReturnByValue:       new(true),
			}.Call(p)
			switch {
			case err == nil:
				if p.helpers.use(cached) {
					return cached, nil
				}
			case errors.Is(err, cdp.ErrCtxNotFound):
				p.helpers.Lock()
				p.helpers.remove(cached)
				p.helpers.Unlock()
			case isOtherJSContext(err), errors.Is(err, cdp.ErrObjNotFound):
				// The object belongs to another context, or a caller released this
				// window. Creating the object's window reports a missing object.
			default:
				return "", err
			}
		}
	}
}
