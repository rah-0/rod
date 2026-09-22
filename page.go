package rod

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/rah-0/rod/internal/observable"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/devices"
	"github.com/rah-0/rod/lib/js"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// Page implements these interfaces.
var (
	_ proto.Client      = &Page{}
	_ proto.Contextable = &Page{}
	_ proto.Sessionable = &Page{}
)

// Page represents the webpage.
// We try to hold as less states as possible.
// When a page is closed by Rod or not all the ongoing operations an events on it will abort.
type Page struct {
	// TargetID is a unique ID for a remote page.
	// It's usually used in events sent from the browser to tell which page an event belongs to.
	TargetID proto.TargetTargetID

	// FrameID is a unique ID for a browsing context.
	// Usually, different FrameID means different javascript execution context.
	// Frames in one renderer share a TargetID; frames in another renderer have
	// their own target and session.
	FrameID proto.PageFrameID

	// SessionID is a unique ID for a page attachment to a controller.
	// It's usually used in transport layer to tell which page to send the control signal.
	// A page can attached to multiple controllers, the browser uses it distinguish controllers.
	SessionID proto.TargetSessionID

	e eFunc

	ctx context.Context
	// sessionCtx ends when the attached target or its connection closes.
	sessionCtx context.Context

	// Used to abort all ongoing actions when a page closes.
	sessionCancel func()

	root *Page

	sleeper func() utils.Sleeper

	browser *Browser
	event   *observable.Observable[*Message]

	// devices
	Mouse    *Mouse
	Keyboard *Keyboard
	Touch    *Touch

	element *Element // iframe only

	jsCtxLock *sync.Mutex
	jsCtxID   *proto.RuntimeRemoteObjectID // use pointer so that page clones can share the change
	helpers   *jsHelperCache
}

// String interface.
func (p *Page) String() string {
	id := p.TargetID
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("<page:%s>", id)
}

// IsIframe tells if it's iframe.
func (p *Page) IsIframe() bool {
	return p.element != nil
}

// GetSessionID interface.
func (p *Page) GetSessionID() proto.TargetSessionID {
	return p.SessionID
}

// Browser of the page.
func (p *Page) Browser() *Browser {
	return p.browser
}

// Info of the page, such as the URL or title of the page.
func (p *Page) Info() (*proto.TargetTargetInfo, error) {
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	defer cancel()
	return p.browser.Context(ctx).pageInfo(p.TargetID)
}

// HTML of the page.
func (p *Page) HTML() (string, error) {
	el, err := p.Element("html")
	if err != nil {
		return "", err
	}
	return el.HTML()
}

// Cookies returns the page cookies. By default it will return the cookies for current page.
// The urls is the list of URLs for which applicable cookies will be fetched.
func (p *Page) Cookies(urls []string) ([]*proto.NetworkCookie, error) {
	if len(urls) == 0 {
		info, err := p.Info()
		if err != nil {
			return nil, err
		}
		urls = []string{info.URL}
	}

	res, err := proto.NetworkGetCookies{Urls: urls}.Call(p)
	if err != nil {
		return nil, err
	}
	return res.Cookies, nil
}

// SetCookies is similar to Browser.SetCookies .
func (p *Page) SetCookies(cookies []*proto.NetworkCookieParam) error {
	if cookies == nil {
		return proto.NetworkClearBrowserCookies{}.Call(p)
	}
	return proto.NetworkSetCookies{Cookies: cookies}.Call(p)
}

// SetExtraHeaders sends extra HTTP headers with requests from this page.
// The returned function releases its Network domain lease and reports cleanup errors.
func (p *Page) SetExtraHeaders(dict []string) (func() error, error) {
	headers := proto.NetworkHeaders{}

	for i := 0; i < len(dict); i += 2 {
		headers[dict[i]] = jsonvalue.New(dict[i+1])
	}

	restore, err := p.EnableDomain(&proto.NetworkEnable{})
	if err != nil {
		return nil, err
	}
	if err := (proto.NetworkSetExtraHTTPHeaders{Headers: headers}).Call(p); err != nil {
		return nil, errors.Join(err, restore())
	}
	return restore, nil
}

// SetUserAgent (browser brand, accept-language, etc) of the page.
// If req is nil, a default user agent will be used, a typical mac chrome.
func (p *Page) SetUserAgent(req *proto.NetworkSetUserAgentOverride) error {
	if req == nil {
		req = devices.LaptopWithMDPIScreen.UserAgentEmulation()
	}
	return req.Call(p)
}

