package rod

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// HijackRequests same as Page.HijackRequests, but can intercept requests of the entire browser.
func (b *Browser) HijackRequests() *HijackRouter {
	return newHijackRouter(b, b).initEvents()
}

// HijackRequests creates a new router instance for requests hijacking.
// When use Fetch domain outside the router should be stopped. Enabling hijacking disables page caching,
// but such as 304 Not Modified will still work as expected.
// The entire process of hijacking one request:
//
//	browser --req-> rod ---> server ---> rod --res-> browser
//
// The --req-> and --res-> are the parts that can be modified.
func (p *Page) HijackRequests() *HijackRouter {
	return newHijackRouter(p.browser, p).initEvents()
}

// HijackRouter context.
type HijackRouter struct {
	run      func() error
	stop     func()
	stopOnce sync.Once
	stopErr  error
	setupErr error
	mu       sync.Mutex
	stopped  bool
	handlers []*hijackHandler
	enable   *proto.FetchEnable
	client   proto.Client
	browser  *Browser
}

func newHijackRouter(browser *Browser, client proto.Client) *HijackRouter {
	return &HijackRouter{
		enable:   &proto.FetchEnable{},
		browser:  browser,
		client:   client,
		handlers: []*hijackHandler{},
	}
}

func (r *HijackRouter) initEvents() *HijackRouter {
	ctx := r.browser.ctx
	if cta, ok := r.client.(proto.Contextable); ok {
		ctx = cta.GetContext()
	}

	var sessionID proto.TargetSessionID
	if tsa, ok := r.client.(proto.Sessionable); ok {
		sessionID = tsa.GetSessionID()
	}

	eventCtx, cancel := context.WithCancelCause(ctx)
	r.stop = func() { cancel(errWaitCompleted) }
	if err := r.enable.Call(r.client); err != nil {
		r.setupErr = errors.Join(err, r.Stop())
		r.run = func() error { return r.setupErr }
		return r
	}

	r.run = r.browser.Context(eventCtx).eachEvent(sessionID, On(func(e *proto.FetchRequestPaused, _ proto.TargetSessionID) bool {
		go func() {
			ctx := r.new(eventCtx, e)
			r.mu.Lock()
			handlers := slices.Clone(r.handlers)
			r.mu.Unlock()
			for _, h := range handlers {
				if !h.regexp.MatchString(e.Request.URL) || (h.resourceType != "" && h.resourceType != e.ResourceType) {
					continue
				}

				ctx.Skip = false
				h.handler(ctx)

				if ctx.continueRequest != nil {
					ctx.continueRequest.RequestID = e.RequestID
					err := ctx.continueRequest.Call(r.client)
					if err != nil {
						ctx.OnError(err)
					}
					return
				}

				if ctx.Skip {
					continue
				}

				if ctx.Response.fail.ErrorReason != "" {
					err := ctx.Response.fail.Call(r.client)
					if err != nil {
						ctx.OnError(err)
					}
					return
				}

				err := ctx.Response.payload.Call(r.client)
				if err != nil {
					ctx.OnError(err)
				}
				return
			}
			// A route may have been removed after Chrome paused this request.
			// Continue unmatched or explicitly skipped requests.
			if err := (proto.FetchContinueRequest{RequestID: e.RequestID}).Call(r.client); err != nil {
				ctx.OnError(err)
			}
		}()

		return false
	}))
	return r
}

