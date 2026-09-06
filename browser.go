//go:generate go run ./lib/proto/generate
//go:generate go run ./lib/js/generate
//go:generate go run ./lib/devices/generate

// Package rod is a high-level driver directly based on DevTools Protocol.
package rod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/rah-0/rod/internal/observable"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/devices"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// Browser implements these interfaces.
var (
	_ proto.Client      = &Browser{}
	_ proto.Contextable = &Browser{}
)

// Browser represents the browser.
// It doesn't depends on file system, it should work with remote browser seamlessly.
// To check the command-line defaults you can use to quickly enable options, check here:
// https://pkg.go.dev/github.com/rah-0/rod/lib/defaults
type Browser struct {
	// BrowserContextID is the id for incognito window
	BrowserContextID proto.BrowserBrowserContextID

	e eFunc

	ctx context.Context

	sleeper func() utils.Sleeper

	logger utils.Logger

	slowMotion time.Duration // see defaults.slow
	trace      bool          // see defaults.Trace
	monitor    string

	defaultDevice devices.Device

	controlURL    string
	client        CDPClient
	event         *observable.Observable[*Message] // all the browser events from cdp client
	connectionCtx context.Context
	targetsLock   chan struct{}
	stateLock     *sync.Mutex
	process       *localBrowserProcess

	// stores all the previous cdp call of same type. Browser doesn't have enough API
	// for us to retrieve all its internal states. This is an workaround to map them to local.
	// For example you can't use cdp API to get the current position of mouse.
	states *sync.Map
}

type localBrowserProcess struct {
	launcher    *launcher.Launcher
	stopOnce    sync.Once
	done        <-chan struct{}
	stopContext func() bool
	closeOnce   sync.Once
	closeLock   chan struct{}
}

func ownLocalBrowserProcess(ctx context.Context, l *launcher.Launcher) *localBrowserProcess {
	p := &localBrowserProcess{launcher: l, done: l.Done()}
	// Cancellation requests termination without starting a cleanup waiter.
	p.stopContext = context.AfterFunc(ctx, l.Kill)
	return p
}

func (p *localBrowserProcess) shutdown() {
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.shutdownContext(ctx)
}

func (p *localBrowserProcess) shutdownContext(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.stopContext != nil {
		p.stopContext()
	}
	if p.launcher != nil {
		p.stopOnce.Do(p.launcher.Kill)
		return p.launcher.CleanupContext(ctx)
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for owned browser cleanup: %w", ctx.Err())
	}
}

// New creates a controller.
// DefaultDevice to emulate is set to [devices.LaptopWithMDPIScreen].Landscape(), it will change the default
// user-agent and can make the actual view area smaller than the browser window on headful mode,
// you can use [Browser.NoDefaultDevice] to disable it.
func New() *Browser {
	defaults.Load()

	return (&Browser{
		ctx:           context.Background(),
		sleeper:       DefaultSleeper,
		controlURL:    defaults.URL,
		slowMotion:    defaults.Slow,
		trace:         defaults.Trace,
		monitor:       defaults.Monitor,
		logger:        DefaultLogger,
		defaultDevice: devices.LaptopWithMDPIScreen.Landscape(),
		targetsLock:   make(chan struct{}, 1),
		stateLock:     &sync.Mutex{},
		states:        &sync.Map{},
	}).WithPanic(utils.Panic)
}

// Incognito creates a new incognito browser.
func (b *Browser) Incognito() (*Browser, error) {
	res, err := proto.TargetCreateBrowserContext{}.Call(b)
	if err != nil {
		return nil, err
	}

	incognito := *b
	incognito.BrowserContextID = res.BrowserContextID

	return &incognito, nil
}

// ControlURL set the url to remote control browser.
func (b *Browser) ControlURL(url string) *Browser {
	b.controlURL = url
	return b
}

// SlowMotion set the delay for each control action, such as the simulation of the human inputs.
func (b *Browser) SlowMotion(delay time.Duration) *Browser {
	b.slowMotion = delay
	return b
}

// Trace enables/disables the visual tracing of the input actions on the page.
func (b *Browser) Trace(enable bool) *Browser {
	b.trace = enable
	return b
}