// SetBlockedURLs For some requests that do not want to be triggered,
// such as some dangerous operations, delete, quit logout, etc.
// Wildcards ('*') are allowed, such as ["*/api/logout/*","delete"].
// NOTE: if you set empty pattern "", it will block all requests.
func (p *Page) SetBlockedURLs(urls []string) error {
	if urls == nil {
		urls = []string{}
	}
	if err := (proto.NetworkEnable{}).Call(p); err != nil {
		return err
	}
	return proto.NetworkSetBlockedURLs{Urls: urls}.Call(p)
}

// Navigate to the url. If the url is empty, "about:blank" will be used.
// It will return immediately after the server responds the http header.
func (p *Page) Navigate(url string) error {
	if url == "" {
		url = "about:blank"
	}

	// try to stop loading
	_ = p.StopLoading()

	request := proto.PageNavigate{URL: url}
	if p.IsIframe() && p.SessionID == p.element.page.SessionID {
		request.FrameID = p.FrameID
	}
	res, err := request.Call(p)
	if err != nil {
		return err
	}
	if res.ErrorText != "" {
		return &NavigationError{res.ErrorText}
	}

	p.unsetJSCtxID()

	return nil
}

// NavigateBack history.
func (p *Page) NavigateBack() error {
	// Not using cdp API because it doesn't work for iframe
	_, err := p.Evaluate(Eval(`() => history.back()`).ByUser())
	return err
}

// ResetNavigationHistory reset history.
func (p *Page) ResetNavigationHistory() error {
	err := proto.PageResetNavigationHistory{}.Call(p)
	return err
}

// GetNavigationHistory get navigation history.
func (p *Page) GetNavigationHistory() (*proto.PageGetNavigationHistoryResult, error) {
	return proto.PageGetNavigationHistory{}.Call(p)
}

// NavigateForward history.
func (p *Page) NavigateForward() error {
	// Not using cdp API because it doesn't work for iframe
	_, err := p.Evaluate(Eval(`() => history.forward()`).ByUser())
	return err
}

// Reload page.
func (p *Page) Reload() error {
	p, cancel := p.WithCancel()
	defer cancel()

	wait := p.EachEvent(On(func(e *proto.PageFrameNavigated, _ proto.TargetSessionID) bool {
		return e.Frame.ID == p.FrameID
	}))

	// Not using cdp API because it doesn't work for iframe
	_, err := p.Evaluate(Eval(`() => location.reload()`).ByUser())
	if err != nil {
		return err
	}

	if err := wait(); err != nil {
		return err
	}

	p.unsetJSCtxID()

	return nil
}

// Activate (focuses) the page.
func (p *Page) Activate() (*Page, error) {
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	defer cancel()
	err := proto.TargetActivateTarget{TargetID: p.TargetID}.Call(p.browser.Context(ctx))
	return p, err
}

func (p *Page) getWindowID() (proto.BrowserWindowID, error) {
	res, err := proto.BrowserGetWindowForTarget{TargetID: p.TargetID}.Call(p)
	if err != nil {
		return 0, err
	}
	return res.WindowID, err
}

// GetWindow position and size info.
func (p *Page) GetWindow() (*proto.BrowserBounds, error) {
	id, err := p.getWindowID()
	if err != nil {
		return nil, err
	}

	res, err := proto.BrowserGetWindowBounds{WindowID: id}.Call(p)
	if err != nil {
		return nil, err
	}

	return res.Bounds, nil
}

// SetWindow location and size.
func (p *Page) SetWindow(bounds *proto.BrowserBounds) error {
	id, err := p.getWindowID()
	if err != nil {
		return err
	}

	err = proto.BrowserSetWindowBounds{WindowID: id, Bounds: bounds}.Call(p)
	return err
}

// SetViewport overrides the values of device screen dimensions.
func (p *Page) SetViewport(params *proto.EmulationSetDeviceMetricsOverride) error {
	if params == nil {
		return proto.EmulationClearDeviceMetricsOverride{}.Call(p)
	}
	return params.Call(p)
}

// SetDocumentContent sets the page document html content.
func (p *Page) SetDocumentContent(html string) error {
	return proto.PageSetDocumentContent{
		FrameID: p.FrameID,
		HTML:    html,
	}.Call(p)
}

// Emulate the device, such as iPhone9. If device is devices.Clear, it will clear the override.
func (p *Page) Emulate(device devices.Device) error {
	err := p.SetViewport(device.MetricsEmulation())
	if err != nil {
		return err
	}

	err = device.TouchEmulation().Call(p)
	if err != nil {
		return err
	}

	return p.SetUserAgent(device.UserAgentEmulation())
}