// Add a hijack handler using FetchRequestPattern.URLPattern glob syntax.
// '?' matches exactly one character, as documented by CDP. Chrome versions that
// also pause a zero-character match have that request continued without handling.
// Failed updates leave the router's previous handlers and filters intact.
func (r *HijackRouter) Add(pattern string, resourceType proto.NetworkResourceType, handler func(*Hijack)) error {
	if handler == nil {
		return errors.New("hijack handler is nil")
	}
	reg, err := regexp.Compile(proto.PatternToReg(pattern))
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setupErr != nil {
		return r.setupErr
	}
	if r.stopped {
		return errors.New("hijack router stopped")
	}
	enable := *r.enable
	enable.Patterns = append(slices.Clone(r.enable.Patterns), &proto.FetchRequestPattern{
		URLPattern:   pattern,
		ResourceType: resourceType,
	})
	if err := enable.Call(r.client); err != nil {
		return err
	}

	r.handlers = append(r.handlers, &hijackHandler{
		pattern:      pattern,
		resourceType: resourceType,
		regexp:       reg,
		handler:      handler,
	})
	r.enable = &enable
	return nil
}

// Remove handler via the pattern.
func (r *HijackRouter) Remove(pattern string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setupErr != nil {
		return r.setupErr
	}
	if r.stopped {
		return errors.New("hijack router stopped")
	}
	patterns := []*proto.FetchRequestPattern{}
	handlers := []*hijackHandler{}
	for _, h := range r.handlers {
		if h.pattern != pattern {
			patterns = append(patterns, &proto.FetchRequestPattern{URLPattern: h.pattern, ResourceType: h.resourceType})
			handlers = append(handlers, h)
		}
	}
	enable := *r.enable
	enable.Patterns = patterns
	if err := enable.Call(r.client); err != nil {
		return err
	}
	r.enable = &enable
	r.handlers = handlers
	return nil
}

// new context.
func (r *HijackRouter) new(ctx context.Context, e *proto.FetchRequestPaused) *Hijack {
	headers := http.Header{}
	for k, v := range e.Request.Headers {
		headers.Set(k, v.String())
	}

	u, _ := url.Parse(e.Request.URL)

	req := &http.Request{
		Method: e.Request.Method,
		URL:    u,
		Header: headers,
	}
	request := &HijackRequest{event: e, req: req.WithContext(ctx)}
	if e.Request.HasPostData || e.Request.PostData != "" || e.Request.PostDataEntries != nil {
		body := []byte(e.Request.PostData)
		if len(e.Request.PostDataEntries) != 0 {
			body = nil
			for _, entry := range e.Request.PostDataEntries {
				if entry != nil {
					body = append(body, entry.Bytes...)
				}
			}
		}
		request.SetBody(body)
	}

	return &Hijack{
		Request: request,
		Response: &HijackResponse{
			payload: &proto.FetchFulfillRequest{
				ResponseCode: 200,
				RequestID:    e.RequestID,
			},
			fail: &proto.FetchFailRequest{
				RequestID: e.RequestID,
			},
		},
		OnError: func(_ error) {},

		browser: r.browser,
	}
}

// Run waits until Stop, cancellation, or a connection error, then releases Fetch.
// Setup, event-wait, and cleanup errors are returned. Handler errors use OnError.
func (r *HijackRouter) Run() error {
	if r.setupErr != nil {
		return r.setupErr
	}
	return errors.Join(r.run(), r.Stop())
}

// Stop the router.
func (r *HijackRouter) Stop() error {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		r.mu.Unlock()
		if r.stop != nil {
			r.stop()
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.browser.ctx), 5*time.Second)
		defer cancel()
		var session string
		if client, ok := r.client.(proto.Sessionable); ok {
			session = string(client.GetSessionID())
		}
		_, r.stopErr = r.client.Call(ctx, session, (proto.FetchDisable{}).ProtoReq(), proto.FetchDisable{})
	})
	return r.stopErr
}

// hijackHandler to handle each request that match the regexp.
type hijackHandler struct {
	pattern      string
	resourceType proto.NetworkResourceType
	regexp       *regexp.Regexp
	handler      func(*Hijack)
}

// Hijack context.
type Hijack struct {
	Request  *HijackRequest
	Response *HijackResponse
	OnError  func(error)

	// Skip to next handler
	Skip bool

	continueRequest *proto.FetchContinueRequest

	// CustomState is used to store things for this context
	CustomState any

	browser *Browser
}