// Monitor address to listen if not empty. Shortcut for [Browser.ServeMonitor].
// The monitor has no authentication, so keep it on loopback or protect it with
// a trusted authenticated proxy.
func (b *Browser) Monitor(url string) *Browser {
	b.monitor = url
	return b
}

// Logger overrides the default log functions for tracing.
func (b *Browser) Logger(l utils.Logger) *Browser {
	b.logger = l
	return b
}

// Client set the cdp client.
func (b *Browser) Client(c CDPClient) *Browser {
	b.client = c
	return b
}

// DefaultDevice sets the default device for new page to emulate in the future.
// Default is [devices.LaptopWithMDPIScreen].
// Set it to [devices.Clear] to disable it.
func (b *Browser) DefaultDevice(d devices.Device) *Browser {
	b.defaultDevice = d
	return b
}

// NoDefaultDevice is the same as [Browser.DefaultDevice](devices.Clear).
func (b *Browser) NoDefaultDevice() *Browser {
	return b.DefaultDevice(devices.Clear)
}

// Connect to the browser and start to control it.
// If it fails to connect, it tries to launch an installed local browser.
// A browser launched by Connect is stopped when this context is canceled or
// its connection ends. Context clones created after Connect do not own its lifetime.
func (b *Browser) Connect() error {
	connected := false
	var createdClient *cdp.Client
	defer func() {
		if !connected {
			if createdClient != nil {
				_ = createdClient.Close()
			}
			b.process.shutdown()
		}
	}()

	if b.client == nil {
		u := b.controlURL
		if u == "" {
			l := launcher.New().Context(b.ctx)
			var err error
			u, err = l.Launch()
			if l.PID() != 0 {
				b.process = ownLocalBrowserProcess(b.ctx, l)
			}
			if err != nil {
				return err
			}
		}

		c, err := cdp.StartWithURL(b.ctx, u, nil)
		if err != nil {
			return err
		}
		createdClient = c
		b.client = c
	} else if b.controlURL != "" {
		panic("Browser.Client and Browser.ControlURL can't be set at the same time")
	}

	b.initEvents()

	if b.monitor != "" {
		launcher.Open(b.ServeMonitor(b.monitor))
	}

	err := proto.TargetSetDiscoverTargets{Discover: true}.Call(b)
	connected = err == nil
	return err
}

// Launch starts an unlaunched, configured local launcher, connects this Browser,
// and owns that new process until Close, owner-context cancellation, or disconnect.
// ControlURL, Client, an existing connection, and incognito contexts conflict with
// this operation. An occupied debugging port is never adopted. The launcher's
// output tail is included in startup and connection errors.
func (b *Browser) Launch(l *launcher.Launcher) (err error) {
	if l == nil || b.controlURL != "" || b.client != nil || b.process != nil || b.BrowserContextID != "" {
		return ErrLaunchConflict
	}
	u, err := l.LaunchNew(b.ctx)
	if err != nil {
		return browserLaunchError(l, err)
	}
	b.process = ownLocalBrowserProcess(b.ctx, l)
	connected := false
	defer func() {
		if !connected {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = errors.Join(err, b.process.shutdownContext(ctx))
			if err != nil {
				err = browserLaunchError(l, err)
			}
		}
	}()
	b.controlURL = u
	err = b.Connect()
	connected = err == nil
	return err
}

func browserLaunchError(l *launcher.Launcher, err error) error {
	if output := l.Output(); output != "" {
		return fmt.Errorf("launch owned browser: %w\nbrowser output (recent bytes):\n%s", err, output)
	}
	return fmt.Errorf("launch owned browser: %w", err)
}

// Close closes the browser. Owned local browsers have a ten-second cleanup
// budget independent of operation cancellation, including up to five seconds
// for graceful shutdown. Closing incognito disposes only that context.
func (b *Browser) Close() error {
	if b.BrowserContextID != "" {
		return proto.TargetDisposeBrowserContext{BrowserContextID: b.BrowserContextID}.Call(b)
	}
	if b.process == nil {
		return proto.BrowserClose{}.Call(b)
	}
	return b.CloseWithTimeout(10 * time.Second)
}