// StopLoading forces the page stop navigation and pending resource fetches.
func (p *Page) StopLoading() error {
	return proto.PageStopLoading{}.Call(p)
}

// Close tries to close page, running its beforeunload hooks, if has any.
func (p *Page) Close() error {
	ctx, cancel := contextWithSession(p.ctx, p.browser.connectionCtx)
	defer cancel()
	messages := p.browser.Context(ctx).Event()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Destruction can end the session before Chrome acknowledges Page.close.
		// This command must survive that session ending to read its acknowledgement.
		req := proto.PageClose{}
		_, err := p.browser.client.Call(ctx, string(p.SessionID), req.ProtoReq(), req)
		if errors.Is(err, cdp.ErrNotAttachedToActivePage) {
			// Chrome can temporarily reject closure during navigation.
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		} else if err != nil {
			return err
		}
		break
	}

	for msg := range messages {
		destroyed := proto.TargetTargetDestroyed{}
		closed := proto.PageJavascriptDialogClosed{}
		if msg.Load(&destroyed) && destroyed.TargetID == p.TargetID {
			p.terminateSession()
			return nil
		}
		if msg.SessionID == p.SessionID && msg.Load(&closed) && !closed.Result {
			return &PageCloseCanceledError{}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrBrowserDisconnected
}

// TriggerFavicon supports when browser in headless mode
// to trigger favicon's request. Pay attention to this
// function only supported when browser in headless mode,
// if you call it in no-headless mode, it will raise an error
// with the message "browser is no-headless".
func (p *Page) TriggerFavicon() error {
	// check if browser whether in headless mode
	// if not in headless mode then raise error
	if !p.browser.isHeadless() {
		return errors.New("browser is no-headless")
	}

	_, err := p.Evaluate(evalHelper(js.TriggerFavicon).ByPromise())
	if err != nil {
		return err
	}
	return nil
}

// HandleDialog accepts or dismisses next JavaScript initiated dialog (alert, confirm, prompt, or onbeforeunload).
// Because modal dialog will block js, usually you have to trigger the dialog in another goroutine.
// For example:
//
//	wait, handle := page.MustHandleDialog()
//	go page.MustElement("button").MustClick()
//	wait()
//	handle(true, "")
func (p *Page) HandleDialog() (
	wait func() (*proto.PageJavascriptDialogOpening, error),
	handle func(*proto.PageHandleJavaScriptDialog) error,
) {
	var e proto.PageJavascriptDialogOpening
	w := p.WaitEvent(&e)

	return func() (*proto.PageJavascriptDialogOpening, error) {
			if err := w(); err != nil {
				return nil, err
			}
			return &e, nil
		}, func(h *proto.PageHandleJavaScriptDialog) error {
			return h.Call(p)
		}
}

// HandleFileDialog returns a function that waits for the next file chooser and
// sets its files. Completion or cancellation restores the previous interception
// setting, including when the returned function is never called.
func (p *Page) HandleFileDialog() (func([]string) error, error) {
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	view := p.Context(ctx)
	previous := proto.PageSetInterceptFileChooserDialog{}
	p.LoadState(&previous)
	var once sync.Once
	var cleanupErr error
	cleanup := func() {
		once.Do(func() {
			defer cancel()
			if !previous.Enabled {
				cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(p.ctx), 5*time.Second)
				defer stop()
				cleanupErr = previous.Call(p.Context(cleanupCtx))
			}
		})
	}
	if !previous.Enabled {
		if err := (proto.PageSetInterceptFileChooserDialog{Enabled: true}).Call(view); err != nil {
			cleanup()
			return nil, errors.Join(err, cleanupErr)
		}
	}
	stopCleanup := context.AfterFunc(ctx, cleanup)

	var e proto.PageFileChooserOpened
	w := view.WaitEvent(&e)

	return func(paths []string) (err error) {
		defer func() {
			stopCleanup()
			cleanup()
			err = errors.Join(err, cleanupErr)
		}()
		if err := w(); err != nil {
			return err
		}

		return proto.DOMSetFileInputFiles{
			Files:         utils.AbsolutePaths(paths),
			BackendNodeID: e.BackendNodeID,
		}.Call(p)
	}, nil
}

