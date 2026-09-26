package rod

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
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

// DefaultMaxResponseBodyBytes is the initial Hijack.MaxResponseBodyBytes. The
// response body reaches the browser base64 encoded in one CDP message, and
// Chrome closes the DevTools connection when it receives a message larger than
// 100 MiB. A 64 MiB body encodes to about 85 MiB. Together with headers within
// DefaultMaxResponseHeaderBytes, the message stays below Chrome's limit.
const DefaultMaxResponseBodyBytes int64 = 64 << 20

// DefaultMaxResponseHeaderBytes is the initial Hijack.MaxResponseHeaderBytes.
// Chrome rejects response headers larger than 256 KiB from the network with
// net::ERR_RESPONSE_HEADERS_TOO_BIG.
const DefaultMaxResponseHeaderBytes int64 = 256 << 10

// ErrResponseBodyTooLarge means Hijack.LoadResponse stopped reading a body
// larger than Hijack.MaxResponseBodyBytes.
var ErrResponseBodyTooLarge = errors.New("rod: hijacked response body exceeds its size limit")

// ErrResponseHeadersTooLarge means Hijack.LoadResponse received response
// headers larger than Hijack.MaxResponseHeaderBytes.
var ErrResponseHeadersTooLarge = errors.New("rod: hijacked response headers exceed their size limit")

// ErrHijackHandlerExited means a hijack handler ended its goroutine with
// runtime.Goexit, such as through a WithPanic fail function, instead of returning.
var ErrHijackHandlerExited = errors.New("rod: hijack handler exited without returning")

// ErrAuthCredentials means HandleAuth received an invalid challenge source or origin.
var ErrAuthCredentials = errors.New("rod: invalid auth credentials")

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
//
// Each paused request is served on its own goroutine, so handlers run
// concurrently. The router resolves every paused request once. If a handler
// panics, including through a Must helper such as Hijack.MustLoadResponse, or
// ends its goroutine with runtime.Goexit, the router fails the request with
// net::ERR_FAILED and reports a *TryError or ErrHijackHandlerExited through
// the request's OnError. The browser request URL is parsed with net/url. The
// router does not run handlers for a request whose URL it cannot parse: it
// fails the request, or continues it unmodified when ContinueUnparsableURLs is
// enabled, and reports the parse error through the router's OnError. With
// [proto.DecodeLenient], a paused request can lack its request details; the
// router fails it without running handlers and reports an error matching
// [proto.ErrMissingField] through the router's OnError.
type HijackRouter struct {
	run      func() error
	stop     func()
	stopOnce sync.Once
	stopErr  error
	setupErr error
	mu       sync.Mutex
	stopped  bool
	handlers []*hijackHandler
	onError  func(error)
	enable   *proto.FetchEnable
	client   proto.Client
	browser  *Browser

	// continueUnparsable continues requests whose URL net/url cannot parse.
	continueUnparsable bool
}

func newHijackRouter(browser *Browser, client proto.Client) *HijackRouter {
	return &HijackRouter{
		enable:   &proto.FetchEnable{},
		browser:  browser,
		client:   client,
		handlers: []*hijackHandler{},
		onError:  func(error) {},
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
		go r.serve(eventCtx, e)
		return false
	}))
	return r
}

// serve dispatches one paused request and resolves it exactly once. Handler
// panics and runtime.Goexit fail the request instead of the process or the page.
func (r *HijackRouter) serve(ctx context.Context, e *proto.FetchRequestPaused) {
	r.mu.Lock()
	handlers, onError, continueUnparsable := slices.Clone(r.handlers), r.onError, r.continueUnparsable
	r.mu.Unlock()

	h, err := r.new(ctx, e)
	if err != nil {
		if _, unparsable := errors.AsType[*url.Error](err); unparsable && continueUnparsable {
			onError(errors.Join(err, proto.FetchContinueRequest{RequestID: e.RequestID}.Call(r.client)))
			return
		}
		// Handlers cannot inspect this request, so by default it must not bypass them.
		onError(errors.Join(err, r.fail(e.RequestID)))
		return
	}
	h.OnError = onError
	report := func(err error) {
		if err == nil {
			return
		}
		if h.OnError != nil {
			h.OnError(err)
			return
		}
		onError(err)
	}

	resolving, returned := false, false
	defer func() {
		if returned {
			return
		}
		// runtime.Goexit skips Try's recover but still runs deferred calls.
		err := ErrHijackHandlerExited
		if !resolving {
			err = errors.Join(err, r.fail(e.RequestID))
		}
		report(err)
	}()
	var resolveErr error
	handlerErr := Try(func() { resolveErr = r.dispatch(h, e, handlers, &resolving) })
	returned = true
	if handlerErr != nil && !resolving {
		resolveErr = r.fail(e.RequestID)
	}
	report(errors.Join(handlerErr, resolveErr))
}