// CloseWithTimeout closes using a positive total budget independent of the
// operation context. Owned processes get up to half the budget (at most five
// seconds) to close gracefully, then are terminated and their cleanup is checked
// in the remaining time. A timeout retains process ownership for later cleanup.
// An incognito browser disposes only its context; an attached browser has no
// local process to terminate. Custom clients must honor request cancellation.
func (b *Browser) CloseWithTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return ErrCleanupTimeout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), timeout)
	defer cancel()
	if b.BrowserContextID != "" {
		return proto.TargetDisposeBrowserContext{BrowserContextID: b.BrowserContextID}.Call(b.Context(ctx))
	}
	if b.process == nil {
		return proto.BrowserClose{}.Call(b.Context(ctx))
	}
	b.process.closeOnce.Do(func() { b.process.closeLock = make(chan struct{}, 1) })
	select {
	case b.process.closeLock <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("wait for browser shutdown: %w", ctx.Err())
	}
	defer func() { <-b.process.closeLock }()
	if b.process.launcher != nil {
		select {
		case <-b.process.done:
			return b.process.launcher.CleanupContext(ctx)
		default:
		}
	}
	grace, stopGrace := context.WithTimeout(ctx, min(timeout/2, 5*time.Second))
	defer stopGrace()
	interrupted := make(chan struct{})
	stop := context.AfterFunc(grace, func() {
		defer close(interrupted)
		if client, ok := b.client.(*cdp.Client); ok {
			_ = client.Close()
		}
		if b.process.launcher != nil {
			b.process.launcher.Kill()
		}
	})
	err := proto.BrowserClose{}.Call(b.Context(grace))
	if !stop() {
		<-interrupted
	}
	if grace.Err() != nil {
		err = errors.Join(err, grace.Err())
	}
	return errors.Join(err, b.process.shutdownContext(ctx))
}

// Page creates a new browser tab. If opts.URL is empty, the default target will be "about:blank".
func (b *Browser) Page(opts proto.TargetCreateTarget) (p *Page, err error) {
	req := opts
	req.BrowserContextID = b.BrowserContextID
	req.URL = "about:blank"

	target, err := req.Call(b)
	if err != nil {
		return nil, err
	}
	defer func() {
		// If Navigate or PageFromTarget fails we should close the target to prevent leak
		if err != nil {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
			defer cancel()
			_, _ = proto.TargetCloseTarget{TargetID: target.TargetID}.Call(b.Context(ctx))
		}
	}()

	p, err = b.PageFromTarget(target.TargetID)
	if err != nil {
		return
	}

	if opts.URL == "" {
		return
	}

	err = p.Navigate(opts.URL)

	return
}

// Pages retrieves visible pages. An incognito Browser returns only pages from
// its own browser context; the root Browser can enumerate every context.
func (b *Browser) Pages() (Pages, error) {
	list, err := proto.TargetGetTargets{}.Call(b)
	if err != nil {
		return nil, err
	}

	pageList := Pages{}
	for _, target := range list.TargetInfos {
		if target.Type != proto.TargetTargetInfoTypePage || (b.BrowserContextID != "" && target.BrowserContextID != b.BrowserContextID) {
			continue
		}

		page, err := b.PageFromTarget(target.TargetID)
		if err != nil {
			return nil, err
		}
		pageList = append(pageList, page)
	}

	return pageList, nil
}