// Screenshot captures the screenshot of current page.
func (p *Page) Screenshot(fullPage bool, req *proto.PageCaptureScreenshot) ([]byte, error) {
	if req == nil {
		req = &proto.PageCaptureScreenshot{}
	}
	if fullPage {
		metrics, err := proto.PageGetLayoutMetrics{}.Call(p)
		if err != nil {
			return nil, err
		}

		if metrics.CSSContentSize == nil {
			return nil, errors.New("failed to get css content size")
		}

		oldView := proto.EmulationSetDeviceMetricsOverride{}
		set := p.LoadState(&oldView)
		view := oldView
		view.Width = int(metrics.CSSContentSize.Width)
		view.Height = int(metrics.CSSContentSize.Height)

		err = p.SetViewport(&view)
		if err != nil {
			return nil, err
		}

		defer func() { // try to recover the viewport
			if !set {
				_ = proto.EmulationClearDeviceMetricsOverride{}.Call(p)
				return
			}

			_ = p.SetViewport(&oldView)
		}()
	}

	shot, err := req.Call(p)
	if err != nil {
		return nil, err
	}
	return shot.Data, nil
}

// ScrollScreenshotOptions is the options for the ScrollScreenshot.
type ScrollScreenshotOptions struct {
	// Format (optional) Image compression format (defaults to png).
	Format proto.PageCaptureScreenshotFormat `json:"format,omitempty"`

	// Quality (optional) Compression quality from range [0..100] (jpeg only).
	Quality *int `json:"quality,omitempty"`

	// FixedTop (optional) The number of pixels to skip from the top.
	// It is suitable for optimizing the screenshot effect when there is a fixed
	// positioning element at the top of the page.
	FixedTop float64

	// FixedBottom (optional) The number of pixels to skip from the bottom.
	FixedBottom float64

	// WaitPerScroll until no animation (default is 300ms)
	WaitPerScroll time.Duration
}

// ScrollScreenshot Scroll screenshot does not adjust the size of the viewport,
// but achieves it by scrolling and capturing screenshots in a loop, and then stitching them together.
// Note that this method also has a flaw: when there are elements with fixed
// positioning on the page (usually header navigation components),
// these elements will appear repeatedly, you can set the FixedTop parameter to optimize it.
//
// Only support png and jpeg format yet, webP is not supported because no suitable processing
// library was found in golang.
func (p *Page) ScrollScreenshot(opt *ScrollScreenshotOptions) ([]byte, error) {
	if opt == nil {
		opt = &ScrollScreenshotOptions{}
	}
	if opt.WaitPerScroll == 0 {
		opt.WaitPerScroll = time.Millisecond * 300
	}

	metrics, err := proto.PageGetLayoutMetrics{}.Call(p)
	if err != nil {
		return nil, err
	}

	if metrics.CSSContentSize == nil || metrics.CSSVisualViewport == nil {
		return nil, errors.New("failed to get css content size")
	}

	viewpointHeight := metrics.CSSVisualViewport.ClientHeight
	contentHeight := metrics.CSSContentSize.Height

	var scrollTop float64
	var images []utils.ImgWithBox

	for {
		clip := &proto.PageViewport{
			X:     0,
			Y:     scrollTop,
			Width: metrics.CSSVisualViewport.ClientWidth,
			Scale: 1,
		}

		scrollY := viewpointHeight - (opt.FixedTop + opt.FixedBottom)
		if scrollTop+viewpointHeight > contentHeight {
			clip.Height = contentHeight - scrollTop
		} else {
			clip.Height = scrollY
			if scrollTop != 0 {
				clip.Y += opt.FixedTop
			}
		}

		req := &proto.PageCaptureScreenshot{
			Format:                opt.Format,
			Quality:               opt.Quality,
			Clip:                  clip,
			FromSurface:           new(true), // View capture ignores the requested clip.
			CaptureBeyondViewport: new(false),
			OptimizeForSpeed:      new(false),
		}
		shot, err := req.Call(p)
		if err != nil {
			return nil, err
		}

		images = append(images, utils.ImgWithBox{Img: shot.Data})

		scrollTop += scrollY
		if scrollTop >= contentHeight {
			break
		}

		err = p.Mouse.Scroll(0, scrollY, 1)
		if err != nil {
			return nil, fmt.Errorf("scroll error: %w", err)
		}

		err = p.WaitDOMStable(opt.WaitPerScroll, 0)
		if err != nil {
			return nil, fmt.Errorf("WaitDOMStable error: %w", err)
		}
	}

	var imgOption *utils.ImgOption
	if opt.Quality != nil {
		imgOption = &utils.ImgOption{
			Quality: *opt.Quality,
		}
	}
	bs, err := utils.SplicePngVertical(images, opt.Format, imgOption)
	if err != nil {
		return nil, err
	}

	return bs, nil
}