// dispatch runs the matching handlers and resolves the request. It sets
// resolving before the first resolution attempt.
func (r *HijackRouter) dispatch(ctx *Hijack, e *proto.FetchRequestPaused, handlers []*hijackHandler, resolving *bool) error {
	for _, h := range handlers {
		if !h.regexp.MatchString(e.Request.URL) || (h.resourceType != "" && h.resourceType != e.ResourceType) {
			continue
		}

		ctx.Skip = false
		h.handler(ctx)

		if ctx.continueRequest != nil {
			ctx.continueRequest.RequestID = e.RequestID
			*resolving = true
			return ctx.continueRequest.Call(r.client)
		}

		if ctx.Skip {
			continue
		}

		*resolving = true
		if ctx.Response.fail.ErrorReason != "" {
			return ctx.Response.fail.Call(r.client)
		}
		return ctx.Response.payload.Call(r.client)
	}
	// A route may have been removed after Chrome paused this request.
	// Continue unmatched or explicitly skipped requests.
	*resolving = true
	return proto.FetchContinueRequest{RequestID: e.RequestID}.Call(r.client)
}

// fail resolves a request that no handler resolved.
func (r *HijackRouter) fail(id proto.FetchRequestID) error {
	return proto.FetchFailRequest{RequestID: id, ErrorReason: proto.NetworkErrorReasonFailed}.Call(r.client)
}

// OnError sets the router's error handler. It is the initial Hijack.OnError of
// each later request and receives errors for requests the router resolves
// without running handlers, such as a URL that net/url cannot parse. It is
// called concurrently from the goroutines serving requests. A nil fn discards
// errors.
func (r *HijackRouter) OnError(fn func(error)) *HijackRouter {
	if fn == nil {
		fn = func(error) {}
	}
	r.mu.Lock()
	r.onError = fn
	r.mu.Unlock()
	return r
}