// Call implements the [proto.Client] to call raw cdp interface directly.
func (b *Browser) Call(ctx context.Context, sessionID, methodName string, params any) (res []byte, err error) {
	ctx, stopConnection := contextWithSession(ctx, b.connectionCtx)
	defer stopConnection()
	sessionCtx := b.sessionContext(proto.TargetSessionID(sessionID))
	ctx, cancel := contextWithSession(ctx, sessionCtx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err = b.client.Call(ctx, sessionID, methodName, params)
	if err != nil {
		return nil, err
	}

	b.setState(proto.TargetSessionID(sessionID), methodName, params, sessionCtx)
	return
}

// PageFromSession is used for low-level debugging. Its requests use the supplied
// browser context and, for known sessions, their shared connection lifetime.
func (b *Browser) PageFromSession(sessionID proto.TargetSessionID) *Page {
	sessionCtx := b.sessionContext(sessionID)
	if sessionCtx == nil {
		sessionCtx = b.connectionContext()
	}
	page := &Page{
		e: b.e, ctx: b.ctx, sessionCtx: sessionCtx, sleeper: b.sleeper,
		browser: b, SessionID: sessionID, jsCtxLock: &sync.Mutex{},
		jsCtxID: new(proto.RuntimeRemoteObjectID), helpers: &jsHelperCache{},
	}
	page.root = page
	return page.newKeyboard().newMouse().newTouch()
}

// PageFromTarget attaches once per target and returns a view using this Browser's
// operation context. Canceling a view does not cancel the shared page session.
func (b *Browser) PageFromTarget(targetID proto.TargetTargetID) (*Page, error) {
	select {
	case b.targetsLock <- struct{}{}:
	case <-b.ctx.Done():
		return nil, b.ctx.Err()
	}
	defer func() { <-b.targetsLock }()
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	if page := b.loadCachedPage(targetID); page != nil {
		return b.pageView(page), nil
	}
	session, err := (proto.TargetAttachToTarget{TargetID: targetID, Flatten: new(true)}).Call(b)
	if err != nil {
		return nil, err
	}
	sessionCtx, cancel := context.WithCancel(b.connectionContext())
	page := &Page{
		e: b.e, ctx: sessionCtx, sessionCtx: sessionCtx, sessionCancel: cancel,
		sleeper: b.sleeper, browser: b,
		TargetID: targetID, SessionID: session.SessionID, FrameID: proto.PageFrameID(targetID),
		jsCtxLock: &sync.Mutex{}, jsCtxID: new(proto.RuntimeRemoteObjectID), helpers: &jsHelperCache{},
	}
	page.root = page
	page.newKeyboard().newMouse().newTouch()
	b.states.Store(sessionStateKey(page.SessionID), sessionCtx)
	page.initEvents()
	view := b.pageView(page)
	if !b.defaultDevice.IsClear() {
		err = view.Emulate(b.defaultDevice)
	}
	if err == nil {
		// Page scripts and lifecycle events require this domain for the entire session.
		err = (proto.PageEnable{}).Call(view)
	}
	if err != nil {
		page.terminateSession()
		ctx, stop := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
		defer stop()
		cleanupErr := (proto.TargetDetachFromTarget{SessionID: session.SessionID}).Call(b.Context(ctx))
		return nil, errors.Join(err, cleanupErr)
	}
	b.cachePage(page)
	return view, nil
}

func (b *Browser) pageView(page *Page) *Page {
	view := page.Context(b.ctx)
	view.browser, view.e, view.sleeper = b, b.e, b.sleeper
	return view
}

// EachEvent subscribes to the given handlers across all browser sessions.
// Handlers run in message order; returning true stops the wait. The returned wait
// reports domain setup, cancellation, connection termination, and cleanup errors.
func (b *Browser) EachEvent(handlers ...EventHandler) func() error {
	return b.eachEvent("", handlers...)
}

// WaitEvent loads the next matching event into out. E is a concrete protocol event type.
func (b *Browser) WaitEvent[E proto.Event](out *E) func() error {
	return b.waitEvent("", out)
}

func (b *Browser) waitEvent[E proto.Event](sessionID proto.TargetSessionID, out *E) func() error {
	return b.eachEvent(sessionID, On(func(event *E, _ proto.TargetSessionID) bool {
		*out = *event
		return true
	}))
}

// eachEvent enables related domains until the wait ends, then restores them.
func (b *Browser) eachEvent(sessionID proto.TargetSessionID, handlers ...EventHandler) func() error {
	callbacks := make(map[string]func(*Message) bool, len(handlers))
	for _, handler := range handlers {
		if handler.handle == nil {
			panic("rod: empty event handler; use On")
		}
		callbacks[handler.method] = handler.handle
	}
	callerCtx := b.ctx
	ctx, stopConnection := contextWithSession(b.ctx, b.connectionCtx)
	ctx, stopSession := contextWithSession(ctx, b.sessionContext(sessionID))
	cancel := func() { stopSession(); stopConnection() }
	b = b.Context(ctx)
	messages := b.Event() // Runtime.enable may emit buffered events immediately.
	var releases []func(context.Context) error
	var setupErr error
	for _, handler := range handlers {
		domain, _ := proto.ParseMethodName(handler.method)
		if req := proto.GetType(domain + ".enable"); req != nil {
			release, err := b.acquireDomain(sessionID, reflect.New(req).Interface().(proto.Request))
			if err != nil {
				setupErr = err
				break
			}
			releases = append(releases, release)
		}
	}
	var restoreOnce sync.Once
	var restoreErr error
	restore := func() {
		restoreOnce.Do(func() {
			ctx, stop := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
			defer stop()
			for _, release := range releases {
				restoreErr = errors.Join(restoreErr, release(ctx))
			}
		})
	}
	stopRestore := context.AfterFunc(b.ctx, restore)
	if setupErr != nil {
		cancel()
	}
	return func() (err error) {
		if messages == nil {
			panic("can't use wait function twice")
		}
		defer func() {
			cancel()
			messages = nil
			stopRestore()
			restore()
			err = errors.Join(err, restoreErr)
		}()
		if setupErr != nil {
			return setupErr
		}
		for msg := range messages {
			if sessionID != "" && msg.SessionID != sessionID {
				continue
			}
			if callback, ok := callbacks[msg.Method]; ok && callback(msg) {
				return nil
			}
		}
		if cause := context.Cause(callerCtx); cause != nil {
			if errors.Is(cause, errWaitCompleted) {
				return nil
			}
			return cause
		}
		if b.connectionCtx != nil && b.connectionCtx.Err() != nil {
			return ErrBrowserDisconnected
		}
		if cause := context.Cause(b.ctx); cause != nil {
			return cause
		}
		return ErrBrowserDisconnected
	}
}

// Event of the browser.
func (b *Browser) Event() <-chan *Message {
	ctx, cancel := contextWithSession(b.ctx, b.connectionCtx)
	src := b.event.Subscribe(ctx)
	dst := make(chan *Message)
	go func() {
		defer close(dst)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-src:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					return
				case dst <- e:
				}
			}
		}
	}()
	return dst
}