// ContinueRequest without hijacking. The RequestID will be set by the router, you don't have to set it.
func (h *Hijack) ContinueRequest(cq *proto.FetchContinueRequest) {
	h.continueRequest = cq
}

// LoadResponse will send request to the real destination and load the response as default response to override.
func (h *Hijack) LoadResponse(client *http.Client, loadBody bool) error {
	res, err := client.Do(h.Request.req)
	if err != nil {
		return err
	}

	defer func() { _ = res.Body.Close() }()

	h.Response.payload.ResponseCode = res.StatusCode
	h.Response.RawResponse = res

	for k, vs := range res.Header {
		if len(vs) == 0 {
			continue
		}
		h.Response.SetHeader(k, vs[0])
		for _, v := range vs[1:] {
			h.Response.AddHeader(k, v)
		}
	}

	if loadBody {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return err
		}
		h.Response.payload.Body = b
	}

	return nil
}

// HijackRequest context.
type HijackRequest struct {
	event *proto.FetchRequestPaused
	req   *http.Request
	body  []byte
}

// Type of the resource.
func (ctx *HijackRequest) Type() proto.NetworkResourceType {
	return ctx.event.ResourceType
}

// Method of the request.
func (ctx *HijackRequest) Method() string {
	return ctx.req.Method
}

// URL of the request.
func (ctx *HijackRequest) URL() *url.URL {
	return ctx.req.URL
}

// Header via a key.
func (ctx *HijackRequest) Header(key string) string {
	return ctx.req.Header.Get(key)
}

// Headers returns a snapshot of the outgoing request headers. Use Req().Header
// to modify them.
func (ctx *HijackRequest) Headers() proto.NetworkHeaders {
	headers := make(proto.NetworkHeaders, len(ctx.req.Header))
	for key, values := range ctx.req.Header {
		headers[key] = jsonvalue.New(strings.Join(values, "\n"))
	}
	return headers
}

// Body returns the captured or SetBody replacement bytes as a Go string, which
// preserves binary data. Bytes omitted by the browser, such as unavailable file
// contents, cannot be recovered.
func (ctx *HijackRequest) Body() string {
	return string(ctx.body)
}

// JSONBody of the request.
func (ctx *HijackRequest) JSONBody() jsonvalue.Value {
	return jsonvalue.NewFrom(ctx.Body())
}

// Req returns the underlying http.Request instance that will be used to send the request.
func (ctx *HijackRequest) Req() *http.Request {
	return ctx.req
}

// SetContext of the underlying http.Request instance.
func (ctx *HijackRequest) SetContext(c context.Context) *HijackRequest {
	ctx.req = ctx.req.WithContext(c)
	return ctx
}