// CaptureDOMSnapshot Returns a document snapshot, including the full DOM tree of the root node
// (including iframes, template contents, and imported documents) in a flattened array,
// as well as layout and white-listed computed style information for the nodes.
// Shadow DOM in the returned DOM tree is flattened.
// `Documents` The nodes in the DOM tree. The DOMNode at index 0 corresponds to the root document.
// `Strings` Shared string table that all string properties refer to with indexes.
// Normally use `Strings` is enough.
func (p *Page) CaptureDOMSnapshot() (domSnapshot *proto.DOMSnapshotCaptureSnapshotResult, err error) {
	if err := (proto.DOMSnapshotEnable{}).Call(p); err != nil {
		return nil, err
	}

	snapshot, err := proto.DOMSnapshotCaptureSnapshot{
		ComputedStyles:                 []string{},
		IncludePaintOrder:              new(true),
		IncludeDOMRects:                new(true),
		IncludeBlendedBackgroundColors: new(true),
		IncludeTextColorOpacities:      new(true),
	}.Call(p)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// PDF prints page as PDF.
func (p *Page) PDF(req *proto.PagePrintToPDF) (*StreamReader, error) {
	req.TransferMode = proto.PagePrintToPDFTransferModeReturnAsStream
	res, err := req.Call(p)
	if err != nil {
		return nil, err
	}

	return NewStreamReader(p, res.Stream), nil
}

// GetResource content by the url. Such as image, css, html, etc.
// Use the [proto.PageGetResourceTree] to list all the resources.
func (p *Page) GetResource(url string) ([]byte, error) {
	res, err := proto.PageGetResourceContent{
		FrameID: p.FrameID,
		URL:     url,
	}.Call(p)
	if err != nil {
		return nil, err
	}

	data := res.Content

	var bin []byte
	if res.Base64Encoded {
		bin, err = base64.StdEncoding.DecodeString(data)
		utils.E(err)
	} else {
		bin = []byte(data)
	}

	return bin, nil
}

// WaitOpen waits for the next new page opened by the current one.
// The wait ends if the opener's session ends. The returned page uses the caller's
// operation context independently of the opener's session and the wait's cleanup.
func (p *Page) WaitOpen() func() (*Page, error) {
	var targetID proto.TargetTargetID

	b := p.browser.Context(p.ctx)
	wait := b.eachEventWithSession("", p.sessionCtx, On(func(e *proto.TargetTargetCreated, _ proto.TargetSessionID) bool {
		targetID = e.TargetInfo.TargetID
		return e.TargetInfo.OpenerID == p.TargetID
	}))

	return func() (*Page, error) {
		defer p.tryTrace(TraceTypeWait, "wait open")()
		if err := wait(); err != nil {
			return nil, err
		}
		return b.PageFromTarget(targetID)
	}
}

// EachEvent subscribes to typed handlers for this page. Returning true from a
// handler stops the wait. For example, dismiss dialogs until the page is closed:
//
//	go page.EachEvent(rod.On(func(e *proto.PageJavascriptDialogOpening, _ proto.TargetSessionID) bool {
//	    _ = proto.PageHandleJavaScriptDialog{Accept: false}.Call(page)
//	    return false
//	}))()
func (p *Page) EachEvent(handlers ...EventHandler) func() error {
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	wait := p.browser.Context(ctx).eachEvent(p.SessionID, handlers...)
	return func() error {
		defer cancel()
		return wait()
	}
}

// WaitEvent loads the next matching event into out. E is a concrete protocol event type.
func (p *Page) WaitEvent[E proto.Event](out *E) func() error {
	defer p.tryTrace(TraceTypeWait, "event", (*out).ProtoEvent())()
	return p.EachEvent(On(func(event *E, _ proto.TargetSessionID) bool {
		*out = *event
		return true
	}))
}

// WaitNavigation wait for a page lifecycle event when navigating.
// Usually you will wait for [proto.PageLifecycleEventNameNetworkAlmostIdle].
func (p *Page) WaitNavigation(name proto.PageLifecycleEventName) func() error {
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	p = p.Context(ctx)
	release, setupErr := p.browser.Context(ctx).acquireDomain(p.SessionID, &proto.PageSetLifecycleEventsEnabled{Enabled: true})
	// Enabling lifecycle events replays milestones from the current document.
	// Subscribe afterward so a later navigation cannot consume those old events.
	wait := func() error { return setupErr }
	if setupErr == nil {
		wait = p.EachEvent(On(func(e *proto.PageLifecycleEvent, _ proto.TargetSessionID) bool {
			return e.Name == name && e.FrameID == p.FrameID
		}))
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() {
		cleanupOnce.Do(func() {
			if release != nil {
				ctx, stop := context.WithTimeout(context.WithoutCancel(p.ctx), 5*time.Second)
				defer stop()
				cleanupErr = release(ctx)
			}
		})
	}
	stopCleanup := context.AfterFunc(ctx, cleanup)
	if setupErr != nil {
		cancel()
	}

	return func() (err error) {
		defer p.tryTrace(TraceTypeWait, "navigation", name)()
		defer func() {
			cancel()
			stopCleanup()
			cleanup()
			err = errors.Join(err, cleanupErr)
		}()
		return wait()
	}
}

// WaitRequestIdle returns a wait function that waits until no request for d duration.
// Be careful, d is not the max wait timeout, it's the least idle time.
// If you want to set a timeout you can use the [Page.Timeout] function.
// Use the includes and excludes regexp list to filter the requests by their url.
func (p *Page) WaitRequestIdle(
	d time.Duration,
	includes, excludes []string,
	excludeTypes []proto.NetworkResourceType,
) func() error {
	defer p.tryTrace(TraceTypeWait, "request-idle")()
	if d < 0 {
		return func() error { return fmt.Errorf("idle duration must not be negative: %s", d) }
	}

	if excludeTypes == nil {
		excludeTypes = []proto.NetworkResourceType{
			proto.NetworkResourceTypeWebSocket,
			proto.NetworkResourceTypeEventSource,
			proto.NetworkResourceTypeMedia,
			proto.NetworkResourceTypeImage,
			proto.NetworkResourceTypeFont,
		}
	}

	if len(includes) == 0 {
		includes = []string{""}
	}

	ctx, stop := contextWithSession(p.ctx, p.sessionCtx)
	ctx, cancel := context.WithCancelCause(ctx)
	p = p.Context(ctx)
	match := genRegMatcher(includes, excludes)
	waitList := map[proto.NetworkRequestID]string{}
	idleCounter := utils.NewIdleCounter(d)
	update := p.tryTraceReq(includes, excludes)
	update(nil)

	checkDone := func(id proto.NetworkRequestID) {
		if _, has := waitList[id]; has {
			delete(waitList, id)
			update(waitList)
			idleCounter.Done()
		}
	}

	wait := p.EachEvent(On(func(sent *proto.NetworkRequestWillBeSent, _ proto.TargetSessionID) bool {
		if slices.Contains(excludeTypes, sent.Type) {
			return false
		}

		if match(sent.Request.URL) {
			// Redirect will send multiple NetworkRequestWillBeSent events with the same RequestID,
			// we should filter them out.
			if _, has := waitList[sent.RequestID]; !has {
				waitList[sent.RequestID] = sent.Request.URL
				update(waitList)
				idleCounter.Add()
			}
		}
		return false
	}), On(func(e *proto.NetworkLoadingFinished, _ proto.TargetSessionID) bool {
		checkDone(e.RequestID)
		return false
	}), On(func(e *proto.NetworkLoadingFailed, _ proto.TargetSessionID) bool {
		checkDone(e.RequestID)
		return false
	}))

	return func() error {
		defer stop()
		defer cancel(context.Canceled)
		done := make(chan struct{})
		go func() {
			defer close(done)
			idleCounter.Wait(p.ctx)
			cancel(errWaitCompleted)
		}()
		err := wait()
		cancel(context.Canceled)
		<-done
		return err
	}
}

// WaitDOMStable waits until the change of the DOM tree is less or equal than diff percent for d duration.
// Be careful, d is not the max wait timeout, it's the least stable time.
// If you want to set a timeout you can use the [Page.Timeout] function.
func (p *Page) WaitDOMStable(d time.Duration, diff float64) error {
	defer p.tryTrace(TraceTypeWait, "dom-stable")()
	if d <= 0 {
		return fmt.Errorf("stable duration must be positive: %s", d)
	}
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	defer cancel()
	p = p.Context(ctx)

	domSnapshot, err := p.CaptureDOMSnapshot()
	if err != nil {
		return err
	}

	t := time.NewTicker(d)
	defer t.Stop()

	for {
		select {
		case <-t.C:
		case <-p.ctx.Done():
			return p.ctx.Err()
		}

		currentDomSnapshot, err := p.CaptureDOMSnapshot()
		if err != nil {
			return err
		}

		common := longestCommonSubsequenceLength(p.ctx, domSnapshot.Strings, currentDomSnapshot.Strings)

		df := float64(0)
		if len(currentDomSnapshot.Strings) != 0 {
			df = 1 - float64(common)/float64(len(currentDomSnapshot.Strings))
		}
		if df <= diff {
			break
		}

		domSnapshot = currentDomSnapshot
	}
	return nil
}

// WaitStable waits until the page is stable for d duration.
func (p *Page) WaitStable(d time.Duration) error {
	defer p.tryTrace(TraceTypeWait, "stable")()
	p, cancel := p.WithCancel()
	defer cancel()
	var err error
	var firstError sync.Once
	setErr := func(e error) {
		if e != nil {
			firstError.Do(func() { err = e; cancel() })
		}
	}

	utils.All(func() {
		setErr(p.WaitLoad())
	}, func() {
		setErr(p.WaitRequestIdle(d, nil, nil, nil)())
	}, func() {
		setErr(p.WaitDOMStable(d, 0))
	})()

	return err
}

// WaitIdle waits until the next window.requestIdleCallback is called.
func (p *Page) WaitIdle(timeout time.Duration) (err error) {
	_, err = p.Evaluate(evalHelper(js.WaitIdle, timeout.Milliseconds()).ByPromise())
	return err
}

// WaitRepaint waits until the next repaint.
// Doc: https://developer.mozilla.org/en-US/docs/Web/API/window/requestAnimationFrame
func (p *Page) WaitRepaint() error {
	// we use root here because iframe doesn't trigger requestAnimationFrame
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	defer cancel()
	_, err := p.root.Context(ctx).Eval(`() => new Promise(r => requestAnimationFrame(r))`)
	return err
}

// WaitInteractive waits until the document is parsed (readyState is interactive
// or complete). It does not wait for images, asynchronous application work, or DOM
// stability. If navigation replaces the document, it waits for the new document.
// Use [Page.WaitLoad] for full load or [Page.WaitDOMStable] for DOM stability.
func (p *Page) WaitInteractive() error {
	// Trace overlays wait for full load, which would delay this parsing wait.
	if p.browser.trace {
		p.browser.logger.Println(TraceTypeWait, "interactive", p)
	}
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	defer cancel()
	p = p.Context(ctx)
	return utils.Retry(ctx, p.sleeper(), func() (bool, error) {
		res, err := p.Evaluate(evalHelper(js.WaitInteractive))
		if errors.Is(err, cdp.ErrCtxDestroyed) {
			return false, nil
		}
		if err != nil {
			return true, err
		}
		return res.Value.Bool(), nil
	})
}

// WaitLoad waits for the `window.onload` event, it returns immediately if the event is already fired.
func (p *Page) WaitLoad() error {
	defer p.tryTrace(TraceTypeWait, "load")()
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	defer cancel()
	p = p.Context(ctx)
	return utils.Retry(ctx, utils.BackoffSleeper(10*time.Millisecond, 100*time.Millisecond, nil), func() (bool, error) {
		_, err := p.Evaluate(evalHelper(js.WaitLoad).ByPromise())
		if errors.Is(err, cdp.ErrCtxDestroyed) {
			return false, nil
		}
		return true, err
	})
}

// AddScriptTag to page. If url is empty, content will be used.
func (p *Page) AddScriptTag(url, content string) error {
	hash := md5.Sum([]byte(url + content))
	id := hex.EncodeToString(hash[:])
	_, err := p.Evaluate(evalHelper(js.AddScriptTag, id, url, content).ByPromise())
	return err
}

// AddStyleTag to page. If url is empty, content will be used.
func (p *Page) AddStyleTag(url, content string) error {
	hash := md5.Sum([]byte(url + content))
	id := hex.EncodeToString(hash[:])
	_, err := p.Evaluate(evalHelper(js.AddStyleTag, id, url, content).ByPromise())
	return err
}

// EvalOnNewDocument Evaluates given script in every frame upon creation (before loading frame's scripts).
func (p *Page) EvalOnNewDocument(js string) (remove func() error, err error) {
	res, err := proto.PageAddScriptToEvaluateOnNewDocument{Source: js}.Call(p)
	if err != nil {
		return
	}

	remove = func() error {
		return proto.PageRemoveScriptToEvaluateOnNewDocument{
			Identifier: res.Identifier,
		}.Call(p)
	}

	return
}

// Wait until the js returns true.
func (p *Page) Wait(opts *EvalOptions) error {
	return utils.Retry(p.ctx, p.sleeper(), func() (bool, error) {
		res, err := p.Evaluate(opts)
		if err != nil {
			return true, err
		}

		return res.Value.Bool(), nil
	})
}

// WaitElementsMoreThan waits until there are more than num elements that match the selector.
func (p *Page) WaitElementsMoreThan(selector string, num int) error {
	return p.Wait(Eval(`(s, n) => document.querySelectorAll(s).length > n`, selector, num))
}

// ObjectToJSON by object id.
func (p *Page) ObjectToJSON(obj *proto.RuntimeRemoteObject) (jsonvalue.Value, error) {
	if obj.ObjectID == "" {
		return obj.Value, nil
	}

	res, err := proto.RuntimeCallFunctionOn{
		ObjectID:            obj.ObjectID,
		FunctionDeclaration: `function() { return this }`,
		ReturnByValue:       new(true),
	}.Call(p)
	if err != nil {
		return jsonvalue.New(nil), err
	}
	return res.Result.Value, nil
}

// ElementFromObject creates an Element from the remote object id.
func (p *Page) ElementFromObject(obj *proto.RuntimeRemoteObject) (*Element, error) {
	// If the element is in an iframe, we need the jsCtxID to inject helper.js to the correct context.
	id, err := p.jsCtxIDByObjectID(obj.ObjectID)
	if err != nil {
		return nil, err
	}

	pid, err := p.getJSCtxID()
	if err != nil {
		return nil, err
	}

	if id != pid {
		p.helpers.addContext(id)
		clone := *p
		clone.jsCtxID = &id
		p = &clone
	}

	return &Element{
		e:       p.e,
		ctx:     p.ctx,
		sleeper: p.sleeper,
		page:    p,
		Object:  obj,
	}, nil
}

// ElementFromNode creates an Element from the node, [proto.DOMNodeID] or [proto.DOMBackendNodeID] must be specified.
func (p *Page) ElementFromNode(node *proto.DOMNode) (*Element, error) {
	res, err := proto.DOMResolveNode{
		NodeID:        node.NodeID,
		BackendNodeID: node.BackendNodeID,
	}.Call(p)
	if err != nil {
		return nil, err
	}

	el, err := p.ElementFromObject(res.Object)
	if err != nil {
		return nil, err
	}

	// make sure always return an element node
	desc, err := el.Describe(0, false)
	if err != nil {
		return nil, err
	}
	if desc.NodeName == "#text" {
		el, err = el.Parent()
		if err != nil {
			return nil, err
		}
	}

	return el, nil
}

// ElementFromPoint creates an Element from the absolute point on the page.
// The point should include the window scroll offset.
func (p *Page) ElementFromPoint(x, y int) (*Element, error) {
	node, err := proto.DOMGetNodeForLocation{X: x, Y: y}.Call(p)
	if err != nil {
		return nil, err
	}

	return p.ElementFromNode(&proto.DOMNode{
		BackendNodeID: node.BackendNodeID,
	})
}

// Release the remote object. Usually, you don't need to call it.
// When a page is closed or reloaded, all remote objects will be released automatically.
// It's useful if the page never closes or reloads.
func (p *Page) Release(obj *proto.RuntimeRemoteObject) error {
	err := proto.RuntimeReleaseObject{ObjectID: obj.ObjectID}.Call(p)
	return err
}

// Call implements the [proto.Client].
func (p *Page) Call(ctx context.Context, sessionID, methodName string, params any) (res []byte, err error) {
	ctx, cancel := contextWithSession(ctx, p.sessionCtx)
	defer cancel()
	return p.browser.Call(ctx, sessionID, methodName, params)
}

// Event of the page.
func (p *Page) Event() <-chan *Message {
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	dst := make(chan *Message)
	s := p.event.Subscribe(ctx)

	go func() {
		defer close(dst)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-s:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					return
				case dst <- msg:
				}
			}
		}
	}()

	return dst
}

func (p *Page) initEvents() {
	ctx := p.sessionCtx
	if ctx == nil {
		ctx = p.ctx
	}
	p.event = observable.New[*Message](ctx)
	event := p.browser.Context(ctx).Event()

	go func() {
		defer p.terminateSession()
		for msg := range event {
			detached := proto.TargetDetachedFromTarget{}
			destroyed := proto.TargetTargetDestroyed{}

			if (msg.Load(&detached) && detached.SessionID == p.SessionID) ||
				(msg.Load(&destroyed) && destroyed.TargetID == p.TargetID) {
				return
			}

			if msg.SessionID != p.SessionID {
				continue
			}

			p.event.Publish(msg)
		}
	}()
}