func (b *Browser) initEvents() {
	ctx, cancel := context.WithCancel(b.ctx)
	b.connectionCtx = ctx
	b.event = observable.New[*Message](ctx)
	event := b.client.Event()

	go func() {
		defer b.process.shutdown()
		defer func() {
			cancel()
			b.stateLock.Lock()
			b.states.Clear()
			b.stateLock.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-event:
				if !ok {
					return
				}
				b.event.Publish(&Message{SessionID: proto.TargetSessionID(e.SessionID), Method: e.Method, data: e.Params})
			}
		}
	}()
}

func (b *Browser) pageInfo(id proto.TargetTargetID) (*proto.TargetTargetInfo, error) {
	res, err := proto.TargetGetTargetInfo{TargetID: id}.Call(b)
	if err != nil {
		return nil, err
	}
	return res.TargetInfo, nil
}

func (b *Browser) isHeadless() (enabled bool) {
	res, _ := proto.BrowserGetBrowserCommandLine{}.Call(b)
	for _, v := range res.Arguments {
		if strings.Contains(v, "headless") {
			return true
		}
	}
	return false
}

// IgnoreCertErrors switch. If enabled, all certificate errors will be ignored.
func (b *Browser) IgnoreCertErrors(enable bool) error {
	return proto.SecuritySetIgnoreCertificateErrors{Ignore: enable}.Call(b)
}

// GetCookies from the browser.
func (b *Browser) GetCookies() ([]*proto.NetworkCookie, error) {
	res, err := proto.StorageGetCookies{BrowserContextID: b.BrowserContextID}.Call(b)
	if err != nil {
		return nil, err
	}
	return res.Cookies, nil
}

// SetCookies to the browser. If the cookies is nil it will clear all the cookies.
func (b *Browser) SetCookies(cookies []*proto.NetworkCookieParam) error {
	if cookies == nil {
		return proto.StorageClearCookies{BrowserContextID: b.BrowserContextID}.Call(b)
	}

	return proto.StorageSetCookies{
		Cookies:          cookies,
		BrowserContextID: b.BrowserContextID,
	}.Call(b)
}

// ErrDownloadInProgress indicates another wait owns download behavior in this context.
var ErrDownloadInProgress = errors.New("rod: a download wait is already active in this browser context")

// ErrDownloadCanceled indicates that Chrome canceled the selected download.
var ErrDownloadCanceled = errors.New("rod: download canceled")