// SetBody replaces the body and its replay function and content length. Byte
// slices and strings are used directly as data; other values are JSON encoded.
func (ctx *HijackRequest) SetBody(obj any) *HijackRequest {
	var b []byte

	switch body := obj.(type) {
	case []byte:
		b = body
	case string:
		b = []byte(body)
	default:
		b = utils.MustToJSONBytes(body)
	}

	ctx.body = bytes.Clone(b)
	body := ctx.body
	ctx.req.GetBody = func() (io.ReadCloser, error) {
		if len(body) == 0 {
			return http.NoBody, nil
		}
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	ctx.req.Body, _ = ctx.req.GetBody()
	ctx.req.ContentLength = int64(len(body))
	if ctx.req.Header.Get("Content-Length") != "" {
		ctx.req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}

	return ctx
}

// IsNavigation determines whether the request is a navigation request.
func (ctx *HijackRequest) IsNavigation() bool {
	return ctx.Type() == proto.NetworkResourceTypeDocument
}

// HijackResponse context.
type HijackResponse struct {
	payload     *proto.FetchFulfillRequest
	RawResponse *http.Response
	fail        *proto.FetchFailRequest
}

// Payload to respond the request from the browser.
func (ctx *HijackResponse) Payload() *proto.FetchFulfillRequest {
	return ctx.payload
}

// Body of the payload.
func (ctx *HijackResponse) Body() string {
	return string(ctx.payload.Body)
}

// Headers returns the clone of response headers.
// Use SetHeader to replace values or AddHeader to retain repeated values.
func (ctx *HijackResponse) Headers() http.Header {
	header := http.Header{}

	for _, h := range ctx.payload.ResponseHeaders {
		header.Add(h.Name, h.Value)
	}

	return header
}

// SetHeader replaces all values for each case-insensitive header name.
func (ctx *HijackResponse) SetHeader(pairs ...string) *HijackResponse {
	for i := 0; i < len(pairs); i += 2 {
		name := pairs[i]
		value := pairs[i+1]
		ctx.payload.ResponseHeaders = slices.DeleteFunc(ctx.payload.ResponseHeaders, func(header *proto.FetchHeaderEntry) bool {
			return strings.EqualFold(header.Name, name)
		})
		ctx.AddHeader(name, value)
	}
	return ctx
}

// AddHeader appends key-value pairs to the end of the response headers.
// Duplicate keys will be preserved.
func (ctx *HijackResponse) AddHeader(pairs ...string) *HijackResponse {
	for i := 0; i < len(pairs); i += 2 {
		ctx.payload.ResponseHeaders = append(ctx.payload.ResponseHeaders, &proto.FetchHeaderEntry{
			Name:  pairs[i],
			Value: pairs[i+1],
		})
	}
	return ctx
}

// SetBody of the payload, if obj is []byte or string, raw body will be used, else it will be encoded as json.
func (ctx *HijackResponse) SetBody(obj any) *HijackResponse {
	switch body := obj.(type) {
	case []byte:
		ctx.payload.Body = body
	case string:
		ctx.payload.Body = []byte(body)
	default:
		ctx.payload.Body = utils.MustToJSONBytes(body)
	}
	return ctx
}

// Fail request.
func (ctx *HijackResponse) Fail(reason proto.NetworkErrorReason) *HijackResponse {
	ctx.fail.ErrorReason = reason
	return ctx
}

// HandleAuth for the next basic HTTP authentication.
// It will prevent the popup that requires user to input user name and password.
// Call the returned wait function to handle authentication and restore the
// previous Fetch configuration. It returns setup, wait, and cleanup errors.
// Ref: https://developer.mozilla.org/en-US/docs/Web/HTTP/Authentication
func (b *Browser) HandleAuth(username, password string) func() error {
	previous := &proto.FetchEnable{}
	enabled := b.LoadState("", previous)
	ctx, cancel := context.WithCancel(b.ctx)
	b = b.Context(ctx)
	setupErr := (proto.FetchEnable{HandleAuthRequests: new(true)}).Call(b)
	var operationErr error
	var wait func() error
	if setupErr == nil {
		wait = b.eachEvent("",
			On(func(paused *proto.FetchRequestPaused, _ proto.TargetSessionID) bool {
				operationErr = (proto.FetchContinueRequest{RequestID: paused.RequestID}).Call(b)
				return operationErr != nil
			}),
			On(func(auth *proto.FetchAuthRequired, _ proto.TargetSessionID) bool {
				operationErr = (proto.FetchContinueWithAuth{
					RequestID: auth.RequestID,
					AuthChallengeResponse: &proto.FetchAuthChallengeResponse{
						Response: proto.FetchAuthChallengeResponseResponseProvideCredentials,
						Username: username,
						Password: password,
					},
				}).Call(b)
				return true
			}),
		)
	}

	return func() (err error) {
		defer func() {
			cancel()
			cleanup, stop := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
			defer stop()
			if enabled {
				err = errors.Join(err, previous.Call(b.Context(cleanup)))
			} else {
				err = errors.Join(err, (proto.FetchDisable{}).Call(b.Context(cleanup)))
			}
		}()
		if setupErr != nil {
			return setupErr
		}
		err = wait()
		return errors.Join(err, operationErr)
	}
}