// ContinueUnparsableURLs sets whether the router continues, unmodified, the
// paused requests whose URL net/url cannot parse, such as a path with a stray
// '%' like "/sale-50%-off" or a host name containing '{'. By default the router
// fails them with net::ERR_FAILED. Either way, no handler runs for them, and
// the parse error goes to the router's OnError. It applies to later requests.
//
// Enabling it lets a page evade routes: a URL with an invalid percent escape,
// such as "/api/users%zz", or with such a host name still matches a route's
// pattern but reaches the network without the blocking, mocking, or rewriting
// that the route would apply. Enable it only when no route enforces a policy
// that the page must not evade.
func (r *HijackRouter) ContinueUnparsableURLs(enabled bool) *HijackRouter {
	r.mu.Lock()
	r.continueUnparsable = enabled
	r.mu.Unlock()
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

// new context. It fails when the event lacks a request or Go cannot parse its URL.
func (r *HijackRouter) new(ctx context.Context, e *proto.FetchRequestPaused) (*Hijack, error) {
	if e.Request == nil {
		return nil, missingEventField(e.ProtoEvent(), "FetchRequestPaused", "request")
	}
	u, err := url.Parse(e.Request.URL)
	if err != nil {
		return nil, fmt.Errorf("rod: hijack request URL: %w", err)
	}

	headers := http.Header{}
	for k, v := range e.Request.Headers {
		headers.Set(k, v.String())
	}

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
		OnError:                func(_ error) {},
		MaxResponseBodyBytes:   DefaultMaxResponseBodyBytes,
		MaxResponseHeaderBytes: DefaultMaxResponseHeaderBytes,

		browser: r.browser,
	}, nil
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

	// OnError receives errors resolving this request and recovered handler
	// panics. It starts as the router's OnError handler; nil also reports there.
	OnError func(error)

	// Skip to next handler
	Skip bool

	// MaxResponseBodyBytes limits the body that LoadResponse reads. It starts at
	// DefaultMaxResponseBodyBytes; zero or a negative value selects that default.
	// Fulfilling a body larger than about 74 MiB, or less with large headers,
	// exceeds Chrome's DevTools message limit and closes the connection.
	MaxResponseBodyBytes int64

	// MaxResponseHeaderBytes limits the response headers that LoadResponse
	// applies, counted as an HTTP/1.1 header block: each value adds the length
	// of its name and value plus four bytes. It starts at
	// DefaultMaxResponseHeaderBytes; zero or a negative value selects that
	// default. A higher limit lets LoadResponse apply headers that Chrome
	// rejects from the network. The headers and the base64 encoded body reach
	// Chrome in one message, and a message larger than 100 MiB closes the
	// DevTools connection. The client's transport applies its own limit first;
	// http.Transport allows 10 MiB by default.
	MaxResponseHeaderBytes int64

	// FollowRedirects makes LoadResponse follow redirects in Go, using the
	// client's CheckRedirect policy, or http.Client's default policy of at most
	// 10 redirects when the client has none. The final response then fulfills
	// the original request, and headers of the redirect responses, such as
	// Set-Cookie, do not reach the browser. It starts false: LoadResponse
	// returns a 3xx response to the browser, which follows it under its own
	// policies.
	//
	// Following redirects in Go gives the page the final response as the
	// response of the original URL. A page whose request is redirected, such
	// as by its own server or an open redirect, can then read the response of
	// any URL the client can reach, including internal services, as
	// same-origin data; the browser's CORS and private network protections do
	// not apply. The final response's headers, such as Set-Cookie,
	// Content-Security-Policy, and CORS headers, also apply to the original
	// URL, so a redirect target can set cookies for the page's origin. Go also
	// forwards the request headers to every redirect target, including custom
	// headers such as API tokens. It drops Authorization, Cookie, and the other
	// credential headers that net/http removes only when the target host is
	// neither the original host nor one of its subdomains, so they reach
	// subdomains and other ports and schemes of the same host, including plain
	// http. When the
	// original request has a Referer, Go sends it unchanged to each target;
	// otherwise it sends the previous URL, with its path and query, as the
	// Referer, except on a redirect from https to http. Enable it only for
	// requests whose redirect targets you trust, or give the client a
	// CheckRedirect that rejects other targets and removes the headers they
	// must not receive.
	FollowRedirects bool

	continueRequest *proto.FetchContinueRequest

	// CustomState is used to store things for this context
	CustomState any

	browser *Browser
}

// ContinueRequest without hijacking. The RequestID will be set by the router, you don't have to set it.
func (h *Hijack) ContinueRequest(cq *proto.FetchContinueRequest) {
	h.continueRequest = cq
}