type downloadWaitKey proto.BrowserBrowserContextID

type downloadEventsKey struct{}
type downloadEventsEnabledKey struct{}

type downloadEvents struct {
	gate              chan struct{}
	owners            int
	originallyEnabled bool
}

func (events *downloadEvents) lock(ctx context.Context) error {
	select {
	case events.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (events *downloadEvents) unlock() { <-events.gate }

// WaitDownload configures downloads and subscribes before returning. Invoke its
// single-use wait after triggering a download; it returns the first download in
// this browser context when that GUID completes, or an error on cancellation.
// Files are saved as filepath.Join(dir, info.GUID). Only one wait may configure a
// browser context at once. Both completion and context cancellation restore the
// previous behavior, using an independent five-second cleanup budget. Browser
// download events remain enabled until the last concurrent context waiter stops.
func (b *Browser) WaitDownload(dir string) (func() (*proto.BrowserDownloadWillBegin, error), error) {
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	key, owner := downloadWaitKey(b.BrowserContextID), new(int)
	b.stateLock.Lock()
	if b.connectionCtx != nil && b.connectionCtx.Err() != nil {
		b.stateLock.Unlock()
		return nil, ErrBrowserDisconnected
	}
	if _, active := b.states.LoadOrStore(key, owner); active {
		b.stateLock.Unlock()
		return nil, ErrDownloadInProgress
	}
	shared, _ := b.states.LoadOrStore(downloadEventsKey{}, &downloadEvents{gate: make(chan struct{}, 1)})
	b.stateLock.Unlock()
	events := shared.(*downloadEvents)
	ctx, cancel := contextWithSession(b.ctx, b.connectionCtx)
	browser := b.Context(ctx)
	messages := browser.Event()
	var oldBehavior proto.BrowserSetDownloadBehavior
	if !b.LoadState("", &oldBehavior) {
		oldBehavior = proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorDefault, BrowserContextID: b.BrowserContextID}
	}
	var once sync.Once
	var cleanupErr error
	attempted := false
	cleanup := func() {
		once.Do(func() {
			defer b.states.CompareAndDelete(key, owner)
			if attempted {
				ctx, stop := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
				defer stop()
				if cleanupErr = events.lock(ctx); cleanupErr != nil {
					return
				}
				defer events.unlock()
				events.owners--
				oldBehavior.EventsEnabled = new(events.owners > 0 || events.originallyEnabled)
				cleanupErr = oldBehavior.Call(b.Context(ctx))
			}
		})
	}
	targets, err := (proto.TargetGetTargets{}).Call(browser)
	incognito := make(map[proto.BrowserBrowserContextID]bool)
	if err == nil && b.BrowserContextID == "" {
		var result *proto.TargetGetBrowserContextsResult
		result, err = (proto.TargetGetBrowserContexts{}).Call(browser)
		if err == nil {
			for _, id := range result.BrowserContextIDs {
				incognito[id] = true
			}
		}
	}
	if err == nil {
		err = events.lock(ctx)
	}
	if err == nil {
		if events.owners == 0 {
			enabled, _ := b.states.Load(downloadEventsEnabledKey{})
			events.originallyEnabled, _ = enabled.(bool)
		}
		events.owners++
		attempted = true
		err = (proto.BrowserSetDownloadBehavior{
			Behavior:         proto.BrowserSetDownloadBehaviorBehaviorAllowAndName,
			BrowserContextID: b.BrowserContextID, DownloadPath: dir, EventsEnabled: new(true),
		}).Call(browser)
		events.unlock()
	}
	if err != nil {
		cancel()
		cleanup()
		return nil, errors.Join(err, cleanupErr)
	}
	contexts := make(map[proto.PageFrameID]proto.BrowserBrowserContextID)
	for _, target := range targets.TargetInfos {
		contexts[proto.PageFrameID(target.TargetID)] = target.BrowserContextID
	}
	stopCleanup := context.AfterFunc(ctx, cleanup)
	return func() (info *proto.BrowserDownloadWillBegin, err error) {
		if messages == nil {
			panic("can't use download wait function twice")
		}
		defer func() {
			cancel()
			messages = nil
			stopCleanup()
			cleanup()
			err = errors.Join(err, cleanupErr)
		}()
		for message := range messages {
			var created proto.TargetTargetCreated
			var changed proto.TargetTargetInfoChanged
			var begin proto.BrowserDownloadWillBegin
			var progress proto.BrowserDownloadProgress
			switch {
			case message.Load(&created):
				contexts[proto.PageFrameID(created.TargetInfo.TargetID)] = created.TargetInfo.BrowserContextID
			case message.Load(&changed):
				contexts[proto.PageFrameID(changed.TargetInfo.TargetID)] = changed.TargetInfo.BrowserContextID
			case message.Load(&begin):
				if info != nil {
					continue
				}
				matches, matchErr := browser.downloadInContext(begin.FrameID, contexts, incognito)
				if matchErr != nil {
					return nil, matchErr
				}
				if matches {
					info = &begin
				}
			case message.Load(&progress):
				if info == nil || progress.GUID != info.GUID {
					continue
				}
				switch progress.State {
				case proto.BrowserDownloadProgressStateCompleted:
					return info, nil
				case proto.BrowserDownloadProgressStateCanceled:
					return info, ErrDownloadCanceled
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return info, err
		}
		return info, ErrBrowserDisconnected
	}, nil
}

// Browser download events omit the browser context. Target discovery resolves
// main frames, including download-only tabs that have already closed. For a
// child frame, inspect frame trees without changing device settings or page state.
func (b *Browser) downloadInContext(frame proto.PageFrameID, contexts map[proto.PageFrameID]proto.BrowserBrowserContextID, incognito map[proto.BrowserBrowserContextID]bool) (bool, error) {
	if b.BrowserContextID == "" {
		result, err := (proto.TargetGetBrowserContexts{}).Call(b)
		if err != nil {
			return false, err
		}
		for _, id := range result.BrowserContextIDs {
			incognito[id] = true
		}
	}
	matches := func(id proto.BrowserBrowserContextID) bool {
		if b.BrowserContextID != "" {
			return id == b.BrowserContextID
		}
		return !incognito[id]
	}
	if id, known := contexts[frame]; known {
		return matches(id), nil
	}
	targets, err := (proto.TargetGetTargets{}).Call(b)
	if err != nil {
		return false, err
	}
	for _, target := range targets.TargetInfos {
		contexts[proto.PageFrameID(target.TargetID)] = target.BrowserContextID
		if proto.PageFrameID(target.TargetID) == frame {
			return matches(target.BrowserContextID), nil
		}
	}
	for _, target := range targets.TargetInfos {
		if target.Type != proto.TargetTargetInfoTypePage || !matches(target.BrowserContextID) {
			continue
		}
		var tree *proto.PageGetFrameTreeResult
		if page := b.loadCachedPage(target.TargetID); page != nil {
			tree, err = (proto.PageGetFrameTree{}).Call(page.Context(b.ctx))
		} else {
			tree, err = b.downloadFrameTree(target.TargetID)
		}
		if err != nil {
			return false, err
		}
		var contains func(*proto.PageFrameTree) bool
		contains = func(tree *proto.PageFrameTree) bool {
			if tree == nil {
				return false
			}
			if tree.Frame != nil && tree.Frame.ID == frame {
				return true
			}
			for _, child := range tree.ChildFrames {
				if contains(child) {
					return true
				}
			}
			return false
		}
		if contains(tree.FrameTree) {
			return true, nil
		}
	}
	return false, nil
}

func (b *Browser) downloadFrameTree(target proto.TargetTargetID) (_ *proto.PageGetFrameTreeResult, err error) {
	session, err := (proto.TargetAttachToTarget{TargetID: target, Flatten: new(true)}).Call(b)
	if err != nil {
		return nil, err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, (proto.TargetDetachFromTarget{SessionID: session.SessionID}).Call(b.Context(ctx)))
	}()
	// This temporary session has no Rod page or retained command state.
	data, err := b.client.Call(b.ctx, string(session.SessionID), "Page.getFrameTree", nil)
	if err != nil {
		return nil, err
	}
	var result proto.PageGetFrameTreeResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Version info of the browser.
func (b *Browser) Version() (*proto.BrowserGetVersionResult, error) {
	return proto.BrowserGetVersion{}.Call(b)
}