// LoadResponse sends the request with client and loads the response as the
// default response to override. A nil client uses http.DefaultClient.
//
// Unless FollowRedirects is set, redirects are not followed. A 3xx response and
// its Location header return to the browser, which follows the redirect under
// its own CORS and network policies; routes intercept the next request again,
// and the client's CheckRedirect is not used. The request is sent by Go, so it
// uses the client's transport, proxy, and TLS settings and bypasses the
// browser's proxy configuration and private network protections.
//
// Response headers larger than MaxResponseHeaderBytes return an error matching
// ErrResponseHeadersTooLarge. With loadBody, a body larger than
// MaxResponseBodyBytes returns an error matching ErrResponseBodyTooLarge.
// Responses to HEAD requests and 1xx, 204,
// and 304 responses load an empty body whatever their Content-Length. The
// status, headers, and body are applied only after the response loads
// successfully.
func (h *Hijack) LoadResponse(client *http.Client, loadBody bool) error {
	if client == nil {
		client = http.DefaultClient
	}
	if !h.FollowRedirects {
		noRedirect := *client
		noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &noRedirect
	}
	res, err := client.Do(h.Request.req)
	if err != nil {
		return err
	}

	defer func() { _ = res.Body.Close() }()

	headerLimit := h.MaxResponseHeaderBytes
	if headerLimit <= 0 {
		headerLimit = DefaultMaxResponseHeaderBytes
	}
	if size := responseHeaderSize(res.Header); size > headerLimit {
		return fmt.Errorf("%w: %d bytes exceed %d", ErrResponseHeadersTooLarge, size, headerLimit)
	}

	// A nil body would reach Chrome as null instead of an empty body.
	body := []byte{}
	if loadBody && hasResponseBody(h.Request.req.Method, res.StatusCode) {
		limit := h.MaxResponseBodyBytes
		if limit <= 0 {
			limit = DefaultMaxResponseBodyBytes
		}
		if res.ContentLength > limit {
			return fmt.Errorf("%w: Content-Length %d exceeds %d bytes", ErrResponseBodyTooLarge, res.ContentLength, limit)
		}
		// Read one byte past the limit to detect an oversized body.
		body, err = io.ReadAll(io.LimitReader(res.Body, min(limit, math.MaxInt64-1)+1))
		if err != nil {
			return err
		}
		if int64(len(body)) > limit {
			return fmt.Errorf("%w: body exceeds %d bytes", ErrResponseBodyTooLarge, limit)
		}
	}

	h.Response.payload.ResponseCode = res.StatusCode
	h.Response.RawResponse = res

	h.Response.replaceHeaders(res.Header)

	if loadBody {
		h.Response.payload.Body = body
	}

	return nil
}

// responseHeaderSize returns the size of header as an HTTP/1.1 header block.
func responseHeaderSize(header http.Header) int64 {
	var size int64
	for name, values := range header {
		for _, value := range values {
			size += int64(len(name) + len(value) + len(": \r\n"))
		}
	}
	return size
}

// hasResponseBody reports whether a response to method with status can carry
// content. Responses to HEAD and 1xx, 204, and 304 responses cannot, and their
// Content-Length describes the resource instead of a body (RFC 9110).
func hasResponseBody(method string, status int) bool {
	return method != http.MethodHead && (status < 100 || status > 199) &&
		status != http.StatusNoContent && status != http.StatusNotModified
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

// URL of the request. It is never nil: handlers never run for requests whose
// URL net/url cannot parse. The router fails those requests or, with
// HijackRouter.ContinueUnparsableURLs, continues them.
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

// replaceHeaders applies SetHeader semantics to every name in header in one
// pass: existing values of those names are removed, and all of header's values
// are appended. Calling SetHeader per name would scan the list once per name.
func (ctx *HijackResponse) replaceHeaders(header http.Header) {
	names := make(map[string]bool, len(header))
	for name, values := range header {
		if len(values) > 0 {
			names[strings.ToLower(name)] = true
		}
	}
	headers := slices.DeleteFunc(ctx.payload.ResponseHeaders, func(entry *proto.FetchHeaderEntry) bool {
		return names[strings.ToLower(entry.Name)]
	})
	for name, values := range header {
		for _, value := range values {
			headers = append(headers, &proto.FetchHeaderEntry{Name: name, Value: value})
		}
	}
	ctx.payload.ResponseHeaders = headers
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

// AuthCredentials answer HTTP authentication challenges from one challenger.
type AuthCredentials struct {
	// Source selects server (HTTP 401) or proxy (HTTP 407) challenges.
	Source proto.FetchAuthChallengeSource

	// Origin is the challenger's scheme, host, and optional port, such as
	// "https://example.com" or "http://proxy.example:3128". For a proxy, use the
	// proxy's own origin. The port defaults to 80 for http and 443 for https.
	// Use the ASCII (Punycode) form of internationalized host names. It must be
	// empty when AnyOrigin is set.
	Origin string

	// AnyOrigin answers the first challenge from Source whatever its origin.
	// When it is false, Origin selects the challenger.
	//
	// Set it only when the challenger cannot be named in advance, such as a
	// proxy that a PAC script selects: the credentials go to whichever
	// challenger of Source comes first during the wait. Proxy challenges come
	// only from proxies that Chrome sends requests through; Chrome fails a 407
	// response from a server that it reaches directly. Server challenges can
	// come from any server that the browser loads a resource from, including a
	// hostile page's own server, and Basic authentication over http sends the
	// credentials unencrypted.
	AnyOrigin bool

	Username string
	Password string
}

// HandleAuth answers the next HTTP authentication challenge whose source and
// origin match credentials, or whose source matches when
// credentials.AnyOrigin is set, then restores the previous Fetch configuration.
// Other challenges receive the browser's default response while the wait is
// active; headless Chrome fails those requests with
// net::ERR_INVALID_AUTH_CREDENTIALS rather than exposing the credentials. So
// does a challenge that [proto.DecodeLenient] leaves without its source, or
// without its origin when credentials.AnyOrigin is not set.
// Requests paused during the wait continue unmodified, so run at most one
// HandleAuth wait per browser and no other Fetch interception during it.
//
// Call the returned wait function to handle authentication. It returns
// validation errors matching ErrAuthCredentials, setup, wait, and cleanup errors.
// Ref: https://developer.mozilla.org/en-US/docs/Web/HTTP/Authentication
func (b *Browser) HandleAuth(credentials AuthCredentials) func() error {
	origin, err := credentials.origin()
	if err != nil {
		return func() error { return err }
	}
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
				matched := credentials.matches(auth.AuthChallenge, origin)
				response := &proto.FetchAuthChallengeResponse{Response: proto.FetchAuthChallengeResponseResponseDefault}
				if matched {
					response = &proto.FetchAuthChallengeResponse{
						Response: proto.FetchAuthChallengeResponseResponseProvideCredentials,
						Username: credentials.Username,
						Password: credentials.Password,
					}
				}
				operationErr = (proto.FetchContinueWithAuth{
					RequestID:             auth.RequestID,
					AuthChallengeResponse: response,
				}).Call(b)
				return matched || operationErr != nil
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

// origin validates the credentials and returns their normalized origin, which
// is empty when AnyOrigin is set.
func (c AuthCredentials) origin() (string, error) {
	if c.Source != proto.FetchAuthChallengeSourceServer && c.Source != proto.FetchAuthChallengeSourceProxy {
		return "", fmt.Errorf("%w: source %q is neither Server nor Proxy", ErrAuthCredentials, c.Source)
	}
	if c.AnyOrigin {
		if c.Origin != "" {
			return "", fmt.Errorf("%w: Origin must be empty when AnyOrigin is set", ErrAuthCredentials)
		}
		return "", nil
	}
	origin, err := normalizeAuthOrigin(c.Origin)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrAuthCredentials, err)
	}
	return origin, nil
}

// matches reports whether challenge comes from the credentials' source and
// origin, or from their source when AnyOrigin is set.
func (c AuthCredentials) matches(challenge *proto.FetchAuthChallenge, origin string) bool {
	if challenge == nil || challenge.Source != c.Source {
		return false
	}
	if c.AnyOrigin {
		return true
	}
	challenger, err := normalizeAuthOrigin(challenge.Origin)
	return err == nil && challenger == origin
}

// normalizeAuthOrigin returns scheme://host:port with a lowercase scheme and
// host and an explicit port.
// Errors never quote raw, which can carry credentials such as a proxy URL's
// user information.
func normalizeAuthOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// Parse errors quote the input, and escape errors quote part of it.
		return "", errors.New("origin is not a valid URL")
	}
	if u.User != nil {
		return "", errors.New("origin must not contain user information")
	}
	if u.Scheme == "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("origin for host %q must contain only a scheme, host, and optional port", u.Host)
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if host == "" {
		return "", errors.New("origin has no host")
	}
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", fmt.Errorf("origin for host %q needs a port for scheme %s", u.Host, scheme)
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}
